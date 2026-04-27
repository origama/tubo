package p2p

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"

	"p2p-api-tunnel/internal/protocol"
)

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

type memoryClientStream struct {
	reader *io.PipeReader
	writer *io.PipeWriter
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
func (s *memoryClientStream) Conn() network.Conn                           { return nil }
func (s *memoryClientStream) Scope() network.StreamScope                   { return nil }
