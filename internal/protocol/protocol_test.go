package protocol_test

import (
	"bytes"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/origama/tubo/internal/protocol"
)

type partialWriter struct {
	buf      bytes.Buffer
	maxWrite int
}

func (w *partialWriter) Write(p []byte) (int, error) {
	if w.maxWrite <= 0 || len(p) <= w.maxWrite {
		return w.buf.Write(p)
	}
	return w.buf.Write(p[:w.maxWrite])
}

func TestHelloRoundtrip(t *testing.T) {
	original := protocol.Hello{
		ProtocolMajor: uint16(protocol.ProtocolMajor),
		ProtocolMinor: uint16(protocol.ProtocolMinor),
		Role:          "edge",
		Capabilities:  []string{protocol.CapabilityHelloV1},
	}

	var buf bytes.Buffer
	if err := protocol.EncodeFrame(&buf, &original); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	reader := protocol.NewStreamReader(&buf)
	decoded, err := reader.ReadHello()
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decoded.ProtocolMajor != original.ProtocolMajor || decoded.ProtocolMinor != original.ProtocolMinor {
		t.Fatalf("protocol version mismatch: got=%d.%d want=%d.%d", decoded.ProtocolMajor, decoded.ProtocolMinor, original.ProtocolMajor, original.ProtocolMinor)
	}
	if decoded.Role != original.Role {
		t.Fatalf("role: got %q want %q", decoded.Role, original.Role)
	}
	if len(decoded.Capabilities) != 1 || decoded.Capabilities[0] != protocol.CapabilityHelloV1 {
		t.Fatalf("capabilities=%v", decoded.Capabilities)
	}
}

// --- RequestHeader tests ---

func TestRequestHeaderRoundtrip(t *testing.T) {
	original := protocol.RequestHeader{
		Method: "POST",
		Path:   "/api/v1/users",
		Query:  "limit=10&offset=0",
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
			"Accept":       {"text/html", "application/xml"},
			"X-Custom":     {"value1"},
			"Set-Cookie":   {"a=1; Path=/", "b=2; Path=/"},
		},
		ContentLengthHint: 42,
	}

	var buf bytes.Buffer
	if err := protocol.EncodeFrame(&buf, &original); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	reader := protocol.NewStreamReader(&buf)
	decoded, err := reader.ReadRequestHeader()
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// Verify fields
	if decoded.Method != original.Method {
		t.Errorf("Method: got %q, want %q", decoded.Method, original.Method)
	}
	if decoded.Path != original.Path {
		t.Errorf("Path: got %q, want %q", decoded.Path, original.Path)
	}
	if decoded.Query != original.Query {
		t.Errorf("Query: got %q, want %q", decoded.Query, original.Query)
	}
	if decoded.ContentLengthHint != original.ContentLengthHint {
		t.Errorf("ContentLengthHint: got %d, want %d", decoded.ContentLengthHint, original.ContentLengthHint)
	}

	// Verify multi-value headers are preserved
	for key := range original.Headers {
		got, ok := decoded.Headers[key]
		if !ok {
			t.Errorf("Header %q missing in decoded", key)
			continue
		}
		want := original.Headers[key]
		if len(got) != len(want) {
			t.Errorf("Header %q: got %d values, want %d", key, len(got), len(want))
			continue
		}
		for i, v := range want {
			if got[i] != v {
				t.Errorf("Header %q[%d]: got %q, want %q", key, i, got[i], v)
			}
		}
	}
}

func TestEncodeFrameHandlesPartialWrites(t *testing.T) {
	original := protocol.BodyChunk{Data: bytes.Repeat([]byte("Z"), 70*1024), IsFinal: true}
	pw := &partialWriter{maxWrite: 1024}
	if err := protocol.EncodeFrame(pw, &original); err != nil {
		t.Fatalf("encode failed: %v", err)
	}
	reader := protocol.NewStreamReader(bytes.NewReader(pw.buf.Bytes()))
	decoded, err := reader.ReadBodyChunk()
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if !decoded.IsFinal {
		t.Fatal("decoded final flag = false, want true")
	}
	if !bytes.Equal(decoded.Data, original.Data) {
		t.Fatalf("decoded payload mismatch: got=%d want=%d", len(decoded.Data), len(original.Data))
	}
}

