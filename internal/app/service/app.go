package service

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	circuitclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/multiformats/go-multiaddr"

	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/protocol"
)

type Config struct {
	Listen, Seed, ServiceName, Target, HealthListen, PrivateKeyFile, PrivateKeyB64, ForceReachability string
	BootstrapPeers, RelayPeers                                                                        []string
	Autorelay, HolePunching                                                                           bool
	HeartbeatInterval, BootstrapRetryInterval                                                         time.Duration
}
type App struct {
	cfg                   Config
	host                  host.Host
	publisher             *discovery.Publisher
	hb                    *discovery.HeartbeatLoop
	health                *http.Server
	cache                 *discovery.Cache
	stopSubscriber        chan struct{}
	relayInfos            []peer.AddrInfo
	announcementTTL       time.Duration
	requireRelayReadyAnn  bool
	reservationMu         sync.RWMutex
	reservationReadyUntil time.Time
	relayConnMu           sync.RWMutex
	relayConnected        map[peer.ID]bool
}

func LoadConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{Listen: first(getenv("SERVICE_P2P_LISTEN"), "/ip4/127.0.0.1/tcp/40123"), Seed: first(getenv("NODE_SEED"), "service-demo-seed"), ServiceName: first(getenv("SERVICE_NAME"), "demo-service"), Target: first(getenv("SERVICE_TARGET"), "http://127.0.0.1:8000"), HealthListen: first(getenv("SERVICE_HEALTH_LISTEN"), "127.0.0.1:8091"), PrivateKeyFile: getenv("LIBP2P_PRIVATE_NETWORK_KEY"), PrivateKeyB64: getenv("LIBP2P_PRIVATE_NETWORK_KEY_B64"), BootstrapPeers: csv(getenv("BOOTSTRAP_PEERS")), RelayPeers: csv(getenv("RELAY_PEERS")), Autorelay: parseBool(getenv("ENABLE_AUTORELAY"), true), HolePunching: parseBool(getenv("ENABLE_HOLE_PUNCHING"), true), BootstrapRetryInterval: 5 * time.Second}
	if parseBool(getenv("FORCE_REACHABILITY_PRIVATE"), false) {
		cfg.ForceReachability = "private"
	}
	d, err := time.ParseDuration(first(getenv("HEARTBEAT_INTERVAL"), "15s"))
	if err != nil {
		return cfg, err
	}
	cfg.HeartbeatInterval = d
	return cfg, nil
}
func New(ctx context.Context, cfg Config) (*App, error) {
	psk, using, err := p2p.LoadPrivateNetworkPSK(cfg.PrivateKeyFile, cfg.PrivateKeyB64)
	if err != nil {
		return nil, err
	}
	var opts []libp2p.Option
	relays := parseAddrInfos(cfg.RelayPeers)
	if len(relays) > 0 && cfg.Autorelay {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(relays))
	}
	if cfg.HolePunching {
		opts = append(opts, libp2p.EnableHolePunching())
	}
	if cfg.ForceReachability == "private" {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions(cfg.Listen, cfg.Seed, psk, opts...)
	if err != nil {
		return nil, err
	}
	p2p.LogNetworkEvents(h, "service")
	if using {
		log.Printf("libp2p private network enabled")
	}
	h.SetStreamHandler(p2p.ProtocolID, p2p.HandleServiceStream(cfg.Target))
	h.SetStreamHandler(p2p.LegacyProtocolID, p2p.HandleServiceStream(cfg.Target))
	gs, err := pubsub.NewGossipSub(ctx, h, pubsub.WithFloodPublish(true))
	if err != nil {
		_ = h.Close()
		return nil, err
	}
	topic, err := gs.Join(discovery.DiscoveryTopic)
	if err != nil {
		_ = h.Close()
		return nil, err
	}
	cache := discovery.NewCache(30*time.Second, time.Second)
	subscriber := discovery.NewPubSubSubscriber(topic, cache)
	if pubKey := h.Peerstore().PubKey(h.ID()); pubKey != nil {
		subscriber.AddPublicKey(h.ID(), pubKey)
	}
	stopSubscriber := subscriber.Start(ctx)

	pk := h.Peerstore().PrivKey(h.ID())
	if pk == nil {
		close(stopSubscriber)
		cache.Stop()
		_ = h.Close()
		return nil, fmt.Errorf("no private key for peer")
	}
	pub := discovery.NewPublisher(topic, pk)
	app := &App{
		cfg:                  cfg,
		host:                 h,
		publisher:            pub,
		cache:                cache,
		stopSubscriber:       stopSubscriber,
		relayInfos:           relays,
		announcementTTL:      computeAnnouncementTTL(cfg.HeartbeatInterval),
		requireRelayReadyAnn: len(relays) > 0 && (cfg.Autorelay || cfg.ForceReachability == "private"),
		relayConnected:       make(map[peer.ID]bool),
	}
	h.SetStreamHandler(discoveryquery.ProtocolID, discoveryquery.HandleStream(h, "attach", cache))
	app.hb = discovery.NewHeartbeatLoopFunc(pub, cfg.HeartbeatInterval, app.currentAnnouncement)
	app.registerRelayNotifiee()
	return app, nil
}
func (a *App) Start(ctx context.Context) error {
	defer a.host.Close()
	log.Printf("service agent config service=%q target=%s p2p_listen=%s health_listen=%s", a.cfg.ServiceName, a.cfg.Target, a.cfg.Listen, a.cfg.HealthListen)
	log.Printf("peer_id=%s", a.host.ID())
	dialBootstrapPeers(a.host, a.cfg.BootstrapPeers)
	if len(a.cfg.BootstrapPeers) > 0 && a.cfg.BootstrapRetryInterval > 0 {
		go func() {
			ticker := time.NewTicker(a.cfg.BootstrapRetryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					dialBootstrapPeers(a.host, a.cfg.BootstrapPeers)
				}
			}
		}()
	}
	if a.cfg.HealthListen != "" {
		a.health = &http.Server{Addr: a.cfg.HealthListen, Handler: healthMux(a.host)}
		go func() {
			log.Printf("service health listening on %s", a.cfg.HealthListen)
			if err := a.health.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("health: %v", err)
			}
		}()
	}
	go a.maintainRelayReservations(ctx)
	if !a.hb.PublishNow(ctx) {
		log.Printf("initial announcement deferred: relay reservation not ready yet")
	}
	a.hb.Start(ctx)
	<-ctx.Done()
	a.hb.Stop()
	if a.health != nil {
		sd, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = a.health.Shutdown(sd)
	}
	if a.stopSubscriber != nil {
		close(a.stopSubscriber)
	}
	if a.cache != nil {
		a.cache.Stop()
	}
	return nil
}
func computeAnnouncementTTL(interval time.Duration) time.Duration {
	ttl := interval * 2
	if ttl < 10*time.Second {
		ttl = 10 * time.Second
	}
	if ttl > 30*time.Second {
		ttl = 30 * time.Second
	}
	return ttl
}

