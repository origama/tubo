package relay

import (
	"context"
	"testing"
	"time"

	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	"github.com/origama/tubo/internal/p2p"
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

func TestRelayLimitFromConfigTreatsZeroDataAsUnlimited(t *testing.T) {
	limit := relayLimitFromConfig(5*time.Minute, 0)
	if limit == nil {
		t.Fatal("expected duration-limited relay limit")
	}
	if limit.Duration != 5*time.Minute {
		t.Fatalf("duration = %s", limit.Duration)
	}
	if limit.Data != relayUnlimitedDataBytes {
		t.Fatalf("data = %d, want unlimited sentinel %d", limit.Data, relayUnlimitedDataBytes)
	}
}

func TestRelayLimitFromConfigCanDisableAllLimits(t *testing.T) {
	if limit := relayLimitFromConfig(0, 0); limit != nil {
		t.Fatalf("limit = %#v, want nil", limit)
	}
}

func TestLoadConfigFromEnvDefaultsToUnlimitedData(t *testing.T) {
	cfg, err := LoadConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LimitDataBytes != 0 {
		t.Fatalf("LimitDataBytes = %d, want 0/unlimited", cfg.LimitDataBytes)
	}
}

func TestLoadConfigFromEnvAcceptsExplicitDataLimit(t *testing.T) {
	cfg, err := LoadConfigFromEnv(func(key string) string {
		if key == "RELAY_LIMIT_DATA_BYTES" {
			return "12345"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LimitDataBytes != 12345 {
		t.Fatalf("LimitDataBytes = %d", cfg.LimitDataBytes)
	}
}
