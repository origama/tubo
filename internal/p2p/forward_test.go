package p2p

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo/internal/protocol"
	statspkg "github.com/origama/tubo/internal/runtime/stats"
)

func TestHandleClientRequestUsesContentLengthHeaderAsHint(t *testing.T) {
	reqReader, reqWriter := io.Pipe()
	respReader, respWriter := io.Pipe()
	stream := &memoryClientStream{reader: respReader, writer: reqWriter, protocolID: ProtocolID}

	serverDone := make(chan error, 1)
	go func() {
		defer reqReader.Close()
		defer respWriter.Close()

		reader := protocol.NewStreamReader(reqReader)
		hello, err := reader.ReadHello()
		if err != nil {
			serverDone <- err
			return
		}
		if hello.Role != "edge" {
			serverDone <- fmt.Errorf("hello role: got %q want edge", hello.Role)
			return
		}
		writer := protocol.NewStreamWriter(respWriter)
		if err := writer.WriteHello(&protocol.Hello{ProtocolMajor: uint16(protocol.ProtocolMajor), ProtocolMinor: uint16(protocol.ProtocolMinor), Role: "service", Capabilities: protocol.SupportedCapabilities()}); err != nil {
			serverDone <- err
			return
		}
		req, err := reader.ReadRequestHeader()
		if err != nil {
			serverDone <- err
			return
		}
		if req.ContentLengthHint != 3 {
			serverDone <- fmt.Errorf("content length hint: got %d want 3", req.ContentLengthHint)
			return
		}
		var got []byte
		final := false
		for !final {
			chunk, err := reader.ReadBodyChunk()
			if err != nil {
				serverDone <- err
				return
			}
			got = append(got, chunk.Data...)
			final = chunk.IsFinal
		}
		if string(got) != "abc" {
			serverDone <- fmt.Errorf("body bytes = %q, want abc", string(got))
			return
		}
		_ = writer.WriteResponseHeader(&protocol.ResponseHeader{StatusCode: 204, StatusText: "No Content"})
		_ = writer.WriteBodyChunk(&protocol.BodyChunk{Data: []byte{}, IsFinal: true})
		serverDone <- nil
	}()

	resp, err := HandleClientRequest(stream, "edge", "POST", "/v1/dummy", "", map[string][]string{"Content-Length": {"3"}}, strings.NewReader("abc"), nil)
	if err != nil {
		t.Fatalf("HandleClientRequest: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)

	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server side timed out waiting for request header")
	}
}

func TestHandleClientRequestSendsFinalChunkForEmptyBodyReader(t *testing.T) {
	reqReader, reqWriter := io.Pipe()
	respReader, respWriter := io.Pipe()
	stream := &memoryClientStream{reader: respReader, writer: reqWriter, protocolID: ProtocolID}

	serverDone := make(chan error, 1)
	go func() {
		defer reqReader.Close()
		defer respWriter.Close()

		reader := protocol.NewStreamReader(reqReader)
		hello, err := reader.ReadHello()
		if err != nil {
			serverDone <- err
			return
		}
		if hello.Role != "edge" {
			serverDone <- fmt.Errorf("hello role: got %q want edge", hello.Role)
			return
		}
		writer := protocol.NewStreamWriter(respWriter)
		if err := writer.WriteHello(&protocol.Hello{ProtocolMajor: uint16(protocol.ProtocolMajor), ProtocolMinor: uint16(protocol.ProtocolMinor), Role: "service", Capabilities: protocol.SupportedCapabilities()}); err != nil {
			serverDone <- err
			return
		}
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

	resp, err := HandleClientRequest(stream, "edge", "GET", "/v1/dummy", "", nil, strings.NewReader(""), nil)
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

func TestHandleServiceStreamRejectsMissingConnectProofForNonBridgeRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	serviceHost, err := NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "service-protected-http")
	if err != nil {
		t.Fatal(err)
	}
	defer serviceHost.Close()
	serviceHost.SetStreamHandler(ProtocolID, HandleServiceStream(upstream.URL, &ConnectProofValidation{Require: true}))

	clientHost, err := NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "client-protected-http")
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	serviceInfo, err := AddrInfoFromString(PeerAddrs(serviceHost)[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := clientHost.Connect(ctx, serviceInfo); err != nil {
		t.Fatal(err)
	}
	stream, err := clientHost.NewStream(ctx, serviceHost.ID(), SupportedProtocolIDs()...)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	errCh := make(chan error, 1)
	go func() {
		_, err := HandleClientRequest(stream, "evil", http.MethodGet, "/v1/dummy", "", nil, nil, nil)
		errCh <- err
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected connect proof error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client side timed out waiting for rejection")
	}
	if got := atomic.LoadInt32(&upstreamHits); got != 0 {
		t.Fatalf("upstream hits = %d, want 0", got)
	}
}

func TestHandleServiceTCPStreamRejectsMissingConnectProofForNonBridgeRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var targetHits int32
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			atomic.AddInt32(&targetHits, 1)
			_ = conn.Close()
		}
	}()

	serviceHost, err := NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "service-protected-tcp")
	if err != nil {
		t.Fatal(err)
	}
	defer serviceHost.Close()
	serviceHost.SetStreamHandler(ProtocolID, HandleServiceTCPStream("tcp://"+ln.Addr().String(), &ConnectProofValidation{Require: true}))

	clientHost, err := NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "client-protected-tcp")
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	serviceInfo, err := AddrInfoFromString(PeerAddrs(serviceHost)[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := clientHost.Connect(ctx, serviceInfo); err != nil {
		t.Fatal(err)
	}
	stream, err := clientHost.NewStream(ctx, serviceHost.ID(), SupportedProtocolIDs()...)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	errCh := make(chan error, 1)
	go func() {
		errCh <- StartClientTCPTunnel(stream, "evil", nil)
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected connect proof error, got nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("client side timed out waiting for rejection")
	}
	if got := atomic.LoadInt32(&targetHits); got != 0 {
		t.Fatalf("tcp target hits = %d, want 0", got)
	}
}

type memoryClientStream struct {
	reader     *io.PipeReader
	writer     *io.PipeWriter
	protocolID libprotocol.ID
}

func TestSupportedProtocolIDsUsesOnlyPreferredID(t *testing.T) {
	ids := SupportedProtocolIDs()
	if len(ids) != 1 || ids[0] != libprotocol.ID(ProtocolID) {
		t.Fatalf("supported protocol IDs = %#v", ids)
	}
}

type testStatsRecorder struct {
	begin, tx, rx, completed, errors int64
}

func (r *testStatsRecorder) Begin()                     { r.begin++ }
func (r *testStatsRecorder) AddRx(n int64)              { r.rx += n }
func (r *testStatsRecorder) AddTx(n int64)              { r.tx += n }
func (r *testStatsRecorder) Observe(int, time.Duration) {}
func (r *testStatsRecorder) Finish(err error) {
	if err != nil {
		r.errors++
	} else {
		r.completed++
	}
}

func TestProxyTCPStreamUpdatesRecorderDuringTransfer(t *testing.T) {
	leftPeer, leftConn := net.Pipe()
	defer leftPeer.Close()
	reqReader, reqWriter := io.Pipe()
	respReader, respWriter := io.Pipe()
	stream := &memoryClientStream{reader: respReader, writer: reqWriter, protocolID: ProtocolID}
	recorder := statspkg.New(statspkg.Snapshot{Role: "service", Kind: "tcp"})
	recorder.Begin()
	go func() { _, _ = io.Copy(io.Discard, reqReader) }()
	done := make(chan error, 1)
	go func() {
		_, _, err := ProxyTCPStream(leftConn, stream, recorder)
		done <- err
	}()
	if _, err := leftPeer.Write([]byte("hello world")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := recorder.Snapshot()
		if snap.Active == 1 && snap.TxBytesTotal > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected live tx update before completion, got %#v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = leftPeer.Close()
	_ = respWriter.Close()
	if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ProxyTCPStream: %v", err)
	}
	recorder.Finish(nil)
	snap := recorder.Snapshot()
	if snap.TxBytesTotal == 0 || snap.Completed != 1 || snap.Active != 0 {
		t.Fatalf("unexpected final snapshot: %#v", snap)
	}
}

func TestHandleServiceStreamRecordsErrorOnUpstreamFailure(t *testing.T) {
	reqReader, reqWriter := io.Pipe()
	respReader, respWriter := io.Pipe()
	stream := &memoryClientStream{reader: respReader, writer: reqWriter, protocolID: ProtocolID}
	recorder := &testStatsRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleServiceStream("http://127.0.0.1:0", nil, recorder)(stream)
	}()
	go func() { _, _ = io.Copy(io.Discard, reqReader) }()
	writer := protocol.NewStreamWriter(respWriter)
	if err := writer.WriteHello(&protocol.Hello{ProtocolMajor: uint16(protocol.ProtocolMajor), ProtocolMinor: uint16(protocol.ProtocolMinor), Role: "bridge", Capabilities: protocol.SupportedCapabilities()}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteRequestHeader(&protocol.RequestHeader{Method: "GET", Path: "/x", ContentLengthHint: 0}); err != nil {
		t.Fatal(err)
	}
	_ = respWriter.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler timed out")
	}
	if recorder.begin != 1 || recorder.errors != 1 || recorder.completed != 0 {
		t.Fatalf("unexpected recorder state: %+v", recorder)
	}
}

