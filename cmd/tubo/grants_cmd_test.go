package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/p2p"
)

func TestGrantServiceSeedUsesRoleSpecificDefault(t *testing.T) {
	clusterID := "cluster-123"
	got := grantServiceSeed("", clusterID)
	want := "grants-" + clusterID
	if got != want {
		t.Fatalf("grantServiceSeed() = %q, want %q", got, want)
	}

	queryPeerID, err := p2p.PeerIDFromSeed(discoveryQuerySeed(clusterID, "default"))
	if err != nil {
		t.Fatal(err)
	}
	authorityPeerID, err := p2p.PeerIDFromSeed(got)
	if err != nil {
		t.Fatal(err)
	}
	if authorityPeerID == queryPeerID {
		t.Fatalf("authority peer id %s collides with discovery-query peer id", authorityPeerID)
	}
}

func TestGrantServiceSeedPreservesExplicitSeed(t *testing.T) {
	if got := grantServiceSeed(" explicit-authority-seed ", "cluster-123"); got != "explicit-authority-seed" {
		t.Fatalf("grantServiceSeed() = %q, want explicit seed", got)
	}
}

func TestGrantServicePeersForTokensPrefersRelayCircuitAddresses(t *testing.T) {
	addrs := []string{
		"/ip4/127.0.0.1/tcp/39385/p2p/12D3KooWGrant",
		"/ip4/192.168.1.44/tcp/39385/p2p/12D3KooWGrant",
		"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant",
		"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant",
	}
	got := grantServicePeersForTokens(addrs)
	want := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grantServicePeersForTokens() = %#v, want %#v", got, want)
	}
}

func TestGrantServicePeersForTokensFallsBackToRemoteDialableDirectAddresses(t *testing.T) {
	addrs := []string{
		"",
		"/ip4/127.0.0.1/tcp/39385/p2p/12D3KooWGrant",
		"/ip4/0.0.0.0/tcp/39385/p2p/12D3KooWGrant",
		"/ip6/::1/tcp/39385/p2p/12D3KooWGrant",
		"/dns4/localhost/tcp/39385/p2p/12D3KooWGrant",
		"/ip4/203.0.113.10/tcp/39385/p2p/12D3KooWGrant",
		"/dns4/grants.tubo.click/tcp/39385/p2p/12D3KooWGrant",
	}
	got := grantServicePeersForTokens(addrs)
	want := []string{
		"/ip4/203.0.113.10/tcp/39385/p2p/12D3KooWGrant",
		"/dns4/grants.tubo.click/tcp/39385/p2p/12D3KooWGrant",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grantServicePeersForTokens() = %#v, want %#v", got, want)
	}
}

func TestGrantServicePeersForTokensDropsLocalOnlyCandidates(t *testing.T) {
	addrs := []string{
		"/ip4/127.0.0.1/tcp/39385/p2p/12D3KooWGrant",
		"/ip4/0.0.0.0/tcp/39385/p2p/12D3KooWGrant",
		"/ip6/::/tcp/39385/p2p/12D3KooWGrant",
		"/dns4/localhost/tcp/39385/p2p/12D3KooWGrant",
	}
	got := grantServicePeersForTokens(addrs)
	if len(got) != 0 {
		t.Fatalf("grantServicePeersForTokens() = %#v, want empty", got)
	}
}

func TestWaitForGrantServiceDiscoveryAddrsReturnsFirstUsablePeer(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	want := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant"}
	got, err := waitForGrantServiceDiscoveryAddrs(ctx, func() []string {
		calls++
		if calls < 3 {
			return nil
		}
		return want
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("waitForGrantServiceDiscoveryAddrs() = %#v, want %#v", got, want)
	}
}

func TestWaitForGrantServiceDiscoveryAddrsAcceptsDirectUsableAddresses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	want := []string{"/ip4/172.17.0.1/tcp/40191/p2p/12D3KooWGrant"}
	got, err := waitForGrantServiceDiscoveryAddrs(ctx, func() []string { return want })
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("waitForGrantServiceDiscoveryAddrs() = %#v, want %#v", got, want)
	}
}

func TestWaitForGrantServiceDiscoveryAddrsTimesOutCleanly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := waitForGrantServiceDiscoveryAddrs(ctx, func() []string { return nil })
	if err == nil || got != nil || !strings.Contains(err.Error(), "timed out waiting for a usable reachable grant-service address") {
		t.Fatalf("waitForGrantServiceDiscoveryAddrs() = %#v, %v", got, err)
	}
}

func TestGrantServiceDiscoveryQueryPeersPrefersRelayCircuitAddressesButKeepsDirectFallbacks(t *testing.T) {
	addrs := []string{
		"/ip4/127.0.0.1/tcp/39385/p2p/12D3KooWGrant",
		"/ip4/203.0.113.10/tcp/39385/p2p/12D3KooWGrant",
		"/dns4/grants.tubo.click/tcp/39385/p2p/12D3KooWGrant",
		"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant",
	}
	got := grantServiceDiscoveryQueryPeers(addrs)
	want := []string{
		"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant",
		"/ip4/203.0.113.10/tcp/39385/p2p/12D3KooWGrant",
		"/dns4/grants.tubo.click/tcp/39385/p2p/12D3KooWGrant",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grantServiceDiscoveryQueryPeers() = %#v, want %#v", got, want)
	}
}

func TestMergeAuthorityBootstrapPeersKeepsBetterExistingPeersWhenIncomingIsWorse(t *testing.T) {
	existing := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWAuthority"}
	incoming := []string{"/ip4/203.0.113.10/tcp/39385/p2p/12D3KooWAuthority"}
	got := mergeAuthorityBootstrapPeers(existing, incoming)
	if !reflect.DeepEqual(got, []string{
		"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWAuthority",
		"/ip4/203.0.113.10/tcp/39385/p2p/12D3KooWAuthority",
	}) {
		t.Fatalf("mergeAuthorityBootstrapPeers() = %#v", got)
	}
}

func TestServiceEndpointAddrsForTokensPrefersRelayCircuitAddrs(t *testing.T) {
	servicePeerID, err := p2p.PeerIDFromSeed("service-endpoint-seed")
	if err != nil {
		t.Fatal(err)
	}
	relayPeerID, err := p2p.PeerIDFromSeed("relay-endpoint-seed")
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{Network: cfgpkg.Network{RelayPeers: []string{"/dns4/relay.tubo.click/tcp/4001/p2p/" + relayPeerID.String()}}}
	got := serviceEndpointAddrsForTokens(cfg, servicePeerID.String())
	want := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/" + relayPeerID.String() + "/p2p-circuit/p2p/" + servicePeerID.String()}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("serviceEndpointAddrsForTokens() = %#v, want %#v", got, want)
	}
}
