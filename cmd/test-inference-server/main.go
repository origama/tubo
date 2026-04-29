package main

import (
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	listen := getenv("TEST_INFERENCE_LISTEN", "0.0.0.0:9443")
	certFile := os.Getenv("TEST_INFERENCE_TLS_CERT_FILE")
	keyFile := os.Getenv("TEST_INFERENCE_TLS_KEY_FILE")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list",
			"data":   []map[string]string{{"id": "test-local-model", "object": "model"}},
		})
	})
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\n"))
		flusher.Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"))
		flusher.Flush()
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
		flusher.Flush()
	})
	mux.HandleFunc("/inference.Model/Chat", func(w http.ResponseWriter, r *http.Request) {
		frame, _ := hex.DecodeString("0000000000")
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(frame)
		w.Header().Set("Grpc-Status", "0")
	})

	log.Printf("test inference server listening on %s tls=%t", listen, certFile != "" && keyFile != "")
	if certFile != "" && keyFile != "" {
		log.Fatal(http.ListenAndServeTLS(listen, certFile, keyFile, mux))
	}
	log.Fatal(http.ListenAndServe(listen, mux))
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
