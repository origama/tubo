package service

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	discoveryquery "p2p-api-tunnel/internal/discovery/query"
	"p2p-api-tunnel/internal/p2p"
)

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

func TestServiceDiscoveryQueryServesOwnAnnouncement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "service-query-seed", ServiceName: "myapi", Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if app.cache == nil {
		t.Fatal("expected service cache")
	}
	if _, ok := app.currentAnnouncement(); !ok {
		t.Fatal("expected current announcement")
	}
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "service-query-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(app.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := discoveryquery.GetService(ctx, client, info, "myapi")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "attach" || resp.Service == nil || resp.Service.Name != "myapi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
