package service

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/reachability"
	statspkg "github.com/origama/tubo/internal/runtime/stats"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/ssh"
)

type fakeAnnouncementPublisher struct {
	mu    sync.Mutex
	calls int
}

func (p *fakeAnnouncementPublisher) PublishV3(context.Context, discovery.AnnouncementV3) error {
	p.mu.Lock()
	p.calls++
	p.mu.Unlock()
	return nil
}

func (p *fakeAnnouncementPublisher) CallCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func ptrTime(t time.Time) *time.Time { return &t }

func mustParseMultiaddrs(t *testing.T, raw ...string) []multiaddr.Multiaddr {
	t.Helper()
	out := make([]multiaddr.Multiaddr, 0, len(raw))
	for _, addr := range raw {
		m, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			t.Fatalf("parse multiaddr %q: %v", addr, err)
		}
		out = append(out, m)
	}
	return out
}

func TestServiceStatsEndpointExposesCollectorSnapshot(t *testing.T) {
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "service-stats-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	collector := statspkg.New(statspkg.Snapshot{Role: "service", Kind: "http", Service: "demo", ServiceID: h.ID().String(), Status: "running"})
	collector.AddRx(100)
	collector.AddTx(200)
	collector.Observe(http.StatusOK, 15*time.Millisecond)
	server := httptest.NewServer(healthMux(h, collector))
	defer server.Close()
	resp, err := http.Get(server.URL + "/statsz")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var snap statspkg.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.Role != "service" || snap.ServiceID != h.ID().String() || snap.RxBytesTotal != 100 || snap.TxBytesTotal != 200 || snap.LastLatencyMS != 15 {
		t.Fatalf("unexpected stats snapshot: %#v", snap)
	}
}

func TestAnnouncementReachabilityWakeOnRecovery(t *testing.T) {
	app := &App{announcementReachability: reachability.NewManager(reachability.ManagerConfig{Buffer: 4})}
	app.recordAnnouncementReachabilityFailure(AnnouncementBlockedRelayNotReady)
	done := make(chan bool, 1)
	go func() {
		done <- reachability.WaitForRecovered(context.Background(), app.announcementRecoveryEvents(), time.Hour)
	}()
	app.recordAnnouncementReachabilitySuccess(reachability.SuccessKindRelay)
	select {
	case recovered := <-done:
		if !recovered {
			t.Fatal("expected recovered wake")
		}
	case <-time.After(time.Second):
		t.Fatal("expected recovered wake")
	}
}

func TestAnnouncementReachabilitySuccessKinds(t *testing.T) {
	app := &App{announcementReachability: reachability.NewManager(reachability.ManagerConfig{Buffer: 4})}
	app.recordAnnouncementReachabilityFailure(AnnouncementBlockedPublishLeaseMissing)
	app.recordAnnouncementReachabilitySuccess(reachability.SuccessKindGrant)
	if got := app.announcementReachability.Snapshot().LastEvent; got == nil || got.Subject != string(reachability.SuccessKindGrant) {
		t.Fatalf("expected grant recovery event, got %#v", got)
	}
	app.recordAnnouncementReachabilityFailure(AnnouncementBlockedRelayNotReady)
	app.recordAnnouncementReachabilitySuccess(reachability.SuccessKindRelay)
	if got := app.announcementReachability.Snapshot().LastEvent; got == nil || got.Subject != string(reachability.SuccessKindRelay) {
		t.Fatalf("expected relay recovery event, got %#v", got)
	}
}

func TestAnnouncementRecoveryBroadcastWakesSubscriber(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app := &App{announcementReachability: reachability.NewManager(reachability.ManagerConfig{Buffer: 4}), announcementRecoveryBus: reachability.NewBroadcaster()}
	go app.announcementRecoveryBus.Run(ctx, app.announcementReachability.Events())
	app.recordAnnouncementReachabilityFailure(AnnouncementBlockedRelayNotReady)
	done := make(chan bool, 1)
	go func() {
		done <- reachability.WaitForRecovered(context.Background(), app.announcementRecoveryEvents(), time.Hour)
	}()
	time.Sleep(50 * time.Millisecond)
	app.recordAnnouncementReachabilitySuccess(reachability.SuccessKindRelay)
	select {
	case recovered := <-done:
		if !recovered {
			t.Fatal("expected recovery wake")
		}
	case <-time.After(time.Second):
		t.Fatal("expected recovery wake")
	}
}

