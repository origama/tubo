package query

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/clusterinvite"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
)

const (
	ProtocolID            libprotocol.ID = "/tubo/discovery/query/1.0"
	RequestTypeList                      = "list_services"
	RequestTypeGet                       = "get_service"
	RequestTypeAnnounce                  = "announce_service"
	RequestTypeAnnounceV3                = "announce_service_v3"
	maxRequestBytes                      = 64 << 10
	maxResponseBytes                     = 1 << 20
	maxServices                          = 256
)

type Cache interface {
	Resolve(serviceName string) (*discovery.ServiceEntry, bool)
	List() []*discovery.ServiceEntry
	Add(peer.ID, string, []string, time.Duration) error
	AddV2(peer.ID, string, string, string, string, string, string, string, string, *grantspkg.GrantServiceEndpoint, []string, []string, time.Duration) error
}

type Request struct {
	Type                 string                           `json:"type"`
	Name                 string                           `json:"name,omitempty"`
	Service              *Service                         `json:"service,omitempty"`
	Announcement         *discovery.AnnouncementV3        `json:"announcement_v3,omitempty"`
	MembershipCapability *capability.MembershipCapability `json:"membership_capability,omitempty"`
	MembershipGrantToken string                           `json:"membership_grant_token,omitempty"`
}

type Metadata struct {
	ServedBy     string `json:"served_by"`
	ServedByRole string `json:"served_by_role"`
	CacheTime    string `json:"cache_time"`
}

type Service struct {
	Kind             string                          `json:"kind"`
	ClusterID        string                          `json:"cluster_id,omitempty"`
	NamespaceID      string                          `json:"namespace_id,omitempty"`
	ServiceKind      string                          `json:"service_kind,omitempty"`
	Name             string                          `json:"name"`
	ServiceID        string                          `json:"service_id,omitempty"`
	ServicePublicKey string                          `json:"service_public_key,omitempty"`
	ConnectPolicy    string                          `json:"connect_policy,omitempty"`
	GrantService     *grantspkg.GrantServiceEndpoint `json:"grant_service,omitempty"`
	PeerID           string                          `json:"peer_id"`
	Addresses        []string                        `json:"addresses"`
	DirectAddresses  []string                        `json:"direct_addresses"`
	RelayedAddresses []string                        `json:"relayed_addresses"`
	Status           string                          `json:"status"`
	Path             string                          `json:"path"`
	TTLSeconds       int64                           `json:"ttl_seconds"`
	ExpiresInSeconds int64                           `json:"expires_in_seconds"`
	Capabilities     []string                        `json:"capabilities"`
	RegisteredAt     string                          `json:"registered_at"`
}

type Response struct {
	Metadata Metadata  `json:"metadata"`
	Services []Service `json:"services,omitempty"`
	Service  *Service  `json:"service,omitempty"`
	Error    string    `json:"error,omitempty"`
}

// Option configures request handling behavior.
type Option func(*serverConfig)

type serverConfig struct {
	announcementV3AuthorityPublicKey ed25519.PublicKey
	announcementV3Contexts           []discovery.NamespaceDiscoveryContext
	membershipAuthorityPublicKey     ed25519.PublicKey
	membershipContexts               []discovery.NamespaceDiscoveryContext
}

// WithAnnouncementV3Validation enables namespace-scoped AnnouncementV3
// validation for query ingestion.
func WithAnnouncementV3Validation(authorityPublicKey ed25519.PublicKey, contexts ...discovery.NamespaceDiscoveryContext) Option {
	return func(cfg *serverConfig) {
		cfg.announcementV3AuthorityPublicKey = append(ed25519.PublicKey(nil), authorityPublicKey...)
		cfg.announcementV3Contexts = append([]discovery.NamespaceDiscoveryContext(nil), contexts...)
		cfg.membershipAuthorityPublicKey = append(ed25519.PublicKey(nil), authorityPublicKey...)
		cfg.membershipContexts = append([]discovery.NamespaceDiscoveryContext(nil), contexts...)
	}
}

func HandleStream(h host.Host, role string, cache Cache, opts ...Option) network.StreamHandler {
	cfg := serverConfig{}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	return func(stream network.Stream) {
		defer stream.Close()

		var req Request
		if err := json.NewDecoder(io.LimitReader(stream, maxRequestBytes)).Decode(&req); err != nil {
			_ = json.NewEncoder(stream).Encode(errorResponse(h, role, fmt.Sprintf("decode request: %v", err)))
			return
		}

		resp := responseForRequestWithConfig(h, role, cache, cfg, stream.Conn().RemotePeer(), req)
		if err := json.NewEncoder(stream).Encode(resp); err != nil {
			_ = stream.Reset()
			return
		}
	}
}

func responseForRequest(h host.Host, role string, cache Cache, req Request) Response {
	return responseForRequestWithConfig(h, role, cache, serverConfig{}, "", req)
}

