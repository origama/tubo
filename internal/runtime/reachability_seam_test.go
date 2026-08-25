package runtime

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/origama/tubo-rebuilt/internal/reachability"
	"github.com/origama/tubo-rebuilt/internal/reachability/bundle"
	"github.com/origama/tubo-rebuilt/internal/store"
)

const echoProto = protocol.ID("/tubo/test/reachability/1")

func bundleTestRoot(t *testing.T) (bundle.Root, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return bundle.Root{KeyID: "test-bootstrap-a", Role: bundle.RoleBootstrap, Public: pub}, priv
}

func loopbackTCP(t *testing.T, h host.Host) ma.Multiaddr {
	t.Helper()
	for _, a := range h.Addrs() {
		if hasTCP(a) {
			return a
		}
	}
	t.Fatal("no tcp address")
	return nil
}

func hasTCP(value ma.Multiaddr) bool {
	found := false
	ma.ForEach(value, func(c ma.Component) bool {
		if c.Protocol().Name == "tcp" {
			found = true
		}
		return true
	})
	return found
}

func p2pAddress(t *testing.T, id peer.ID, addr ma.Multiaddr) (string, error) {
	t.Helper()
	values, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: id, Addrs: []ma.Multiaddr{addr}})
	if err != nil || len(values) == 0 {
		t.Fatalf("p2p address: %#v %v", values, err)
	}
	return values[0].String(), nil
}

func addrInfoFromBundle(t *testing.T, b *bundle.Bundle) (*peer.AddrInfo, error) {
	t.Helper()
	if len(b.Addresses) == 0 {
		return nil, errors.New("bundle has no addresses")
	}
	m, err := ma.NewMultiaddr(b.Addresses[0])
	if err != nil {
		return nil, err
	}
	return peer.AddrInfoFromP2pAddr(m)
}

// TestReachabilityBundleCarrierThroughSession wires the L1 seam end to end
// deterministically and in-process: a signed test bootstrap bundle (pinned to a
// test-only bootstrap-role root) resolves to a Provider, Open acquires a real
// libp2p node.Carrier to an in-process peer, and the existing Session pump
// moves full-duplex bytes with exact half-close across that Carrier. This pins
// A0 exit criterion 6 (Reachability testable as a carrier provider) with no
// policy decision in the seam.
func TestReachabilityBundleCarrierThroughSession(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	root, priv := bundleTestRoot(t)
	server, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	server.SetStreamHandler(echoProto, func(s core.Stream) {
		defer s.Close()
		buf := make([]byte, 4096)
		for {
			n, rerr := s.Read(buf)
			if n > 0 {
				if _, werr := s.Write(buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	})

	tcpAddr := loopbackTCP(t, server)
	addrStr, err := p2pAddress(t, server.ID(), tcpAddr)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	b := &bundle.Bundle{
		Version:   1,
		Kind:      bundle.KindBootstrap,
		RootKeyID: root.KeyID,
		PeerID:    server.ID().String(),
		Addresses: []string{addrStr},
		NotBefore: now.Add(-time.Hour),
		NotAfter:  now.Add(time.Hour),
	}
	blob, err := bundle.Sign(b, priv)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := bundle.ParseVerify(blob, []bundle.Root{root}, now)
	if err != nil {
		t.Fatalf("test bundle failed verification: %v", err)
	}
	info, err := addrInfoFromBundle(t, verified)
	if err != nil {
		t.Fatal(err)
	}
	provider := reachability.New(*info)

	client, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	carrier, err := provider.Open(ctx, client, echoProto)
	if err != nil {
		t.Fatalf("provider.Open failed: %v", err)
	}
	defer carrier.Close()

	appNear, app := pairDuplex()
	sp := newTestSession(t, sessionConfig{
		SessionID:  "reach-session",
		PipeID:     "pip_test",
		Scope:      "test/default",
		TargetConn: appNear,
		IsClient:   true,
	})
	if !sp.SetActiveCarrier(carrier) {
		t.Fatal("session rejected active reachability carrier")
	}
	done := make(chan struct{})
	go func() { sp.runClientSession(); close(done) }()

	payload := bytes.Repeat([]byte("via-reachability-carrier-"), 20)
	go func() {
		_, _ = app.Write(payload)
		_ = app.CloseWrite()
	}()
	got := drainStream(t, app)
	if !bytes.Equal(got, payload) {
		t.Fatalf("session echoed %d bytes, want %d", len(got), len(payload))
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session did not complete after reachability carrier half-close")
	}
}

// TestOpenOverlayHostZeroConfigFailsClosed verifies the wiring in the host
// acquisition seam (openOverlayHost -> relayOrBootstrap): with no user overlay
// and no production trust material, zero-config resolution fails closed with
// the deterministic ErrNoBootstrapBundle — no overlay fallback, no hardcoded
// address, no host opened.
func TestOpenOverlayHostZeroConfigFailsClosed(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, _, err := OpenOverlayHost(ctx, state, "zero-config"); err == nil || !errors.Is(err, reachability.ErrNoBootstrapBundle) {
		t.Fatalf("expected deterministic ErrNoBootstrapBundle, got %v", err)
	}
}
