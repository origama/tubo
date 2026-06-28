package grants

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	capability "github.com/origama/tubo/internal/capability"
)

const (
	DefaultMaxPendingRequests     = 1024
	DefaultMaxPendingPerRequester = 16
	DefaultMaxPendingPerService   = 4
)

// Namespace-scoped grant semantics:
//   - publish submit/auto-approve mints a service claim and membership capability
//     with the same expiry, and share invite TTL is capped by the publish lease.
//   - connect requests require live membership or a valid membership invite, and
//     minted connect leases are capped by that membership expiry.
//   - connect refresh only extends an already bounded session; it never widens the
//     membership/session window.
// MembershipGrantTokenVerifier validates a membership grant token and returns
// its expiry time. The caller is responsible for providing an implementation
// that uses clusterinvite.ParseAndVerifyToken and clusterinvite.MatchesAuthority.
type MembershipGrantTokenVerifier func(token string, authorityPub ed25519.PublicKey, clusterID, namespaceID string) (time.Time, error)

type ServerConfig struct {
	ClusterName                    string
	ClusterID                      string
	NamespaceID                    string
	Store                          *Store
	Now                            func() time.Time
	MaxPendingRequests             int
	MaxPendingPerRequester         int
	MaxPendingPerService           int
	AutoApprove                    bool
	AuthorityPrivateKey            ed25519.PrivateKey
	ClaimTTL                       time.Duration
	ServiceShareTTL                time.Duration
	GrantServicePeers              []string
	GrantServicePeersProvider      func() []string
	ConnectAccessTTL               time.Duration
	ConnectRefreshTTL              time.Duration
	Revocations                    *RevocationStore
	ShareRedemptions               *ShareRedemptionStore
	MembershipGrantTokenVerifier   MembershipGrantTokenVerifier
}

type Server struct {
	cfg ServerConfig
}

func NewServer(cfg ServerConfig) (*Server, error) {
	if cfg.ClusterName == "" || cfg.ClusterID == "" || cfg.NamespaceID == "" {
		return nil, fmt.Errorf("grant server requires cluster name, cluster id, and namespace id")
	}
	if cfg.Store == nil {
		cfg.Store = NewStore(DefaultStorePath())
	}
	if cfg.ShareRedemptions == nil {
		path := DefaultShareRedemptionStorePath()
		if cfg.Store != nil && cfg.Store.Path() != "" {
			path = filepath.Join(filepath.Dir(cfg.Store.Path()), "share-redemptions.json")
		}
		cfg.ShareRedemptions = NewShareRedemptionStore(path)
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.MaxPendingRequests <= 0 {
		cfg.MaxPendingRequests = DefaultMaxPendingRequests
	}
	if cfg.MaxPendingPerRequester <= 0 {
		cfg.MaxPendingPerRequester = DefaultMaxPendingPerRequester
	}
	if cfg.MaxPendingPerService <= 0 {
		cfg.MaxPendingPerService = DefaultMaxPendingPerService
	}
	if cfg.ConnectAccessTTL <= 0 {
		cfg.ConnectAccessTTL = DefaultConnectAccessLeaseTTL
	}
	if cfg.ConnectRefreshTTL <= 0 {
		cfg.ConnectRefreshTTL = DefaultConnectRefreshLeaseTTL
	}
	return &Server{cfg: cfg}, nil
}

func (s *Server) Register(h host.Host) {
	h.SetStreamHandler(ProtocolID, s.HandleStream)
}

func (s *Server) HandleStream(stream network.Stream) {
	defer stream.Close()
	msg, err := DecodeMessage(stream)
	if err != nil {
		_ = EncodeMessage(stream, Message{Type: TypeDenied, Version: VersionV1, RequestID: "invalid", Reason: err.Error()})
		return
	}
	resp := s.HandleMessage(msg, stream.Conn().RemotePeer())
	if err := EncodeMessage(stream, resp); err != nil {
		_ = stream.Reset()
	}
}

func (s *Server) HandleMessage(msg Message, requester peer.ID) Message {
	switch msg.Type {
	case TypeSubmit:
		return s.handleSubmit(msg, requester)
	case TypePoll:
		return s.handlePoll(msg)
	case TypeShareRedeem:
		return s.handleShareRedeem(msg, requester)
	case TypeShareMintRequest:
		return s.handleShareMint(msg, requester)
	case TypeConnectRequest:
		return s.handleConnectRequest(msg, requester)
	case TypeConnectRefresh:
		return s.handleConnectRefresh(msg)
	default:
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestID), Reason: fmt.Sprintf("unsupported grant operation %q", msg.Type)}
	}
}

