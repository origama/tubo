package query

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
)

func TestResponseForRequestListAndGet(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	pid, err := peer.Decode("12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd")
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Add(pid, "myapi", []string{"/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd", "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-test-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()

	list := responseForRequest(h, "relay", cache, Request{Type: RequestTypeList})
	if list.Metadata.ServedByRole != "relay" || len(list.Services) != 1 {
		t.Fatalf("unexpected list response: %#v", list)
	}
	if list.Services[0].Path != "direct" || len(list.Services[0].DirectAddresses) != 1 || len(list.Services[0].RelayedAddresses) != 1 {
		t.Fatalf("unexpected list service: %#v", list.Services[0])
	}

	get := responseForRequest(h, "relay", cache, Request{Type: RequestTypeGet, Name: "myapi"})
	if get.Service == nil || get.Service.Name != "myapi" {
		t.Fatalf("unexpected get response: %#v", get)
	}
	miss := responseForRequest(h, "relay", cache, Request{Type: RequestTypeGet, Name: "missing"})
	if miss.Error != "service not found" {
		t.Fatalf("unexpected miss response: %#v", miss)
	}
}

func TestResponseForRequestListAndGetRequireMembershipVisibility(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	pid, err := peer.Decode("12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd")
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.AddV2(pid, "cluster-123", "observability", "svc-123", "myapi", discovery.ResourceKindService, "http", "", "namespace_members", nil, []string{"/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}, []string{"hello-v1"}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-auth-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx := discovery.NamespaceDiscoveryContext{ClusterID: "cluster-123", NamespaceID: "observability", KeyID: "nsdk_query", Secret: bytes.Repeat([]byte{0x31}, 32)}
	clientPeerID := peer.ID("12D3KooWQueryClient")
	valid, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, SubjectPeerID: clientPeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, SubjectPeerID: clientPeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(-time.Minute)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := serverConfig{membershipAuthorityPublicKey: authorityPub, membershipContexts: []discovery.NamespaceDiscoveryContext{ctx}}
	missing := responseForRequestWithConfig(h, "relay", cache, cfg, clientPeerID, Request{Type: RequestTypeList})
	if missing.Error != "membership capability missing" {
		t.Fatalf("unexpected missing-membership response: %#v", missing)
	}
	listed := responseForRequestWithConfig(h, "relay", cache, cfg, clientPeerID, Request{Type: RequestTypeList, MembershipCapability: &valid})
	if len(listed.Services) != 1 || listed.Services[0].Name != "myapi" {
		t.Fatalf("unexpected authorized list: %#v", listed)
	}
	got := responseForRequestWithConfig(h, "relay", cache, cfg, clientPeerID, Request{Type: RequestTypeGet, Name: "myapi", MembershipCapability: &valid})
	if got.Service == nil || got.Service.Name != "myapi" {
		t.Fatalf("unexpected authorized get: %#v", got)
	}
	expiredResp := responseForRequestWithConfig(h, "relay", cache, cfg, clientPeerID, Request{Type: RequestTypeList, MembershipCapability: &expired})
	if !strings.Contains(strings.ToLower(expiredResp.Error), "expired") {
		t.Fatalf("expected expired membership error, got %#v", expiredResp)
	}
	emptyCache := discovery.NewCache(30*time.Second, time.Second)
	defer emptyCache.Stop()
	empty := responseForRequestWithConfig(h, "relay", emptyCache, cfg, clientPeerID, Request{Type: RequestTypeList, MembershipCapability: &valid})
	if len(empty.Services) != 0 || empty.Error != "" {
		t.Fatalf("unexpected empty list response: %#v", empty)
	}
	missingObserved := responseForRequestWithConfig(h, "relay", emptyCache, cfg, "", Request{Type: RequestTypeList, MembershipCapability: &valid})
	if !strings.Contains(strings.ToLower(missingObserved.Error), "observed peer") {
		t.Fatalf("expected observed peer error, got %#v", missingObserved)
	}
	wrongSubject, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, SubjectPeerID: "wrong-peer", Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	wrongSubjectResp := responseForRequestWithConfig(h, "relay", emptyCache, cfg, clientPeerID, Request{Type: RequestTypeList, MembershipCapability: &wrongSubject})
	if !strings.Contains(strings.ToLower(wrongSubjectResp.Error), "subject peer id mismatch") {
		t.Fatalf("expected subject peer mismatch, got %#v", wrongSubjectResp)
	}
	notFound := responseForRequestWithConfig(h, "relay", emptyCache, cfg, clientPeerID, Request{Type: RequestTypeGet, Name: "missing", MembershipCapability: &valid})
	if notFound.Error != "service not found" {
		t.Fatalf("unexpected empty-ns get response: %#v", notFound)
	}
}

func TestListServicesWorksOverRelayCircuitLimitedConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	relayHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "query-relay-test-relay", nil, libp2p.ForceReachabilityPublic())
	if err != nil {
		t.Fatal(err)
	}
	defer relayHost.Close()
	relayService, err := relayv2.New(relayHost)
	if err != nil {
		t.Fatal(err)
	}
	defer relayService.Close()
	relayAddrs := p2p.PeerAddrs(relayHost)
	if len(relayAddrs) == 0 {
		t.Fatal("relay has no addrs")
	}
	authority, err := p2p.NewOverlayHost(p2p.OverlayHostConfig{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "query-relay-test-authority", RelayPeers: []string{relayAddrs[0]}, ForceReachability: "private"})
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	if err := cache.Add(authority.Host.ID(), "myapi", []string{p2p.PeerAddrs(authority.Host)[0]}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	authority.Host.SetStreamHandler(ProtocolID, HandleStream(authority.Host, "authority", cache))
	authority.StartRelayReservations(ctx)
	waitUntilQueryTest(t, ctx, func() bool { return authority.HasRelayReservation() }, "relay reservation")
	relayed := firstCircuitAddrQueryTest(authority.ReachableAddrs())
	if relayed == "" {
		t.Fatalf("authority did not advertise relay circuit addr: %#v", authority.ReachableAddrs())
	}
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-relay-test-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := p2p.AddrInfoFromString(relayed)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ListServices(ctx, client, info)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "authority" || len(resp.Services) != 1 || resp.Services[0].Name != "myapi" {
		t.Fatalf("unexpected relay query response: %#v", resp)
	}
}

func firstCircuitAddrQueryTest(addrs []string) string {
	for _, addr := range addrs {
		if strings.Contains(addr, "/p2p-circuit") {
			return addr
		}
	}
	return ""
}

func waitUntilQueryTest(t *testing.T, ctx context.Context, pred func() bool, name string) {
	t.Helper()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		if pred() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timeout waiting for %s", name)
		case <-ticker.C:
		}
	}
}

func TestResponseForRequestReportsCacheUnavailable(t *testing.T) {
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-cache-unavailable-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	resp := responseForRequest(h, "relay", nil, Request{Type: RequestTypeList})
	if resp.Error != "discovery cache unavailable" {
		t.Fatalf("unexpected cache unavailable response: %#v", resp)
	}
}

func TestResponseForRequestAnnounce(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-announce-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	service := Service{Name: "myapi", ServiceKind: "tcp", PeerID: h.ID().String(), Addresses: []string{"/ip4/127.0.0.1/tcp/40123/p2p/" + h.ID().String()}, Capabilities: []string{"hello-v1", "raw-tcp-v1"}, TTLSeconds: 30}
	resp := responseForRequest(h, "relay", cache, Request{Type: RequestTypeAnnounce, Service: &service})
	if resp.Error != "" {
		t.Fatalf("unexpected announce error: %#v", resp)
	}
	if got := cache.Count(); got != 1 {
		t.Fatalf("cache count = %d, want 1", got)
	}
	entry, ok := cache.Resolve("myapi")
	if !ok || entry.PeerID != h.ID() {
		t.Fatalf("cache peer id = %s, want %s", entry.PeerID, h.ID())
	}
	if entry.ConnectPolicy != "" {
		t.Fatalf("connect policy = %q", entry.ConnectPolicy)
	}
	if entry.ServiceKind != "tcp" {
		t.Fatalf("service kind = %q", entry.ServiceKind)
	}
	if entry.ClusterID != "" || entry.NamespaceID != "" {
		t.Fatalf("scope ids = %q/%q", entry.ClusterID, entry.NamespaceID)
	}
	if len(entry.Capabilities) != 2 || entry.Capabilities[1] != "raw-tcp-v1" {
		t.Fatalf("capabilities = %#v", entry.Capabilities)
	}
	if entry.GrantService != nil {
		t.Fatalf("grant service = %#v", entry.GrantService)
	}
}

func TestResponseForRequestAnnounceRejectsNamespaceScopedDTO(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-announce-namespace-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	service := Service{ClusterID: "cluster-123", NamespaceID: "observability", Name: "myapi", ServiceKind: "tcp", PeerID: h.ID().String(), ConnectPolicy: "namespace_members", GrantService: &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/9.8.7.6/tcp/4001/p2p/12D3KooWGrant"}}, Addresses: []string{"/ip4/127.0.0.1/tcp/40123/p2p/" + h.ID().String()}, Capabilities: []string{"hello-v1", "raw-tcp-v1"}, TTLSeconds: 30}
	resp := responseForRequest(h, "relay", cache, Request{Type: RequestTypeAnnounce, Service: &service})
	if resp.Error != "namespace-scoped announce_service requires verifiable AnnouncementV3" {
		t.Fatalf("unexpected announce error: %#v", resp)
	}
	if got := cache.Count(); got != 0 {
		t.Fatalf("cache count = %d, want 0", got)
	}
}

func TestServiceFromEntryMarksExpiredFreshness(t *testing.T) {
	entry := &discovery.ServiceEntry{ServiceName: "grant-service", ServiceKind: "grant-service", PeerID: peer.ID("12D3KooWExpired"), Addresses: []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWExpired"}, Registered: time.Now().Add(-2 * time.Hour), TTL: time.Hour}
	service := serviceFromEntry(entry)
	if service.Status != "expired" {
		t.Fatalf("status = %q, want expired", service.Status)
	}
	if service.ExpiresInSeconds != 0 {
		t.Fatalf("expires_in_seconds = %d, want 0", service.ExpiresInSeconds)
	}
}

func TestResponseForRequestAnnounceRejectedByGateway(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-announce-gateway-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	service := Service{Name: "myapi", PeerID: h.ID().String(), Addresses: []string{"/ip4/127.0.0.1/tcp/40123/p2p/" + h.ID().String()}, TTLSeconds: 30}
	resp := responseForRequest(h, "gateway", cache, Request{Type: RequestTypeAnnounce, Service: &service})
	if resp.Error != "announce_service is only accepted by relay caches" {
		t.Fatalf("unexpected announce response: %#v", resp)
	}
	if got := cache.Count(); got != 0 {
		t.Fatalf("cache count = %d, want 0", got)
	}
}

func TestRequestResponseJSONRoundTrip(t *testing.T) {
	buf := new(bytes.Buffer)
	wantReq := Request{Type: RequestTypeGet, Name: "myapi"}
	if err := json.NewEncoder(buf).Encode(wantReq); err != nil {
		t.Fatal(err)
	}
	var gotReq Request
	if err := json.NewDecoder(buf).Decode(&gotReq); err != nil {
		t.Fatal(err)
	}
	if gotReq != wantReq {
		t.Fatalf("request round trip = %#v, want %#v", gotReq, wantReq)
	}

	buf.Reset()
	wantResp := Response{Metadata: Metadata{ServedBy: "12D3", ServedByRole: "relay", CacheTime: time.Now().Format(time.RFC3339)}, Services: []Service{{Name: "myapi", Path: "direct"}}}
	if err := json.NewEncoder(buf).Encode(wantResp); err != nil {
		t.Fatal(err)
	}
	var gotResp Response
	if err := json.NewDecoder(buf).Decode(&gotResp); err != nil {
		t.Fatal(err)
	}
	if gotResp.Metadata.ServedByRole != wantResp.Metadata.ServedByRole || len(gotResp.Services) != 1 || gotResp.Services[0].Name != "myapi" {
		t.Fatalf("response round trip = %#v", gotResp)
	}
}

func TestListServicesAcrossRealStream(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	server, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-server")
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	if err := cache.Add(server.ID(), "myapi", []string{p2p.PeerAddrs(server)[0]}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	server.SetStreamHandler(ProtocolID, HandleStream(server, "gateway", cache))

	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(server)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := ListServices(ctx, client, info)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "gateway" || len(resp.Services) != 1 || resp.Services[0].Name != "myapi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func buildQueryV3Fixture(t *testing.T) (discovery.AnnouncementV3, discovery.NamespaceDiscoveryContext, ed25519.PublicKey, crypto.PrivKey, peer.ID) {
	t.Helper()
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signerPriv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	signerPeerID, err := peer.IDFromPrivateKey(signerPriv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := discovery.NamespaceDiscoveryContext{ClusterID: "cluster-123", NamespaceID: "observability", KeyID: "nsdk_query_v3", Secret: bytes.Repeat([]byte{0x31}, 32)}
	service, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, SubjectPeerID: signerPeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionAnnounce}, ExpiresAt: time.Now().UTC().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	membershipBytes, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceID: service.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(service.PublicKey), PublisherPeerID: signerPeerID.String(), RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "query-v3-nonce"}, service.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "svc-current", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaseBytes, err := json.Marshal(leaseArtifacts.Lease)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := json.Marshal(leaseArtifacts.ServiceClaim)
	if err != nil {
		t.Fatal(err)
	}
	ann, err := discovery.NewAnnouncementV3(ctx, signerPeerID, 30*time.Second, discovery.AnnouncementV3Payload{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceName: "svc-current", ServiceKind: "http", ServiceID: service.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(service.PublicKey), ConnectPolicy: "namespace_members", Addresses: []string{"/ip4/127.0.0.1/tcp/40123/p2p/" + signerPeerID.String()}, MembershipCapability: membershipBytes, PublishLease: leaseBytes, ServiceClaim: claimBytes, RegisteredAt: time.Now().UTC().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ann.Sign(signerPriv); err != nil {
		t.Fatal(err)
	}
	return ann, ctx, authorityPub, signerPriv, signerPeerID
}

func TestResponseForRequestAnnounceV3UsesSharedValidation(t *testing.T) {
	ann, ctx, authorityPub, _, signerPeerID := buildQueryV3Fixture(t)
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-v3-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	cfg := serverConfig{}
	WithAnnouncementV3Validation(authorityPub, ctx)(&cfg)

	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	resp := responseForRequestWithConfig(h, "gateway", cache, cfg, signerPeerID, Request{Type: RequestTypeAnnounceV3, Announcement: &ann})
	if resp.Error != "" {
		t.Fatalf("unexpected announce_v3 error: %#v", resp)
	}
	if got := cache.Count(); got != 1 {
		t.Fatalf("cache count = %d, want 1", got)
	}

	mutated := ann
	mutated.Signature = append([]byte(nil), ann.Signature...)
	mutated.Signature[0] ^= 0xff
	resp = responseForRequestWithConfig(h, "gateway", cache, cfg, signerPeerID, Request{Type: RequestTypeAnnounceV3, Announcement: &mutated})
	if resp.Error == "" || !strings.Contains(strings.ToLower(resp.Error), "signature") {
		t.Fatalf("expected signature rejection, got %#v", resp)
	}
}

func buildQueryV3ExpiryFixture(t *testing.T, membershipTTL, claimTTL, leaseTTL, announcementTTL time.Duration) (discovery.AnnouncementV3, discovery.NamespaceDiscoveryContext, ed25519.PublicKey, peer.ID) {
	t.Helper()
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signerPriv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	signerPeerID, err := peer.IDFromPrivateKey(signerPriv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := discovery.NamespaceDiscoveryContext{ClusterID: "cluster-123", NamespaceID: "observability", KeyID: "nsdk_query_v3_expiry", Secret: bytes.Repeat([]byte{0x41}, 32)}
	service, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, SubjectPeerID: signerPeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionAnnounce}, ExpiresAt: time.Now().UTC().Add(membershipTTL)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	membershipBytes, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceID: service.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(service.PublicKey), PublisherPeerID: signerPeerID.String(), RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "query-v3-expiry-nonce"}, service.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "svc-expiry", claimTTL, leaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	leaseBytes, err := json.Marshal(leaseArtifacts.Lease)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := json.Marshal(leaseArtifacts.ServiceClaim)
	if err != nil {
		t.Fatal(err)
	}
	ann, err := discovery.NewAnnouncementV3(ctx, signerPeerID, announcementTTL, discovery.AnnouncementV3Payload{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceName: "svc-expiry", ServiceKind: "http", ServiceID: service.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(service.PublicKey), ConnectPolicy: "namespace_members", Addresses: []string{"/ip4/127.0.0.1/tcp/40123/p2p/" + signerPeerID.String()}, MembershipCapability: membershipBytes, PublishLease: leaseBytes, ServiceClaim: claimBytes, RegisteredAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ann.Sign(signerPriv); err != nil {
		t.Fatal(err)
	}
	return ann, ctx, authorityPub, signerPeerID
}

func TestResponseForRequestAnnounceV3BoundsTTLByMembershipExpiry(t *testing.T) {
	ann, ctx, authorityPub, signerPeerID := buildQueryV3ExpiryFixture(t, 200*time.Millisecond, time.Second, time.Second, 5*time.Second)
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-v3-expiry-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	cfg := serverConfig{}
	WithAnnouncementV3Validation(authorityPub, ctx)(&cfg)
	cache := discovery.NewCache(50*time.Millisecond, 10*time.Millisecond)
	defer cache.Stop()
	resp := responseForRequestWithConfig(h, "gateway", cache, cfg, signerPeerID, Request{Type: RequestTypeAnnounceV3, Announcement: &ann})
	if resp.Error != "" {
		t.Fatalf("unexpected announce_v3 error: %#v", resp)
	}
	entry, ok := cache.Resolve("svc-expiry")
	if !ok {
		t.Fatal("expected cached announcement")
	}
	if entry.TTL > 400*time.Millisecond {
		t.Fatalf("ttl = %s, want membership-bound ttl", entry.TTL)
	}
	time.Sleep(350 * time.Millisecond)
	if _, ok := cache.Resolve("svc-expiry"); ok {
		t.Fatal("expected cache entry to expire when membership expires first")
	}
}

func TestResponseForRequestAnnounceV3RejectsExpiredPublishLease(t *testing.T) {
	ann, ctx, authorityPub, signerPeerID := buildQueryV3ExpiryFixture(t, time.Second, time.Second, 150*time.Millisecond, 5*time.Second)
	time.Sleep(250 * time.Millisecond)
	h, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "query-v3-expired-lease-host")
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	cfg := serverConfig{}
	WithAnnouncementV3Validation(authorityPub, ctx)(&cfg)
	cache := discovery.NewCache(50*time.Millisecond, 10*time.Millisecond)
	defer cache.Stop()
	resp := responseForRequestWithConfig(h, "gateway", cache, cfg, signerPeerID, Request{Type: RequestTypeAnnounceV3, Announcement: &ann})
	if resp.Error == "" || !strings.Contains(strings.ToLower(resp.Error), "publish lease expired") {
		t.Fatalf("expected publish lease expiry rejection, got %#v", resp)
	}
}
