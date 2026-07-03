package service

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	capability "github.com/origama/tubo/internal/capability"
	clusterinvite "github.com/origama/tubo/internal/clusterinvite"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/serviceidentity"
)

const (
	publicConnectRateLimitBurst  = 4
	publicConnectRateLimitWindow = time.Minute
)

type serviceGrantEndpoint struct {
	serviceID         string
	servicePeerID     string
	clusterName       string
	clusterID         string
	namespaceID       string
	connectPolicy     string
	authorityPub      ed25519.PublicKey
	authorityPriv     ed25519.PrivateKey
	serviceOwnerKey   string
	publishLeaseFile  string
	server            *grantspkg.Server
	revocations       *grantspkg.RevocationStore
	shareRedemptions  *grantspkg.ShareRedemptionStore
	now               func() time.Time
	abuse             *grantEndpointAbuseController
	publicRateLimitMu sync.Mutex
	publicRateLimit   map[peer.ID][]time.Time
}

func newServiceGrantEndpoint(cfg Config, serviceID, servicePeerID string) (*serviceGrantEndpoint, error) {
	if strings.TrimSpace(serviceID) == "" || strings.TrimSpace(servicePeerID) == "" || strings.TrimSpace(cfg.DiscoveryClusterID) == "" || strings.TrimSpace(cfg.DiscoveryNamespaceID) == "" {
		return nil, errors.New("service-scoped grant endpoint requires service and scope identifiers")
	}
	authorityPub, err := discovery.ParseAuthorityPublicKey(cfg.AuthorityPublicKey)
	if err != nil {
		return nil, err
	}
	var authorityPriv ed25519.PrivateKey
	if strings.TrimSpace(cfg.AuthorityPrivateKeyFile) != "" {
		authorityPriv, err = loadAuthorityPrivateKey(cfg.AuthorityPrivateKeyFile)
		if err != nil {
			return nil, err
		}
	}
	revocations := grantspkg.NewRevocationStore(grantspkg.DefaultRevocationStorePath())
	shareRedemptions := grantspkg.NewShareRedemptionStore(serviceGrantRedemptionStorePath(cfg, serviceID))
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{
		ClusterName:         first(cfg.ClusterName, cfg.DiscoveryClusterID),
		ClusterID:           cfg.DiscoveryClusterID,
		NamespaceID:         cfg.DiscoveryNamespaceID,
		Store:               grantspkg.NewStore(serviceGrantStorePath(cfg, serviceID)),
		AuthorityPrivateKey: authorityPriv,
		Revocations:         revocations,
		ShareRedemptions:    shareRedemptions,
	})
	if err != nil {
		return nil, err
	}
	policy := strings.TrimSpace(cfg.ConnectPolicy)
	if policy == "" {
		policy = "invite_only"
	}
	endpoint := &serviceGrantEndpoint{
		serviceID:        serviceID,
		servicePeerID:    servicePeerID,
		clusterName:      first(cfg.ClusterName, cfg.DiscoveryClusterID),
		clusterID:        cfg.DiscoveryClusterID,
		namespaceID:      cfg.DiscoveryNamespaceID,
		connectPolicy:    policy,
		authorityPub:     authorityPub,
		authorityPriv:    authorityPriv,
		serviceOwnerKey:  strings.TrimSpace(cfg.ServiceOwnerKeyFile),
		publishLeaseFile: strings.TrimSpace(cfg.ServicePublishLeaseFile),
		server:           server,
		revocations:      revocations,
		shareRedemptions: shareRedemptions,
		now:              func() time.Time { return time.Now().UTC() },
		publicRateLimit:  make(map[peer.ID][]time.Time),
	}
	endpoint.abuse = newGrantEndpointAbuseController(grantEndpointAbuseConfig{now: endpoint.now})
	return endpoint, nil
}