func (a *App) hasConnectedRelay() bool {
	a.relayConnMu.RLock()
	defer a.relayConnMu.RUnlock()
	for _, connected := range a.relayConnected {
		if connected {
			return true
		}
	}
	return false
}

func (a *App) hasRelayReservation() bool {
	if len(a.relayInfos) > 0 && !a.hasConnectedRelay() {
		return false
	}
	if a.host != nil {
		for _, addr := range p2p.PeerAddrs(a.host) {
			if strings.Contains(addr, "/p2p-circuit") {
				return true
			}
		}
	}
	a.reservationMu.RLock()
	readyUntil := a.reservationReadyUntil
	a.reservationMu.RUnlock()
	return !readyUntil.IsZero() && time.Now().Before(readyUntil)
}

func mergeRelayCircuitAddrs(base []string, relayInfos []peer.AddrInfo, self peer.ID) []string {
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

func (a *App) currentAnnouncement() (discovery.Announcement, bool) {
	addrs := p2p.PeerAddrs(a.host)
	if a.requireRelayReadyAnn && !hasCircuitAddr(addrs) && !a.hasRelayReservation() {
		return discovery.Announcement{}, false
	}
	if a.requireRelayReadyAnn {
		addrs = mergeRelayCircuitAddrs(addrs, a.relayInfos, a.host.ID())
	}
	ann := discovery.Announcement{
		ServiceName: a.cfg.ServiceName,
		PeerID:      a.host.ID(),
		Addresses:   addrs,
		TTL:         a.announcementTTL,
	}
	if a.cache != nil {
		_ = a.cache.Add(ann.PeerID, ann.ServiceName, ann.Addresses, ann.TTL)
	}
	return ann, true
}

func hasCircuitAddr(addrs []string) bool {
	for _, addr := range addrs {
		if strings.Contains(addr, "/p2p-circuit") {
			return true
		}
	}
	return false
}

func (a *App) registerRelayNotifiee() {
	if a.host == nil || len(a.relayInfos) == 0 {
		return
	}
	relaySet := make(map[peer.ID]struct{}, len(a.relayInfos))
	for _, relayInfo := range a.relayInfos {
		relaySet[relayInfo.ID] = struct{}{}
	}
	a.host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			if _, ok := relaySet[conn.RemotePeer()]; !ok {
				return
			}
			a.relayConnMu.Lock()
			a.relayConnected[conn.RemotePeer()] = true
			a.relayConnMu.Unlock()
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			if _, ok := relaySet[conn.RemotePeer()]; !ok {
				return
			}
			a.relayConnMu.Lock()
			delete(a.relayConnected, conn.RemotePeer())
			a.relayConnMu.Unlock()
			a.reservationMu.Lock()
			a.reservationReadyUntil = time.Time{}
			a.reservationMu.Unlock()
			log.Printf("relay peer disconnected relay=%s; forcing reservation refresh", conn.RemotePeer())
		},
	})
}

