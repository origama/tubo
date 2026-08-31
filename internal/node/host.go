package node

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	crypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	coremetrics "github.com/libp2p/go-libp2p/core/metrics"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/pnet"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	circuitclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	circuitrelay "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	holepunch "github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	ma "github.com/multiformats/go-multiaddr"
)

type Options struct {
	KeyFile           string
	SwarmKeyFile      string
	Listen            string
	Private           bool
	RelayService      bool
	BandwidthReporter coremetrics.Reporter
}

func Open(ctx context.Context, options Options) (host.Host, error) {
	identity, err := loadOrCreateIdentity(options.KeyFile)
	if err != nil {
		return nil, err
	}
	listen := options.Listen
	if listen == "" {
		listen = "/ip4/0.0.0.0/tcp/0"
	}
	opts := []libp2p.Option{
		libp2p.Identity(identity),
		libp2p.ListenAddrStrings(listen, "/ip4/0.0.0.0/udp/0/quic-v1"),
		libp2p.DisableMetrics(),
		libp2p.EnableRelay(),
		libp2p.EnableHolePunching(holepunch.DirectDialTimeout(5 * time.Second)),
		libp2p.EnableAutoNATv2(),
	}
	if options.BandwidthReporter != nil {
		opts = append(opts, libp2p.BandwidthReporter(options.BandwidthReporter))
	}
	if options.SwarmKeyFile != "" {
		file, err := os.Open(options.SwarmKeyFile)
		if err != nil {
			return nil, fmt.Errorf("open swarm key: %w", err)
		}
		psk, err := pnet.DecodeV1PSK(file)
		file.Close()
		if err != nil {
			return nil, fmt.Errorf("decode swarm key: %w", err)
		}
		opts = append(opts, libp2p.PrivateNetwork(psk))
	}
	if options.Private {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
	}
	if options.RelayService {
		resources := circuitrelay.DefaultResources()
		resources.Limit = nil // Tubo sessions enforce authorization lifetime; relay must not truncate raw streams.
		opts = append(opts, libp2p.ForceReachabilityPublic(), libp2p.EnableRelayService(circuitrelay.WithResources(resources)))
	}
	return libp2p.New(opts...)
}

func loadOrCreateIdentity(path string) (crypto.PrivKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		return crypto.UnmarshalPrivateKey(data)
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	key, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}
	data, err := crypto.MarshalPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

func RelayInfo(address string) (*peer.AddrInfo, error) {
	value, err := ma.NewMultiaddr(address)
	if err != nil {
		return nil, fmt.Errorf("invalid relay address: %w", err)
	}
	p2pCount := 0
	hasCircuit := false
	ma.ForEach(value, func(component ma.Component) bool {
		switch component.Protocol().Code {
		case ma.P_P2P:
			p2pCount++
		case ma.P_CIRCUIT:
			hasCircuit = true
		}
		return true
	})
	if p2pCount != 1 || hasCircuit {
		return nil, errors.New("invalid relay address: require one terminal relay peer and no circuit")
	}
	info, err := peer.AddrInfoFromP2pAddr(value)
	if err != nil {
		return nil, fmt.Errorf("invalid relay address: %w", err)
	}
	if len(info.Addrs) != 1 {
		return nil, errors.New("invalid relay address: missing relay transport")
	}
	return info, nil
}

// RelayAddress renders one canonical relay multiaddr from transport facts.
// Input addresses may be a bare transport address or already end in the
// matching relay /p2p component; output always contains that component once.
func RelayAddress(info peer.AddrInfo) (string, error) {
	if err := info.ID.Validate(); err != nil {
		return "", fmt.Errorf("invalid relay peer: %w", err)
	}
	for _, address := range info.Addrs {
		base, err := relayBase(address, info.ID)
		if err != nil {
			return "", err
		}
		if base == nil {
			continue
		}
		relayPeer, err := ma.NewComponent("p2p", info.ID.String())
		if err != nil {
			return "", fmt.Errorf("invalid relay peer component: %w", err)
		}
		return base.Encapsulate(relayPeer).String(), nil
	}
	return "", errors.New("relay has no address")
}

