package main

import (
	"reflect"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/p2p"
)

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