func (e *serviceGrantEndpoint) HandleStream(stream network.Stream) {
	defer stream.Close()
	msg, err := grantspkg.DecodeMessage(stream)
	if err != nil {
		_ = grantspkg.EncodeMessage(stream, grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "invalid", Reason: err.Error()})
		return
	}
	resp := e.handleMessage(msg, stream.Conn().RemotePeer())
	if err := grantspkg.EncodeMessage(stream, resp); err != nil {
		_ = stream.Reset()
	}
}

func (e *serviceGrantEndpoint) handleMessage(msg grantspkg.Message, requester peer.ID) grantspkg.Message {
	if err := e.applyAbuseControls(msg.Type, requester); err != nil {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: fallbackGrantRequestID(msg.RequestID), Reason: err.Error()}
	}
	var resp grantspkg.Message
	switch msg.Type {
	case grantspkg.TypeShareRedeem:
		resp = e.handleShareRedeem(msg, requester)
	case grantspkg.TypeConnectRequest:
		resp = e.handleConnectRequest(msg, requester)
	case grantspkg.TypeConnectRefresh:
		resp = e.handleConnectRefresh(msg, requester)
	default:
		resp = grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: fallbackGrantRequestID(msg.RequestID), Reason: "attached-service grant endpoint only exposes service-scoped connect operations"}
	}
	if resp.Type == grantspkg.TypeDenied {
		e.recordDeniedGrantRequest(msg.Type, requester, resp.Reason)
	}
	return resp
}

func (e *serviceGrantEndpoint) handleShareRedeem(msg grantspkg.Message, requester peer.ID) grantspkg.Message {
	payload, err := grantspkg.ParseAndVerifyServiceShareToken(msg.ShareInviteToken)
	if err != nil {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "share-redeem", Reason: err.Error()}
	}
	if payload.ClusterID != e.clusterID || payload.NamespaceID != e.namespaceID || payload.TargetServiceID != e.serviceID {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: payload.JTI, Reason: "share invite does not match attached service scope"}
	}
	if artifacts, err := e.buildDelegatedArtifacts(payload.JTI, payload.AccessEpoch, msg.ClientPublicKey, grantspkg.DefaultConnectAccessLeaseTTL, grantspkg.DefaultConnectRefreshLeaseTTL); err == nil {
		if err := e.consumeShareInvite(payload, requester, msg.ClientPublicKey, artifacts.RefreshLease.SessionID); err != nil {
			return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: payload.JTI, Reason: err.Error()}
		}
		return grantspkg.Message{Type: grantspkg.TypeShareRedeem, Version: grantspkg.VersionV1, RequestID: payload.JTI, ConnectAccessLease: &artifacts.AccessLease, ConnectRefreshLease: &artifacts.RefreshLease}
	}
	if len(e.authorityPriv) > 0 {
		return e.server.HandleMessage(msg, requester)
	}
	return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: payload.JTI, Reason: "share invite redemption requires a valid local service delegation"}
}

func (e *serviceGrantEndpoint) handleConnectRequest(msg grantspkg.Message, requester peer.ID) grantspkg.Message {
	requestID := fallbackGrantRequestID(msg.RequestID)
	if msg.ClusterID != e.clusterID || msg.NamespaceID != e.namespaceID || msg.ServiceID != e.serviceID {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: requestID, Reason: "connect lease request scope does not match attached service"}
	}
	membershipExpiry, err := e.authorizeConnectRequest(requester, msg)
	if err != nil {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: requestID, Reason: err.Error()}
	}
	accessTTL := grantspkg.DefaultConnectAccessLeaseTTL
	refreshTTL := grantspkg.DefaultConnectRefreshLeaseTTL
	if !membershipExpiry.IsZero() {
		remaining := time.Until(membershipExpiry.UTC())
		if remaining <= 0 {
			return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: requestID, Reason: "namespace membership expired"}
		}
		if accessTTL > remaining {
			accessTTL = remaining
		}
		if refreshTTL > remaining {
			refreshTTL = remaining
		}
	}
	artifacts, err := e.buildDelegatedArtifacts("", 0, msg.ClientPublicKey, accessTTL, refreshTTL)
	if err != nil {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: requestID, Reason: err.Error()}
	}
	return grantspkg.Message{Type: grantspkg.TypeConnectGranted, Version: grantspkg.VersionV1, RequestID: requestID, ConnectAccessLease: &artifacts.AccessLease, ConnectRefreshLease: &artifacts.RefreshLease}
}