func relayBase(address ma.Multiaddr, expected peer.ID) (ma.Multiaddr, error) {
	p2pCount := 0
	hasCircuit := false
	ma.ForEach(address, func(component ma.Component) bool {
		switch component.Protocol().Code {
		case ma.P_P2P:
			p2pCount++
		case ma.P_CIRCUIT:
			hasCircuit = true
		}
		return true
	})
	if hasCircuit {
		return nil, nil
	}
	switch p2pCount {
	case 0:
		return address, nil
	case 1:
		parsed, err := peer.AddrInfoFromP2pAddr(address)
		if err != nil {
			return nil, fmt.Errorf("invalid relay address: %w", err)
		}
		if parsed.ID != expected || len(parsed.Addrs) != 1 {
			return nil, errors.New("invalid relay address: relay identity mismatch")
		}
		return parsed.Addrs[0], nil
	default:
		return nil, errors.New("invalid relay address: duplicate peer components")
	}
}
func ConnectRelay(ctx context.Context, h host.Host, address string, reserve bool) (*peer.AddrInfo, error) {
	info, err := RelayInfo(address)
	if err != nil {
		return nil, err
	}
	if err := EnsureRelay(ctx, h, *info, reserve); err != nil {
		return nil, err
	}
	return info, nil
}
func PeerConnected(h host.Host, id peer.ID) bool {
	return h.Network().Connectedness(id) == core.Connected
}

func clearDialBackoff(h host.Host, id peer.ID) {
	if value, ok := h.Network().(*swarm.Swarm); ok {
		value.Backoff().Clear(id)
	}
}

func EnsureRelay(ctx context.Context, h host.Host, info peer.AddrInfo, reserve bool) error {
	if !PeerConnected(h, info.ID) {
		clearDialBackoff(h, info.ID)
		connectCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		if err := h.Connect(connectCtx, info); err != nil {
			return fmt.Errorf("connect relay: %w", err)
		}
	}
	if reserve {
		_, err := ReserveRelay(ctx, h, info)
		return err
	}
	return nil
}

// ReserveRelay acquires a Circuit Relay v2 slot and returns the reservation.
// The returned Reservation carries the relay-provided expiration time, which is
// the L1 scheduling fact for renewal: Tubo renews before that expiration, never
// merely before an assumed relay TTL. Reserve fails closed when the relay
// returns a past or overflowing expiration.
func ReserveRelay(ctx context.Context, h host.Host, info peer.AddrInfo) (*circuitclient.Reservation, error) {
	if !PeerConnected(h, info.ID) {
		if err := EnsureRelay(ctx, h, info, false); err != nil {
			return nil, err
		}
	}
	reservationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	reservation, err := circuitclient.Reserve(reservationCtx, h, info)
	if err != nil {
		return nil, fmt.Errorf("reserve relay circuit: %w", err)
	}
	return reservation, nil
}

func CircuitAddress(relay string, target peer.ID) (string, error) {
	if err := target.Validate(); err != nil {
		return "", fmt.Errorf("invalid circuit target peer: %w", err)
	}
	info, err := RelayInfo(relay)
	if err != nil {
		return "", err
	}
	canonicalRelay, err := RelayAddress(*info)
	if err != nil {
		return "", err
	}
	value, err := ma.NewMultiaddr(canonicalRelay)
	if err != nil {
		return "", fmt.Errorf("invalid canonical relay address: %w", err)
	}
	circuit, err := ma.NewComponent("p2p-circuit", "")
	if err != nil {
		return "", fmt.Errorf("build circuit component: %w", err)
	}
	targetPeer, err := ma.NewComponent("p2p", target.String())
	if err != nil {
		return "", fmt.Errorf("build circuit target component: %w", err)
	}
	return value.Encapsulate(circuit).Encapsulate(targetPeer).String(), nil
}
func DialPeer(ctx context.Context, h host.Host, address string) (peer.ID, error) {
	value, err := ma.NewMultiaddr(address)
	if err != nil {
		return "", err
	}
	info, err := peer.AddrInfoFromP2pAddr(value)
	if err != nil {
		return "", err
	}
	h.Peerstore().AddAddrs(info.ID, info.Addrs, time.Hour)
	clearDialBackoff(h, info.ID)
	if err := h.Connect(ctx, *info); err != nil {
		return "", err
	}
	return info.ID, nil
}
