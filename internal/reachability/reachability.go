// Package reachability is the L1 Reachability boundary (A0 issue #44).
//
// Reachability turns verified bootstrap/relay provider identity into an
// acquirable dial target and a raw full-duplex node.Carrier. Reachability
// obtains connectivity only; it can neither express nor decide authorization.
// The API intentionally carries no policy, scope, lease, or authority inputs
// and produces no authorization outputs — the type surface is pure transport:
// peer identity and dial addresses in, a dial target or Carrier out.
//
// Public bootstrap/relay/cache nodes provide reachability, never ambient
// authorization. A verified bundle or injected peer.AddrInfo identifies where
// to dial; it is never treated as permission to open any Pipe.
package reachability

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/origama/tubo/internal/node"
	"github.com/origama/tubo/internal/reachability/bundle"
)

// ErrNoBootstrapBundle is the deterministic fail-closed outcome of a zero-config
// resolution when no valid, verified production bootstrap bundle is installed.
// It deliberately does not substitute an overlay, a hardcoded relay address, or
// any temporary provider path (issue #44 stop rule).
var ErrNoBootstrapBundle = errors.New("reachability: no verified production bootstrap bundle installed")

// ErrNoReachableAddress is returned when none of a provider's addresses can be
// reached.
var ErrNoReachableAddress = errors.New("reachability: no reachable address")

// directDialTimeout bounds each initial direct dial attempt during path
// selection. It is a guarded resource bound, not a discovery or retry policy.
const directDialTimeout = 8 * time.Second

// Provider is one verified/dialable reachability provider: an identified peer
// and the dial addresses that reach it. The type carries only transport and
// identity facts; it cannot express authorization (it has no policy, lease, or
// scope fields).
type Provider struct {
	id    peer.ID
	addrs []ma.Multiaddr
}

// New wraps a verified or injected peer identity into a Provider. The caller is
// expected to have obtained info from a verified source (a signed bootstrap
// bundle) or a trusted test/operator source; New itself only copies transport
// facts and performs no trust decision.
func New(info peer.AddrInfo) *Provider {
	return &Provider{id: info.ID, addrs: append([]ma.Multiaddr(nil), info.Addrs...)}
}

// FromAddress parses a single full multiaddr (including the /p2p/<peer-id>
// component) into a Provider.
func FromAddress(address string) (*Provider, error) {
	value, err := ma.NewMultiaddr(address)
	if err != nil {
		return nil, err
	}
	info, err := peer.AddrInfoFromP2pAddr(value)
	if err != nil {
		return nil, err
	}
	return New(*info), nil
}

// ID returns the identified peer.
func (p *Provider) ID() peer.ID { return p.id }

// AddrInfo returns the acquired dial target: the identified peer and the
// addresses that reach it. It remains pure transport metadata.
func (p *Provider) AddrInfo() peer.AddrInfo {
	return peer.AddrInfo{ID: p.id, Addrs: append([]ma.Multiaddr(nil), p.addrs...)}
}

// PrimaryAddress renders the first dial address of the provider as a single
// full p2p multiaddr string, suitable for round-tripping through callers that
// consume single-address relay strings.
func (p *Provider) PrimaryAddress() (string, error) {
	address, err := node.RelayAddress(p.AddrInfo())
	if err != nil {
		return "", fmt.Errorf("reachability: no primary address: %w", err)
	}
	return address, nil
}

// hasCircuit reports whether an address carries a p2p-circuit relay hop.
func hasCircuit(value ma.Multiaddr) bool {
	for _, p := range value.Protocols() {
		if p.Code == ma.P_CIRCUIT {
			return true
		}
	}
	return false
}