func (s *Server) handleSubmit(msg Message, requester peer.ID) Message {
	if msg.ClusterID != s.cfg.ClusterID || msg.NamespaceID != s.cfg.NamespaceID {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "invalid", Reason: "grant request scope does not match authority server"}
	}
	if err := VerifyPublishLeaseRequest(PublishLeaseRequest{Version: msg.Version, Kind: PublishLeaseRequestKind, ClusterID: msg.ClusterID, NamespaceID: msg.NamespaceID, ServiceID: msg.ServiceID, ServicePublicKey: msg.ServicePublicKey, PublisherPeerID: msg.ServicePeerID, PublisherInstancePublicKey: msg.PublisherInstanceKey, RequestedCapabilities: append([]string(nil), msg.RequestedPermissions...), Nonce: msg.RequestNonce, ServiceOwnerSignature: append([]byte(nil), msg.ServiceOwnerSignature...)}); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "invalid", Reason: err.Error()}
	}
	if s.cfg.Revocations != nil {
		if revoked, _, err := s.cfg.Revocations.IsPublishRevoked(msg.ServiceID); err != nil {
			return Message{Type: TypeDenied, Version: VersionV1, RequestID: "invalid", Reason: err.Error()}
		} else if revoked {
			return Message{Type: TypeDenied, Version: VersionV1, RequestID: "invalid", Reason: fmt.Sprintf("publish revoked for service %q", msg.ServiceID)}
		}
	}
	now := s.cfg.Now().UTC()
	req := Request{
		ClusterName:           s.cfg.ClusterName,
		ClusterID:             msg.ClusterID,
		NamespaceID:           msg.NamespaceID,
		RequesterPeerID:       requester.String(),
		ServiceName:           msg.ServiceName,
		ServiceID:             msg.ServiceID,
		ServiceKind:           NormalizeServiceShareKind(msg.ServiceKind),
		ServicePublicKey:      msg.ServicePublicKey,
		ServiceOwnerSignature: append([]byte(nil), msg.ServiceOwnerSignature...),
		RequestNonce:          msg.RequestNonce,
		ServicePeerID:         msg.ServicePeerID,
		RequestedPermissions:  append([]string(nil), msg.RequestedPermissions...),
		RequestedTTLSeconds:   msg.RequestedTTLSeconds,
		RequestedAt:           now,
		ExpiresAt:             now.Add(24 * time.Hour),
	}
	if err := s.enforcePendingPolicy(req); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "invalid", Reason: err.Error()}
	}
	req, err := s.cfg.Store.CreatePending(req)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "invalid", Reason: err.Error()}
	}
	if !s.cfg.AutoApprove {
		return PendingMessage(req)
	}
	if len(s.cfg.AuthorityPrivateKey) == 0 {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: req.ID, Reason: "auto-approve requires an authority private key"}
	}
	claimTTL := time.Duration(req.RequestedTTLSeconds) * time.Second
	if s.cfg.ClaimTTL > 0 && claimTTL > s.cfg.ClaimTTL {
		claimTTL = s.cfg.ClaimTTL
	}
	shareTTL := s.cfg.ServiceShareTTL
	if shareTTL <= 0 {
		shareTTL = ServiceShareDefaultTTL
	}
	if claimTTL > 0 && shareTTL > claimTTL {
		shareTTL = claimTTL
	}
	grantServicePeers := append([]string(nil), s.cfg.GrantServicePeers...)
	if s.cfg.GrantServicePeersProvider != nil {
		grantServicePeers = append([]string(nil), s.cfg.GrantServicePeersProvider()...)
	}
	artifacts, err := BuildApprovalArtifactsWithGrantService(s.cfg.AuthorityPrivateKey, s.cfg.ClusterName, s.cfg.ClusterID, s.cfg.NamespaceID, req.ServiceName, req.ServiceID, req.ServicePeerID, req.ServiceKind, claimTTL, shareTTL, req.RequestedPermissions, req.ServicePublicKey, req.RequestNonce, req.ServiceOwnerSignature, grantServicePeers, msg.ServiceAddresses)
	if err == nil && s.cfg.Revocations != nil {
		artifacts, err = s.applyRevocationEpochsToApproval(artifacts, req.ServiceID)
	}
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: req.ID, Reason: err.Error()}
	}
	approved, err := s.cfg.Store.Approve(req.ID, artifacts.ServiceClaim, &artifacts.PublishLease, &artifacts.MembershipCapability, artifacts.ServiceShareToken)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: req.ID, Reason: err.Error()}
	}
	return ResponseForRequest(approved)
}