func responseForRequestWithConfig(h host.Host, role string, cache Cache, cfg serverConfig, observedPeerID peer.ID, req Request) Response {
	resp := Response{Metadata: Metadata{ServedBy: h.ID().String(), ServedByRole: role, CacheTime: time.Now().Format(time.RFC3339)}}
	if cache == nil {
		resp.Error = "discovery cache unavailable"
		return resp
	}

	switch req.Type {
	case RequestTypeList:
		if err := validateMembershipVisibility(req, cfg, observedPeerID); err != nil {
			resp.Error = err.Error()
			return resp
		}
		entries := cache.List()
		if len(entries) > maxServices {
			entries = entries[:maxServices]
		}
		resp.Services = servicesFromEntries(entries)
		return resp
	case RequestTypeGet:
		if req.Name == "" {
			resp.Error = "missing service name"
			return resp
		}
		if err := validateMembershipVisibility(req, cfg, observedPeerID); err != nil {
			resp.Error = err.Error()
			return resp
		}
		entry, ok := cache.Resolve(req.Name)
		if !ok {
			resp.Error = "service not found"
			return resp
		}
		service := serviceFromEntry(entry)
		resp.Service = &service
		return resp
	case RequestTypeAnnounce:
		if role != "relay" {
			resp.Error = "announce_service is only accepted by relay caches"
			return resp
		}
		if req.Service == nil {
			resp.Error = "missing service payload"
			return resp
		}
		if isNamespaceScopedServiceDTO(req.Service) {
			resp.Error = "namespace-scoped announce_service requires verifiable AnnouncementV3"
			return resp
		}
		pID, err := peer.Decode(req.Service.PeerID)
		if err != nil {
			resp.Error = fmt.Sprintf("invalid service peer id: %v", err)
			return resp
		}
		if err := cache.AddV2(pID, req.Service.ClusterID, req.Service.NamespaceID, req.Service.ServiceID, req.Service.Name, req.Service.Kind, req.Service.ServiceKind, req.Service.ServicePublicKey, req.Service.ConnectPolicy, grantspkg.SanitizeGrantServiceEndpoint(req.Service.GrantService), append([]string(nil), req.Service.Addresses...), append([]string(nil), req.Service.Capabilities...), time.Duration(req.Service.TTLSeconds)*time.Second); err != nil {
			resp.Error = fmt.Sprintf("cache announce: %v", err)
			return resp
		}
		return resp
	case RequestTypeAnnounceV3:
		if req.Announcement == nil {
			resp.Error = "missing announcement payload"
			return resp
		}
		if len(cfg.announcementV3Contexts) == 0 {
			resp.Error = "announcement_v3 validation unavailable"
			return resp
		}
		peerPub, err := req.Announcement.PeerID.ExtractPublicKey()
		if err != nil {
			resp.Error = fmt.Sprintf("extract announcement signer public key: %v", err)
			return resp
		}
		validated, err := discovery.ValidateAnnouncementV3AcrossContexts(*req.Announcement, peerPub, cfg.announcementV3AuthorityPublicKey, observedPeerID, cfg.announcementV3Contexts...)
		if err != nil {
			resp.Error = fmt.Sprintf("validate announcement_v3: %v", err)
			return resp
		}
		if err := cache.AddV2(validated.PeerID, validated.ClusterID, validated.NamespaceID, validated.ServiceID, validated.ServiceName, validated.Kind, validated.ServiceKind, validated.ServicePublicKey, validated.ConnectPolicy, validated.GrantService, append([]string(nil), validated.Addresses...), append([]string(nil), validated.Capabilities...), validated.TTL); err != nil {
			resp.Error = fmt.Sprintf("cache announce: %v", err)
			return resp
		}
		return resp
	default:
		resp.Error = fmt.Sprintf("unsupported request type %q", req.Type)
		return resp
	}
}

func isNamespaceScopedServiceDTO(service *Service) bool {
	if service == nil {
		return false
	}
	return strings.TrimSpace(service.ClusterID) != "" || strings.TrimSpace(service.NamespaceID) != ""
}

func errorResponse(h host.Host, role, msg string) Response {
	return Response{Metadata: Metadata{ServedBy: h.ID().String(), ServedByRole: role, CacheTime: time.Now().Format(time.RFC3339)}, Error: msg}
}

