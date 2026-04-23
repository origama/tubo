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
	var servicePeerID string
	var err error
	if serviceAddr != "" {
		serviceInfo, err = p2p.AddrInfoFromString(serviceAddr)
		if err != nil {
			log.Fatal(err)
		}
		servicePeerID = serviceInfo.ID.String()
	} else {
		serviceInfo, err = p2p.AddrInfoFromListenAndSeed(serviceListen, serviceSeed)
		if err != nil {
			log.Fatal(err)
		}
		servicePeerID = serviceInfo.ID.String()
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
	log.Printf("connected to service peer %s", servicePeerID)
	log.Printf("bridge peer_id=%s", h.ID())

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		reqBody, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_ = r.Body.Close()

		headers := map[string]string{}
		for k, vals := range r.Header {
			if len(vals) > 0 {
				headers[k] = vals[0]
			}
		}
		delete(headers, "Connection")
		delete(headers, "Keep-Alive")
		delete(headers, "Proxy-Authenticate")
		delete(headers, "Proxy-Authorization")
		delete(headers, "Te")
		delete(headers, "Trailer")
		delete(headers, "Transfer-Encoding")
		delete(headers, "Upgrade")

		s, err := h.NewStream(context.Background(), serviceInfo.ID, p2p.ProtocolID)
		if err != nil {
			http.Error(w, fmt.Sprintf("open stream: %v", err), http.StatusBadGateway)
			return
		}
		defer s.Close()

		resp, err := p2p.HandleClientRequest(s, p2p.RequestMessage{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Headers:  headers,
			Body:     reqBody,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}

		for k, v := range resp.Headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(resp.Body)
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