func TestHandleServiceTCPStreamRecordsErrorOnTargetFailure(t *testing.T) {
	reqReader, reqWriter := io.Pipe()
	respReader, respWriter := io.Pipe()
	stream := &memoryClientStream{reader: respReader, writer: reqWriter, protocolID: ProtocolID}
	recorder := &testStatsRecorder{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		HandleServiceTCPStream("http://127.0.0.1:0", nil, recorder)(stream)
	}()
	go func() { _, _ = io.Copy(io.Discard, reqReader) }()
	writer := protocol.NewStreamWriter(respWriter)
	if err := writer.WriteHello(&protocol.Hello{ProtocolMajor: uint16(protocol.ProtocolMajor), ProtocolMinor: uint16(protocol.ProtocolMinor), Role: "bridge", Capabilities: protocol.SupportedCapabilities()}); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteTunnelRequest(&protocol.TunnelRequest{Kind: "tcp"}); err != nil {
		t.Fatal(err)
	}
	_ = respWriter.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler timed out")
	}
	if recorder.begin != 1 || recorder.errors != 1 || recorder.completed != 0 {
		t.Fatalf("unexpected recorder state: %+v", recorder)
	}
}

func TestHandleClientRequestRejectsIncompatibleProtocolMajor(t *testing.T) {
	reqReader, reqWriter := io.Pipe()
	respReader, respWriter := io.Pipe()
	stream := &memoryClientStream{reader: respReader, writer: reqWriter, protocolID: ProtocolID}

	serverDone := make(chan error, 1)
	go func() {
		defer reqReader.Close()
		defer respWriter.Close()
		reader := protocol.NewStreamReader(reqReader)
		if _, err := reader.ReadHello(); err != nil {
			serverDone <- err
			return
		}
		writer := protocol.NewStreamWriter(respWriter)
		serverDone <- writer.WriteHello(&protocol.Hello{ProtocolMajor: uint16(protocol.ProtocolMajor + 1), ProtocolMinor: 0, Role: "service", Capabilities: protocol.SupportedCapabilities()})
	}()

	_, err := HandleClientRequest(stream, "edge", "GET", "/bad", "", nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "incompatible protocol major") {
		t.Fatalf("err=%v, want incompatible protocol major", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestServiceResponseStreamingSendsFinalChunkWhenReaderEndsOnEmptyEOF(t *testing.T) {
	body := &scriptedReader{reads: []scriptedRead{
		{data: []byte("hello"), err: nil},
		{data: nil, err: io.EOF},
	}}
	chunks, err := collectBodyChunksFromReader(body)
	if err != nil {
		t.Fatalf("collectBodyChunksFromReader: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("chunk count = %d, want 2", len(chunks))
	}
	if string(chunks[0].Data) != "hello" || chunks[0].IsFinal {
		t.Fatalf("first chunk = %+v, want data=hello final=false", chunks[0])
	}
	if len(chunks[1].Data) != 0 || !chunks[1].IsFinal {
		t.Fatalf("final chunk = %+v, want empty final chunk", chunks[1])
	}
}

func collectBodyChunksFromReader(body io.Reader) ([]protocol.BodyChunk, error) {
	buf := make([]byte, 32)
	var chunks []protocol.BodyChunk
	finalSent := false
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			isFinal := readErr == io.EOF
			chunks = append(chunks, protocol.BodyChunk{Data: append([]byte(nil), buf[:n]...), IsFinal: isFinal})
			finalSent = isFinal
		}
		if readErr != nil {
			if readErr == io.EOF {
				if !finalSent {
					chunks = append(chunks, protocol.BodyChunk{Data: []byte{}, IsFinal: true})
				}
				return chunks, nil
			}
			return nil, readErr
		}
	}
}

type scriptedRead struct {
	data []byte
	err  error
}

type scriptedReader struct {
	reads []scriptedRead
	idx   int
}

func (r *scriptedReader) Read(p []byte) (int, error) {
	if r.idx >= len(r.reads) {
		return 0, io.EOF
	}
	cur := r.reads[r.idx]
	r.idx++
	copy(p, cur.data)
	if cur.err != nil && !errors.Is(cur.err, io.EOF) {
		return len(cur.data), cur.err
	}
	return len(cur.data), cur.err
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
func (s *memoryClientStream) Protocol() libprotocol.ID {
	if s.protocolID != "" {
		return s.protocolID
	}
	return ProtocolID
}
func (s *memoryClientStream) SetProtocol(libprotocol.ID) error { return nil }
func (s *memoryClientStream) Stat() network.Stats              { return network.Stats{} }
func (s *memoryClientStream) Conn() network.Conn               { return nil }
func (s *memoryClientStream) Scope() network.StreamScope       { return nil }