// Dial acquires reachability to the identified peer. It is initial path
// selection only: it first attempts the direct addresses (each bounded by
// directDialTimeout), then falls back to relay/circuit addresses. It never
// migrates an established path mid-session.
func (p *Provider) Dial(ctx context.Context, h host.Host) (*peer.AddrInfo, error) {
	if len(p.addrs) == 0 {
		return nil, ErrNoReachableAddress
	}
	var direct, relayed []ma.Multiaddr
	for _, a := range p.addrs {
		if hasCircuit(a) {
			relayed = append(relayed, a)
		} else {
			direct = append(direct, a)
		}
	}
	// Direct path first (initial moment selection; hole punching/AutoNATv2 are
	// already enabled at the host).
	if len(direct) > 0 {
		dctx, cancel := context.WithTimeout(ctx, directDialTimeout)
		err := h.Connect(dctx, peer.AddrInfo{ID: p.id, Addrs: direct})
		cancel()
		if err == nil {
			info := p.AddrInfo()
			return &info, nil
		}
	}
	// Relay fallback.
	if len(relayed) > 0 {
		if err := h.Connect(ctx, peer.AddrInfo{ID: p.id, Addrs: relayed}); err != nil {
			return nil, err
		}
		info := p.AddrInfo()
		return &info, nil
	}
	return nil, ErrNoReachableAddress
}

// Open acquires a raw full-duplex node.Carrier to the provider over the given
// protocol after direct-first/relay-fallback reachability. The returned
// Carrier is a transport fact; opening it confers no authorization to open any
// Pipe and no policy applies.
func (p *Provider) Open(ctx context.Context, h host.Host, proto protocol.ID) (node.Carrier, error) {
	info, err := p.Dial(ctx, h)
	if err != nil {
		return nil, err
	}
	stream, err := h.NewStream(network.WithAllowLimitedConn(ctx, string(proto)), info.ID, proto)
	if err != nil {
		return nil, fmt.Errorf("reachability: open carrier: %w", err)
	}
	return node.WrapStream(stream), nil
}

// Resolver supplies the currently installed, verified bootstrap provider for
// zero-config reachability. In A0 there is deliberately no production trust
// material (issue #45 ratifies it), so the default resolver fails closed rather
// than inventing or hardcoding a provider.
type Resolver struct {
	provide func(context.Context) (*Provider, error)
}

// DefaultResolver returns the resolver backed by the currently embedded
// (empty) production trust set.
func DefaultResolver() Resolver {
	return Resolver{provide: defaultBootstrap}
}

// ResolveDefault resolves the embedded zero-config bootstrap.
func ResolveDefault(ctx context.Context) (*Provider, error) {
	return DefaultResolver().Resolve(ctx)
}

// Resolve returns the verified zero-config bootstrap provider, or fails closed
// deterministically with ErrNoBootstrapBundle when production trust material is
// absent.
func (r Resolver) Resolve(ctx context.Context) (*Provider, error) {
	if r.provide == nil {
		return nil, ErrNoBootstrapBundle
	}
	return r.provide(ctx)
}

// WithProvider returns a resolver backed by an injected provider singleton. It
// grounds deterministic in-process tests without any production trust.
func (r Resolver) WithProvider(p *Provider) Resolver {
	return Resolver{provide: func(context.Context) (*Provider, error) { return p, nil }}
}

// defaultBootstrap is the production trust set: the embedded signed
// bootstrap bundle is verified against the pinned production root and
// parsed into a Provider. Fail-closed on corrupt/missing/expired/not-yet-
// valid/unpinned/wrong-role/wrong-peer bundle.
func defaultBootstrap(context.Context) (*Provider, error) {
	raw := bundle.MustProductionBundle()
	roots := bundle.ProductionRoots()
	b, err := bundle.ParseVerify(raw, roots, time.Now())
	if err != nil {
		return nil, fmt.Errorf("reachability: production bundle: %w", err)
	}
	addrs := make([]ma.Multiaddr, 0, len(b.Addresses))
	for _, addr := range b.Addresses {
		maddr, err := ma.NewMultiaddr(addr)
		if err != nil {
			return nil, fmt.Errorf("reachability: invalid address in bundle: %w", err)
		}
		addrs = append(addrs, maddr)
	}
	peerID, err := peer.Decode(b.PeerID)
	if err != nil {
		return nil, fmt.Errorf("reachability: invalid peer ID in bundle: %w", err)
	}
	return New(peer.AddrInfo{ID: peerID, Addrs: addrs}), nil
}
