package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"p2p-api-tunnel/internal/p2p"
)

func main() {
	listen := getenv("BRIDGE_LISTEN", "127.0.0.1:18081")
	serviceAddr := getenv("SERVICE_ADDR", "")
	serviceSeed := getenv("SERVICE_SEED", "")
	serviceListen := getenv("SERVICE_P2P_LISTEN", "/ip4/127.0.0.1/tcp/40123")
	if serviceAddr == "" && serviceSeed == "" {
		log.Fatal("set SERVICE_ADDR or SERVICE_SEED")
	}

	var serviceInfo peer.AddrInfo
	var err error
	if serviceAddr != "" {
		serviceInfo, err = p2p.AddrInfoFromString(serviceAddr)
		if err != nil {
			log.Fatal(err)
		}
	} else {
		serviceInfo, err = p2p.AddrInfoFromListenAndSeed(serviceListen, serviceSeed)
		if err != nil {
			log.Fatal(err)
		}
	}

	h, err := p2p.NewHostWithSeed(getenv("BRIDGE_P2P_LISTEN", "/ip4/127.0.0.1/tcp/0"), getenv("BRIDGE_SEED", "bridge-demo-seed"))
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Connect(ctx, serviceInfo); err != nil {
		log.Fatalf("connect service peer: %v", err)
	}
	log.Printf("connected to service peer %s", serviceInfo.ID)
	log.Printf("bridge peer_id=%s", h.ID())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s, err := h.NewStream(context.Background(), serviceInfo.ID, p2p.ProtocolID)
		if err != nil {
			http.Error(w, fmt.Sprintf("open stream: %v", err), http.StatusBadGateway)
			return
		}
		defer s.Close()

		// Build headers map — preserves multi-value headers
		headers := make(map[string][]string, len(r.Header))
		for k, vals := range r.Header {
			switch k {
			case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
				continue // skip hop-by-hop
			default:
				headers[k] = vals
			}
		}

		resp, err := p2p.HandleClientRequest(s, r.Method, r.URL.Path, r.URL.RawQuery, headers, r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()

		// Write response headers — multi-value preserved
		for k, vals := range resp.Header {
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)

		// Stream response body directly to client
		if _, err := io.Copy(w, resp.Body); err != nil {
			log.Printf("streaming response body: %v", err)
		}
	})

	log.Printf("client bridge listening on %s", listen)
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