func testDiscoveryContext(t *testing.T, clusterID, namespaceID string) (string, *discovery.NamespaceDiscoveryContext) {
	t.Helper()
	secret, err := cfgpkg.GenerateSecretBytes(cfgpkg.NamespaceDiscoverySecretLength)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &discovery.NamespaceDiscoveryContext{ClusterID: clusterID, NamespaceID: namespaceID, KeyID: "nsdk_test", Secret: secret}
	topic, err := discovery.DeriveNamespaceTopicV3(*ctx)
	if err != nil {
		t.Fatal(err)
	}
	return topic, ctx
}

func TestMergeRelayCircuitAddrsAddsRelayPath(t *testing.T) {
	relayID, err := p2p.PeerIDFromSeed("relay-seed-test")
	if err != nil {
		t.Fatal(err)
	}
	serviceID, err := p2p.PeerIDFromSeed("service-seed-test")
	if err != nil {
		t.Fatal(err)
	}
	relayInfo := peer.AddrInfo{ID: relayID, Addrs: mustParseMultiaddrs(t, "/ip4/172.104.128.174/tcp/4001")}
	out := mergeRelayCircuitAddrs([]string{"/ip4/127.0.0.1/tcp/4001/p2p/" + serviceID.String()}, []peer.AddrInfo{relayInfo}, serviceID)
	want := "/ip4/172.104.128.174/tcp/4001/p2p/" + relayID.String() + "/p2p-circuit/p2p/" + serviceID.String()
	found := false
	for _, addr := range out {
		if addr == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("merged addrs missing relay circuit addr %q: %v", want, out)
	}
}

func TestHasRelayReservationUsesTrackedExpiry(t *testing.T) {
	app := &App{reservationReadyUntil: time.Now().Add(30 * time.Second), relayConnected: map[peer.ID]bool{}, relayInfos: []peer.AddrInfo{{ID: peer.ID("12D3KooWRelay")}}}
	if app.hasRelayReservation() {
		t.Fatal("expected no connected relay to suppress tracked reservation")
	}
	app.relayConnected[peer.ID("12D3KooWRelay")] = true
	if !app.hasRelayReservation() {
		t.Fatal("expected tracked reservation to count as ready once relay is connected")
	}
	app.reservationReadyUntil = time.Now().Add(-time.Second)
	if app.hasRelayReservation() {
		t.Fatal("expected expired tracked reservation to be ignored")
	}
}

func TestNeedsRelayReservation_NoReservationYet(t *testing.T) {
	app := &App{
		reservationReadyUntil: time.Time{},
		relayConnected:        map[peer.ID]bool{peer.ID("12D3KooWRelay"): true},
		relayInfos:            []peer.AddrInfo{{ID: peer.ID("12D3KooWRelay")}},
	}
	if !app.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true when no reservation has been acquired")
	}
}

func TestNeedsRelayReservation_FreshReservation(t *testing.T) {
	app := &App{
		reservationReadyUntil: time.Now().Add(30 * time.Minute),
		relayConnected:        map[peer.ID]bool{peer.ID("12D3KooWRelay"): true},
		relayInfos:            []peer.AddrInfo{{ID: peer.ID("12D3KooWRelay")}},
	}
	if app.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=false for a fresh reservation outside the renewal margin")
	}
}

func TestNeedsRelayReservation_WithinRenewMargin(t *testing.T) {
	// Expires within the 10-minute margin: proactive renewal is due.
	app := &App{
		reservationReadyUntil: time.Now().Add(5 * time.Minute),
		relayConnected:        map[peer.ID]bool{peer.ID("12D3KooWRelay"): true},
		relayInfos:            []peer.AddrInfo{{ID: peer.ID("12D3KooWRelay")}},
	}
	if !app.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true when reservation is within the renewal margin")
	}
}

