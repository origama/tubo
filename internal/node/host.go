package node

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		return nil, err
	}
	return peer.AddrInfoFromP2pAddr(value)
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
		return ReserveRelay(ctx, h, info)
	}
	return nil
}

func ReserveRelay(ctx context.Context, h host.Host, info peer.AddrInfo) error {
	if !PeerConnected(h, info.ID) {
		if err := EnsureRelay(ctx, h, info, false); err != nil {
			return err
		}
	}
	reservationCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := circuitclient.Reserve(reservationCtx, h, info); err != nil {
		return fmt.Errorf("reserve relay circuit: %w", err)
	}
	return nil
}

func CircuitAddress(relay string, target peer.ID) (string, error) {
	info, err := RelayInfo(relay)
	if err != nil {
		return "", err
	}
	var base string
	for _, addr := range info.Addrs {
		if strings.Contains(addr.String(), "/p2p-circuit") {
			continue
		}
		base = addr.String()
		break
	}
	if base == "" {
		return "", fmt.Errorf("relay has no address")
	}
	return fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s", base, info.ID, target), nil
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
