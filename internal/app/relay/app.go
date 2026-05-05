package relay

import (
	"context"
	"encoding/json"
	"fmt"
	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	"github.com/origama/tubo/internal/p2p"
	"log"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	Listen, Seed, HealthListen, PublicAddr, PrivateKeyFile, PrivateKeyB64                                      string
	EnableRelayService, EnableAutoNATService, EnableDiscoveryPubSub, ForceReachabilityPublic, PrintRunCommands bool
	MaxReservations, MaxReservationsPerIP, MaxReservationsPerASN, MaxCircuitsPerPeer, BufferSize               int
	ReservationTTL, LimitDuration                                                                              time.Duration
	LimitDataBytes                                                                                             int64
}
type App struct {
	cfg            Config
	host           host.Host
	health         *http.Server
	cache          *discovery.Cache
	stopSubscriber chan struct{}
}

func LoadConfigFromEnv(g func(string) string) (Config, error) {
	return Config{Listen: first(g("P2P_LISTEN"), "/ip4/0.0.0.0/tcp/4001"), Seed: first(g("NODE_SEED"), "public-relay-seed"), HealthListen: first(g("RELAY_HEALTH_LISTEN"), "127.0.0.1:8092"), PublicAddr: g("RELAY_PUBLIC_ADDR"), PrivateKeyFile: g("LIBP2P_PRIVATE_NETWORK_KEY"), PrivateKeyB64: g("LIBP2P_PRIVATE_NETWORK_KEY_B64"), EnableRelayService: bo(g("ENABLE_RELAY_SERVICE"), true), EnableAutoNATService: bo(g("ENABLE_AUTONAT_SERVICE"), true), EnableDiscoveryPubSub: bo(g("ENABLE_DISCOVERY_PUBSUB"), true), ForceReachabilityPublic: bo(g("FORCE_REACHABILITY_PUBLIC"), true), PrintRunCommands: bo(g("PRINT_RUN_COMMANDS"), true), MaxReservations: in(g("RELAY_MAX_RESERVATIONS"), 256), MaxReservationsPerIP: in(g("RELAY_MAX_RESERVATIONS_PER_IP"), 16), MaxReservationsPerASN: in(g("RELAY_MAX_RESERVATIONS_PER_ASN"), 64), MaxCircuitsPerPeer: in(g("RELAY_MAX_CIRCUITS"), 64), BufferSize: in(g("RELAY_BUFFER_SIZE"), 65536), ReservationTTL: du(g("RELAY_RESERVATION_TTL"), time.Hour), LimitDuration: du(g("RELAY_LIMIT_DURATION"), 5*time.Minute), LimitDataBytes: int64(in(g("RELAY_LIMIT_DATA_BYTES"), 256<<20))}, nil
}
func New(ctx context.Context, cfg Config) (*App, error) {
	psk, using, err := p2p.LoadPrivateNetworkPSK(cfg.PrivateKeyFile, cfg.PrivateKeyB64)
	if err != nil {
		return nil, err
	}
	var opts []libp2p.Option
	if cfg.EnableRelayService {
		r := relayv2.DefaultResources()
		r.MaxReservations = cfg.MaxReservations
		r.MaxReservationsPerIP = cfg.MaxReservationsPerIP
		r.MaxReservationsPerASN = cfg.MaxReservationsPerASN
		r.MaxCircuits = cfg.MaxCircuitsPerPeer
		r.BufferSize = cfg.BufferSize
		r.ReservationTTL = cfg.ReservationTTL
		r.Limit = &relayv2.RelayLimit{Duration: cfg.LimitDuration, Data: cfg.LimitDataBytes}
		opts = append(opts, libp2p.EnableRelayService(relayv2.WithResources(r)))
	}
	if cfg.EnableAutoNATService {
		opts = append(opts, libp2p.EnableNATService())
	}
	if cfg.ForceReachabilityPublic {
		opts = append(opts, libp2p.ForceReachabilityPublic())
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions(cfg.Listen, cfg.Seed, psk, opts...)
	if err != nil {
		return nil, err
	}
	p2p.LogNetworkEvents(h, "relay")
	if using {
		log.Printf("libp2p private network enabled")
	}
	var cache *discovery.Cache
	var stopSubscriber chan struct{}
	if cfg.EnableDiscoveryPubSub {
		cache, stopSubscriber, err = startDiscovery(ctx, h)
		if err != nil {
			_ = h.Close()
			return nil, err
		}
	}
	h.SetStreamHandler(discoveryquery.ProtocolID, discoveryquery.HandleStream(h, "relay", cache))
	return &App{cfg: cfg, host: h, cache: cache, stopSubscriber: stopSubscriber}, nil
}
func (a *App) Start(ctx context.Context) error {
	defer a.host.Close()
	log.Printf("p2p relay ready peer_id=%s", a.host.ID())
	for _, addr := range p2p.PeerAddrs(a.host) {
		log.Printf("addr=%s", addr)
	}
	if a.cfg.PrintRunCommands {
		PrintStartupCommandHints(a.host, a.cfg.PublicAddr)
	}
	if a.cfg.HealthListen != "" {
		a.health = &http.Server{Addr: a.cfg.HealthListen, Handler: a.mux()}
		go func() {
			log.Printf("relay health listening on %s", a.cfg.HealthListen)
			if err := a.health.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("relay health: %v", err)
			}
		}()
	}
	<-ctx.Done()
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
func (a *App) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) })
	m.HandleFunc("/debug/peer", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"peer_id": a.host.ID().String(), "addrs": p2p.PeerAddrs(a.host), "relay_public_addr": RelayAdvertiseAddr(a.host, a.cfg.PublicAddr)})
	})
	return m
}
func startDiscovery(ctx context.Context, h host.Host) (*discovery.Cache, chan struct{}, error) {
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return nil, nil, err
	}
	topic, err := ps.Join(discovery.DiscoveryTopic)
	if err != nil {
		return nil, nil, err
	}
	cache := discovery.NewCache(30*time.Second, time.Second)
	subscriber := discovery.NewPubSubSubscriber(topic, cache)
	if pubKey := h.Peerstore().PubKey(h.ID()); pubKey != nil {
		subscriber.AddPublicKey(h.ID(), pubKey)
	}
	stopCh := subscriber.Start(ctx)
	log.Printf("discovery pubsub router joined topic %s", discovery.DiscoveryTopic)
	go func() {
		<-ctx.Done()
		log.Printf("discovery pubsub router stopped: %v", ctx.Err())
	}()
	return cache, stopCh, nil
}
func PrintStartupCommandHints(h host.Host, addr string) {
	ra := RelayAdvertiseAddr(h, addr)
	if ra == "" {
		return
	}
	log.Printf("startup command hints: relay_addr=%s", ra)
	log.Printf("  tubo edge run --relay %s --bootstrap %s", ra, ra)
	log.Printf("  tubo service run --relay %s --bootstrap %s", ra, ra)
}
func RelayAdvertiseAddr(h host.Host, configured string) string {
	if configured != "" {
		if strings.Contains(configured, "/p2p/") {
			return configured
		}
		return strings.TrimRight(configured, "/") + "/p2p/" + h.ID().String()
	}
	addrs := p2p.PeerAddrs(h)
	if len(addrs) > 0 {
		return addrs[0]
	}
	return ""
}
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func bo(s string, d bool) bool {
	if s == "" {
		return d
	}
	return s == "true" || s == "1"
}
func in(s string, d int) int {
	if s == "" {
		return d
	}
	var i int
	_, _ = fmt.Sscanf(s, "%d", &i)
	return i
}
func du(s string, d time.Duration) time.Duration {
	if s == "" {
		return d
	}
	x, err := time.ParseDuration(s)
	if err != nil {
		return d
	}
	return x
}