func (a *App) maintainRelayReservations(ctx context.Context) {
	if !a.requireRelayReadyAnn || len(a.relayInfos) == 0 {
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastReady := false

	for {
		ready := a.hasRelayReservation()
		if ready && !lastReady {
			log.Printf("relay reservation observed in host addrs; publishing refreshed announcement")
			a.hb.PublishNow(ctx)
		}
		lastReady = ready

		if !ready {
			for _, relayInfo := range a.relayInfos {
				reserveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := a.host.Connect(reserveCtx, relayInfo); err != nil {
					cancel()
					log.Printf("relay reservation connect failed relay=%s err=%v", relayInfo.ID, err)
					continue
				}
				reservation, err := circuitclient.Reserve(reserveCtx, a.host, relayInfo)
				cancel()
				if err != nil {
					log.Printf("relay reservation failed relay=%s err=%v", relayInfo.ID, err)
					continue
				}
				a.reservationMu.Lock()
				a.reservationReadyUntil = reservation.Expiration
				a.reservationMu.Unlock()
				log.Printf("relay reservation ready relay=%s expires=%s addrs=%d", relayInfo.ID, reservation.Expiration.Format(time.RFC3339), len(reservation.Addrs))
				if !lastReady {
					log.Printf("relay reservation refreshed; publishing announcement using reserved relay path")
					a.hb.PublishNow(ctx)
				}
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

func healthMux(h host.Host) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) })
	m.HandleFunc("/debug/peer", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("peer_id=" + h.ID().String() + "\n"))
		for _, a := range p2p.PeerAddrs(h) {
			_, _ = w.Write([]byte("addr=" + a + "\n"))
		}
	})
	m.HandleFunc("/debug/protocol", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		events := p2p.RecentNegotiations()
		fmt.Fprintf(w, `{"preferred_stream_protocol_id":%q,"legacy_stream_protocol_id":%q,"protocol_version":%q,"protocol_major":%d,"protocol_minor":%d,"supported_capabilities":[`,
			p2p.ProtocolID, p2p.LegacyProtocolID, p2p.ProtocolVersion, protocol.ProtocolMajor, protocol.ProtocolMinor)
		for i, cap := range protocol.SupportedCapabilities() {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, "%q", cap)
		}
		fmt.Fprint(w, `],"recent_negotiations":[`)
		for i, ev := range events {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"timestamp":%q,"local_role":%q,"remote_role":%q,"stream_protocol_id":%q,"local_protocol_version":%q,"remote_protocol_version":%q,"capabilities":[`,
				ev.Timestamp.Format(time.RFC3339), ev.LocalRole, ev.RemoteRole, ev.StreamProtocolID, ev.LocalProtocolVersion, ev.RemoteProtocolVersion)
			for j, cap := range ev.Capabilities {
				if j > 0 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, "%q", cap)
			}
			fmt.Fprint(w, "]}")
		}
		fmt.Fprint(w, "]}\n")
	})
	return m
}
func dialBootstrapPeers(h host.Host, peers []string) {
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
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		err = h.Connect(ctx, *info)
		c()
		if err != nil {
			log.Printf("failed to dial bootstrap peer %s: %v", info.ID, err)
		}
	}
}
func parseAddrInfos(ss []string) []peer.AddrInfo {
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
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func csv(s string) []string {
	var o []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			o = append(o, p)
		}
	}
	return o
}
func parseBool(s string, d bool) bool {
	if s == "" {
		return d
	}
	return s == "true" || s == "1"
}
