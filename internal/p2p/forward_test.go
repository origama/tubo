package p2p

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo/internal/protocol"
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
	go io.Copy(io.Discard, reqReader)
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
	go io.Copy(io.Discard, reqReader)
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