func (e *serviceGrantEndpoint) handleConnectRefresh(msg grantspkg.Message, requester peer.ID) grantspkg.Message {
	if msg.ConnectRefreshLease == nil {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "connect-refresh", Reason: "connect_refresh_lease is required"}
	}
	lease := msg.ConnectRefreshLease
	if lease.ClusterID != e.clusterID || lease.NamespaceID != e.namespaceID || lease.ServiceID != e.serviceID {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: lease.JTI, Reason: "connect refresh lease does not match attached service scope"}
	}
	isDelegated := len(lease.DelegationPublishLease) > 0
	owner, _, delegation, _, err := e.loadDelegatedSigner()
	if err == nil {
		accessTTL := grantspkg.DefaultConnectAccessLeaseTTL
		if remaining := time.Until(delegation.ExpiresAt.UTC()); remaining <= 0 {
			log.Printf("grant endpoint connect refresh denied requester=%s service_id=%s issuer=delegated reason=%q", requester, e.serviceID, "publish lease expired")
			return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: lease.JTI, Reason: "publish lease expired"}
		} else if accessTTL > remaining {
			accessTTL = remaining
		}
		access, err := grantspkg.RefreshDelegatedConnectAccessLease(e.authorityPub, owner.PrivateKey, *lease, accessTTL, delegation.PublisherPeerID)
		if err == nil {
			log.Printf("grant endpoint connect refresh accepted requester=%s service_id=%s issuer=delegated expires_at=%s", requester, e.serviceID, access.ExpiresAt.UTC().Format(time.RFC3339))
			return grantspkg.Message{Type: grantspkg.TypeConnectRefresh, Version: grantspkg.VersionV1, RequestID: lease.JTI, ConnectAccessLease: &access}
		}
		log.Printf("grant endpoint connect refresh delegated verify failed requester=%s service_id=%s err=%q", requester, e.serviceID, err.Error())
	}
	if len(e.authorityPriv) > 0 {
		log.Printf("grant endpoint connect refresh fallback to authority requester=%s service_id=%s lease_delegated=%t", requester, e.serviceID, isDelegated)
		return e.server.HandleMessage(msg, requester)
	}
	log.Printf("grant endpoint connect refresh denied requester=%s service_id=%s issuer=unavailable reason=%q", requester, e.serviceID, "connect refresh requires a valid local service delegation")
	return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: lease.JTI, Reason: "connect refresh requires a valid local service delegation"}
}

func (e *serviceGrantEndpoint) buildDelegatedArtifacts(shareInviteJTI string, accessEpoch int64, clientPublicKey string, accessTTL, refreshTTL time.Duration) (grantspkg.ConnectLeaseArtifacts, error) {
	owner, _, delegation, _, err := e.loadDelegatedSigner()
	if err != nil {
		return grantspkg.ConnectLeaseArtifacts{}, err
	}
	return grantspkg.BuildDelegatedConnectLeaseArtifacts(e.authorityPub, owner.PrivateKey, delegation, shareInviteJTI, clientPublicKey, accessEpoch, accessTTL, refreshTTL)
}

