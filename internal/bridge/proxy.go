package bridge

import "net/http"

type Proxy struct {
	UpstreamURL string
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotImplemented)
	_, _ = w.Write([]byte("bridge proxy not implemented yet"))
}
