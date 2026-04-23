package forwarding

import "net/http"

type Forwarder interface {
	Forward(req *http.Request) (*http.Response, error)
}

// StripHopByHopHeaders removes hop-by-hop headers from the given header map.
func StripHopByHopHeaders(h http.Header) {
	for _, k := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(k)
	}
}

func CopyHopByHopHeaders(dst, src http.Header) {
	for k, values := range src {
		switch k {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		default:
			for _, v := range values {
				dst.Add(k, v)
			}
		}
	}
}