func (s *Server) enforcePendingPolicy(req Request) error {
	requests, err := s.cfg.Store.ListAll()
	if err != nil {
		return err
	}
	pendingTotal := 0
	pendingRequester := 0
	pendingService := 0
	for _, existing := range requests {
		if existing.Status == StatusPending && equivalentActive(existing, req) {
			return nil
		}
		if existing.ClusterID == req.ClusterID && existing.NamespaceID == req.NamespaceID && existing.ServiceID == req.ServiceID && existing.Status != StatusDenied && existing.Status != StatusExpired && existing.ServicePeerID != req.ServicePeerID {
			return fmt.Errorf("service %q already has an active grant request or claim for a different peer", req.ServiceID)
		}
		if existing.Status != StatusPending {
			continue
		}
		pendingTotal++
		if existing.RequesterPeerID == req.RequesterPeerID {
			pendingRequester++
		}
		if existing.ClusterID == req.ClusterID && existing.NamespaceID == req.NamespaceID && existing.ServiceID == req.ServiceID {
			pendingService++
		}
	}
	if pendingTotal >= s.cfg.MaxPendingRequests {
		return fmt.Errorf("too many pending grant requests: limit %d", s.cfg.MaxPendingRequests)
	}
	if pendingRequester >= s.cfg.MaxPendingPerRequester {
		return fmt.Errorf("too many pending grant requests for requester: limit %d", s.cfg.MaxPendingPerRequester)
	}
	if pendingService >= s.cfg.MaxPendingPerService {
		return fmt.Errorf("too many pending grant requests for service %q: limit %d", req.ServiceID, s.cfg.MaxPendingPerService)
	}
	return nil
}

func (s *Server) handlePoll(msg Message) Message {
	req, ok, err := s.cfg.Store.Get(msg.RequestID)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestID), Reason: err.Error()}
	}
	if !ok {
		return Message{Type: TypeExpired, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestID), Reason: "grant request not found"}
	}
	return ResponseForRequest(req)
}

func (s *Server) handleShareRedeem(msg Message, requester peer.ID) Message {
	if len(s.cfg.AuthorityPrivateKey) == 0 {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "share-redeem", Reason: "share invite redemption requires an authority private key"}
	}
	payload, err := ParseAndVerifyServiceShareToken(msg.ShareInviteToken)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "share-redeem", Reason: err.Error()}
	}
	if payload.ClusterID != s.cfg.ClusterID || payload.NamespaceID != s.cfg.NamespaceID {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: payload.JTI, Reason: "share invite scope does not match authority server"}
	}
	if err := s.validateShareInviteRevocation(payload); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: payload.JTI, Reason: err.Error()}
	}
	serverAuthority, err := authorityPublicKeyString(s.cfg.AuthorityPrivateKey)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: payload.JTI, Reason: err.Error()}
	}
	if !sameAuthorizedKeyMaterial(payload.AuthorityPublicKey, serverAuthority) {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: payload.JTI, Reason: "share invite authority does not match server"}
	}
	artifacts, err := BuildConnectLeaseArtifacts(s.cfg.AuthorityPrivateKey, payload, msg.ClientPublicKey, s.cfg.ConnectAccessTTL, s.cfg.ConnectRefreshTTL)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: payload.JTI, Reason: err.Error()}
	}
	if err := s.consumeShareInvite(payload, requester, msg.ClientPublicKey, artifacts.RefreshLease.SessionID); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: payload.JTI, Reason: err.Error()}
	}
	return Message{Type: TypeShareRedeem, Version: VersionV1, RequestID: payload.JTI, ConnectAccessLease: &artifacts.AccessLease, ConnectRefreshLease: &artifacts.RefreshLease}
}

