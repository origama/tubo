package forwarding

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStripHopByHopHeaders(t *testing.T) {
	h := make(http.Header)
	// Hop-by-hop headers that should be removed
	h.Set("Connection", "keep-alive")
	h.Set("Keep-Alive", "timeout=5")
	h.Set("Proxy-Authenticate", "Basic")
	h.Set("Proxy-Authorization", "Basic abc123")
	h.Set("Te", "deflate")
	h.Set("Trailer", "X-Checksum")
	h.Set("Transfer-Encoding", "chunked")
	h.Set("Upgrade", "h2c")
	// Regular headers that should be preserved
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "text/html")
	h.Set("X-Custom", "my-value")

	StripHopByHopHeaders(h)

	// Verify hop-by-hop headers are removed
	for _, k := range []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		if got := h.Get(k); got != "" {
			t.Errorf("hop-by-hop header %q should be removed, got: %q", k, got)
		}
	}

	// Verify regular headers are preserved
	if got := h.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", got)
	}
	if got := h.Get("Accept"); got != "text/html" {
		t.Errorf("Accept: want text/html, got %q", got)
	}
	if got := h.Get("X-Custom"); got != "my-value" {
		t.Errorf("X-Custom: want my-value, got %q", got)
	}
}

func TestCopyHopByHopHeaders(t *testing.T) {
	src := make(http.Header)
	src.Set("Connection", "keep-alive")
	src.Set("Keep-Alive", "timeout=5")
	src.Set("Transfer-Encoding", "chunked")
	src.Set("Content-Type", "application/json")
	src.Add("Accept", "text/html")
	src.Add("Accept", "application/xml")
	src.Set("X-Custom", "my-value")

	dst := make(http.Header)
	CopyHopByHopHeaders(dst, src)

	// Verify hop-by-hop headers were NOT copied
	for _, k := range []string{"Connection", "Keep-Alive", "Transfer-Encoding"} {
		if got := dst.Get(k); got != "" {
			t.Errorf("hop-by-hop header %q should not be in dest, got: %q", k, got)
		}
	}

	// Verify regular headers were copied
	if got := dst.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type: want application/json, got %q", got)
	}
	if vals := dst["Accept"]; len(vals) != 2 || vals[0] != "text/html" || vals[1] != "application/xml" {
		t.Errorf("Accept: want [text/html application/xml], got %v", vals)
	}
	if got := dst.Get("X-Custom"); got != "my-value" {
		t.Errorf("X-Custom: want my-value, got %q", got)
	}
}

func TestHTTPForwarderIntegration(t *testing.T) {
	// Start a test echo server that reflects back method, path, and headers.
	echoServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo-Method", r.Method)
		w.Header().Set("X-Echo-Path", r.URL.Path)
		w.Header().Set("X-Echo-Custom", r.Header.Get("X-Custom"))
		w.WriteHeader(http.StatusOK)
	}))
	defer echoServer.Close()

	forwarder := NewHTTPForwarder(echoServer.Listener.Addr().String())

	req := httptest.NewRequest("POST", "/api/test?foo=bar", nil)
	req.RequestURI = "" // Client requests must not have RequestURI set
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Custom", "hello-world")
	req.Header.Set("Connection", "keep-alive") // hop-by-hop, should be stripped before forwarding

	resp, err := forwarder.Forward(req)
	if err != nil {
		t.Fatalf("Forward() error: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// Verify response status
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status code: want 200, got %d", resp.StatusCode)
	}

	// Echo server should reflect method and path
	if got := resp.Header.Get("X-Echo-Method"); got != "POST" {
		t.Errorf("X-Echo-Method: want POST, got %q", got)
	}
	if got := resp.Header.Get("X-Echo-Path"); got != "/api/test" {
		t.Errorf("X-Echo-Path: want /api/test, got %q", got)
	}

	// Regular headers should be forwarded
	if got := resp.Header.Get("X-Echo-Custom"); got != "hello-world" {
		t.Errorf("X-Echo-Custom: want hello-world, got %q", got)
	}

	// Hop-by-hop headers from the response should be stripped by RoundTrip
	if got := resp.Header.Get("Connection"); got != "" {
		t.Errorf("response Connection header should be stripped, got: %q", got)
	}

	// Body should be empty (echo server doesn't write body)
	if len(body) > 0 {
		t.Logf("body not empty: %s", string(body))
	}
}
