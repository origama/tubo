package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"p2p-api-tunnel/internal/p2p"
)

type relayConfig struct {
	Listen                string
	Seed                  string
	HealthListen          string
	EnableRelayService    bool
	EnableAutoNATService  bool
	ForceReachabilityPub  bool
	MaxReservations       int
	MaxReservationsPerIP  int
	MaxReservationsPerASN int
	MaxCircuitsPerPeer    int
	BufferSize            int
	ReservationTTL        time.Duration
	RelayLimitDuration    time.Duration
	RelayLimitData        int64
}

func main() {
	cfg := relayConfig{
		Listen:                getenv("P2P_LISTEN", "/ip4/0.0.0.0/tcp/4001"),
		Seed:                  getenv("NODE_SEED", "public-relay-seed"),
		HealthListen:          getenv("RELAY_HEALTH_LISTEN", "127.0.0.1:8092"),
		EnableRelayService:    getenvBool("ENABLE_RELAY_SERVICE", true),
		EnableAutoNATService:  getenvBool("ENABLE_AUTONAT_SERVICE", true),
		ForceReachabilityPub:  getenvBool("FORCE_REACHABILITY_PUBLIC", true),
		MaxReservations:       getenvInt("RELAY_MAX_RESERVATIONS", 256),
		MaxReservationsPerIP:  getenvInt("RELAY_MAX_RESERVATIONS_PER_IP", 16),
		MaxReservationsPerASN: getenvInt("RELAY_MAX_RESERVATIONS_PER_ASN", 64),
		MaxCircuitsPerPeer:    getenvInt("RELAY_MAX_CIRCUITS", 16),
		BufferSize:            getenvInt("RELAY_BUFFER_SIZE", 4096),
		ReservationTTL:        getenvDuration("RELAY_RESERVATION_TTL", time.Hour),
		RelayLimitDuration:    getenvDuration("RELAY_LIMIT_DURATION", 5*time.Minute),
		RelayLimitData:        getenvInt64("RELAY_LIMIT_DATA_BYTES", 1<<20),
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

	log.Printf("p2p relay ready")
	log.Printf("peer_id=%s", h.ID())
	for _, addr := range p2p.PeerAddrs(h) {
		log.Printf("addr=%s", addr)
	}
	log.Printf("enable_relay_service=%t enable_autonat_service=%t force_reachability_public=%t", cfg.EnableRelayService, cfg.EnableAutoNATService, cfg.ForceReachabilityPub)
	if usingPrivateNetwork {
		log.Printf("libp2p private network enabled")
	}
	if hasAllowlist {
		log.Printf("peer allowlist enabled peers=%d", len(allowedPeers))
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
				"force_reachability_public": cfg.ForceReachabilityPub,
				"using_private_network":     usingPrivateNetwork,
				"allowlist_enabled":         hasAllowlist,
				"allowlist_peers":           len(allowedPeers),
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

	if hs != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(ctx)
	}
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
