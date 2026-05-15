package grants

import (
	"context"
	"strings"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/origama/tubo/internal/p2p"
)

func TestGrantServiceProducesRelayCircuitAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	relayHost, err := p2p.NewHostWithSeedAndPSKAndOptions(
		"/ip4/127.0.0.1/tcp/0",
		"grant-relay-test-relay",
		nil,
		libp2p.ForceReachabilityPublic(),
	)
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
		t.Fatal("relay has no advertised addresses")
	}

	grantOverlay, err := p2p.NewOverlayHost(p2p.OverlayHostConfig{
		Listen:            "/ip4/127.0.0.1/tcp/0",
		Seed:              "grant-relay-test-authority",
		RelayPeers:        []string{relayAddrs[0]},
		Autorelay:         false,
		HolePunching:      false,
		ForceReachability: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer grantOverlay.Close()
	store := NewStore(t.TempDir() + "/requests.json")
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(grantOverlay.Host)
	grantOverlay.StartRelayReservations(ctx)

	waitUntil(t, ctx, func() bool { return grantOverlay.HasRelayReservation() }, "relay reservation")
	time.Sleep(500 * time.Millisecond)
	relayedAddr := firstCircuitAddr(grantOverlay.ReachableAddrs())
	if relayedAddr == "" {
		t.Fatalf("no relay circuit address in reachable addrs: %#v", grantOverlay.ReachableAddrs())
	}
	t.Logf("relayed grant addr: %s", relayedAddr)
	if !strings.Contains(relayedAddr, relayHost.ID().String()) || !strings.Contains(relayedAddr, grantOverlay.Host.ID().String()) {
		t.Fatalf("relay circuit address does not include relay and grant peer ids: %s", relayedAddr)
	}

	directOnlyClient, err := p2p.NewOverlayHost(p2p.OverlayHostConfig{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "grant-relay-test-direct-client"})
	if err != nil {
		t.Fatal(err)
	}
	defer directOnlyClient.Close()
	badDirect, err := p2p.AddrInfoFromString("/ip4/127.0.0.1/tcp/1/p2p/" + grantOverlay.Host.ID().String())
	if err != nil {
		t.Fatal(err)
	}
	shortCtx, shortCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	_, err = Submit(shortCtx, directOnlyClient.Host, badDirect, validSubmit())
	shortCancel()
	if err == nil {
		t.Fatal("direct-only unreachable grant address unexpectedly succeeded")
	}

	info, err := p2p.AddrInfoFromString(relayedAddr)
	if err != nil {
		t.Fatalf("relay-aware grant address should be parseable: %v", err)
	}
	if info.ID != grantOverlay.Host.ID() {
		t.Fatalf("relay-aware grant address resolves to %s, want %s", info.ID, grantOverlay.Host.ID())
	}
}

func firstCircuitAddr(addrs []string) string {
	for _, addr := range addrs {
		if strings.Contains(addr, "/p2p-circuit") {
			return addr
		}
	}
	return ""
}

func waitUntil(t *testing.T, ctx context.Context, pred func() bool, name string) {
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