func (e *serviceGrantEndpoint) loadDelegatedSigner() (serviceidentity.Identity, []byte, grantspkg.PublishLease, []byte, error) {
	if e.serviceOwnerKey == "" {
		return serviceidentity.Identity{}, nil, grantspkg.PublishLease{}, nil, errors.New("service owner key file is not configured")
	}
	owner, _, err := serviceidentity.Load(e.serviceOwnerKey)
	if err != nil {
		return serviceidentity.Identity{}, nil, grantspkg.PublishLease{}, nil, err
	}
	if owner.ServiceID != e.serviceID {
		return serviceidentity.Identity{}, nil, grantspkg.PublishLease{}, nil, fmt.Errorf("service owner key does not match service %q", e.serviceID)
	}
	if e.publishLeaseFile == "" {
		return serviceidentity.Identity{}, nil, grantspkg.PublishLease{}, nil, errors.New("service publish lease file is not configured")
	}
	raw, err := os.ReadFile(e.publishLeaseFile)
	if err != nil {
		return serviceidentity.Identity{}, nil, grantspkg.PublishLease{}, nil, err
	}
	lease, err := grantspkg.ParseAndVerifyPublishLeaseBytes(raw, e.authorityPub, e.clusterID, e.namespaceID, e.serviceID, e.servicePeerID)
	if err != nil {
		return serviceidentity.Identity{}, nil, grantspkg.PublishLease{}, nil, err
	}
	if lease.ServicePublicKey != serviceidentity.EncodePublicKey(owner.PublicKey) {
		return serviceidentity.Identity{}, nil, grantspkg.PublishLease{}, nil, errors.New("publish lease service public key does not match local service owner key")
	}
	return owner, raw, lease, raw, nil
}

// authorizeConnectRequest validates the namespace membership boundary for a
// connect request and returns the expiry that should cap any minted connect
// leases. A granted connect session must not outlive that membership boundary.
func (e *serviceGrantEndpoint) authorizeConnectRequest(requester peer.ID, msg grantspkg.Message) (time.Time, error) {
	switch e.connectPolicy {
	case "invite_only":
		return time.Time{}, errors.New("service is invite_only; use `tubo connect --token <share-invite>`")
	case "namespace_members":
		if msg.MembershipCapability == nil && strings.TrimSpace(msg.MembershipGrantToken) == "" {
			return time.Time{}, errors.New("namespace_members policy requires a membership capability or membership invite with connect permission")
		}
		var errs []string
		if msg.MembershipCapability != nil {
			if expiry, err := verifyConnectMembership(*msg.MembershipCapability, e.authorityPub, e.clusterID, e.namespaceID, requester.String()); err == nil {
				return expiry, nil
			} else {
				errs = append(errs, err.Error())
			}
		}
		if strings.TrimSpace(msg.MembershipGrantToken) != "" {
			if expiry, err := e.verifyConnectMembershipGrantToken(msg.MembershipGrantToken); err == nil {
				return expiry, nil
			} else {
				errs = append(errs, err.Error())
			}
		}
		return time.Time{}, fmt.Errorf("namespace_members policy denied connect: %s", strings.Join(errs, "; "))
	case "public":
		if !e.allowPublicConnect(requester) {
			return time.Time{}, errors.New("public connect policy rate limit exceeded; retry later")
		}
		return time.Time{}, nil
	default:
		return time.Time{}, fmt.Errorf("unsupported connect policy %q", e.connectPolicy)
	}
}

func (e *serviceGrantEndpoint) allowPublicConnect(requester peer.ID) bool {
	now := e.now().UTC()
	e.publicRateLimitMu.Lock()
	defer e.publicRateLimitMu.Unlock()
	items := e.publicRateLimit[requester]
	keep := items[:0]
	for _, ts := range items {
		if now.Sub(ts) < publicConnectRateLimitWindow {
			keep = append(keep, ts)
		}
	}
	if len(keep) >= publicConnectRateLimitBurst {
		e.publicRateLimit[requester] = keep
		return false
	}
	keep = append(keep, now)
	e.publicRateLimit[requester] = keep
	return true
}

func verifyConnectMembership(membership capability.MembershipCapability, authorityPub ed25519.PublicKey, clusterID, namespaceID, requesterPeerID string) (time.Time, error) {
	// Delegate to shared verification in grants package. Pass zero time to let
	// capability.VerifyMembershipCapability handle expiry with real time.
	return grantspkg.VerifyConnectMembershipCapability(membership, authorityPub, clusterID, namespaceID, requesterPeerID, time.Time{})
}

