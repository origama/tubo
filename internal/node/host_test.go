package node

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	swarm "github.com/libp2p/go-libp2p/p2p/net/swarm"
)

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
	if err := client.Connect(ctx, *info); err == nil || !strings.Contains(err.Error(), "dial backoff") {
		t.Fatalf("expected dial backoff, got %v", err)
	}
	if err := EnsureRelay(ctx, client, *info, true); err != nil {
		t.Fatalf("self-heal: %v", err)
	}
	if !PeerConnected(client, info.ID) {
		t.Fatal("relay not connected after self-heal")
	}
}
