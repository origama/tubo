package p2p

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	circuitclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/multiformats/go-multiaddr"
)

type OverlayHostConfig struct {
	Listen            string
	Seed              string
	PrivateKeyFile    string
	PrivateKeyB64     string
	BootstrapPeers    []string
	RelayPeers        []string
	Autorelay         bool
	HolePunching      bool
	ForceReachability string
	Component         string
}

type OverlayHost struct {
	Host       host.Host
	RelayInfos []peer.AddrInfo
	cfg        OverlayHostConfig

	reservationMu         sync.RWMutex
	reservationReadyUntil time.Time
	relayConnMu           sync.RWMutex
	relayConnected        map[peer.ID]bool
}

func NewOverlayHost(cfg OverlayHostConfig) (*OverlayHost, error) {
	psk, using, err := LoadPrivateNetworkPSK(cfg.PrivateKeyFile, cfg.PrivateKeyB64)
	if err != nil {
		return nil, err
	}
	var opts []libp2p.Option
	if allowed, configured, err := LoadAllowedPeersFromEnv(); err != nil {
		return nil, err
	} else if configured {
		opts = append(opts, libp2p.ConnectionGater(NewPeerAllowlistConnectionGater(allowed)))
		log.Printf("peer allowlist enabled peers=%d", len(allowed))
	}
	relays := ParseAddrInfos(cfg.RelayPeers)
	if len(relays) > 0 {
		opts = append(opts, libp2p.EnableRelay())
	}
	if len(relays) > 0 && cfg.Autorelay {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(relays))
	}
	if cfg.HolePunching {
		opts = append(opts, libp2p.EnableHolePunching())
	}
	if cfg.ForceReachability == "private" {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
	}
	h, err := NewHostWithSeedAndPSKAndOptions(cfg.Listen, cfg.Seed, psk, opts...)
	if err != nil {
		return nil, err
	}
	if cfg.Component != "" {
		LogNetworkEvents(h, cfg.Component)
	}
	if using {
		log.Printf("libp2p private network enabled")
	}
	oh := &OverlayHost{Host: h, RelayInfos: relays, cfg: cfg, relayConnected: make(map[peer.ID]bool)}
	oh.registerRelayNotifiee()
	return oh, nil
}

func (o *OverlayHost) Close() error {
	if o == nil || o.Host == nil {
		return nil
	}
	return o.Host.Close()
}

func (o *OverlayHost) DialBootstrapPeers() {
	if o == nil || o.Host == nil {
		return
	}
	DialBootstrapPeers(o.Host, o.cfg.BootstrapPeers)
}

func (o *OverlayHost) StartBootstrapRetry(ctx context.Context, interval time.Duration) {
	if o == nil || o.Host == nil || len(o.cfg.BootstrapPeers) == 0 || interval <= 0 {
		return
	}
	o.DialBootstrapPeers()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.DialBootstrapPeers()
			}
		}
	}()
}

func (o *OverlayHost) StartRelayReservations(ctx context.Context) {
	if o == nil || o.Host == nil || len(o.RelayInfos) == 0 {
		return
	}
	go o.maintainRelayReservations(ctx)
}

func (o *OverlayHost) ReachableAddrs() []string {
	if o == nil || o.Host == nil {
		return nil
	}
	addrs := ExpandUnspecifiedListenAddrs(PeerAddrs(o.Host), o.cfg.Listen, o.Host.ID())
	if len(o.RelayInfos) > 0 {
		addrs = MergeRelayCircuitAddrs(addrs, o.RelayInfos, o.Host.ID())
	}
	return addrs
}

func (o *OverlayHost) HasRelayReservation() bool {
	if o == nil || o.Host == nil {
		return false
	}
	if len(o.RelayInfos) > 0 && !o.hasConnectedRelay() {
		return false
	}
	for _, addr := range PeerAddrs(o.Host) {
		if strings.Contains(addr, "/p2p-circuit") {
			return true
		}
	}
	o.reservationMu.RLock()
	readyUntil := o.reservationReadyUntil
	o.reservationMu.RUnlock()
	return !readyUntil.IsZero() && time.Now().Before(readyUntil)
}

func (o *OverlayHost) hasConnectedRelay() bool {
	o.relayConnMu.RLock()
	defer o.relayConnMu.RUnlock()
	for _, connected := range o.relayConnected {
		if connected {
			return true
		}
	}
	return false
}

