package query

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
)

const (
	ProtocolID          libprotocol.ID = "/tubo/discovery/query/1.0"
	RequestTypeList                    = "list_services"
	RequestTypeGet                     = "get_service"
	RequestTypeAnnounce                = "announce_service"
	maxRequestBytes                    = 64 << 10
	maxResponseBytes                   = 1 << 20
	maxServices                        = 256
)

type Cache interface {
	Resolve(serviceName string) (*discovery.ServiceEntry, bool)
	List() []*discovery.ServiceEntry
	Add(peer.ID, string, []string, time.Duration) error
	AddV2(peer.ID, string, string, string, string, string, string, *grantspkg.GrantServiceEndpoint, []string, []string, time.Duration) error
}

type Request struct {
	Type    string   `json:"type"`
	Name    string   `json:"name,omitempty"`
	Service *Service `json:"service,omitempty"`
}

type Metadata struct {
	ServedBy     string `json:"served_by"`
	ServedByRole string `json:"served_by_role"`
	CacheTime    string `json:"cache_time"`
}

type Service struct {
	Kind             string                          `json:"kind"`
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

func HandleStream(h host.Host, role string, cache Cache) network.StreamHandler {
	return func(stream network.Stream) {
		defer stream.Close()

		var req Request
		if err := json.NewDecoder(io.LimitReader(stream, maxRequestBytes)).Decode(&req); err != nil {
			_ = json.NewEncoder(stream).Encode(errorResponse(h, role, fmt.Sprintf("decode request: %v", err)))
			return
		}

		resp := responseForRequest(h, role, cache, req)
		if err := json.NewEncoder(stream).Encode(resp); err != nil {
			_ = stream.Reset()
			return
		}
	}
}

func responseForRequest(h host.Host, role string, cache Cache, req Request) Response {
	resp := Response{Metadata: Metadata{ServedBy: h.ID().String(), ServedByRole: role, CacheTime: time.Now().Format(time.RFC3339)}}
	if cache == nil {
		resp.Error = "discovery cache unavailable"
		return resp
	}

	switch req.Type {
	case RequestTypeList:
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
		if cache == nil {
			resp.Error = "discovery cache unavailable"
			return resp
		}
		pID, err := peer.Decode(req.Service.PeerID)
		if err != nil {
			resp.Error = fmt.Sprintf("invalid service peer id: %v", err)
			return resp
		}
		if err := cache.AddV2(pID, req.Service.ServiceID, req.Service.Name, req.Service.Kind, req.Service.ServiceKind, req.Service.ServicePublicKey, req.Service.ConnectPolicy, grantspkg.SanitizeGrantServiceEndpoint(req.Service.GrantService), append([]string(nil), req.Service.Addresses...), append([]string(nil), req.Service.Capabilities...), time.Duration(req.Service.TTLSeconds)*time.Second); err != nil {
			resp.Error = fmt.Sprintf("cache announce: %v", err)
			return resp
		}
		return resp
	default:
		resp.Error = fmt.Sprintf("unsupported request type %q", req.Type)
		return resp
	}
}

func errorResponse(h host.Host, role, msg string) Response {
	return Response{Metadata: Metadata{ServedBy: h.ID().String(), ServedByRole: role, CacheTime: time.Now().Format(time.RFC3339)}, Error: msg}
}

func Query(ctx context.Context, h host.Host, info peer.AddrInfo, req Request) (Response, error) {
	if err := h.Connect(ctx, info); err != nil {
		return Response{}, err
	}
	stream, err := h.NewStream(ctx, info.ID, ProtocolID)
	if err != nil {
		return Response{}, err
	}
	defer stream.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = stream.SetDeadline(deadline)
	}
	if err := json.NewEncoder(stream).Encode(req); err != nil {
		_ = stream.Reset()
		return Response{}, err
	}
	_ = stream.CloseWrite()
	var resp Response
	if err := json.NewDecoder(io.LimitReader(stream, maxResponseBytes)).Decode(&resp); err != nil {
		return Response{}, err
	}
	return resp, nil
}

func ListServices(ctx context.Context, h host.Host, info peer.AddrInfo) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeList})
}

func GetService(ctx context.Context, h host.Host, info peer.AddrInfo, name string) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeGet, Name: name})
}

func AnnounceService(ctx context.Context, h host.Host, info peer.AddrInfo, service Service) (Response, error) {
	return Query(ctx, h, info, Request{Type: RequestTypeAnnounce, Service: &service})
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
	direct, relayed := splitAddresses(entry.Addresses)
	kind := strings.TrimSpace(entry.Kind)
	if kind == "" {
		kind = discovery.ResourceKindService
	}
	return Service{
		Kind:             kind,
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
		Status:           "online",
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
