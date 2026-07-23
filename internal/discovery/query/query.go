package query

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"sort"
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

	// Opaque forwarding: relay caches without cluster validation context accept
	// AnnouncementV3 records as raw bytes and forwards them to consumers, which
	// verify signatures against their own local cluster authority.
	//
	// The relay never treats opaque records as trusted; they are transport-level
	// only. Limits below cap DoS/spam surface.
	OpaqueAnnouncementV3MaxRecords     = discovery.DefaultOpaqueAnnouncementMaxRecords
	OpaqueAnnouncementV3MaxBytes       = discovery.DefaultOpaqueAnnouncementMaxRecordBytes
	OpaqueAnnouncementV3MaxTotalBytes  = discovery.DefaultOpaqueAnnouncementMaxTotalBytes
	OpaqueAnnouncementV3MaxPeerRecords = discovery.DefaultOpaqueAnnouncementMaxPeerRecords
	OpaqueAnnouncementV3MaxPeerBytes   = discovery.DefaultOpaqueAnnouncementMaxPeerBytes
	OpaqueAnnouncementV3MaxTTL         = discovery.DefaultOpaqueAnnouncementMaxTTL
)

type Cache interface {
	Resolve(serviceName string) (*discovery.ServiceEntry, bool)
	List() []*discovery.ServiceEntry
	Add(peer.ID, string, []string, time.Duration) error
	AddV2(peer.ID, string, string, string, string, string, string, string, string, *grantspkg.GrantServiceEndpoint, []string, []string, time.Duration) error
}

// OpaqueAnnouncementV3Store is an optional relay-side store for opaque
// AnnouncementV3 records that the relay itself cannot verify (private clusters
// whose authority is not known to the relay).
//
// The relay treats these records as opaque transport payloads:
//   - it does NOT verify the announcement signature;
//   - it does NOT decrypt or interpret the payload beyond routing hints
//     (peer id, TTL);
//   - trust boundary is enforced by consumers, which re-verify the announcement
//     against their own cluster authority before surfacing it as a service.
type OpaqueAnnouncementV3Store interface {
	Put(peerID peer.ID, ann discovery.AnnouncementV3, ttl time.Duration, size int) error
	List() []discovery.OpaqueAnnouncementV3Record
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
	Metadata  Metadata  `json:"metadata"`
	Services  []Service `json:"services,omitempty"`
	Service   *Service  `json:"service,omitempty"`
	Error     string    `json:"error,omitempty"`
	Truncated bool      `json:"truncated"`

	// OpaqueAnnouncementsV3 carries raw AnnouncementV3 records forwarded by a
	// relay that lacks the cluster validation context. Consumers MUST verify
	// each announcement against their local cluster authority before treating
	// it as a trusted service. Records that fail verification are discarded.
	OpaqueAnnouncementsV3 []discovery.AnnouncementV3 `json:"opaque_announcements_v3,omitempty"`
}

// Option configures request handling behavior.
type Option func(*serverConfig)