func (o *OverlayHost) registerRelayNotifiee() {
	if o == nil || o.Host == nil || len(o.RelayInfos) == 0 {
		return
	}
	relaySet := make(map[peer.ID]struct{}, len(o.RelayInfos))
	for _, relayInfo := range o.RelayInfos {
		relaySet[relayInfo.ID] = struct{}{}
	}
	o.Host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			if _, ok := relaySet[conn.RemotePeer()]; !ok {
				return
			}
			o.relayConnMu.Lock()
			o.relayConnected[conn.RemotePeer()] = true
			o.relayConnMu.Unlock()
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			if _, ok := relaySet[conn.RemotePeer()]; !ok {
				return
			}
			o.relayConnMu.Lock()
			delete(o.relayConnected, conn.RemotePeer())
			o.relayConnMu.Unlock()
			o.reservationMu.Lock()
			o.reservationReadyUntil = time.Time{}
			o.reservationMu.Unlock()
			log.Printf("relay peer disconnected relay=%s; forcing reservation refresh", conn.RemotePeer())
		},
	})
}

func (o *OverlayHost) maintainRelayReservations(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if !o.HasRelayReservation() {
			for _, relayInfo := range o.RelayInfos {
				reserveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := o.Host.Connect(reserveCtx, relayInfo); err != nil {
					cancel()
					log.Printf("relay reservation connect failed relay=%s err=%v", relayInfo.ID, err)
					continue
				}
				reservation, err := circuitclient.Reserve(reserveCtx, o.Host, relayInfo)
				cancel()
				if err != nil {
					log.Printf("relay reservation failed relay=%s err=%v", relayInfo.ID, err)
					continue
				}
				o.reservationMu.Lock()
				o.reservationReadyUntil = reservation.Expiration
				o.reservationMu.Unlock()
				log.Printf("relay reservation ready relay=%s expires=%s addrs=%d", relayInfo.ID, reservation.Expiration.Format(time.RFC3339), len(reservation.Addrs))
				break
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func MergeRelayCircuitAddrs(base []string, relayInfos []peer.AddrInfo, self peer.ID) []string {
	seen := make(map[string]struct{}, len(base)+len(relayInfos))
	out := make([]string, 0, len(base)+len(relayInfos))
	for _, addr := range base {
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	for _, relayInfo := range relayInfos {
		for _, addr := range relayInfo.Addrs {
			relayCircuit := fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s", addr.String(), relayInfo.ID, self)
			if _, ok := seen[relayCircuit]; ok {
				continue
			}
			seen[relayCircuit] = struct{}{}
			out = append(out, relayCircuit)
		}
	}
	return out
}

func ExpandUnspecifiedListenAddrs(addrs []string, listen string, self peer.ID) []string {
	if !strings.Contains(listen, "/ip4/0.0.0.0/") && !strings.Contains(listen, "/ip6/::/") {
		return addrs
	}
	seen := make(map[string]struct{}, len(addrs))
	out := make([]string, 0, len(addrs)+4)
	for _, addr := range addrs {
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	for _, addr := range addrs {
		if strings.Contains(addr, "/p2p-circuit") {
			continue
		}
		port := tcpPortFromMultiaddr(addr)
		if port == "" {
			continue
		}
		for _, ip := range interfaceIPs() {
			candidate := fmt.Sprintf("/ip4/%s/tcp/%s/p2p/%s", ip, port, self)
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out
}

func DialBootstrapPeers(h host.Host, peers []string) {
	for _, raw := range peers {
		m, err := multiaddr.NewMultiaddr(raw)
		if err != nil {
			log.Printf("invalid bootstrap peer %q: %v", raw, err)
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(m)
		if err != nil {
			log.Printf("bootstrap peer parse %q: %v", raw, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = h.Connect(ctx, *info)
		cancel()
		if err != nil {
			log.Printf("failed to dial bootstrap peer %s: %v", info.ID, err)
		}
	}
}

func ParseAddrInfos(ss []string) []peer.AddrInfo {
	var out []peer.AddrInfo
	for _, s := range ss {
		m, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			continue
		}
		i, err := peer.AddrInfoFromP2pAddr(m)
		if err == nil {
			out = append(out, *i)
		}
	}
	return out
}

func tcpPortFromMultiaddr(addr string) string {
	parts := strings.Split(addr, "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "tcp" {
			return parts[i+1]
		}
	}
	return ""
}

func interfaceIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, raw := range addrs {
			var ip net.IP
			switch v := raw.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out
}
