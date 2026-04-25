package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"p2p-api-tunnel/internal/discovery"
	"p2p-api-tunnel/internal/p2p"
)

func main() {
	listen := getenv("SERVICE_P2P_LISTEN", "/ip4/127.0.0.1/tcp/40123")
	localTarget := getenv("SERVICE_TARGET", "http://127.0.0.1:8000")
	seed := getenv("NODE_SEED", "service-demo-seed")
	serviceName := getenv("SERVICE_NAME", "demo-service")

	heartbeatInterval, err := time.ParseDuration(getenv("HEARTBEAT_INTERVAL", "15s"))
	if err != nil {
		log.Fatalf("invalid HEARTBEAT_INTERVAL %q: %v", getenv("HEARTBEAT_INTERVAL", ""), err)
	}

	h, err := p2p.NewHostWithSeed(listen, seed)
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	h.SetStreamHandler(p2p.ProtocolID, p2p.HandleServiceStream(localTarget))

	log.Println("service agent ready")
	log.Printf("peer_id=%s", h.ID())
	for _, addr := range p2p.PeerAddrs(h) {
		log.Printf("addr=%s", addr)
	}
	log.Printf("forwarding to %s", localTarget)

	if health := getenv("SERVICE_HEALTH_LISTEN", "127.0.0.1:8091"); health != "" {
		go serveHealth(health, h.ID().String(), p2p.PeerAddrs(h))
	}

	// --- GossipSub discovery ---
	log.Println("initializing gossipsub")
	gs, err := pubsub.NewGossipSub(context.Background(), h)
	if err != nil {
		log.Fatalf("create gossipsub: %v", err)
	}

	topic, err := gs.Join(discovery.DiscoveryTopic)
	if err != nil {
		log.Fatalf("join discovery topic: %v", err)
	}
	log.Printf("joined topic %s", discovery.DiscoveryTopic)

	privKey := h.Peerstore().PrivKey(h.ID())
	if privKey == nil {
		log.Fatalf("no private key found for peer %s", h.ID())
	}
	publisher := discovery.NewPublisher(topic, privKey)

	// Dial bootstrap peers before publishing.
	bootstrapPeers := getenv("BOOTSTRAP_PEERS", "")
	if bootstrapPeers != "" {
		log.Println("dialing bootstrap peers")
		for _, raw := range strings.Split(bootstrapPeers, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			maddr, err := multiaddr.NewMultiaddr(raw)
			if err != nil {
				log.Printf("invalid bootstrap peer %q: %v", raw, err)
				continue
			}
			info, err := peerInfoFromAddr(maddr)
			if err != nil {
				log.Printf("bootstrap peer parse error %q: %v", raw, err)
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := h.Connect(ctx, *info); err != nil {
				log.Printf("failed to dial bootstrap peer %s: %v", info.ID, err)
			} else {
				log.Printf("connected to bootstrap peer %s", info.ID)
			}
			cancel()
		}
	}

	// Publish initial announcement.
	log.Println("publishing service announcement")
	ann := discovery.Announcement{
		ServiceName: serviceName,
		PeerID:      h.ID(),
		Addresses:   p2p.PeerAddrs(h),
		TTL:         30 * time.Second,
	}
	if err := publisher.Publish(context.Background(), ann); err != nil {
		log.Printf("publish announcement failed: %v", err)
	} else {
		log.Printf("announced service %q (peer=%s)", serviceName, h.ID())
	}

	// --- Heartbeat loop (lease renewal) ---
	hb := discovery.NewHeartbeatLoop(publisher, ann, heartbeatInterval)
	hb.Start(context.Background())
	log.Printf("heartbeat loop started for service %q", serviceName)

	// Block until shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		log.Printf("received signal %s, shutting down", sig)
	}
	hb.Stop()
}

func peerInfoFromAddr(maddr multiaddr.Multiaddr) (*peer.AddrInfo, error) {
	return peer.AddrInfoFromP2pAddr(maddr)
}

func serveHealth(listen, peerID string, addrs []string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/debug/peer", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("peer_id=" + peerID + "\n"))
		for _, addr := range addrs {
			_, _ = w.Write([]byte("addr=" + addr + "\n"))
		}
	})
	log.Printf("service health listening on %s", listen)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatal(err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