func TestNeedsRelayReservation_Expired(t *testing.T) {
	app := &App{
		reservationReadyUntil: time.Now().Add(-time.Second),
		relayConnected:        map[peer.ID]bool{peer.ID("12D3KooWRelay"): true},
		relayInfos:            []peer.AddrInfo{{ID: peer.ID("12D3KooWRelay")}},
	}
	if !app.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true for an expired reservation")
	}
}

func TestNeedsRelayReservation_RelayDisconnected(t *testing.T) {
	// Relay not connected: must reserve regardless of tracked expiry.
	app := &App{
		reservationReadyUntil: time.Now().Add(30 * time.Minute),
		relayConnected:        map[peer.ID]bool{},
		relayInfos:            []peer.AddrInfo{{ID: peer.ID("12D3KooWRelay")}},
	}
	if !app.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true when relay is disconnected")
	}
}

func TestNeedsRelayReservation_IgnoresLingeringCircuitAddr(t *testing.T) {
	// Regression: a lingering /p2p-circuit addr in Host.Addrs() (from autorelay)
	// must NOT suppress proactive renewal when the tracked expiry is within the
	// renewal margin. needsRelayReservation must not inspect Host.Addrs().
	//
	// hasRelayReservation() would return true here (via the addr-scan path when
	// host is non-nil), causing the old maintenance loop to skip renewal.
	// needsRelayReservation must return true regardless.
	app := &App{
		reservationReadyUntil: time.Now().Add(5 * time.Minute),
		relayConnected:        map[peer.ID]bool{peer.ID("12D3KooWRelay"): true},
		relayInfos:            []peer.AddrInfo{{ID: peer.ID("12D3KooWRelay")}},
		// host intentionally left nil: the real bug manifests when host.Addrs()
		// contains a circuit addr. With a nil host, hasRelayReservation falls
		// through to the timer — but needsRelayReservation must still fire
		// based purely on the expiry margin.
	}
	if !app.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true: expiry within margin must trigger renewal")
	}
}

func writeTestPublishLease(t *testing.T, path string, authorityPriv ed25519.PrivateKey, clusterID, namespaceID, serviceName, serviceSeed string, expiresAt time.Time) string {
	t.Helper()
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		t.Fatal(err)
	}
	req, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:        clusterID,
		NamespaceID:      namespaceID,
		ServiceID:        owner.ServiceID,
		ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:  servicePeerID.String(),
		RequestedCapabilities: []string{
			capability.PermissionAttach,
			capability.PermissionAnnounce,
			capability.PermissionShareMint,
		},
		Nonce: "lease-test-nonce",
	}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := grantspkg.BuildApprovalArtifacts(authorityPriv, "home", clusterID, namespaceID, serviceName, owner.ServiceID, servicePeerID.String(), "http", time.Hour, time.Hour, req.RequestedCapabilities, req.ServicePublicKey, req.Nonce, req.ServiceOwnerSignature)
	if err != nil {
		t.Fatal(err)
	}
	lease := artifacts.PublishLease
	if !expiresAt.IsZero() {
		lease.ExpiresAt = expiresAt
	}
	b, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return owner.ServiceID
}

func TestCurrentAnnouncementV2AdvertisesRelayGrantEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-relay-grant-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	relayID, err := p2p.PeerIDFromSeed("relay-relay-grant-seed")
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     "cluster-123",
		NamespaceID:   "default",
		SubjectPeerID: servicePeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", RelayPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/" + relayID.String()}, Autorelay: true, HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	app.reservationReadyUntil = time.Now().Add(time.Minute)
	app.relayConnected[relayID] = true
	_, payload, _, ok := app.currentAnnouncementV3()
	if !ok {
		t.Fatal("expected current announcement")
	}
	if payload.GrantService == nil || payload.GrantService.Protocol != grantspkg.ProtocolID || len(payload.GrantService.Peers) != 1 {
		t.Fatalf("grant service = %#v", payload.GrantService)
	}
	if !strings.Contains(payload.GrantService.Peers[0], "/p2p-circuit/p2p/"+app.host.ID().String()) {
		t.Fatalf("unexpected grant peer %q", payload.GrantService.Peers[0])
	}
}