type serverConfig struct {
	announcementV3AuthorityPublicKey ed25519.PublicKey
	announcementV3Contexts           []discovery.NamespaceDiscoveryContext
	membershipAuthorityPublicKey     ed25519.PublicKey
	membershipContexts               []discovery.NamespaceDiscoveryContext
	opaqueStore                      OpaqueAnnouncementV3Store
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

// WithOpaqueAnnouncementV3Forwarding enables opaque relay forwarding for
// AnnouncementV3 records the relay cannot verify. The relay accepts syntactically
// valid announcements bounded by size/TTL limits and returns them to consumers
// via list_services responses. Consumers must re-verify signatures against their
// own cluster authority before treating any record as trusted.
func WithOpaqueAnnouncementV3Forwarding(store OpaqueAnnouncementV3Store) Option {
	return func(cfg *serverConfig) {
		cfg.opaqueStore = store
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
		return boundedListResponse(resp, cache.List(), cfg.opaqueStore)
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
		if len(cfg.announcementV3Contexts) > 0 {
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
		}
		// Opaque forwarding path: relay has no cluster context for this
		// announcement's cluster. Accept the record as opaque bytes if a store
		// is configured and basic sanity/size/TTL limits pass. The relay is
		// NOT trusted; consumers verify the signature locally against their
		// own cluster authority before surfacing anything as a service.
		if cfg.opaqueStore == nil {
			resp.Error = "announcement_v3 validation unavailable"
			return resp
		}
		if err := acceptOpaqueAnnouncementV3(cfg.opaqueStore, observedPeerID, *req.Announcement); err != nil {
			resp.Error = fmt.Sprintf("opaque announcement_v3 rejected: %v", err)
			return resp
		}
		log.Printf("accepted opaque announcement_v3 peer=%s key_id=%s ttl=%s", req.Announcement.PeerID, req.Announcement.KeyID, req.Announcement.TTL)
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

// acceptOpaqueAnnouncementV3 validates only transport-level invariants of an
// opaque AnnouncementV3 record: version, size, TTL cap, observed peer id. It
// does NOT verify signature or decrypt payload; those checks are the
// consumer's responsibility.
//
// Trust boundary: the relay is a dumb forwarder here. Any tampering by the
// relay would be caught on the consumer by signature verification against the
// cluster authority the consumer trusts locally.
func acceptOpaqueAnnouncementV3(store OpaqueAnnouncementV3Store, observedPeerID peer.ID, ann discovery.AnnouncementV3) error {
	if strings.TrimSpace(ann.Version) != discovery.AnnouncementVersionV3 {
		return fmt.Errorf("unsupported announcement version %q", ann.Version)
	}
	if ann.PeerID == "" {
		return fmt.Errorf("announcement peer id missing")
	}
	if observedPeerID != "" && observedPeerID != ann.PeerID {
		return fmt.Errorf("peer id mismatch: got %q want %q", observedPeerID, ann.PeerID)
	}
	if ann.TTL <= 0 {
		return fmt.Errorf("non-positive ttl")
	}
	ttl := ann.TTL
	if ttl > OpaqueAnnouncementV3MaxTTL {
		ttl = OpaqueAnnouncementV3MaxTTL
	}
	if len(ann.Nonce) == 0 || len(ann.Ciphertext) == 0 || len(ann.Signature) == 0 {
		return fmt.Errorf("announcement missing envelope fields")
	}
	raw, err := ann.Marshal()
	if err != nil {
		return fmt.Errorf("marshal announcement: %w", err)
	}
	if len(raw) > OpaqueAnnouncementV3MaxBytes {
		return fmt.Errorf("announcement size %d exceeds cap %d", len(raw), OpaqueAnnouncementV3MaxBytes)
	}
	return store.Put(ann.PeerID, ann, ttl, len(raw))
}

type opaqueTruncationRecorder interface {
	RecordTruncation()
}

// boundedListResponse orders records deterministically and adds only items that
// keep encoded JSON, including the encoder newline, within the client limit.
// Validated services have priority over opaque transport records.
func boundedListResponse(resp Response, entries []*discovery.ServiceEntry, store OpaqueAnnouncementV3Store) Response {
	services := servicesFromEntries(entries)
	sort.Slice(services, func(i, j int) bool { return serviceOrderKey(services[i]) < serviceOrderKey(services[j]) })
	opaque := opaqueAnnouncementsForList(store)

	base, err := json.Marshal(resp)
	if err != nil {
		resp.Error = fmt.Sprintf("encode list response metadata: %v", err)
		return resp
	}
	baseSize := len(base)
	serviceBytes := 0
	opaqueBytes := 0

	for i, service := range services {
		if i >= maxServices {
			resp.Truncated = true
			break
		}
		encoded, err := json.Marshal(service)
		if err != nil {
			resp.Truncated = true
			continue
		}
		candidateBytes := serviceBytes + len(encoded)
		if listResponseEncodedSize(baseSize, candidateBytes, len(resp.Services)+1, opaqueBytes, len(resp.OpaqueAnnouncementsV3))+1 > maxResponseBytes {
			resp.Truncated = true
			continue
		}
		resp.Services = append(resp.Services, service)
		serviceBytes = candidateBytes
	}
	if len(services) > maxServices {
		resp.Truncated = true
	}

	for _, ann := range opaque {
		encoded, err := json.Marshal(ann)
		if err != nil {
			resp.Truncated = true
			continue
		}
		candidateBytes := opaqueBytes + len(encoded)
		if listResponseEncodedSize(baseSize, serviceBytes, len(resp.Services), candidateBytes, len(resp.OpaqueAnnouncementsV3)+1)+1 > maxResponseBytes {
			resp.Truncated = true
			continue
		}
		resp.OpaqueAnnouncementsV3 = append(resp.OpaqueAnnouncementsV3, ann)
		opaqueBytes = candidateBytes
	}

	// Defensive exact check. Formula above is exact for current struct fields,
	// but keep response bounded if future fields alter JSON layout.
	for {
		encoded, err := json.Marshal(resp)
		if err == nil && len(encoded)+1 <= maxResponseBytes {
			break
		}
		resp.Truncated = true
		switch {
		case len(resp.OpaqueAnnouncementsV3) > 0:
			resp.OpaqueAnnouncementsV3 = resp.OpaqueAnnouncementsV3[:len(resp.OpaqueAnnouncementsV3)-1]
		case len(resp.Services) > 0:
			resp.Services = resp.Services[:len(resp.Services)-1]
		default:
			resp.Error = "list response metadata exceeds response budget"
			break
		}
		if len(resp.OpaqueAnnouncementsV3) == 0 && len(resp.Services) == 0 && resp.Error != "" {
			break
		}
	}
	if resp.Truncated {
		if recorder, ok := store.(opaqueTruncationRecorder); ok {
			recorder.RecordTruncation()
		}
	}
	return resp
}

func listResponseEncodedSize(baseSize, serviceBytes, serviceCount, opaqueBytes, opaqueCount int) int {
	size := baseSize
	if serviceCount > 0 {
		size += len("services") + 6 + serviceBytes + serviceCount - 1
	}
	if opaqueCount > 0 {
		size += len("opaque_announcements_v3") + 6 + opaqueBytes + opaqueCount - 1
	}
	return size
}

func serviceOrderKey(service Service) string {
	encoded, _ := json.Marshal(service)
	return string(encoded)
}

// opaqueAnnouncementsForList returns non-expired records in deterministic
// peer-round-robin order so one publisher cannot monopolize the response head.
func opaqueAnnouncementsForList(store OpaqueAnnouncementV3Store) []discovery.AnnouncementV3 {
	if store == nil {
		return nil
	}
	records := store.List()
	groups := make(map[string][]discovery.OpaqueAnnouncementV3Record)
	peerIDs := make([]string, 0)
	for _, record := range records {
		if record.Expired() {
			continue
		}
		peerID := record.PeerID.String()
		if _, exists := groups[peerID]; !exists {
			peerIDs = append(peerIDs, peerID)
		}
		groups[peerID] = append(groups[peerID], record)
	}
	sort.Strings(peerIDs)
	for _, peerID := range peerIDs {
		sort.Slice(groups[peerID], func(i, j int) bool {
			return groups[peerID][i].Announcement.KeyID < groups[peerID][j].Announcement.KeyID
		})
	}
	out := make([]discovery.AnnouncementV3, 0, len(records))
	for round := 0; len(out) < OpaqueAnnouncementV3MaxRecords; round++ {
		added := false
		for _, peerID := range peerIDs {
			group := groups[peerID]
			if round >= len(group) {
				continue
			}
			out = append(out, group[round].Announcement)
			added = true
			if len(out) >= OpaqueAnnouncementV3MaxRecords {
				break
			}
		}
		if !added {
			break
		}
	}
	return out
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
