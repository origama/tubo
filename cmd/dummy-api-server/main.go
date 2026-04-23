package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
)

type dummyResponse struct {
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	RawQuery   string            `json:"raw_query"`
	Headers    map[string]string `json:"headers"`
	BodyB64    string            `json:"body_b64,omitempty"`
}

func main() {
	listen := getenv("DUMMY_API_LISTEN", "127.0.0.1:8000")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/dummy", func(w http.ResponseWriter, r *http.Request) {
		headers := map[string]string{}
		for k, vals := range r.Header {
			if len(vals) > 0 {
				headers[k] = vals[0]
			}
		}
		body, _ := ioReadAll(r)
		resp := dummyResponse{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Headers:  headers,
			BodyB64:  base64.StdEncoding.EncodeToString(body),
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	})

	log.Printf("dummy api listening on %s", listen)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatal(err)
	}
}

func ioReadAll(r *http.Request) ([]byte, error) {
	defer r.Body.Close()
	return io.ReadAll(r.Body)
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