func (s *Server) handleShareMint(msg Message, requester peer.ID) Message {
	if len(s.cfg.AuthorityPrivateKey) == 0 {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: "share invite minting requires an authority private key"}
	}
	request := ShareMintRequest{
		Version:               ShareMintRequestVersion,
		Kind:                  ShareMintRequestKind,
		ClusterID:             msg.ClusterID,
		NamespaceID:           msg.NamespaceID,
		ServiceID:             msg.ServiceID,
		ServiceKind:           NormalizeServiceShareKind(msg.ServiceKind),
		ServicePeerID:         msg.ServicePeerID,
		ServiceAddresses:      append([]string(nil), msg.ServiceAddresses...),
		RequestedTTLSeconds:   msg.RequestedTTLSeconds,
		RequestNonce:          msg.RequestNonce,
		RequestIssuedAt:       msg.RequestIssuedAt,
		ServiceOwnerSignature: append([]byte(nil), msg.ServiceOwnerSignature...),
	}
	if msg.PublishLease != nil {
		request.PublishLease = *msg.PublishLease
	}
	if request.ClusterID != s.cfg.ClusterID || request.NamespaceID != s.cfg.NamespaceID {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: "share mint request scope does not match authority server"}
	}
	if err := VerifyShareMintRequest(request); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: err.Error()}
	}
	if err := ShareMintRequestMatchesFreshness(request, s.cfg.Now()); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: err.Error()}
	}
	authorityPub := s.cfg.AuthorityPrivateKey.Public().(ed25519.PublicKey)
	if err := VerifyPublishLease(request.PublishLease, authorityPub, request.ClusterID, request.NamespaceID, request.ServiceID, request.ServicePeerID); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: err.Error()}
	}
	if !leaseHasCapability(request.PublishLease.RequestedCapabilities, capability.PermissionShareMint) {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: "publish lease does not allow share invite minting"}
	}
	if s.cfg.Revocations != nil {
		if revoked, _, err := s.cfg.Revocations.IsPublishRevoked(request.ServiceID); err != nil {
			return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: err.Error()}
		} else if revoked {
			return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: fmt.Sprintf("publish revoked for service %q", request.ServiceID)}
		}
	}
	serviceAddrs, err := validateShareMintServiceEndpoint(request.ServicePeerID, request.ServiceAddresses)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: err.Error()}
	}
	shareTTL := time.Duration(request.RequestedTTLSeconds) * time.Second
	if s.cfg.ServiceShareTTL > 0 && shareTTL > s.cfg.ServiceShareTTL {
		shareTTL = s.cfg.ServiceShareTTL
	}
	remaining := time.Until(request.PublishLease.ExpiresAt.UTC())
	if shareTTL > remaining {
		shareTTL = remaining
	}
	if shareTTL <= 0 {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: "publish lease expired"}
	}
	grantServicePeers := append([]string(nil), s.cfg.GrantServicePeers...)
	if s.cfg.GrantServicePeersProvider != nil {
		grantServicePeers = append([]string(nil), s.cfg.GrantServicePeersProvider()...)
	}
	artifacts, err := BuildShareInviteArtifactsFromLeaseWithEndpoints(s.cfg.AuthorityPrivateKey, s.cfg.ClusterName, request.PublishLease, firstNonEmpty(strings.TrimSpace(msg.ServiceName), request.ServiceID), shareTTL, grantServicePeers, request.ServicePeerID, serviceAddrs)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: err.Error()}
	}
	if artifacts.Token, err = ReissueServiceShareTokenWithKind(artifacts.Token, s.cfg.AuthorityPrivateKey, request.ServiceKind); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: err.Error()}
	}
	if artifacts.Token, err = s.applyRevocationEpochsToServiceShareToken(artifacts.Token, request.ServiceID); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), Reason: err.Error()}
	}
	return Message{Type: TypeShareMintGranted, Version: VersionV1, RequestID: fallbackRequestID(msg.RequestNonce), ServiceShareToken: artifacts.Token}
}

