package service

import (
	"context"
	"fmt"
	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"log"
	"net/http"
	"p2p-api-tunnel/internal/discovery"
	"p2p-api-tunnel/internal/p2p"
	"strings"
	"time"
)

type Config struct {
	Listen, Seed, ServiceName, Target, HealthListen, PrivateKeyFile, PrivateKeyB64, ForceReachability string
	BootstrapPeers, RelayPeers                                                                        []string
	Autorelay, HolePunching                                                                           bool
	HeartbeatInterval, BootstrapRetryInterval                                                         time.Duration
}
type App struct {
	cfg       Config
	host      host.Host
	publisher *discovery.Publisher
	ann       discovery.Announcement
	hb        *discovery.HeartbeatLoop
	health    *http.Server
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
	gs, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		_ = h.Close()
		return nil, err
	}
	topic, err := gs.Join(discovery.DiscoveryTopic)
	if err != nil {
		_ = h.Close()
		return nil, err
	}
	pk := h.Peerstore().PrivKey(h.ID())
	if pk == nil {
		_ = h.Close()
		return nil, fmt.Errorf("no private key for peer")
	}
	pub := discovery.NewPublisher(topic, pk)
	ann := discovery.Announcement{ServiceName: cfg.ServiceName, PeerID: h.ID(), Addresses: p2p.PeerAddrs(h), TTL: 30 * time.Second}
	hb := discovery.NewHeartbeatLoop(pub, ann, cfg.HeartbeatInterval)
	return &App{cfg: cfg, host: h, publisher: pub, ann: ann, hb: hb}, nil
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
	if err := a.publisher.Publish(ctx, a.ann); err != nil {
		log.Printf("publish announcement failed: %v", err)
	} else {
		log.Printf("announced service %q (peer=%s)", a.ann.ServiceName, a.ann.PeerID)
	}
	a.hb.Start(ctx)
	<-ctx.Done()
	a.hb.Stop()
	if a.health != nil {
		sd, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = a.health.Shutdown(sd)
	}
	return nil
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
