package edge

import (
	"net/http/httptest"
	"testing"
	"time"

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