func (e *serviceGrantEndpoint) verifyConnectMembershipGrantToken(token string) (time.Time, error) {
	payload, err := clusterinvite.ParseAndVerifyToken(token)
	if err != nil {
		return time.Time{}, err
	}
	if !clusterinvite.MatchesAuthority(payload, e.authorityPub) {
		return time.Time{}, errors.New("membership invite authority does not match attached service authority")
	}
	if payload.ClusterID != e.clusterID || payload.Namespace != e.namespaceID {
		return time.Time{}, errors.New("membership invite does not authorize attached service scope")
	}
	if !grantspkg.HasConnectPermission(payload.Grant.Permissions) {
		return time.Time{}, errors.New("membership invite is missing connect permission")
	}
	if e.revocations != nil {
		if revoked, _, err := e.revocations.IsInviteRevoked(payload.JTI); err != nil {
			return time.Time{}, err
		} else if revoked {
			return time.Time{}, errors.New("membership invite revoked")
		}
	}
	return payload.ExpiresAt.UTC(), nil
}

func (e *serviceGrantEndpoint) consumeShareInvite(payload grantspkg.ServiceSharePayload, requester peer.ID, clientPublicKey, sessionID string) error {
	if e.shareRedemptions == nil {
		return nil
	}
	thumbprint, err := grantspkg.ConnectClientKeyThumbprint(clientPublicKey)
	if err != nil {
		return err
	}
	if err := e.shareRedemptions.TryConsume(grantspkg.ShareRedemptionRecord{JTI: payload.JTI, ClusterID: payload.ClusterID, NamespaceID: payload.NamespaceID, ServiceID: payload.TargetServiceID, RedeemedByPeerID: requester.String(), ClientKeyThumbprint: thumbprint, SessionID: sessionID, TokenExpiresAt: payload.ExpiresAt.UTC()}); err != nil {
		if err == grantspkg.ErrShareInviteAlreadyRedeemed {
			return errors.New("share invite already redeemed")
		}
		return err
	}
	return nil
}

func advertisedGrantServiceEndpoint(addrs []string) *grantspkg.GrantServiceEndpoint {
	peers := grantspkg.PreferredAdvertisedGrantServicePeers(addrs)
	if len(peers) == 0 {
		return nil
	}
	return &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: peers}
}

func serviceGrantStorePath(cfg Config, serviceID string) string {
	return filepath.Join(serviceGrantStateDir(cfg, serviceID), serviceID+".grant-endpoint.requests.json")
}

func serviceGrantRedemptionStorePath(cfg Config, serviceID string) string {
	return filepath.Join(serviceGrantStateDir(cfg, serviceID), serviceID+".grant-endpoint.share-redemptions.json")
}

func serviceGrantStateDir(cfg Config, serviceID string) string {
	return serviceGrantStateDirWithCheck(cfg, serviceID, serviceGrantDirWritable)
}

func serviceGrantStateDirWithCheck(cfg Config, serviceID string, writable func(string) bool) string {
	base := strings.TrimSpace(cfg.ServicePublishLeaseFile)
	if base == "" {
		base = strings.TrimSpace(cfg.ServiceClaimFile)
	}
	if base != "" {
		dir := filepath.Dir(base)
		if writable(dir) {
			return dir
		}
	}
	return filepath.Join(serviceGrantDataRoot(), serviceID)
}

func serviceGrantDataRoot() string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(xdg, "tubo", "services")
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join(os.TempDir(), "tubo-services")
	}
	return filepath.Join(home, ".local", "share", "tubo", "services")
}

func serviceGrantDirWritable(dir string) bool {
	if strings.TrimSpace(dir) == "" {
		return false
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return false
	}
	probe, err := os.CreateTemp(dir, ".grant-endpoint-write-test-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return true
}

func loadAuthorityPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("cluster authority private key is not PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return k, nil
	case *ed25519.PrivateKey:
		return *k, nil
	default:
		return nil, fmt.Errorf("unsupported cluster authority private key type %T", key)
	}
}

func fallbackGrantRequestID(id string) string {
	if strings.TrimSpace(id) == "" {
		return "invalid"
	}
	return id
}