func TestRequestHeaderEmptyQuery(t *testing.T) {
	original := protocol.RequestHeader{
		Method: "GET",
		Path:   "/health",
		Query:  "",
		Headers: map[string][]string{
			"Accept": {"*/*"},
		},
		ContentLengthHint: -1, // unknown
	}

	var buf bytes.Buffer
	if err := protocol.EncodeFrame(&buf, &original); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	reader := protocol.NewStreamReader(&buf)
	decoded, err := reader.ReadRequestHeader()
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.Query != "" {
		t.Errorf("Query: got %q, want empty", decoded.Query)
	}
	if decoded.ContentLengthHint != -1 {
		t.Errorf("ContentLengthHint: got %d, want -1 (unknown)", decoded.ContentLengthHint)
	}
}

// --- ResponseHeader tests ---

func TestResponseHeaderRoundtrip(t *testing.T) {
	original := protocol.ResponseHeader{
		StatusCode: http.StatusCreated,
		StatusText: "Created",
		Headers: map[string][]string{
			"Content-Type":          {"application/json"},
			"Location":              {"/api/v1/users/42"},
			"Set-Cookie":            {"session=abc; Path=/; HttpOnly", "tracking=xyz; Path=/"},
			"X-RateLimit-Remaining": {"95"},
		},
	}

	var buf bytes.Buffer
	if err := protocol.EncodeFrame(&buf, &original); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	reader := protocol.NewStreamReader(&buf)
	decoded, err := reader.ReadResponseHeader()
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.StatusCode != original.StatusCode {
		t.Errorf("StatusCode: got %d, want %d", decoded.StatusCode, original.StatusCode)
	}
	if decoded.StatusText != original.StatusText {
		t.Errorf("StatusText: got %q, want %q", decoded.StatusText, original.StatusText)
	}

	for key := range original.Headers {
		got, ok := decoded.Headers[key]
		if !ok {
			t.Errorf("Header %q missing in decoded", key)
			continue
		}
		want := original.Headers[key]
		if len(got) != len(want) {
			t.Errorf("Header %q: got %d values, want %d", key, len(got), len(want))
			continue
		}
		for i, v := range want {
			if got[i] != v {
				t.Errorf("Header %q[%d]: got %q, want %q", key, i, got[i], v)
			}
		}
	}
}

// --- BodyChunk tests ---

func TestBodyChunkRoundtrip(t *testing.T) {
	chunk := protocol.BodyChunk{
		Data:    []byte("Hello, world! This is a test body with some special chars: àéìòü"),
		IsFinal: false,
	}

	var buf bytes.Buffer
	if err := protocol.EncodeFrame(&buf, &chunk); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	reader := protocol.NewStreamReader(&buf)
	decoded, err := reader.ReadBodyChunk()
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if !bytes.Equal(decoded.Data, chunk.Data) {
		t.Errorf("Data mismatch: got %q, want %q", decoded.Data, chunk.Data)
	}
	if decoded.IsFinal != chunk.IsFinal {
		t.Errorf("IsFinal: got %v, want %v", decoded.IsFinal, chunk.IsFinal)
	}
}

func TestBodyChunkEmpty(t *testing.T) {
	chunk := protocol.BodyChunk{
		Data:    []byte{},
		IsFinal: true,
	}

	var buf bytes.Buffer
	if err := protocol.EncodeFrame(&buf, &chunk); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	reader := protocol.NewStreamReader(&buf)
	decoded, err := reader.ReadBodyChunk()
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if len(decoded.Data) != 0 {
		t.Errorf("Data: got %d bytes, want 0", len(decoded.Data))
	}
	if !decoded.IsFinal {
		t.Error("IsFinal: got false, want true")
	}
}

// --- Error frame tests ---

func TestErrorRoundtrip(t *testing.T) {
	errFrame := protocol.Error{
		Code:    502,
		Message: "upstream connection refused",
	}

	var buf bytes.Buffer
	if err := protocol.EncodeFrame(&buf, &errFrame); err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	reader := protocol.NewStreamReader(&buf)
	decoded, err := reader.ReadError()
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.Code != errFrame.Code {
		t.Errorf("Code: got %d, want %d", decoded.Code, errFrame.Code)
	}
	if decoded.Message != errFrame.Message {
		t.Errorf("Message: got %q, want %q", decoded.Message, errFrame.Message)
	}
}

func TestReadResponseHeaderOrErrorReadsErrorFrame(t *testing.T) {
	errFrame := protocol.Error{
		Code:    502,
		Message: "upstream failed",
	}

	var buf bytes.Buffer
	writer := protocol.NewStreamWriter(&buf)
	if err := writer.WriteError(&errFrame); err != nil {
		t.Fatalf("WriteError: %v", err)
	}

	reader := protocol.NewStreamReader(&buf)
	resp, gotErrFrame, err := reader.ReadResponseHeaderOrError()
	if err != nil {
		t.Fatalf("ReadResponseHeaderOrError: %v", err)
	}
	if resp != nil {
		t.Fatalf("response header = %+v, want nil", resp)
	}
	if gotErrFrame == nil {
		t.Fatal("error frame = nil, want frame")
	}
	if gotErrFrame.Code != errFrame.Code {
		t.Errorf("Code: got %d, want %d", gotErrFrame.Code, errFrame.Code)
	}
	if gotErrFrame.Message != errFrame.Message {
		t.Errorf("Message: got %q, want %q", gotErrFrame.Message, errFrame.Message)
	}
}

