package node

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	swarm "github.com/libp2p/go-libp2p/p2p/net/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	testRelayPeer = "12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME"
	testOwnerPeer = "12D3KooWRJCtSj2fWsnyHYdpcmQoHwDzVzr39VmFrRwmJrwwW57G"
)

func TestCircuitAddressCanonicalRelayStructure(t *testing.T) {
	owner, err := peer.Decode(testOwnerPeer)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		relay string
		want  string
	}{
		{
			name:  "production DNS full relay address",
			relay: "/dns4/relay.tubo.click/tcp/4001/p2p/" + testRelayPeer,
			want:  "/dns4/relay.tubo.click/tcp/4001/p2p/" + testRelayPeer + "/p2p-circuit/p2p/" + testOwnerPeer,
		},
		{
			name:  "production IP full relay address",
			relay: "/ip4/172.232.189.160/tcp/4001/p2p/" + testRelayPeer,
			want:  "/ip4/172.232.189.160/tcp/4001/p2p/" + testRelayPeer + "/p2p-circuit/p2p/" + testOwnerPeer,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := CircuitAddress(test.relay, owner)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("CircuitAddress() = %q, want %q", got, test.want)
			}
			assertCircuitShape(t, got, testRelayPeer, testOwnerPeer)
		})
	}
}

func TestRelayAddressCanonicalizesTerminalPeerAndRejectsMismatch(t *testing.T) {
	relay, err := peer.Decode(testRelayPeer)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := peer.Decode(testOwnerPeer)
	if err != nil {
		t.Fatal(err)
	}
	base, err := ma.NewMultiaddr("/dns4/relay.tubo.click/tcp/4001")
	if err != nil {
		t.Fatal(err)
	}
	terminal, err := ma.NewMultiaddr("/dns4/relay.tubo.click/tcp/4001/p2p/" + testRelayPeer)
	if err != nil {
		t.Fatal(err)
	}
	want := terminal.String()
	for _, address := range []ma.Multiaddr{base, terminal} {
		got, err := RelayAddress(peer.AddrInfo{ID: relay, Addrs: []ma.Multiaddr{address}})
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("RelayAddress(%q) = %q, want %q", address, got, want)
		}
	}
	mismatched, err := ma.NewMultiaddr("/dns4/relay.tubo.click/tcp/4001/p2p/" + owner.String())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := RelayAddress(peer.AddrInfo{ID: relay, Addrs: []ma.Multiaddr{mismatched}}); err == nil {
		t.Fatalf("mismatched relay peer produced %q", got)
	}
}

func TestRelayInfoStripsTerminalPeer(t *testing.T) {
	info, err := RelayInfo("/dns4/relay.tubo.click/tcp/4001/p2p/" + testRelayPeer)
	if err != nil {
		t.Fatal(err)
	}
	if info.ID.String() != testRelayPeer || len(info.Addrs) != 1 || info.Addrs[0].String() != "/dns4/relay.tubo.click/tcp/4001" {
		t.Fatalf("RelayInfo() = %+v", info)
	}
}

func TestCircuitAddressRejectsMalformedRelayOrOwner(t *testing.T) {
	owner, err := peer.Decode(testOwnerPeer)
	if err != nil {
		t.Fatal(err)
	}
	malformedRelays := []string{
		"not-a-multiaddr",
		"/dns4/relay.tubo.click/tcp/4001",
		"/dns4/relay.tubo.click/tcp/4001/p2p/" + testRelayPeer + "/p2p/" + testRelayPeer,
		"/dns4/relay.tubo.click/tcp/4001/p2p/" + testRelayPeer + "/p2p/" + testOwnerPeer,
		"/dns4/relay.tubo.click/tcp/4001/p2p/" + testRelayPeer + "/p2p-circuit",
	}
	for _, relay := range malformedRelays {
		if got, err := CircuitAddress(relay, owner); err == nil {
			t.Fatalf("CircuitAddress(%q) = %q, want error", relay, got)
		}
	}
	if got, err := CircuitAddress("/dns4/relay.tubo.click/tcp/4001/p2p/"+testRelayPeer, peer.ID("not-a-peer")); err == nil {
		t.Fatalf("malformed owner produced %q", got)
	}
}

func assertCircuitShape(t *testing.T, address, relayPeer, ownerPeer string) {
	t.Helper()
	value, err := ma.NewMultiaddr(address)
	if err != nil {
		t.Fatal(err)
	}
	var peers []string
	circuits := 0
	ma.ForEach(value, func(component ma.Component) bool {
		switch component.Protocol().Code {
		case ma.P_P2P:
			peers = append(peers, component.Value())
		case ma.P_CIRCUIT:
			circuits++
		}
		return true
	})
	if len(peers) != 2 || peers[0] != relayPeer || peers[1] != ownerPeer || circuits != 1 {
		t.Fatalf("non-canonical circuit peers=%v circuits=%d", peers, circuits)
	}
}

func TestEnsureRelayClearsDialBackoffAndReserves(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	relay, err := Open(ctx, Options{KeyFile: filepath.Join(t.TempDir(), "relay.key"), Listen: "/ip4/127.0.0.1/tcp/0", RelayService: true})
	if err != nil {
		t.Fatal(err)
	}
	defer relay.Close()
	client, err := Open(ctx, Options{KeyFile: filepath.Join(t.TempDir(), "client.key"), Listen: "/ip4/127.0.0.1/tcp/0", Private: true})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var relayAddress string
	for _, address := range relay.Addrs() {
		if strings.Contains(address.String(), "/ip4/127.0.0.1/") {
			relayAddress = fmt.Sprintf("%s/p2p/%s", address, relay.ID())
			break
		}
	}
	if relayAddress == "" {
		t.Fatal("relay loopback address missing")
	}
	info, err := RelayInfo(relayAddress)
	if err != nil {
		t.Fatal(err)
	}
	if err := EnsureRelay(ctx, client, *info, true); err != nil {
		t.Fatal(err)
	}
	if err := client.Network().ClosePeer(info.ID); err != nil {
		t.Fatal(err)
	}
	value, ok := client.Network().(*swarm.Swarm)
	if !ok {
		t.Fatalf("network type %T", client.Network())
	}
	for _, address := range info.Addrs {
		value.Backoff().AddBackoff(info.ID, address)
	}
	// With DCUtR/AutoNAT, direct Connect may bypass backoff; self-heal via
	// EnsureRelay (which clears backoff) is the primary contract.
	_ = client.Connect(ctx, *info)
	if err := EnsureRelay(ctx, client, *info, true); err != nil {
		t.Fatalf("self-heal: %v", err)
	}
	if !PeerConnected(client, info.ID) {
		t.Fatal("relay not connected after self-heal")
	}
}
