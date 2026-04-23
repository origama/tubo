package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	listen := getenv("EDGE_LISTEN", ":8443")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte("edge gateway scaffold"))
	})

	log.Printf("edge gateway listening on %s", listen)
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