func validateMembershipVisibility(req Request, cfg serverConfig, observedPeerID peer.ID) error {
	if len(cfg.membershipContexts) == 0 {
		return nil
	}
	var lastErr error
	if req.MembershipCapability != nil {
		if strings.TrimSpace(observedPeerID.String()) == "" {
			lastErr = fmt.Errorf("membership capability requires observed peer id")
		} else {
			for _, ctx := range cfg.membershipContexts {
				if err := validateMembershipCapabilityForContext(*req.MembershipCapability, cfg.membershipAuthorityPublicKey, ctx, observedPeerID); err == nil {
					return nil
				} else {
					lastErr = err
				}
			}
		}
	}
	if token := strings.TrimSpace(req.MembershipGrantToken); token != "" {
		for _, ctx := range cfg.membershipContexts {
			if err := validateMembershipGrantTokenForContext(token, ctx); err == nil {
				return nil
			} else {
				lastErr = err
			}
		}
	}
	if req.MembershipCapability == nil && strings.TrimSpace(req.MembershipGrantToken) == "" {
		return fmt.Errorf("membership capability missing")
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("membership capability or grant token does not authorize discovery for this namespace")
}

func validateMembershipCapabilityForContext(membership capability.MembershipCapability, authorityPub ed25519.PublicKey, ctx discovery.NamespaceDiscoveryContext, observedPeerID peer.ID) error {
	if len(authorityPub) == 0 {
		return fmt.Errorf("membership authorization unavailable")
	}
	return capability.VerifyMembershipCapability(membership, authorityPub, ctx.ClusterID, ctx.NamespaceID, observedPeerID.String())
}

func validateMembershipGrantTokenForContext(token string, ctx discovery.NamespaceDiscoveryContext) error {
	_, err := clusterinvite.VerifyMembershipGrantTokenForScope(token, ctx.ClusterID, ctx.NamespaceID)
	return err
}

func Query(ctx context.Context, h host.Host, info peer.AddrInfo, req Request) (Response, error) {
	if err := h.Connect(ctx, info); err != nil {
		return Response{}, fmt.Errorf("connect discovery query peer: %w", err)
	}
	streamCtx := network.WithAllowLimitedConn(ctx, "discovery query stream")
	stream, err := h.NewStream(streamCtx, info.ID, ProtocolID)
	if err != nil {
		return Response{}, fmt.Errorf("open discovery query stream: %w", err)
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if err := json.NewEncoder(stream).Encode(req); err != nil {
		_ = stream.Reset()
		return Response{}, fmt.Errorf("write discovery query request: %w", err)
	}
	_ = stream.CloseWrite()
	var resp Response
	if err := json.NewDecoder(io.LimitReader(stream, maxResponseBytes)).Decode(&resp); err != nil {
		return Response{}, fmt.Errorf("read discovery query response: %w", err)
	}
	return resp, nil
}

func ListServices(ctx context.Context, h host.Host, info peer.AddrInfo) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeList})
}

func ListServicesWithAuthorization(ctx context.Context, h host.Host, info peer.AddrInfo, membershipCapability *capability.MembershipCapability, membershipGrantToken string) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeList, MembershipCapability: membershipCapability, MembershipGrantToken: membershipGrantToken})
}

func GetService(ctx context.Context, h host.Host, info peer.AddrInfo, name string) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeGet, Name: name})
}

func GetServiceWithAuthorization(ctx context.Context, h host.Host, info peer.AddrInfo, name string, membershipCapability *capability.MembershipCapability, membershipGrantToken string) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeGet, Name: name, MembershipCapability: membershipCapability, MembershipGrantToken: membershipGrantToken})
}

func AnnounceService(ctx context.Context, h host.Host, info peer.AddrInfo, service Service) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeAnnounce, Service: &service})
}

func AnnounceAnnouncementV3(ctx context.Context, h host.Host, info peer.AddrInfo, announcement discovery.AnnouncementV3) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeAnnounceV3, Announcement: &announcement})
}

func servicesFromEntries(entries []*discovery.ServiceEntry) []Service {
	out := make([]Service, 0, len(entries))
	for _, entry := range entries {
		out = append(out, serviceFromEntry(entry))
	}
	return out
}

func serviceFromEntry(entry *discovery.ServiceEntry) Service {
	expiresIn := time.Until(entry.Registered.Add(entry.TTL))
	if expiresIn < 0 {
		expiresIn = 0
	}
	status := "online"
	if expiresIn <= 0 {
		status = "expired"
	}
	direct, relayed := splitAddresses(entry.Addresses)
	kind := strings.TrimSpace(entry.Kind)
	if kind == "" {
		kind = discovery.ResourceKindService
	}
	return Service{
		Kind:             kind,
		ClusterID:        entry.ClusterID,
		NamespaceID:      entry.NamespaceID,
		ServiceKind:      entry.ServiceKind,
		Name:             entry.ServiceName,
		ServiceID:        entry.ServiceID,
		ServicePublicKey: entry.ServicePublicKey,
		ConnectPolicy:    entry.ConnectPolicy,
		GrantService:     grantspkg.CloneGrantServiceEndpoint(entry.GrantService),
		PeerID:           entry.PeerID.String(),
		Addresses:        append([]string(nil), entry.Addresses...),
		DirectAddresses:  direct,
		RelayedAddresses: relayed,
		Status:           status,
		Path:             pathFromAddresses(entry.Addresses),
		TTLSeconds:       int64(entry.TTL.Seconds()),
		ExpiresInSeconds: int64(expiresIn.Seconds()),
		Capabilities:     append([]string(nil), entry.Capabilities...),
		RegisteredAt:     entry.Registered.Format(time.RFC3339),
	}
}

func splitAddresses(addresses []string) (direct []string, relayed []string) {
	for _, addr := range addresses {
		if strings.Contains(addr, "/p2p-circuit") {
			relayed = append(relayed, addr)
			continue
		}
		direct = append(direct, addr)
	}
	return direct, relayed
}

func pathFromAddresses(addresses []string) string {
	direct, relayed := splitAddresses(addresses)
	switch {
	case len(direct) > 0:
		return "direct"
	case len(relayed) > 0:
		return "relayed"
	default:
		return "unknown"
	}
}
