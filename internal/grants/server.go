package grants

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	DefaultMaxPendingRequests     = 1024
	DefaultMaxPendingPerRequester = 16
	DefaultMaxPendingPerService   = 4
)

type ServerConfig struct {
	ClusterName            string
	ClusterID              string
	NamespaceID            string
	Store                  *Store
	Now                    func() time.Time
	MaxPendingRequests     int
	MaxPendingPerRequester int
	MaxPendingPerService   int
	AutoApprove            bool
	AuthorityPrivateKey    ed25519.PrivateKey
	ClaimTTL               time.Duration
	ServiceShareTTL        time.Duration
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
	now := s.cfg.Now().UTC()
	req := Request{
		ClusterName:           s.cfg.ClusterName,
		ClusterID:             msg.ClusterID,
		NamespaceID:           msg.NamespaceID,
		RequesterPeerID:       requester.String(),
		ServiceName:           msg.ServiceName,
		ServiceID:             msg.ServiceID,
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
	artifacts, err := BuildApprovalArtifacts(s.cfg.AuthorityPrivateKey, s.cfg.ClusterName, s.cfg.ClusterID, s.cfg.NamespaceID, req.ServiceName, req.ServiceID, req.ServicePeerID, claimTTL, shareTTL, req.RequestedPermissions, req.ServicePublicKey, req.RequestNonce, req.ServiceOwnerSignature)
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

func Submit(ctx context.Context, h host.Host, info peer.AddrInfo, msg Message) (Message, error) {
	return Query(ctx, h, info, msg)
}

func Poll(ctx context.Context, h host.Host, info peer.AddrInfo, requestID string) (Message, error) {
	return Query(ctx, h, info, Message{Type: TypePoll, Version: VersionV1, RequestID: requestID})
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
