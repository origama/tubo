package edge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/routing"
)

func TestLoadConfigFromEnvDefaults(t *testing.T) {
	cfg, err := LoadConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadConfigFromEnv returned error: %v", err)
	}

	if cfg.HTTPListen != ":8443" {
		t.Fatalf("HTTPListen = %q, want %q", cfg.HTTPListen, ":8443")
	}
	if cfg.AdminListen != "127.0.0.1:8444" {
		t.Fatalf("AdminListen = %q, want %q", cfg.AdminListen, "127.0.0.1:8444")
	}
	if cfg.P2PListen != "/ip4/0.0.0.0/tcp/4001" {
		t.Fatalf("P2PListen = %q", cfg.P2PListen)
	}
	if cfg.BootstrapRetryInterval != 5*time.Second {
		t.Fatalf("BootstrapRetryInterval = %s, want 5s", cfg.BootstrapRetryInterval)
	}
	if cfg.DirectStreamTimeout != 750*time.Millisecond {
		t.Fatalf("DirectStreamTimeout = %s, want 750ms", cfg.DirectStreamTimeout)
	}
}

func TestLoadConfigFromEnvInvalidRetryInterval(t *testing.T) {
	_, err := LoadConfigFromEnv(func(key string) string {
		if key == "BOOTSTRAP_RETRY_INTERVAL" {
			return "nope"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestLoadConfigFromEnvInvalidDirectStreamTimeout(t *testing.T) {
	_, err := LoadConfigFromEnv(func(key string) string {
		if key == "EDGE_DIRECT_STREAM_TIMEOUT" {
			return "nope"
		}
		return ""
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestHandleDiscoveryEventAddsAndRemovesRoute(t *testing.T) {
	gw := &Gateway{routes: routing.NewRouteTable()}
	pid := peer.ID("12D3KooWTestPeer")

	gw.handleDiscoveryEvent(discovery.DiscoveryEvent{
		Type:        "added",
		ServiceName: "svc-a",
		PeerID:      pid,
	})

	route, ok := gw.routes.Match("svc-a", "/hello")
	if !ok {
		t.Fatal("expected route to be added")
	}
	if route.PeerID != pid.String() {
		t.Fatalf("route.PeerID = %q, want %q", route.PeerID, pid.String())
	}

	gw.handleDiscoveryEvent(discovery.DiscoveryEvent{
		Type:        "removed",
		ServiceName: "svc-a",
		PeerID:      pid,
	})

	if _, ok := gw.routes.Match("svc-a", "/hello"); ok {
		t.Fatal("expected route to be removed")
	}
}

func TestHandleAddRoute(t *testing.T) {
	gw := &Gateway{routes: routing.NewRouteTable()}
	req := httptest.NewRequest("POST", "/add_route", nil)
	req.Form = map[string][]string{
		"hostname":    {"demo.local"},
		"path_prefix": {"/api"},
		"service":     {"demo-service"},
	}
	w := httptest.NewRecorder()

	gw.handleAddRoute(w, req)

	if w.Code != 201 {
		t.Fatalf("status = %d, want 201", w.Code)
	}
	if _, ok := gw.routes.Match("demo.local", "/api/test"); !ok {
		t.Fatal("expected route to be stored")
	}
}

func TestGatewayDiscoveryQueryServesCachedServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	gw, stopCh, err := newGateway(ctx, "/ip4/127.0.0.1/tcp/0", "edge-query-seed", nil, 750*time.Millisecond, "", "", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer close(stopCh)
	defer gw.cache.Stop()
	defer gw.host.Close()
	if err := gw.cache.Add(gw.host.ID(), "myapi", p2p.PeerAddrs(gw.host), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "edge-query-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(gw.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := discoveryquery.ListServices(ctx, client, info)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "gateway" || len(resp.Services) != 1 || resp.Services[0].Name != "myapi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestShouldEvictDiscoveryEntryAfterOpenStreamFailure(t *testing.T) {
	entry := &discovery.ServiceEntry{ServiceName: "svc", PeerID: peer.ID("12D3KooWTestPeer"), TTL: 10 * time.Second, Registered: time.Now().Add(-6 * time.Second)}
	if !shouldEvictDiscoveryEntryAfterOpenStreamFailure(entry, errors.New("dial backoff")) {
		t.Fatal("expected stale entry to be evicted after hard open-stream failure")
	}
	fresh := &discovery.ServiceEntry{ServiceName: "svc", PeerID: peer.ID("12D3KooWTestPeer"), TTL: 10 * time.Second, Registered: time.Now().Add(-2 * time.Second)}
	if shouldEvictDiscoveryEntryAfterOpenStreamFailure(fresh, errors.New("dial backoff")) {
		t.Fatal("did not expect fresh entry to be evicted")
	}
	if shouldEvictDiscoveryEntryAfterOpenStreamFailure(entry, errors.New("some validation error")) {
		t.Fatal("did not expect non-retryable error to trigger eviction")
	}
}

func TestWaitingForFreshRelayAnnouncementClearsOnNewRegistration(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Hour)
	defer cache.Stop()
	pid := peer.ID("12D3KooWTestPeer")
	if err := cache.Add(pid, "myapi", []string{"/ip4/127.0.0.1/tcp/4001"}, 30*time.Second); err != nil {
		t.Fatalf("cache add: %v", err)
	}
	entry, ok := cache.Resolve("myapi")
	if !ok {
		t.Fatal("expected cache entry")
	}
	gw := &Gateway{cache: cache, relayRecoveryAfter: map[string]time.Time{"myapi": time.Now()}}
	if !gw.waitingForFreshRelayAnnouncement(entry) {
		t.Fatal("expected old registration to be gated")
	}
	time.Sleep(2 * time.Millisecond)
	if err := cache.Add(pid, "myapi", []string{"/ip4/127.0.0.1/tcp/4001"}, 30*time.Second); err != nil {
		t.Fatalf("cache renew: %v", err)
	}
	entry, ok = cache.Resolve("myapi")
	if !ok {
		t.Fatal("expected renewed cache entry")
	}
	if gw.waitingForFreshRelayAnnouncement(entry) {
		t.Fatal("expected fresh registration to clear gate")
	}
}

func TestHandleProxyReturnsBadGatewayWhileWaitingForFreshRelayAnnouncement(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Hour)
	defer cache.Stop()
	pid := peer.ID("12D3KooWTestPeer")
	if err := cache.Add(pid, "myapi", []string{"/ip4/127.0.0.1/tcp/4001"}, 30*time.Second); err != nil {
		t.Fatalf("cache add: %v", err)
	}
	gw := &Gateway{cache: cache, routes: routing.NewRouteTable(), relayRecoveryAfter: map[string]time.Time{"myapi": time.Now()}}
	if err := gw.routes.Add(routing.Route{Hostname: "myapi", PathPrefix: "/", ServiceName: "myapi", PeerID: pid.String()}); err != nil {
		t.Fatalf("add route: %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "http://myapi/v1/dummy", nil)
	req.Host = "myapi"
	gw.handleProxy(w, req)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
}

func TestHandleProxyEvictsStaleRouteAfterHardOpenStreamFailure(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Hour)
	defer cache.Stop()
	pid := peer.ID("12D3KooWTestPeer")
	if err := cache.Add(pid, "myapi", []string{"/ip4/127.0.0.1/tcp/4001"}, time.Second); err != nil {
		t.Fatalf("cache add: %v", err)
	}
	time.Sleep(600 * time.Millisecond)

	gw := &Gateway{
		cache:  cache,
		routes: routing.NewRouteTable(),
		openStream: func(context.Context, peer.ID) (network.Stream, string, error) {
			return nil, "relayed", errors.New("dial backoff")
		},
	}
	if err := gw.routes.Add(routing.Route{Hostname: "myapi", PathPrefix: "/", ServiceName: "myapi", PeerID: pid.String()}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://myapi/v1/dummy", nil)
	req.Host = "myapi"
	w := httptest.NewRecorder()
	gw.handleProxy(w, req)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusBadGateway)
	}
	if _, ok := gw.routes.Match("myapi", "/v1/dummy"); ok {
		t.Fatal("expected stale route to be removed after hard open-stream failure")
	}
	if _, ok := gw.cache.Resolve("myapi"); ok {
		t.Fatal("expected stale cache entry to be removed after hard open-stream failure")
	}
}

func TestOpenStreamWithRetryReResolvesDiscoveryEntryOnRetry(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Hour)
	defer cache.Stop()
	oldPeer := peer.ID("12D3KooWOldPeer")
	freshPeer := peer.ID("12D3KooWFreshPeer")
	if err := cache.Add(oldPeer, "myapi", []string{"/ip4/127.0.0.1/tcp/4001"}, 30*time.Second); err != nil {
		t.Fatalf("cache add old: %v", err)
	}

	attempts := 0
	gw := &Gateway{
		cache:               cache,
		relayRecoveryAfter:  make(map[string]time.Time),
		relayRecoveryActive: make(map[string]*relayRecoveryState),
		openStream: func(_ context.Context, pid peer.ID) (network.Stream, string, error) {
			attempts++
			if pid == oldPeer {
				if err := cache.Add(freshPeer, "myapi", []string{"/ip4/127.0.0.1/tcp/4002"}, 30*time.Second); err != nil {
					return nil, "relayed", err
				}
				return nil, "relayed", errors.New("dial backoff")
			}
			if pid == freshPeer {
				return nil, "relayed", nil
			}
			return nil, "relayed", errors.New("unexpected peer")
		},
	}

	_, path, entry, err := gw.openStreamWithRetry(context.Background(), "myapi")
	if err != nil {
		t.Fatalf("openStreamWithRetry err: %v", err)
	}
	if path != "relayed" {
		t.Fatalf("path = %q, want relayed", path)
	}
	if entry == nil || entry.PeerID != freshPeer {
		t.Fatalf("entry peer = %v, want %v", entry, freshPeer)
	}
	if attempts < 2 {
		t.Fatalf("expected at least 2 attempts, got %d", attempts)
	}
}

func TestRelayRecoverySingleFlight(t *testing.T) {
	gw := &Gateway{relayRecoveryActive: make(map[string]*relayRecoveryState)}
	leader, ch1 := gw.beginRelayRecovery("myapi")
	if !leader || ch1 == nil {
		t.Fatal("expected first recovery caller to become leader")
	}
	leader, ch2 := gw.beginRelayRecovery("myapi")
	if leader {
		t.Fatal("expected second recovery caller to wait behind leader")
	}
	if ch1 != ch2 {
		t.Fatal("expected callers for same service to share wait channel")
	}

	done := make(chan struct{})
	go func() {
		waitForRelayRecovery(context.Background(), ch2, time.Second)
		close(done)
	}()
	gw.endRelayRecovery("myapi", ch1, false)

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected waiter to be released when leader ends recovery")
	}

	leader, ch3 := gw.beginRelayRecovery("myapi")
	if !leader || ch3 == nil {
		t.Fatal("expected new leader after previous recovery completes")
	}
	if ch3 == ch1 {
		t.Fatal("expected new recovery cycle to allocate a new wait channel")
	}
}

func TestResolveServiceEntryDoesNotUseLastKnownWithoutRecovery(t *testing.T) {
	gw := &Gateway{lastKnownEntries: map[string]*discovery.ServiceEntry{"myapi": {ServiceName: "myapi", PeerID: peer.ID("12D3KooWTestPeer")}}}
	if entry, ok := gw.resolveServiceEntry("myapi", false); ok || entry != nil {
		t.Fatal("expected last-known entry to be ignored when recovery is not active")
	}
}

func TestHandleDiscoveryEventDefersExpiryOnlyDuringActiveRecovery(t *testing.T) {
	gw := &Gateway{
		routes:              routing.NewRouteTable(),
		relayRecoveryActive: make(map[string]*relayRecoveryState),
	}
	pid := peer.ID("12D3KooWTestPeer")
	if err := gw.routes.Add(routing.Route{Hostname: "myapi", PathPrefix: "/", ServiceName: "myapi", PeerID: pid.String()}); err != nil {
		t.Fatalf("add route: %v", err)
	}
	leader, waitCh := gw.beginRelayRecovery("myapi")
	if !leader {
		t.Fatal("expected recovery leader")
	}
	gw.handleDiscoveryEvent(discovery.DiscoveryEvent{Type: "removed", ServiceName: "myapi", PeerID: pid})
	if _, ok := gw.routes.Match("myapi", "/v1/dummy"); !ok {
		t.Fatal("expected route expiry to be deferred while recovery is active")
	}
	gw.endRelayRecovery("myapi", waitCh, false)
	gw.handleDiscoveryEvent(discovery.DiscoveryEvent{Type: "removed", ServiceName: "myapi", PeerID: pid})
	if _, ok := gw.routes.Match("myapi", "/v1/dummy"); ok {
		t.Fatal("expected route removal once recovery is no longer active")
	}
}

func TestEndRelayRecoveryDoesNotRemoveRouteAfterSuccessfulRecovery(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Hour)
	defer cache.Stop()
	pid := peer.ID("12D3KooWTestPeer")
	gw := &Gateway{
		cache:               cache,
		routes:              routing.NewRouteTable(),
		relayRecoveryAfter:  map[string]time.Time{"myapi": time.Now()},
		relayRecoveryActive: make(map[string]*relayRecoveryState),
		lastKnownEntries: map[string]*discovery.ServiceEntry{"myapi": {
			ServiceName: "myapi",
			PeerID:      pid,
			Addresses:   []string{"/ip4/127.0.0.1/tcp/4001"},
			TTL:         30 * time.Second,
			Registered:  time.Now(),
		}},
	}
	if err := gw.routes.Add(routing.Route{Hostname: "myapi", PathPrefix: "/", ServiceName: "myapi", PeerID: pid.String()}); err != nil {
		t.Fatalf("add route: %v", err)
	}
	leader, waitCh := gw.beginRelayRecovery("myapi")
	if !leader {
		t.Fatal("expected recovery leader")
	}
	gw.clearRelayRecoveryGate("myapi")
	gw.endRelayRecovery("myapi", waitCh, true)
	if _, ok := gw.routes.Match("myapi", "/v1/dummy"); !ok {
		t.Fatal("expected route to remain after successful recovery")
	}
}

func TestEndRelayRecoveryRemovesRouteIfServiceDoesNotReturn(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Hour)
	defer cache.Stop()
	pid := peer.ID("12D3KooWTestPeer")
	gw := &Gateway{
		cache:               cache,
		routes:              routing.NewRouteTable(),
		relayRecoveryAfter:  map[string]time.Time{"myapi": time.Now()},
		relayRecoveryActive: make(map[string]*relayRecoveryState),
	}
	if err := gw.routes.Add(routing.Route{Hostname: "myapi", PathPrefix: "/", ServiceName: "myapi", PeerID: pid.String()}); err != nil {
		t.Fatalf("add route: %v", err)
	}
	leader, waitCh := gw.beginRelayRecovery("myapi")
	if !leader {
		t.Fatal("expected recovery leader")
	}
	gw.endRelayRecovery("myapi", waitCh, false)
	if _, ok := gw.routes.Match("myapi", "/v1/dummy"); ok {
		t.Fatal("expected route removal when recovery ends without a fresh service entry")
	}
	if gw.relayRecoveryAnnouncementPending("myapi") {
		t.Fatal("expected relay recovery gate to clear when recovery ends without service return")
	}
}
