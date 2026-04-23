package forwarding

import "net/http"

type Forwarder interface {
	Forward(req *http.Request) (*http.Response, error)
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
