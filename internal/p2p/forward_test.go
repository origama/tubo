package p2p

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"

	"p2p-api-tunnel/internal/protocol"
)

func TestHandleServiceStreamForwardsToHTTPSTarget(t *testing.T) {
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			t.Error("expected upstream request to use TLS")
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("path: got %q want /v1/models", r.URL.Path)
		}
		if r.URL.RawQuery != "provider=local" {
			t.Errorf("query: got %q want provider=local", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"local-model"}]}`))
	}))
	defer tlsServer.Close()

	withTrustedDefaultHTTPClient(t, tlsServer.Certificate())

	resp := serviceRoundTrip(t, tlsServer.URL, "GET", "/v1/models", "provider=local", nil, http.NoBody)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type: got %q", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "local-model") {
		t.Fatalf("unexpected body: %s", body)
	}
}

func TestHandleServiceStreamForwardsSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path: got %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer does not support flushing")
		}
		_, _ = w.Write([]byte("data: {\"delta\":\"hel\"}\n\n"))
		flusher.Flush()
		time.Sleep(75 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"delta\":\"lo\"}\n\n"))
		flusher.Flush()
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	resp := serviceRoundTrip(t, upstream.URL, "POST", "/v1/chat/completions", "", map[string][]string{"Content-Type": {"application/json"}}, strings.NewReader(`{"stream":true}`))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content-type: got %q", got)
	}

	buf := make([]byte, 64)
	firstReadStart := time.Now()
	n, err := resp.Body.Read(buf)
	if err != nil {
		t.Fatalf("read first SSE chunk: %v", err)
	}
	if elapsed := time.Since(firstReadStart); elapsed > 60*time.Millisecond {
		t.Fatalf("first SSE chunk looked buffered: elapsed=%s", elapsed)
	}
	first := string(buf[:n])
	if !strings.Contains(first, `"hel"`) {
		t.Fatalf("first SSE chunk missing first delta: %q", first)
	}

	rest, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read remaining SSE body: %v", err)
	}
	full := first + string(rest)
	for _, want := range []string{`"hel"`, `"lo"`, "[DONE]"} {
		if !strings.Contains(full, want) {
			t.Fatalf("SSE response missing %q: %q", want, full)
		}
	}
}

func TestHandleServiceStreamForwardsGRPCOverHTTP2Upstream(t *testing.T) {
	grpcFrame, _ := hex.DecodeString("0000000000") // empty unary gRPC message frame
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor != 2 {
			t.Errorf("upstream protocol: got %s want HTTP/2", r.Proto)
		}
		if got := r.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/grpc") {
			t.Errorf("content-type: got %q want application/grpc", got)
		}
		w.Header().Set("Content-Type", "application/grpc")
		w.Header().Set("Trailer", "Grpc-Status")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(grpcFrame)
		w.Header().Set("Grpc-Status", "0")
	}))
	upstream.EnableHTTP2 = true
	upstream.StartTLS()
	defer upstream.Close()

	withTrustedDefaultHTTPClient(t, upstream.Certificate())

	resp := serviceRoundTrip(t, upstream.URL, "POST", "/inference.Model/Chat", "", map[string][]string{"Content-Type": {"application/grpc"}}, bytes.NewReader(grpcFrame))
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "application/grpc") {
		t.Fatalf("content-type: got %q want application/grpc", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read gRPC body: %v", err)
	}
	if !bytes.Equal(body, grpcFrame) {
		t.Fatalf("gRPC body frame mismatch: got %x want %x", body, grpcFrame)
	}
	if got := resp.Trailer.Get("Grpc-Status"); got != "" {
		t.Fatalf("unexpected grpc trailer propagated by current HTTP/1 response shim: %q", got)
	}
}

func TestHandleClientRequestSendsFinalChunkForEmptyBodyReader(t *testing.T) {
	reqReader, reqWriter := io.Pipe()
	respReader, respWriter := io.Pipe()
	stream := &memoryClientStream{reader: respReader, writer: reqWriter}

	serverDone := make(chan error, 1)
	go func() {
		defer reqReader.Close()
		defer respWriter.Close()

		reader := protocol.NewStreamReader(reqReader)
		req, err := reader.ReadRequestHeader()
		if err != nil {
			serverDone <- err
			return
		}
		if req.ContentLengthHint != -1 {
			t.Errorf("content length hint: got %d want -1", req.ContentLengthHint)
		}

		chunk, err := reader.ReadBodyChunk()
		if err != nil {
			serverDone <- err
			return
		}
		if len(chunk.Data) != 0 || !chunk.IsFinal {
			t.Errorf("empty final chunk: len=%d final=%t", len(chunk.Data), chunk.IsFinal)
		}

		writer := protocol.NewStreamWriter(respWriter)
		if err := writer.WriteResponseHeader(&protocol.ResponseHeader{StatusCode: 204, StatusText: "No Content"}); err != nil {
			serverDone <- err
			return
		}
		if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: []byte{}, IsFinal: true}); err != nil {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	resp, err := HandleClientRequest(stream, "GET", "/v1/dummy", "", nil, strings.NewReader(""))
	if err != nil {
		t.Fatalf("HandleClientRequest: %v", err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadAll(resp.Body); err != nil {
		t.Fatalf("read response body: %v", err)
	}

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("server side failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("server side timed out waiting for final body chunk")
	}
}

func serviceRoundTrip(t *testing.T, target, method, path, query string, headers map[string][]string, body io.Reader) *http.Response {
	t.Helper()

	clientToServiceR, clientToServiceW := io.Pipe()
	serviceToClientR, serviceToClientW := io.Pipe()

	clientStream := &memoryClientStream{reader: serviceToClientR, writer: clientToServiceW}
	serviceStream := &memoryClientStream{reader: clientToServiceR, writer: serviceToClientW, conn: fakeConn{}}

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		HandleServiceStream(target)(serviceStream)
	}()

	resp, err := HandleClientRequest(clientStream, method, path, query, headers, body)
	if err != nil {
		_ = clientStream.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
		}
		t.Fatalf("HandleClientRequest: %v", err)
	}
	t.Cleanup(func() {
		_ = resp.Body.Close()
		_ = clientStream.Close()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			t.Log("service handler did not exit before cleanup timeout")
		}
	})
	return resp
}

func withTrustedDefaultHTTPClient(t *testing.T, cert *x509.Certificate) {
	t.Helper()
	old := http.DefaultClient
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	http.DefaultClient = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}, ForceAttemptHTTP2: true}}
	t.Cleanup(func() { http.DefaultClient = old })
}

type memoryClientStream struct {
	reader *io.PipeReader
	writer *io.PipeWriter
	conn   network.Conn
}

func (s *memoryClientStream) Read(p []byte) (int, error)                   { return s.reader.Read(p) }
func (s *memoryClientStream) Write(p []byte) (int, error)                  { return s.writer.Write(p) }
func (s *memoryClientStream) Close() error                                 { _ = s.reader.Close(); return s.writer.Close() }
func (s *memoryClientStream) CloseWrite() error                            { return s.writer.Close() }
func (s *memoryClientStream) CloseRead() error                             { return s.reader.Close() }
func (s *memoryClientStream) Reset() error                                 { return s.Close() }
func (s *memoryClientStream) ResetWithError(network.StreamErrorCode) error { return s.Close() }
func (s *memoryClientStream) SetDeadline(time.Time) error                  { return nil }
func (s *memoryClientStream) SetReadDeadline(time.Time) error              { return nil }
func (s *memoryClientStream) SetWriteDeadline(time.Time) error             { return nil }
func (s *memoryClientStream) ID() string                                   { return "memory-stream" }
func (s *memoryClientStream) Protocol() libprotocol.ID                     { return ProtocolID }
func (s *memoryClientStream) SetProtocol(libprotocol.ID) error             { return nil }
func (s *memoryClientStream) Stat() network.Stats                          { return network.Stats{} }
func (s *memoryClientStream) Conn() network.Conn {
	if s.conn != nil {
		return s.conn
	}
	return fakeConn{}
}
func (s *memoryClientStream) Scope() network.StreamScope { return nil }

type fakeConn struct{}

func (fakeConn) Close() error                                      { return nil }
func (fakeConn) CloseWithError(network.ConnErrorCode) error        { return nil }
func (fakeConn) ID() string                                        { return "fake-conn" }
func (fakeConn) NewStream(context.Context) (network.Stream, error) { return nil, nil }
func (fakeConn) GetStreams() []network.Stream                      { return nil }
func (fakeConn) IsClosed() bool                                    { return false }
func (fakeConn) As(any) bool                                       { return false }
func (fakeConn) LocalPeer() peer.ID                                { return peer.ID("local") }
func (fakeConn) RemotePeer() peer.ID                               { return peer.ID("remote") }
func (fakeConn) RemotePublicKey() crypto.PubKey                    { return nil }
func (fakeConn) ConnState() network.ConnectionState                { return network.ConnectionState{} }
func (fakeConn) LocalMultiaddr() multiaddr.Multiaddr {
	return multiaddr.StringCast("/ip4/127.0.0.1/tcp/1")
}
func (fakeConn) RemoteMultiaddr() multiaddr.Multiaddr {
	return multiaddr.StringCast("/ip4/127.0.0.1/tcp/2")
}
func (fakeConn) Stat() network.ConnStats  { return network.ConnStats{} }
func (fakeConn) Scope() network.ConnScope { return nil }
