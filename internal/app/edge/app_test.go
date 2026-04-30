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

	"p2p-api-tunnel/internal/discovery"
	"p2p-api-tunnel/internal/routing"
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

func TestHandleProxyEvictsStaleRouteAfterHardOpenStreamFailure(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, time.Hour)
	defer cache.Stop()
	pid := peer.ID("12D3KooWTestPeer")
	if err := cache.Add(pid, "myapi", []string{"/ip4/127.0.0.1/tcp/4001"}, 20*time.Millisecond); err != nil {
		t.Fatalf("cache add: %v", err)
	}
	time.Sleep(12 * time.Millisecond)

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