// --- Streaming integration test ---

func TestStreamingRequestResponse(t *testing.T) {
	// Simulate a full request-response cycle over a stream
	var conn bytes.Buffer

	// Write request
	reqWriter := protocol.NewStreamWriter(&conn)
	err := reqWriter.WriteRequestHeader(&protocol.RequestHeader{
		Method: "POST",
		Path:   "/api/data",
		Query:  "",
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
		},
		ContentLengthHint: -1, // streaming, unknown length
	})
	if err != nil {
		t.Fatalf("write request header failed: %v", err)
	}

	chunks := [][]byte{[]byte(`{"name": "`), []byte(`test`), []byte(`"}\n`)}
	for i, chunk := range chunks {
		isFinal := i == len(chunks)-1
		err := reqWriter.WriteBodyChunk(&protocol.BodyChunk{Data: chunk, IsFinal: isFinal})
		if err != nil {
			t.Fatalf("write body chunk %d failed: %v", i, err)
		}
	}

	// Read request on the other side
	reqReader := protocol.NewStreamReader(bytes.NewReader(conn.Bytes()))
	reqHeader, err := reqReader.ReadRequestHeader()
	if err != nil {
		t.Fatalf("read request header failed: %v", err)
	}
	if reqHeader.Method != "POST" || reqHeader.Path != "/api/data" {
		t.Errorf("request header mismatch: got %+v", reqHeader)
	}

	var bodyParts [][]byte
	for {
		chunk, err := reqReader.ReadBodyChunk()
		if err != nil {
			t.Fatalf("read body chunk failed: %v", err)
		}
		bodyParts = append(bodyParts, chunk.Data)
		if chunk.IsFinal {
			break
		}
	}

	fullBody := bytes.Join(bodyParts, nil)
	expectedBody := `{"name": "test"}\n`
	if string(fullBody) != expectedBody {
		t.Errorf("Request body: got %q, want %q", string(fullBody), expectedBody)
	}

	// Write response
	respWriter := protocol.NewStreamWriter(&conn)
	err = respWriter.WriteResponseHeader(&protocol.ResponseHeader{
		StatusCode: http.StatusOK,
		StatusText: "OK",
		Headers: map[string][]string{
			"Content-Type": {"application/json"},
			"X-Request-ID": {"abc123"},
		},
	})
	if err != nil {
		t.Fatalf("write response header failed: %v", err)
	}

	err = respWriter.WriteBodyChunk(&protocol.BodyChunk{
		Data:    []byte(`{"status":"ok"}`),
		IsFinal: true,
	})
	if err != nil {
		t.Fatalf("write response body failed: %v", err)
	}

	// Read response on the client side
	respReader := protocol.NewStreamReader(bytes.NewReader(conn.Bytes()))

	// Skip request frames (we already read them above)
	_, _ = respReader.ReadRequestHeader()
	for {
		chunk, _ := respReader.ReadBodyChunk()
		if chunk.IsFinal {
			break
		}
	}

	respHeader, err := respReader.ReadResponseHeader()
	if err != nil {
		t.Fatalf("read response header failed: %v", err)
	}

	if respHeader.StatusCode != http.StatusOK {
		t.Errorf("StatusCode: got %d, want %d", respHeader.StatusCode, http.StatusOK)
	}

	respBody, err := respReader.ReadBodyChunk()
	if err != nil {
		t.Fatalf("read response body failed: %v", err)
	}

	if string(respBody.Data) != `{"status":"ok"}` {
		t.Errorf("Response body: got %q, want %q", string(respBody.Data), `{"status":"ok"}`)
	}
	if !respBody.IsFinal {
		t.Error("Expected final chunk")
	}
}

// --- StreamReader EOF handling ---

func TestStreamReaderEOF(t *testing.T) {
	reader := protocol.NewStreamReader(bytes.NewReader([]byte{}))

	_, err := reader.ReadRequestHeader()
	if err != io.EOF {
		t.Errorf("ReadRequestHeader on empty: got %v, want io.EOF", err)
	}

	_, err = reader.ReadResponseHeader()
	if err != io.EOF {
		t.Errorf("ReadResponseHeader on empty: got %v, want io.EOF", err)
	}

	_, err = reader.ReadBodyChunk()
	if err != io.EOF {
		t.Errorf("ReadBodyChunk on empty: got %v, want io.EOF", err)
	}

	_, err = reader.ReadError()
	if err != io.EOF {
		t.Errorf("ReadError on empty: got %v, want io.EOF", err)
	}
}

