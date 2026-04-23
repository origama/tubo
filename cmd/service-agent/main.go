package main

import (
	"log"
	"net/http"
	"os"
	"p2p-api-tunnel/internal/p2p"
)

func main() {
	listen := getenv("SERVICE_P2P_LISTEN", "/ip4/127.0.0.1/tcp/40123")
	localTarget := getenv("SERVICE_TARGET", "http://127.0.0.1:8000")
	seed := getenv("NODE_SEED", "service-demo-seed")

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

	select {}
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
