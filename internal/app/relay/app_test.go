package relay

import (
	"context"
	"testing"
	"time"

	discoveryquery "p2p-api-tunnel/internal/discovery/query"
	"p2p-api-tunnel/internal/p2p"
)

func TestRelayDiscoveryQueryServesCachedServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "relay-query-seed", EnableDiscoveryPubSub: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if app.cache == nil {
		t.Fatal("expected relay cache")
	}
	if err := app.cache.Add(app.host.ID(), "myapi", p2p.PeerAddrs(app.host), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "relay-query-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(app.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := discoveryquery.ListServices(ctx, client, info)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "relay" || len(resp.Services) != 1 || resp.Services[0].Name != "myapi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
