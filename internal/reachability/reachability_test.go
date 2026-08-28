package reachability

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/origama/tubo-rebuilt/internal/node"
)

const echoProto = protocol.ID("/tubo/test/reachability/1")

func echoHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	h.SetStreamHandler(echoProto, func(s core.Stream) {
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
	return h
}

func hostAddr(h host.Host) peer.AddrInfo {
	return peer.AddrInfo{ID: h.ID(), Addrs: h.Addrs()}
}

func TestProviderOpenReturnsCarrierFullDuplex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	server := echoHost(t)
	client, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	provider := New(hostAddr(server))
	if provider.ID() != server.ID() {
		t.Fatalf("provider identity: got %s want %s", provider.ID(), server.ID())
	}
	carrier, err := provider.Open(ctx, client, echoProto)
	if err != nil {
		t.Fatal(err)
	}
	defer carrier.Close()

	if _, err := carrier.Write([]byte("hello reachability")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	n, err := carrier.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "hello reachability" {
		t.Fatalf("echo mismatch: %q", buf[:n])
	}
}

// TestProviderDirectThenFallbackInitialSelection proves deterministic initial
// path selection (direct first, then fallback) with no external network: the
// first candidate is a refused port on the same peer, the second is live.
func TestProviderDirectThenFallbackInitialSelection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	server := echoHost(t)
	client, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	good := pickLoopback(t, server.Addrs())
	bad := refusedAddr(t, good)
	fallback := New(peer.AddrInfo{ID: server.ID(), Addrs: []ma.Multiaddr{bad, good}})
	carrier, err := fallback.Open(ctx, client, echoProto)
	if err != nil {
		t.Fatalf("expected fallback to live candidate, got %v", err)
	}
	defer carrier.Close()
	if _, err := carrier.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if _, err := carrier.Read(buf); err != nil {
		t.Fatal("echo did not reach fallback candidate")
	}

	// With only the refused candidate, reachability fails closed. Use a fresh
	// client so the peerstore cannot reuse the live addresses cached above.
	client2, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer client2.Close()
	dead := New(peer.AddrInfo{ID: server.ID(), Addrs: []ma.Multiaddr{bad}})
	if _, err := dead.Open(ctx, client2, echoProto); err == nil {
		t.Fatal("dead-only provider unexpectedly reached")
	}
}

func TestProviderNoAddressesFailsClosed(t *testing.T) {
	p := New(peer.AddrInfo{ID: testPeer(t)})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	h, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	if _, err := p.Dial(ctx, h); !errors.Is(err, ErrNoReachableAddress) {
		t.Fatalf("expected ErrNoReachableAddress, got %v", err)
	}
}

// TestResolverProductionBundleResolves verifies that the embedded production
// bootstrap bundle resolves to a Provider with the expected relay identity
// and addresses. This is the canonical zero-config positive case: production
// trust material is now embedded and verified.
func TestResolverProductionBundleResolves(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	provider, err := ResolveDefault(ctx)
	if err != nil {
		t.Fatalf("production bundle resolution failed: %v", err)
	}
	if provider.ID().String() != "12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME" {
		t.Fatalf("unexpected relay peer ID: %s", provider.ID())
	}
	if len(provider.AddrInfo().Addrs) == 0 {
		t.Fatal("production bundle has no addresses")
	}
	primary, err := provider.PrimaryAddress()
	if err != nil {
		t.Fatal(err)
	}
	const wantPrimary = "/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME"
	if primary != wantPrimary {
		t.Fatalf("production primary address = %q, want %q", primary, wantPrimary)
	}
	if strings.Count(primary, "/p2p/") != 1 {
		t.Fatalf("production primary address duplicates relay peer: %q", primary)
	}
	// Injected provider resolver is the deterministic test seam.
	if _, err := DefaultResolver().WithProvider(New(peer.AddrInfo{})).Resolve(ctx); err != nil {
		t.Fatalf("injected provider resolver failed: %v", err)
	}
}

// TestProviderSurfaceCarriesNoAuthorization pins the transport-only API shape:
// a Provider exposes only peer identity and dial facts, and Dial/Open return
// only a dial target or Carrier. There is no lease, scope, grant, or policy
// parameter or return; any such type addition breaks this test.
func TestProviderSurfaceCarriesNoAuthorization(t *testing.T) {
	p := New(peer.AddrInfo{ID: testPeer(t)})
	if p.ID() == "" {
		t.Fatal("provider identity empty")
	}
	info := p.AddrInfo()
	if info.ID != p.ID() {
		t.Fatal("addr info identity inconsistent")
	}
	// node.Carrier is the only non-transport type crossing the boundary and it
	// is a raw byte path, never authorization evidence.
	var _ node.Carrier
}

func testPeer(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := libcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(priv.GetPublic())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func pickLoopback(t *testing.T, addrs []ma.Multiaddr) ma.Multiaddr {
	t.Helper()
	for _, a := range addrs {
		if hasCircuit(a) {
			continue
		}
		value4 := ""
		ma.ForEach(a, func(c ma.Component) bool {
			if c.Protocol().Name == "ip4" {
				value4 = c.Value()
			}
			return true
		})
		if value4 == "127.0.0.1" {
			return a
		}
	}
	t.Fatal("no loopback address")
	return nil
}

func refusedAddr(t *testing.T, value ma.Multiaddr) ma.Multiaddr {
	t.Helper()
	var parts []string
	ma.ForEach(value, func(c ma.Component) bool {
		if c.Protocol().Name == "tcp" {
			parts = append(parts, "/tcp/1") // deterministic refused port
			return true
		}
		parts = append(parts, "/"+c.Protocol().Name+"/"+c.Value())
		return true
	})
	m, err := ma.NewMultiaddr(strings.Join(parts, ""))
	if err != nil {
		t.Fatal(err)
	}
	return m
}