func (s *Server) handleConnectRequest(msg Message, requester peer.ID) Message {
	requestID := fallbackRequestID(msg.RequestID)
	if len(s.cfg.AuthorityPrivateKey) == 0 {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: requestID, Reason: "connect lease request requires an authority private key"}
	}
	if msg.ClusterID != s.cfg.ClusterID {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: requestID, Reason: "cluster id mismatch"}
	}
	if msg.NamespaceID != s.cfg.NamespaceID {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: requestID, Reason: "namespace id mismatch"}
	}
	if strings.TrimSpace(msg.ServiceID) == "" {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: requestID, Reason: "service id is required"}
	}
	if strings.TrimSpace(msg.ClientPublicKey) == "" {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: requestID, Reason: "client public key is required"}
	}
	membershipExpiry, err := s.authorizeConnectRequest(requester, msg)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: requestID, Reason: err.Error()}
	}
	var accessEpoch int64
	if s.cfg.Revocations != nil {
		if epoch, err := s.cfg.Revocations.ServiceAccessEpoch(msg.ServiceID); err == nil && epoch > 0 {
			accessEpoch = epoch
		}
	}
	accessTTL := s.cfg.ConnectAccessTTL
	refreshTTL := s.cfg.ConnectRefreshTTL
	if !membershipExpiry.IsZero() {
		remaining := time.Until(membershipExpiry.UTC())
		if remaining <= 0 {
			return Message{Type: TypeDenied, Version: VersionV1, RequestID: requestID, Reason: "namespace membership expired"}
		}
		if accessTTL > remaining {
			accessTTL = remaining
		}
		if refreshTTL > remaining {
			refreshTTL = remaining
		}
	}
	artifacts, err := BuildMemberConnectLeaseArtifacts(s.cfg.AuthorityPrivateKey, s.cfg.ClusterID, s.cfg.NamespaceID, msg.ServiceID, msg.ClientPublicKey, accessEpoch, accessTTL, refreshTTL, membershipExpiry)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: requestID, Reason: err.Error()}
	}
	return Message{Type: TypeConnectGranted, Version: VersionV1, RequestID: requestID, ConnectAccessLease: &artifacts.AccessLease, ConnectRefreshLease: &artifacts.RefreshLease}
}

func (s *Server) authorizeConnectRequest(requester peer.ID, msg Message) (time.Time, error) {
	if msg.MembershipCapability == nil && strings.TrimSpace(msg.MembershipGrantToken) == "" {
		return time.Time{}, fmt.Errorf("namespace_members policy requires a membership capability or membership invite with connect permission")
	}
	var errs []string
	if msg.MembershipCapability != nil {
		if expiry, err := s.verifyConnectMembership(*msg.MembershipCapability, requester.String()); err == nil {
			return expiry, nil
		} else {
			errs = append(errs, err.Error())
		}
	}
	if strings.TrimSpace(msg.MembershipGrantToken) != "" {
		if expiry, err := s.verifyConnectMembershipGrantToken(msg.MembershipGrantToken); err == nil {
			return expiry, nil
		} else {
			errs = append(errs, err.Error())
		}
	}
	return time.Time{}, fmt.Errorf("namespace_members policy denied connect: %s", strings.Join(errs, "; "))
}

