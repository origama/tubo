package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
)

type response struct {
	Instance string              `json:"instance"`
	Method   string              `json:"method"`
	Path     string              `json:"path"`
	RawQuery string              `json:"raw_query"`
	Headers  map[string][]string `json:"headers"`
	BodyB64  string              `json:"body_b64"`
}

func main() {
	listen := flag.String("listen", "127.0.0.1:18080", "HTTP listen address")
	instance := flag.String("instance", "test-http", "response instance marker")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{
			Instance: *instance,
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Headers:  r.Header.Clone(),
			BodyB64:  base64.StdEncoding.EncodeToString(body),
		})
	})
	log.Printf("test HTTP server listening on %s", *listen)
	log.Fatal(http.ListenAndServe(*listen, mux))
}