func TestAnnouncementBlockDescription(t *testing.T) {
	tests := []struct {
		reason AnnouncementBlockReason
		want   string
	}{
		{AnnouncementBlockedPublisherUnavailable, "discovery publisher unavailable"},
		{AnnouncementBlockedRelayNotReady, "relay reservation not ready yet"},
		{AnnouncementBlockedPublishLeaseMissing, "publish lease missing"},
		{AnnouncementBlockedPublishLeaseExpired, "publish lease expired"},
		{AnnouncementBlockedPublishLeaseInvalid, "publish lease invalid or unverifiable"},
	}
	for _, tc := range tests {
		t.Run(string(tc.reason), func(t *testing.T) {
			if got := announcementBlockDescription(tc.reason); got != tc.want {
				t.Fatalf("announcementBlockDescription(%q) = %q, want %q", tc.reason, got, tc.want)
			}
		})
	}
}

func TestAnnouncementBlockLogDetails(t *testing.T) {
	if got := announcementBlockLogDetails(AnnouncementBlockedPublishLeaseMissing, ""); got != `reason=publish_lease_missing message="publish lease missing"` {
		t.Fatalf("announcementBlockLogDetails() = %q", got)
	}
}

func TestPublishCurrentAnnouncementV3ReportsPublisherUnavailable(t *testing.T) {
	reason, ok := (&App{}).publishCurrentAnnouncementV3(context.Background())
	if ok || reason != AnnouncementBlockedPublisherUnavailable {
		t.Fatalf("publishCurrentAnnouncementV3() = (%q, %v)", reason, ok)
	}
}

func TestCurrentAnnouncementV3ReportsRelayNotReady(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-announcement-relay-not-ready"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	relayID, err := p2p.PeerIDFromSeed("relay-announcement-relay-not-ready")
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", RelayPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/" + relayID.String()}, Autorelay: true, HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if _, _, reason, ok := app.currentAnnouncementV3(); ok || reason != AnnouncementBlockedRelayNotReady {
		t.Fatalf("currentAnnouncementV3() = (_, _, %q, %v)", reason, ok)
	}
}

func TestCurrentAnnouncementV3ReportsMissingPublishLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-announcement-lease-missing"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	relayID, err := p2p.PeerIDFromSeed("relay-announcement-lease-missing")
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	if err := os.Remove(leasePath); err != nil {
		t.Fatal(err)
	}
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", RelayPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/" + relayID.String()}, Autorelay: true, HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	app.reservationReadyUntil = time.Now().Add(time.Minute)
	app.relayConnected[relayID] = true
	if _, _, reason, ok := app.currentAnnouncementV3(); ok || reason != AnnouncementBlockedPublishLeaseMissing {
		t.Fatalf("currentAnnouncementV3() = (_, _, %q, %v)", reason, ok)
	}
}

func TestCurrentAnnouncementV3ReportsExpiredPublishLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-announcement-lease-expired"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	relayID, err := p2p.PeerIDFromSeed("relay-announcement-lease-expired")
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Now().Add(-time.Minute))
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", RelayPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/" + relayID.String()}, Autorelay: true, HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	app.reservationReadyUntil = time.Now().Add(time.Minute)
	app.relayConnected[relayID] = true
	if _, _, reason, ok := app.currentAnnouncementV3(); ok || reason != AnnouncementBlockedPublishLeaseExpired {
		t.Fatalf("currentAnnouncementV3() = (_, _, %q, %v)", reason, ok)
	}
}