func (s *Server) verifyConnectMembership(membership capability.MembershipCapability, requesterPeerID string) (time.Time, error) {
	if len(s.cfg.AuthorityPrivateKey) == 0 {
		return time.Time{}, fmt.Errorf("membership verification requires authority key")
	}
	authorityPub := s.cfg.AuthorityPrivateKey.Public().(ed25519.PublicKey)
	var lastErr error
	for _, subject := range []string{requesterPeerID, s.cfg.ClusterID} {
		candidateNamespaces := []string{s.cfg.NamespaceID}
		if membership.NamespaceID == "*" {
			candidateNamespaces = append(candidateNamespaces, "*")
		}
		for _, candidateNamespace := range candidateNamespaces {
			if err := capability.VerifyMembershipCapability(membership, authorityPub, s.cfg.ClusterID, candidateNamespace, subject); err != nil {
				lastErr = err
				continue
			}
			if membership.NamespaceID != s.cfg.NamespaceID && membership.NamespaceID != "*" {
				lastErr = fmt.Errorf("membership capability does not authorize namespace %q", s.cfg.NamespaceID)
				continue
			}
			if !containsConnectPermission(membership.Permissions) {
				return time.Time{}, fmt.Errorf("membership capability is missing connect permission")
			}
			return membership.ExpiresAt.UTC(), nil
		}
	}
	if lastErr != nil {
		return time.Time{}, lastErr
	}
	return time.Time{}, fmt.Errorf("membership capability rejected")
}

func (s *Server) verifyConnectMembershipGrantToken(token string) (time.Time, error) {
	if s.cfg.MembershipGrantTokenVerifier == nil {
		return time.Time{}, fmt.Errorf("membership grant token verification not configured")
	}
	if len(s.cfg.AuthorityPrivateKey) == 0 {
		return time.Time{}, fmt.Errorf("membership verification requires authority key")
	}
	authorityPub := s.cfg.AuthorityPrivateKey.Public().(ed25519.PublicKey)
	return s.cfg.MembershipGrantTokenVerifier(token, authorityPub, s.cfg.ClusterID, s.cfg.NamespaceID)
}

func containsConnectPermission(perms []string) bool {
	for _, perm := range perms {
		if perm == capability.PermissionConnect {
			return true
		}
	}
	return false
}

func (s *Server) handleConnectRefresh(msg Message) Message {
	if len(s.cfg.AuthorityPrivateKey) == 0 {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "connect-refresh", Reason: "connect lease refresh requires an authority private key"}
	}
	if msg.ConnectRefreshLease == nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "connect-refresh", Reason: "connect_refresh_lease is required"}
	}
	if msg.ConnectRefreshLease.ClusterID != s.cfg.ClusterID || msg.ConnectRefreshLease.NamespaceID != s.cfg.NamespaceID {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: msg.ConnectRefreshLease.JTI, Reason: "connect refresh lease scope does not match authority server"}
	}
	if err := s.validateConnectRefreshRevocation(*msg.ConnectRefreshLease); err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: msg.ConnectRefreshLease.JTI, Reason: err.Error()}
	}
	access, err := RefreshConnectAccessLease(s.cfg.AuthorityPrivateKey, *msg.ConnectRefreshLease, s.cfg.ConnectAccessTTL)
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: msg.ConnectRefreshLease.JTI, Reason: err.Error()}
	}
	return Message{Type: TypeConnectRefresh, Version: VersionV1, RequestID: msg.ConnectRefreshLease.JTI, ConnectAccessLease: &access}
}

func (s *Server) applyRevocationEpochsToApproval(artifacts ApprovalArtifacts, serviceID string) (ApprovalArtifacts, error) {
	if artifacts.ServiceShareToken == "" {
		return artifacts, nil
	}
	token, err := s.applyRevocationEpochsToServiceShareToken(artifacts.ServiceShareToken, serviceID)
	if err != nil {
		return ApprovalArtifacts{}, err
	}
	artifacts.ServiceShareToken = token
	return artifacts, nil
}

