package grants

import (
	"context"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

type ServerConfig struct {
	ClusterName string
	ClusterID   string
	NamespaceID string
	Store       *Store
	Now         func() time.Time
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
	now := s.cfg.Now().UTC()
	req, err := s.cfg.Store.CreatePending(Request{
		ClusterName:          s.cfg.ClusterName,
		ClusterID:            msg.ClusterID,
		NamespaceID:          msg.NamespaceID,
		RequesterPeerID:      requester.String(),
		ServiceName:          msg.ServiceName,
		ServiceID:            msg.ServiceID,
		ServicePeerID:        msg.ServicePeerID,
		RequestedPermissions: append([]string(nil), msg.RequestedPermissions...),
		RequestedTTLSeconds:  msg.RequestedTTLSeconds,
		RequestedAt:          now,
		ExpiresAt:            now.Add(24 * time.Hour),
	})
	if err != nil {
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: "invalid", Reason: err.Error()}
	}
	return PendingMessage(req)
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
	stream, err := h.NewStream(ctx, info.ID, ProtocolID)
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