func TestCurrentAnnouncementV3ReportsInvalidPublishLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-announcement-lease-invalid"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	relayID, err := p2p.PeerIDFromSeed("relay-announcement-lease-invalid")
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	if err := os.WriteFile(leasePath, []byte("not-json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", RelayPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/" + relayID.String()}, Autorelay: true, HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	app.reservationReadyUntil = time.Now().Add(time.Minute)
	app.relayConnected[relayID] = true
	if _, _, reason, ok := app.currentAnnouncementV3(); ok || reason != AnnouncementBlockedPublishLeaseInvalid {
		t.Fatalf("currentAnnouncementV3() = (_, _, %q, %v)", reason, ok)
	}
}

func TestServiceGrantEndpointIsReachableFromSecondPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-grant-endpoint-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "service-grant-endpoint-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(app.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := grantspkg.Query(ctx, client, info, grantspkg.Message{Type: grantspkg.TypePoll, Version: grantspkg.VersionV1, RequestID: "reachability-check"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "attached-service grant endpoint") {
		t.Fatalf("unexpected grant response: %#v", resp)
	}
}

func TestServiceDiscoverySubscriberConfiguresAuthorityKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-authority-subscriber-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if app.subscriber == nil || !app.subscriber.HasAuthorityPublicKey() {
		t.Fatalf("expected service subscriber authority key to be configured: %#v", app.subscriber)
	}
}

func TestServiceDiscoveryV3RequiresValidAuthorityKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	if _, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "service-missing-authority-seed", ServiceName: "myapi", ServiceID: "svc-123", Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx}); err == nil || !strings.Contains(err.Error(), "authority public key is required") {
		t.Fatalf("expected missing authority key error, got %v", err)
	}
	if _, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "service-invalid-authority-seed", ServiceName: "myapi", ServiceID: "svc-123", Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: "not-a-valid-authorized-key"}); err == nil || !strings.Contains(err.Error(), "parse authority public key") {
		t.Fatalf("expected invalid authority key error, got %v", err)
	}
}

func TestServiceDiscoveryQueryServesOwnAnnouncement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-query-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     "cluster-123",
		NamespaceID:   "default",
		SubjectPeerID: servicePeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if app.cache == nil {
		t.Fatal("expected service cache")
	}
	if reason, ok := app.publishCurrentAnnouncementV3(ctx); !ok || reason != AnnouncementReady {
		t.Fatalf("expected publishCurrentAnnouncementV3 to succeed, got (%q, %v)", reason, ok)
	}
	entry, ok := app.cache.Resolve("myapi")
	if !ok {
		t.Fatal("expected local cache entry after publish")
	}
	if entry.ServiceID != serviceID || entry.ServiceName != "myapi" {
		t.Fatalf("unexpected cache entry: %#v", entry)
	}
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "service-query-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	queryCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: client.ID().String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(app.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := discoveryquery.GetServiceWithAuthorization(ctx, client, info, "myapi", &queryCap, "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "attach" || resp.Service == nil || resp.Service.Name != "myapi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestPublishCurrentAnnouncementAuthorizationStatusAndRecovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed := "service-publish-recovery-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     "cluster-123",
		NamespaceID:   "default",
		SubjectPeerID: servicePeerID.String(),
		Permissions:   []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	leaseBytes, err := os.ReadFile(leasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(leasePath); err != nil {
		t.Fatal(err)
	}
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")

	t.Run("pending stays blocked", func(t *testing.T) {
		var statuses []RuntimeStatus
		app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath, PublishAuthorizationHandler: func(ctx context.Context, req PublishAuthorizationRequest) PublishAuthorizationResult {
			return PublishAuthorizationResult{Outcome: PublishAuthorizationOutcomePending, Message: "pending approval", RetryAfter: ptrTime(time.Now().Add(5 * time.Second))}
		}, StatusReporter: func(status RuntimeStatus) { statuses = append(statuses, status) }})
		if err != nil {
			t.Fatal(err)
		}
		defer app.host.Close()
		app.publisher = &fakeAnnouncementPublisher{}
		if reason, ok := app.publishCurrentAnnouncementV3(ctx); ok || reason != AnnouncementBlockedPublishLeaseMissing {
			t.Fatalf("expected blocked publish, got (%q, %v)", reason, ok)
		}
		if got := app.publisher.(*fakeAnnouncementPublisher).CallCount(); got != 0 {
			t.Fatalf("expected no publish calls, got %d", got)
		}
		if len(statuses) == 0 {
			t.Fatalf("expected status updates, got none")
		}
		got := statuses[len(statuses)-1]
		if got.Status != "running" || got.AdvertisementStatus != "not advertised" || got.AuthorizationStatus != "waiting for reauthorization" || !strings.Contains(strings.ToLower(got.AdvertisementReason), "publish lease file missing") {
			t.Fatalf("expected blocked publish status, got %#v", got)
		}
	})

	t.Run("renewal restores publish", func(t *testing.T) {
		var statuses []RuntimeStatus
		publisher := &fakeAnnouncementPublisher{}
		app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath, PublishAuthorizationHandler: func(ctx context.Context, req PublishAuthorizationRequest) PublishAuthorizationResult {
			if err := os.WriteFile(leasePath, leaseBytes, 0600); err != nil {
				t.Fatalf("restore lease: %v", err)
			}
			return PublishAuthorizationResult{Outcome: PublishAuthorizationOutcomeReady, Message: "renewed lease for recovery test"}
		}, StatusReporter: func(status RuntimeStatus) { statuses = append(statuses, status) }})
		if err != nil {
			t.Fatal(err)
		}
		defer app.host.Close()
		app.publisher = publisher
		if reason, ok := app.publishCurrentAnnouncementV3(ctx); !ok || reason != AnnouncementReady {
			t.Fatalf("expected recovery publish to succeed, got (%q, %v)", reason, ok)
		}
		if publisher.CallCount() != 1 {
			t.Fatalf("expected one publish call, got %d", publisher.CallCount())
		}
		if len(statuses) == 0 {
			t.Fatalf("expected status updates, got none")
		}
		got := statuses[len(statuses)-1]
		if got.Status != "running" || got.AdvertisementStatus != "advertised" || got.AuthorizationStatus != "authorized" {
			t.Fatalf("expected recovered publish status, got %#v", got)
		}
	})
}