func (s *Server) applyRevocationEpochsToServiceShareToken(token, serviceID string) (string, error) {
	if s.cfg.Revocations == nil || token == "" {
		return token, nil
	}
	epochs, err := s.cfg.Revocations.EpochsForService(serviceID)
	if err != nil {
		return "", err
	}
	if epochs.AccessEpoch == 0 && epochs.PublishEpoch == 0 {
		return token, nil
	}
	return ReissueServiceShareTokenWithEpochs(token, s.cfg.AuthorityPrivateKey, epochs)
}

func (s *Server) validateShareInviteRevocation(payload ServiceSharePayload) error {
	if s.cfg.Revocations == nil {
		return nil
	}
	if revoked, _, err := s.cfg.Revocations.IsInviteRevoked(payload.JTI); err != nil {
		return err
	} else if revoked {
		return fmt.Errorf("share invite revoked")
	}
	epoch, err := s.cfg.Revocations.ServiceAccessEpoch(payload.TargetServiceID)
	if err != nil {
		return err
	}
	if payload.AccessEpoch < epoch {
		return fmt.Errorf("service access revoked for service %q", payload.TargetServiceID)
	}
	return nil
}

func (s *Server) consumeShareInvite(payload ServiceSharePayload, requester peer.ID, clientPublicKey, sessionID string) error {
	if s.cfg.ShareRedemptions == nil {
		return nil
	}
	thumbprint, err := ConnectClientKeyThumbprint(clientPublicKey)
	if err != nil {
		return err
	}
	if err := s.cfg.ShareRedemptions.TryConsume(ShareRedemptionRecord{JTI: payload.JTI, ClusterID: payload.ClusterID, NamespaceID: payload.NamespaceID, ServiceID: payload.TargetServiceID, RedeemedByPeerID: requester.String(), ClientKeyThumbprint: thumbprint, SessionID: sessionID, TokenExpiresAt: payload.ExpiresAt.UTC()}); err != nil {
		if err == ErrShareInviteAlreadyRedeemed {
			return fmt.Errorf("share invite already redeemed")
		}
		return err
	}
	return nil
}

func (s *Server) validateConnectRefreshRevocation(refresh ConnectRefreshLease) error {
	if s.cfg.Revocations == nil {
		return nil
	}
	if revoked, _, err := s.cfg.Revocations.IsSessionRevoked(refresh.SessionID); err != nil {
		return err
	} else if revoked {
		return fmt.Errorf("connect session revoked")
	}
	epoch, err := s.cfg.Revocations.ServiceAccessEpoch(refresh.ServiceID)
	if err != nil {
		return err
	}
	if refresh.AccessEpoch < epoch {
		return fmt.Errorf("service access revoked for service %q", refresh.ServiceID)
	}
	return nil
}

func Submit(ctx context.Context, h host.Host, info peer.AddrInfo, msg Message) (Message, error) {
	return Query(ctx, h, info, msg)
}

func Poll(ctx context.Context, h host.Host, info peer.AddrInfo, requestID string) (Message, error) {
	return Query(ctx, h, info, Message{Type: TypePoll, Version: VersionV1, RequestID: requestID})
}

func MintShareInvite(ctx context.Context, h host.Host, info peer.AddrInfo, req ShareMintRequest, displayNameHint string) (string, error) {
	resp, err := Query(ctx, h, info, Message{Type: TypeShareMintRequest, Version: VersionV1, ClusterID: req.ClusterID, NamespaceID: req.NamespaceID, ServiceName: displayNameHint, ServiceID: req.ServiceID, ServiceKind: NormalizeServiceShareKind(req.ServiceKind), PublishLease: &req.PublishLease, ServiceOwnerSignature: req.ServiceOwnerSignature, ServicePeerID: req.ServicePeerID, ServiceAddresses: append([]string(nil), req.ServiceAddresses...), RequestedTTLSeconds: req.RequestedTTLSeconds, RequestNonce: req.RequestNonce, RequestIssuedAt: req.RequestIssuedAt})
	if err != nil {
		return "", err
	}
	if resp.Type == TypeDenied || resp.Type == TypeExpired {
		return "", fmt.Errorf("%s", resp.Reason)
	}
	if resp.Type != TypeShareMintGranted || strings.TrimSpace(resp.ServiceShareToken) == "" {
		return "", fmt.Errorf("invalid share invite mint response")
	}
	return resp.ServiceShareToken, nil
}

