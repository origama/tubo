package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	listen := getenv("CONTROL_PLANE_LISTEN", ":8080")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/services", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte("control-plane scaffold"))
	})

	log.Printf("control plane listening on %s", listen)
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
