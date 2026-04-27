package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"p2p-api-tunnel/internal/discovery"
	"p2p-api-tunnel/internal/p2p"
)

type relayConfig struct {
	Listen                string
	Seed                  string
	HealthListen          string
	EnableRelayService    bool
	EnableAutoNATService  bool
	EnableDiscoveryPubSub bool
	ForceReachabilityPub  bool
	MaxReservations       int
	MaxReservationsPerIP  int
	MaxReservationsPerASN int
	MaxCircuitsPerPeer    int
	BufferSize            int
	ReservationTTL        time.Duration
	RelayLimitDuration    time.Duration
	RelayLimitData        int64
	RelayPublicAddr       string
	PrintRunCommands      bool
}

func main() {
	cfg := relayConfig{
		Listen:                getenv("P2P_LISTEN", "/ip4/0.0.0.0/tcp/4001"),
		Seed:                  getenv("NODE_SEED", "public-relay-seed"),
		HealthListen:          getenv("RELAY_HEALTH_LISTEN", "127.0.0.1:8092"),
		EnableRelayService:    getenvBool("ENABLE_RELAY_SERVICE", true),
		EnableAutoNATService:  getenvBool("ENABLE_AUTONAT_SERVICE", true),
		EnableDiscoveryPubSub: getenvBool("ENABLE_DISCOVERY_PUBSUB", true),
		ForceReachabilityPub:  getenvBool("FORCE_REACHABILITY_PUBLIC", true),
		MaxReservations:       getenvInt("RELAY_MAX_RESERVATIONS", 256),
		MaxReservationsPerIP:  getenvInt("RELAY_MAX_RESERVATIONS_PER_IP", 16),
		MaxReservationsPerASN: getenvInt("RELAY_MAX_RESERVATIONS_PER_ASN", 64),
		MaxCircuitsPerPeer:    getenvInt("RELAY_MAX_CIRCUITS", 16),
		BufferSize:            getenvInt("RELAY_BUFFER_SIZE", 4096),
		ReservationTTL:        getenvDuration("RELAY_RESERVATION_TTL", time.Hour),
		RelayLimitDuration:    getenvDuration("RELAY_LIMIT_DURATION", 5*time.Minute),
		RelayLimitData:        getenvInt64("RELAY_LIMIT_DATA_BYTES", 1<<20),
		RelayPublicAddr:       getenv("RELAY_PUBLIC_ADDR", ""),
		PrintRunCommands:      getenvBool("PRINT_RUN_COMMANDS", true),
	}

	psk, usingPrivateNetwork, err := p2p.LoadPrivateNetworkPSKFromEnv()
	if err != nil {
		log.Fatalf("load private network key: %v", err)
	}
	allowedPeers, hasAllowlist, err := p2p.LoadAllowedPeersFromEnv()
	if err != nil {
		log.Fatalf("load allowed peers: %v", err)
	}

	var opts []libp2p.Option
	if hasAllowlist {
		opts = append(opts, libp2p.ConnectionGater(p2p.NewPeerAllowlistConnectionGater(allowedPeers)))
	}
	if cfg.EnableRelayService {
		resources := relayv2.DefaultResources()
		resources.MaxReservations = cfg.MaxReservations
		resources.MaxReservationsPerIP = cfg.MaxReservationsPerIP
		resources.MaxReservationsPerASN = cfg.MaxReservationsPerASN
		resources.MaxCircuits = cfg.MaxCircuitsPerPeer
		resources.BufferSize = cfg.BufferSize
		resources.ReservationTTL = cfg.ReservationTTL
		resources.Limit = &relayv2.RelayLimit{
			Duration: cfg.RelayLimitDuration,
			Data:     cfg.RelayLimitData,
		}
		opts = append(opts, libp2p.EnableRelayService(relayv2.WithResources(resources)))
	}
	if cfg.EnableAutoNATService {
		opts = append(opts, libp2p.EnableNATService())
	}
	if cfg.ForceReachabilityPub {
		opts = append(opts, libp2p.ForceReachabilityPublic())
	}

	h, err := p2p.NewHostWithSeedAndPSKAndOptions(cfg.Listen, cfg.Seed, psk, opts...)
	if err != nil {
		log.Fatalf("create relay host: %v", err)
	}
	defer h.Close()
	p2p.LogNetworkEvents(h, "relay")

	log.Printf("p2p relay ready")
	log.Printf("peer_id=%s", h.ID())
	for _, addr := range p2p.PeerAddrs(h) {
		log.Printf("addr=%s", addr)
	}
	log.Printf("relay config listen=%s health_listen=%s enable_relay_service=%t enable_autonat_service=%t enable_discovery_pubsub=%t force_reachability_public=%t", cfg.Listen, cfg.HealthListen, cfg.EnableRelayService, cfg.EnableAutoNATService, cfg.EnableDiscoveryPubSub, cfg.ForceReachabilityPub)
	log.Printf("relay limits reservations=%d reservations_per_ip=%d reservations_per_asn=%d circuits_per_peer=%d buffer_size=%d reservation_ttl=%s limit_duration=%s limit_data_bytes=%d", cfg.MaxReservations, cfg.MaxReservationsPerIP, cfg.MaxReservationsPerASN, cfg.MaxCircuitsPerPeer, cfg.BufferSize, cfg.ReservationTTL, cfg.RelayLimitDuration, cfg.RelayLimitData)
	if usingPrivateNetwork {
		log.Printf("libp2p private network enabled")
	}
	if hasAllowlist {
		log.Printf("peer allowlist enabled peers=%d", len(allowedPeers))
	}
	if cfg.PrintRunCommands {
		printStartupCommandHints(h, cfg.RelayPublicAddr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if cfg.EnableDiscoveryPubSub {
		if err := startDiscoveryPubSubRouter(ctx, h); err != nil {
			log.Fatalf("start discovery pubsub router: %v", err)
		}
	}

	var hs *http.Server
	if cfg.HealthListen != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		mux.HandleFunc("/debug/peer", func(w http.ResponseWriter, _ *http.Request) {
			resp := map[string]any{
				"peer_id":                   h.ID().String(),
				"addrs":                     p2p.PeerAddrs(h),
				"enable_relay_service":      cfg.EnableRelayService,
				"enable_autonat_service":    cfg.EnableAutoNATService,
				"enable_discovery_pubsub":   cfg.EnableDiscoveryPubSub,
				"force_reachability_public": cfg.ForceReachabilityPub,
				"using_private_network":     usingPrivateNetwork,
				"allowlist_enabled":         hasAllowlist,
				"allowlist_peers":           len(allowedPeers),
				"relay_public_addr":         relayAdvertiseAddr(h, cfg.RelayPublicAddr),
				"print_run_commands":        cfg.PrintRunCommands,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
		})
		hs = &http.Server{
			Addr:    cfg.HealthListen,
			Handler: mux,
		}
		go func() {
			log.Printf("relay health listening on %s", cfg.HealthListen)
			if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("relay health server: %v", err)
			}
		}()
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("received signal %s, shutting down", sig)
	cancel()

	if hs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(ctx)
	}
}

func printStartupCommandHints(h host.Host, configuredAddr string) {
	relayAddr := relayAdvertiseAddr(h, configuredAddr)
	if relayAddr == "" {
		log.Printf("startup command hints unavailable: no relay multiaddr found")
		return
	}

	log.Printf("startup command hints:")
	log.Printf("  relay_addr=%s", relayAddr)
	log.Printf("  edge-gateway:")
	log.Printf("    EDGE_LISTEN=:8443 \\")
	log.Printf("    EDGE_ADMIN_LISTEN=127.0.0.1:8444 \\")
	log.Printf("    EDGE_P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \\")
	log.Printf("    EDGE_SEED=edge-seed \\")
	log.Printf("    LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \\")
	log.Printf("    BOOTSTRAP_PEERS=%s \\", relayAddr)
	log.Printf("    RELAY_PEERS=%s \\", relayAddr)
	log.Printf("    ./edge-gateway")
	log.Printf("  service-agent:")
	log.Printf("    SERVICE_NAME=lmstudio \\")
	log.Printf("    SERVICE_TARGET=http://127.0.0.1:1234 \\")
	log.Printf("    SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40123 \\")
	log.Printf("    NODE_SEED=service-lmstudio-seed \\")
	log.Printf("    LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \\")
	log.Printf("    BOOTSTRAP_PEERS=%s \\", relayAddr)
	log.Printf("    RELAY_PEERS=%s \\", relayAddr)
	log.Printf("    ENABLE_AUTORELAY=true \\")
	log.Printf("    ENABLE_HOLE_PUNCHING=true \\")
	log.Printf("    FORCE_REACHABILITY_PRIVATE=true \\")
	log.Printf("    HEARTBEAT_INTERVAL=5s \\")
	log.Printf("    ./service-agent")
	log.Printf("  note: replace seed/service values as needed and keep the PSK file private")
}

func relayAdvertiseAddr(h host.Host, configuredAddr string) string {
	if configuredAddr != "" {
		if strings.Contains(configuredAddr, "/p2p/") {
			return configuredAddr
		}
		return strings.TrimRight(configuredAddr, "/") + "/p2p/" + h.ID().String()
	}

	for _, addr := range p2p.PeerAddrs(h) {
		if strings.Contains(addr, "/ip4/127.") || strings.Contains(addr, "/ip6/::1") || strings.Contains(addr, "/ip4/0.0.0.0") {
			continue
		}
		return addr
	}
	addrs := p2p.PeerAddrs(h)
	if len(addrs) == 0 {
		return ""
	}
	return addrs[0]
}

func startDiscoveryPubSubRouter(ctx context.Context, h host.Host) error {
	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		return err
	}
	topic, err := ps.Join(discovery.DiscoveryTopic)
	if err != nil {
		return err
	}
	sub, err := topic.Subscribe()
	if err != nil {
		return err
	}

	log.Printf("discovery pubsub router joined topic %s", discovery.DiscoveryTopic)
	go func() {
		defer sub.Cancel()
		for {
			if _, err := sub.Next(ctx); err != nil {
				log.Printf("discovery pubsub router stopped: %v", err)
				return
			}
		}
	}()
	return nil
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getenvBool(key string, def bool) bool {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, raw, err)
	}
	return v
}

func getenvInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, raw, err)
	}
	return v
}

func getenvInt64(key string, def int64) int64 {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, raw, err)
	}
	return v
}

func getenvDuration(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := time.ParseDuration(raw)
	if err != nil {
		log.Fatalf("invalid %s=%q: %v", key, raw, err)
	}
	return v
}
