package forwarding

import (
	"io"
	"net/http"
	"net/http/httputil"
)

// HTTPForwarder forwards HTTP requests to a local backend URL using http.ReverseProxy.
type HTTPForwarder struct {
	proxy *httputil.ReverseProxy
}

// NewHTTPForwarder creates a forwarder that proxies requests to the given target URL.
func NewHTTPForwarder(targetURL string) *HTTPForwarder {
	p := &httputil.ReverseProxy{
		Director: func(r *http.Request) {
			r.URL.Scheme = "http"
			r.Host = targetURL
			r.URL.Host = targetURL
			// Keep original path and query
		},
	}
	return &HTTPForwarder{proxy: p}
}

// Forward proxies the request to the backend. The response body must be closed by the caller.
func (f *HTTPForwarder) Forward(req *http.Request) (*http.Response, error) {
	// Use a transport that doesn't follow redirects and returns raw response
	transport := &roundTripTransport{director: f.proxy.Director}

	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// roundTripTransport is a simple RoundTripper that applies the director and makes the request.
type roundTripTransport struct {
	director func(*http.Request)
}

func (t *roundTripTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Clone the request to avoid mutating the original
	cloned := req.Clone(req.Context())
	t.director(cloned)

	// Strip hop-by-hop headers from outgoing request
	StripHopByHopHeaders(cloned.Header)

	resp, err := http.DefaultTransport.RoundTrip(cloned)
	if err != nil {
		return nil, err
	}

	// Strip hop-by-hop headers from response
	StripHopByHopHeaders(resp.Header)

	// Wrap body to ensure original is closed
	resp.Body = &closeWrapper{resp.Body}
	return resp, nil
}

type closeWrapper struct {
	io.ReadCloser
}

func (w *closeWrapper) Close() error {
	return w.ReadCloser.Close()
}
