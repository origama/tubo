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

	psk, usingPrivateNetwork, err := p2p.LoadPrivateNetworkPSKFromEnv()
	if err != nil {
		log.Fatalf("load private network key: %v", err)
	}

	h, err := p2p.NewHostWithSeedAndPSK(
		getenv("BRIDGE_P2P_LISTEN", "/ip4/127.0.0.1/tcp/0"),
		getenv("BRIDGE_SEED", "bridge-demo-seed"),
		psk,
	)
	if err != nil {
		log.Fatal(err)
	}
	defer h.Close()
	p2p.LogNetworkEvents(h, "client-bridge")
	if usingPrivateNetwork {
		log.Printf("libp2p private network enabled")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := h.Connect(ctx, serviceInfo); err != nil {
		log.Fatalf("connect service peer: %v", err)
	}
	log.Printf("connected to service peer %s", serviceInfo.ID)
	log.Printf("bridge peer_id=%s", h.ID())
	log.Printf("client bridge config listen=%s service_peer=%s service_addrs=%v private_network=%t", listen, serviceInfo.ID, serviceInfo.Addrs, usingPrivateNetwork)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("bridge request method=%s path=%s query=%q remote=%s service_peer=%s", r.Method, r.URL.Path, r.URL.RawQuery, r.RemoteAddr, serviceInfo.ID)
		s, err := h.NewStream(context.Background(), serviceInfo.ID, p2p.ProtocolID)
		if err != nil {
			log.Printf("bridge open stream failed service_peer=%s err=%v duration=%s", serviceInfo.ID, err, time.Since(start))
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
			log.Printf("bridge forward failed service_peer=%s err=%v duration=%s", serviceInfo.ID, err, time.Since(start))
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
		bytesWritten, err := io.Copy(w, resp.Body)
		if err != nil {
			log.Printf("streaming response body: %v", err)
		}
		log.Printf("bridge completed service_peer=%s status=%d bytes=%d duration=%s", serviceInfo.ID, resp.StatusCode, bytesWritten, time.Since(start))
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