// --- Invalid frame type handling ---

func TestStreamReaderInvalidFrameType(t *testing.T) {
	// Write a BodyChunk but try to read as RequestHeader
	var buf bytes.Buffer
	err := protocol.EncodeFrame(&buf, &protocol.BodyChunk{Data: []byte("test"), IsFinal: true})
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	reader := protocol.NewStreamReader(bytes.NewReader(buf.Bytes()))
	_, err = reader.ReadRequestHeader()
	if err == nil {
		t.Error("Expected error when reading wrong frame type")
	} else {
		t.Logf("Got expected error: %v", err)
	}
}

// --- BodyReader streaming test ---

func TestBodyReaderStreaming(t *testing.T) {
	var buf bytes.Buffer
	writer := protocol.NewStreamWriter(&buf)

	// Write a request header first (to simulate real stream)
	err := writer.WriteRequestHeader(&protocol.RequestHeader{
		Method: "POST", Path: "/data", Query: "",
		Headers: map[string][]string{"Content-Type": {"text/plain"}},
	})
	if err != nil {
		t.Fatalf("write header: %v", err)
	}

	// Write multiple body chunks
	chunks := [][]byte{[]byte("Hello "), []byte("World"), []byte("!")}
	for i, c := range chunks {
		isFinal := i == len(chunks)-1
		if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: c, IsFinal: isFinal}); err != nil {
			t.Fatalf("write chunk %d: %v", i, err)
		}
	}

	// Read back using BodyReader
	reader := protocol.NewStreamReader(bytes.NewReader(buf.Bytes()))
	_, _ = reader.ReadRequestHeader() // skip header

	bodyReader := reader.BodyReader()
	defer bodyReader.Close()

	got, err := io.ReadAll(bodyReader)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}

	want := "Hello World!"
	if string(got) != want {
		t.Errorf("BodyReader: got %q, want %q", string(got), want)
	}
}

// --- BodyReader with empty body ---

func TestBodyReaderEmpty(t *testing.T) {
	var buf bytes.Buffer
	writer := protocol.NewStreamWriter(&buf)

	err := writer.WriteRequestHeader(&protocol.RequestHeader{Method: "GET", Path: "/", Query: "", Headers: nil})
	if err != nil {
		t.Fatalf("write header: %v", err)
	}

	// Write empty final chunk (no body)
	if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: []byte{}, IsFinal: true}); err != nil {
		t.Fatalf("write empty chunk: %v", err)
	}

	reader := protocol.NewStreamReader(bytes.NewReader(buf.Bytes()))
	_, _ = reader.ReadRequestHeader()

	bodyReader := reader.BodyReader()
	defer bodyReader.Close()

	got, err := io.ReadAll(bodyReader)
	if err != nil {
		t.Fatalf("readAll: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("BodyReader empty: got %d bytes, want 0", len(got))
	}
}

// BodyReader must stop at final chunk even if underlying stream stays open.
func TestBodyReaderFinalChunkDoesNotBlock(t *testing.T) {
	pr, pw := io.Pipe()
	writer := protocol.NewStreamWriter(pw)

	go func() {
		_ = writer.WriteRequestHeader(&protocol.RequestHeader{
			Method:  "POST",
			Path:    "/data",
			Query:   "",
			Headers: map[string][]string{"Content-Type": {"text/plain"}},
		})
		_ = writer.WriteBodyChunk(&protocol.BodyChunk{
			Data:    []byte("done"),
			IsFinal: true,
		})

		// Keep stream open briefly to simulate real bidirectional protocol usage.
		time.Sleep(200 * time.Millisecond)
		_ = pw.Close()
	}()

	reader := protocol.NewStreamReader(pr)
	if _, err := reader.ReadRequestHeader(); err != nil {
		t.Fatalf("read request header: %v", err)
	}

	bodyReader := reader.BodyReader()
	defer bodyReader.Close()

	done := make(chan struct{})
	var (
		got []byte
		err error
	)
	go func() {
		got, err = io.ReadAll(bodyReader)
		close(done)
	}()

	select {
	case <-done:
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(got) != "done" {
			t.Fatalf("body mismatch: got %q, want %q", string(got), "done")
		}
	case <-time.After(150 * time.Millisecond):
		t.Fatalf("BodyReader blocked after final chunk")
	}
}