func TestPublishCurrentAnnouncementMembershipRecoveryAfterImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed := "service-membership-recovery-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	leaseBytes, err := os.ReadFile(leasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(leasePath); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(capPath)
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	var statuses []RuntimeStatus
	publisher := &fakeAnnouncementPublisher{}
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath, PublishAuthorizationHandler: func(ctx context.Context, req PublishAuthorizationRequest) PublishAuthorizationResult {
		if req.Reason != AnnouncementBlockedMembershipCapabilityMissing && req.Reason != AnnouncementBlockedMembershipCapabilityInvalid {
			t.Fatalf("expected membership-related block, got %q", req.Reason)
		}
		membership, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
		if err != nil {
			t.Fatalf("sign membership: %v", err)
		}
		b, err := json.MarshalIndent(membership, "", "  ")
		if err != nil {
			t.Fatalf("marshal membership: %v", err)
		}
		if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
			t.Fatalf("write membership: %v", err)
		}
		if err := os.WriteFile(leasePath, leaseBytes, 0600); err != nil {
			t.Fatalf("restore lease: %v", err)
		}
		return PublishAuthorizationResult{Outcome: PublishAuthorizationOutcomeReady, Message: "membership imported"}
	}, StatusReporter: func(status RuntimeStatus) { statuses = append(statuses, status) }})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	app.publisher = publisher
	if reason, ok := app.publishCurrentAnnouncementV3(ctx); !ok || reason != AnnouncementReady {
		t.Fatalf("expected recovery publish to succeed, got (%q, %v)", reason, ok)
	}
	if publisher.CallCount() != 1 {
		t.Fatalf("expected one publish call, got %d", publisher.CallCount())
	}
	if len(statuses) == 0 {
		t.Fatalf("expected status updates, got none")
	}
	got := statuses[len(statuses)-1]
	if got.Status != "running" || got.AdvertisementStatus != "advertised" || got.AuthorizationStatus != "authorized" {
		t.Fatalf("expected running status after recovery, got %#v", got)
	}
}

