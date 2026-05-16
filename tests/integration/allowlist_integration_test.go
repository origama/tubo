package integration_test

import (
	"context"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo/internal/p2p"
)

func TestPeerAllowlistBlocksAndAllowsConnections(t *testing.T) {
	allowedID, err := p2p.PeerIDFromSeed("allowlist-allowed-seed")
	if err != nil {
		t.Fatal(err)
	}
	rogueID, err := p2p.PeerIDFromSeed("allowlist-rogue-seed")
	if err != nil {
		t.Fatal(err)
	}

	relay := mustHostWithAllowlist(t, "allowlist-relay-seed", map[peer.ID]struct{}{allowedID: {}})
	defer relay.Close()
	relayAddr := peerAddrInfo(t, relay)

	allowed := mustHost(t, "allowlist-allowed-seed")
	defer allowed.Close()
	rogueOutbound := mustHostWithAllowlist(t, "allowlist-rogue-outbound-seed", map[peer.ID]struct{}{rogueID: {}})
	defer rogueOutbound.Close()
	rogueInbound := mustHost(t, "allowlist-rogue-inbound-seed")
	defer rogueInbound.Close()

	if err := connectHost(allowed, relayAddr); err != nil {
		t.Fatalf("allowed peer should connect to relay: %v", err)
	}
	waitForConn(t, allowed, relayAddr.ID, true)

	if err := connectHost(rogueOutbound, relayAddr); err == nil {
		t.Log("rogue outbound connect returned nil; verifying connection does not stick")
	}
	waitForConn(t, rogueOutbound, relayAddr.ID, false)

	if err := connectHost(rogueInbound, relayAddr); err == nil {
		t.Log("rogue inbound connect returned nil; verifying connection does not stick")
	}
	waitForConn(t, rogueInbound, relayAddr.ID, false)
}

func mustHost(t *testing.T, seed string) host.Host {
	t.Helper()
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", seed, nil)
	if err != nil {
		t.Fatalf("create host %s: %v", seed, err)
	}
	return h
}

func mustHostWithAllowlist(t *testing.T, seed string, allowed map[peer.ID]struct{}) host.Host {
	t.Helper()
	h, err := p2p.NewHostWithSeedAndPSKAndOptions(
		"/ip4/127.0.0.1/tcp/0",
		seed,
		nil,
		libp2p.ConnectionGater(p2p.NewPeerAllowlistConnectionGater(allowed)),
	)
	if err != nil {
		t.Fatalf("create allowlisted host %s: %v", seed, err)
	}
	return h
}

func peerAddrInfo(t *testing.T, h host.Host) peer.AddrInfo {
	t.Helper()
	addrs := p2p.PeerAddrs(h)
	if len(addrs) == 0 {
		t.Fatal("host has no addrs")
	}
	info, err := p2p.AddrInfoFromString(addrs[0])
	if err != nil {
		t.Fatalf("addr info: %v", err)
	}
	return info
}

func connectHost(src host.Host, addr peer.AddrInfo) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return src.Connect(ctx, addr)
}

func waitForConn(t *testing.T, h host.Host, peerID peer.ID, want bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got := len(h.Network().ConnsToPeer(peerID)) > 0
		if got == want {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got := len(h.Network().ConnsToPeer(peerID)) > 0
	if got != want {
		t.Fatalf("connection state to %s = %t want %t", peerID, got, want)
	}
}