func RedeemShareInvite(ctx context.Context, h host.Host, info peer.AddrInfo, token, clientPublicKey string) (ConnectLeaseArtifacts, error) {
	resp, err := Query(ctx, h, info, Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: token, ClientPublicKey: clientPublicKey})
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	if resp.Type == TypeDenied || resp.Type == TypeExpired {
		return ConnectLeaseArtifacts{}, fmt.Errorf("%s", resp.Reason)
	}
	if resp.Type != TypeShareRedeem || resp.ConnectAccessLease == nil || resp.ConnectRefreshLease == nil {
		return ConnectLeaseArtifacts{}, fmt.Errorf("invalid share invite redemption response")
	}
	return ConnectLeaseArtifacts{AccessLease: *resp.ConnectAccessLease, RefreshLease: *resp.ConnectRefreshLease}, nil
}

func RequestConnectLease(ctx context.Context, h host.Host, info peer.AddrInfo, clusterID, namespaceID, serviceID, clientPublicKey string, membership *capability.MembershipCapability, membershipGrantToken string) (ConnectLeaseArtifacts, error) {
	resp, err := Query(ctx, h, info, Message{Type: TypeConnectRequest, Version: VersionV1, ClusterID: clusterID, NamespaceID: namespaceID, ServiceID: serviceID, ClientPublicKey: clientPublicKey, MembershipCapability: membership, MembershipGrantToken: membershipGrantToken})
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	if resp.Type == TypeDenied || resp.Type == TypeExpired {
		return ConnectLeaseArtifacts{}, fmt.Errorf("%s", resp.Reason)
	}
	if resp.Type != TypeConnectGranted || resp.ConnectAccessLease == nil || resp.ConnectRefreshLease == nil {
		return ConnectLeaseArtifacts{}, fmt.Errorf("invalid connect lease response")
	}
	return ConnectLeaseArtifacts{AccessLease: *resp.ConnectAccessLease, RefreshLease: *resp.ConnectRefreshLease}, nil
}

func RefreshConnectLease(ctx context.Context, h host.Host, info peer.AddrInfo, refresh ConnectRefreshLease) (ConnectAccessLease, error) {
	resp, err := Query(ctx, h, info, Message{Type: TypeConnectRefresh, Version: VersionV1, ConnectRefreshLease: &refresh})
	if err != nil {
		return ConnectAccessLease{}, err
	}
	if resp.Type == TypeDenied || resp.Type == TypeExpired {
		return ConnectAccessLease{}, fmt.Errorf("%s", resp.Reason)
	}
	if resp.Type != TypeConnectRefresh || resp.ConnectAccessLease == nil {
		return ConnectAccessLease{}, fmt.Errorf("invalid connect lease refresh response")
	}
	return *resp.ConnectAccessLease, nil
}

func firstNonEmpty(v ...string) string {
	for _, item := range v {
		if strings.TrimSpace(item) != "" {
			return strings.TrimSpace(item)
		}
	}
	return ""
}

func Query(ctx context.Context, h host.Host, info peer.AddrInfo, msg Message) (Message, error) {
	if err := h.Connect(ctx, info); err != nil {
		return Message{}, err
	}
	streamCtx := network.WithAllowLimitedConn(ctx, "grant protocol stream")
	stream, err := h.NewStream(streamCtx, info.ID, ProtocolID)
	if err != nil {
		return Message{}, err
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if err := EncodeMessage(stream, msg); err != nil {
		_ = stream.Reset()
		return Message{}, err
	}
	_ = stream.CloseWrite()
	return DecodeMessage(stream)
}

func fallbackRequestID(id string) string {
	if id == "" {
		return "invalid"
	}
	return id
}