func TestPublishCurrentAnnouncementReportsConcreteMembershipErrorAfterRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	seed := "service-membership-error-seed"
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	wrongMembership, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: "wrong-peer", Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	wrongMembershipBytes, err := json.MarshalIndent(wrongMembership, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	var statuses []RuntimeStatus
	var logs bytes.Buffer
	oldWriter := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(oldWriter)
	publisher := &fakeAnnouncementPublisher{}
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath, PublishAuthorizationHandler: func(ctx context.Context, req PublishAuthorizationRequest) PublishAuthorizationResult {
		if req.Reason != AnnouncementBlockedMembershipCapabilityMissing {
			t.Fatalf("expected missing membership block, got %q", req.Reason)
		}
		if err := os.WriteFile(capPath, append(wrongMembershipBytes, '\n'), 0600); err != nil {
			t.Fatalf("write membership: %v", err)
		}
		return PublishAuthorizationResult{Outcome: PublishAuthorizationOutcomeReady, Message: "publish authorization refreshed"}
	}, StatusReporter: func(status RuntimeStatus) { statuses = append(statuses, status) }})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	app.publisher = publisher
	if reason, ok := app.publishCurrentAnnouncementV3(ctx); ok || reason != AnnouncementBlockedMembershipCapabilityInvalid {
		t.Fatalf("expected final membership invalid block, got (%q, %v)", reason, ok)
	}
	if publisher.CallCount() != 0 {
		t.Fatalf("expected no publish call, got %d", publisher.CallCount())
	}
	if len(statuses) == 0 {
		t.Fatal("expected status updates")
	}
	got := statuses[len(statuses)-1]
	if got.AuthorizationStatus != "membership invalid" || !strings.Contains(got.AuthorizationReason, "subject peer id mismatch") || !strings.Contains(got.AdvertisementReason, "subject peer id mismatch") {
		t.Fatalf("expected concrete membership status, got %#v", got)
	}
	if !strings.Contains(logs.String(), "subject peer id mismatch") {
		t.Fatalf("expected log to contain concrete membership error, got %s", logs.String())
	}
}

func TestNewSkipsDiscoveryPublisherForUnlistedMode(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authorityPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "service-unlisted-seed", ServiceName: "myapi", ServiceID: "svc-123", Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: false, Visibility: "unlisted", DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if app.publisher != nil || app.cache != nil || app.stopSubscriber != nil {
		t.Fatalf("expected discovery publisher/subscriber to be skipped in unlisted mode: publisher=%#v cache=%#v stop=%#v", app.publisher, app.cache, app.stopSubscriber)
	}
}

func TestServiceGrantEndpointIsReachableWithoutDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authorityPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "service-unlisted-grant-seed", ServiceName: "myapi", ServiceID: "svc-123", Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: false, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "service-unlisted-grant-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(app.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := grantspkg.Query(ctx, client, info, grantspkg.Message{Type: grantspkg.TypePoll, Version: grantspkg.VersionV1, RequestID: "reachability-check"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "attached-service grant endpoint") {
		t.Fatalf("unexpected grant response: %#v", resp)
	}
}

func TestPublishCurrentAnnouncementV3SkipsWithoutPublisher(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authorityPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "service-unlisted-publish-seed", ServiceName: "myapi", ServiceID: "svc-123", Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: false, Visibility: "unlisted", DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if reason, ok := app.publishCurrentAnnouncementV3(ctx); ok || reason != AnnouncementBlockedPublisherUnavailable {
		t.Fatalf("expected publisher unavailable skip, got (%q, %v)", reason, ok)
	}
}

func TestServiceDiscoveryQuerySuspendsWithoutValidPublishLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-query-seed-expired"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Now().Add(-time.Minute))
	topic, dctx := testDiscoveryContext(t, "cluster-123", "default")
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryEnabled: true, DiscoveryMode: discovery.ModeNamespaceV3.String(), DiscoveryTopic: topic, DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", DiscoveryContext: dctx, AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if _, _, reason, ok := app.currentAnnouncementV3(); ok || reason != AnnouncementBlockedPublishLeaseExpired {
		t.Fatalf("expected suspended announcement, got (_, _, %q, %v)", reason, ok)
	}
}
