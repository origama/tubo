package service

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
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
	if artifacts, err := e.buildDelegatedArtifacts(payload.JTI, payload.AccessEpoch, msg.ClientPublicKey); err == nil {
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
	if err := e.authorizeConnectRequest(requester, msg); err != nil {
		return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: requestID, Reason: err.Error()}
	}
	artifacts, err := e.buildDelegatedArtifacts("", 0, msg.ClientPublicKey)
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
	owner, _, delegation, _, err := e.loadDelegatedSigner()
	if err == nil {
		access, err := grantspkg.RefreshDelegatedConnectAccessLease(e.authorityPub, owner.PrivateKey, *lease, grantspkg.DefaultConnectAccessLeaseTTL, delegation.PublisherPeerID)
		if err == nil {
			return grantspkg.Message{Type: grantspkg.TypeConnectRefresh, Version: grantspkg.VersionV1, RequestID: lease.JTI, ConnectAccessLease: &access}
		}
	}
	if len(e.authorityPriv) > 0 {
		return e.server.HandleMessage(msg, requester)
	}
	return grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: lease.JTI, Reason: "connect refresh requires a valid local service delegation"}
}

func (e *serviceGrantEndpoint) buildDelegatedArtifacts(shareInviteJTI string, accessEpoch int64, clientPublicKey string) (grantspkg.ConnectLeaseArtifacts, error) {
	owner, _, delegation, _, err := e.loadDelegatedSigner()
	if err != nil {
		return grantspkg.ConnectLeaseArtifacts{}, err
	}
	return grantspkg.BuildDelegatedConnectLeaseArtifacts(e.authorityPub, owner.PrivateKey, delegation, shareInviteJTI, clientPublicKey, accessEpoch, grantspkg.DefaultConnectAccessLeaseTTL, grantspkg.DefaultConnectRefreshLeaseTTL)
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

func (e *serviceGrantEndpoint) authorizeConnectRequest(requester peer.ID, msg grantspkg.Message) error {
	switch e.connectPolicy {
	case "invite_only":
		return errors.New("service is invite_only; use `tubo connect --token <share-invite>`")
	case "namespace_members":
		if msg.MembershipCapability == nil && strings.TrimSpace(msg.MembershipGrantToken) == "" {
			return errors.New("namespace_members policy requires a membership capability or membership invite with connect permission")
		}
		var errs []string
		if msg.MembershipCapability != nil {
			if err := verifyConnectMembership(*msg.MembershipCapability, e.authorityPub, e.clusterID, e.namespaceID, requester.String()); err == nil {
				return nil
			} else {
				errs = append(errs, err.Error())
			}
		}
		if strings.TrimSpace(msg.MembershipGrantToken) != "" {
			if err := e.verifyConnectMembershipGrantToken(msg.MembershipGrantToken); err == nil {
				return nil
			} else {
				errs = append(errs, err.Error())
			}
		}
		return fmt.Errorf("namespace_members policy denied connect: %s", strings.Join(errs, "; "))
	case "public":
		if !e.allowPublicConnect(requester) {
			return errors.New("public connect policy rate limit exceeded; retry later")
		}
		return nil
	default:
		return fmt.Errorf("unsupported connect policy %q", e.connectPolicy)
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

func verifyConnectMembership(membership capability.MembershipCapability, authorityPub ed25519.PublicKey, clusterID, namespaceID, requesterPeerID string) error {
	var lastErr error
	for _, subject := range []string{requesterPeerID, clusterID} {
		candidateNamespaces := []string{namespaceID}
		if membership.NamespaceID == "*" {
			candidateNamespaces = append(candidateNamespaces, "*")
		}
		for _, candidateNamespace := range candidateNamespaces {
			if err := capability.VerifyMembershipCapability(membership, authorityPub, clusterID, candidateNamespace, subject); err != nil {
				lastErr = err
				continue
			}
			if membership.NamespaceID != namespaceID && membership.NamespaceID != "*" {
				lastErr = fmt.Errorf("membership capability does not authorize namespace %q", namespaceID)
				continue
			}
			if !containsConnectPermission(membership.Permissions) {
				return errors.New("membership capability is missing connect permission")
			}
			return nil
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("membership capability rejected")
}

func (e *serviceGrantEndpoint) verifyConnectMembershipGrantToken(token string) error {
	payload, err := clusterinvite.ParseAndVerifyToken(token)
	if err != nil {
		return err
	}
	if !clusterinvite.MatchesAuthority(payload, e.authorityPub) {
		return errors.New("membership invite authority does not match attached service authority")
	}
	if payload.ClusterID != e.clusterID || payload.Namespace != e.namespaceID {
		return errors.New("membership invite does not authorize attached service scope")
	}
	if !containsConnectPermission(payload.Grant.Permissions) {
		return errors.New("membership invite is missing connect permission")
	}
	if e.revocations != nil {
		if revoked, _, err := e.revocations.IsInviteRevoked(payload.JTI); err != nil {
			return err
		} else if revoked {
			return errors.New("membership invite revoked")
		}
	}
	return nil
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

func containsConnectPermission(perms []string) bool {
	for _, perm := range perms {
		if perm == capability.PermissionConnect {
			return true
		}
	}
	return false
}

func advertisedGrantServiceEndpoint(addrs []string) *grantspkg.GrantServiceEndpoint {
	peers := grantspkg.PreferredAdvertisedGrantServicePeers(addrs)
	if len(peers) == 0 {
		return nil
	}
	return &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: peers}
}

func serviceGrantStorePath(cfg Config, serviceID string) string {
	base := strings.TrimSpace(cfg.ServicePublishLeaseFile)
	if base == "" {
		base = strings.TrimSpace(cfg.ServiceClaimFile)
	}
	if base != "" {
		return filepath.Join(filepath.Dir(base), serviceID+".grant-endpoint.requests.json")
	}
	return filepath.Join(os.TempDir(), "tubo-"+serviceID+".grant-endpoint.requests.json")
}

func serviceGrantRedemptionStorePath(cfg Config, serviceID string) string {
	base := strings.TrimSpace(cfg.ServicePublishLeaseFile)
	if base == "" {
		base = strings.TrimSpace(cfg.ServiceClaimFile)
	}
	if base != "" {
		return filepath.Join(filepath.Dir(base), serviceID+".grant-endpoint.share-redemptions.json")
	}
	return filepath.Join(os.TempDir(), "tubo-"+serviceID+".grant-endpoint.share-redemptions.json")
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
