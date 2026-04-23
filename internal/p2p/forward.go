package p2p

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/libp2p/go-libp2p/core/network"
	"p2p-api-tunnel/internal/forwarding"
	"p2p-api-tunnel/internal/protocol"
)

// readCloser wraps an io.Reader to satisfy io.ReadCloser.
type readCloser struct {
	io.Reader
}

func (rc *readCloser) Close() error { return nil }

// HandleServiceStream handles an incoming libp2p stream as a service (server side).
// It reads the HTTP request from the peer, forwards it to the local upstream target,
// and streams the response back. Supports streaming bodies for large responses.
func HandleServiceStream(localTarget string) func(network.Stream) {
	return func(s network.Stream) {
		defer s.Close()

		reader := protocol.NewStreamReader(s)
		writer := protocol.NewStreamWriter(s)

		// Read request header
		reqHeader, err := reader.ReadRequestHeader()
		if err != nil {
			_ = writer.WriteError(&protocol.Error{Code: 400, Message: "decode request header: " + err.Error()})
			return
		}

		// Build upstream URL
		upURL := strings.TrimRight(localTarget, "/") + reqHeader.Path
		if reqHeader.Query != "" {
			upURL += "?" + reqHeader.Query
		}

		// Create upstream request — body will be streamed via BodyReader
		upReq, err := http.NewRequest(reqHeader.Method, upURL, nil) // body set below
		if err != nil {
			_ = writer.WriteError(&protocol.Error{Code: 500, Message: "build upstream request: " + err.Error()})
			return
		}

		// Copy headers (hop-by-hop filtering done by forwarding.StripHopByHopHeaders)
		for k, values := range reqHeader.Headers {
			for _, v := range values {
				upReq.Header.Add(k, v)
			}
		}
		forwarding.StripHopByHopHeaders(upReq.Header)

		// Set up streaming body reader — BodyReader returns io.ReadCloser
		bodyReader := reader.BodyReader()
		if reqHeader.ContentLengthHint > 0 {
			lr := &io.LimitedReader{R: bodyReader, N: int64(reqHeader.ContentLengthHint)}
			upReq.Body = &readCloser{lr}
		} else {
			upReq.Body = bodyReader
		}

		// Forward to upstream
		resp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			bodyReader.Close() // drain remaining request body
			_ = writer.WriteError(&protocol.Error{Code: 502, Message: "upstream failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()

		// Write response header
		respHeader := &protocol.ResponseHeader{
			StatusCode: resp.StatusCode,
			StatusText: http.StatusText(resp.StatusCode),
			Headers:    make(map[string][]string, len(resp.Header)),
		}
		for k, values := range resp.Header {
			if len(values) == 0 {
				continue
			}
			respHeader.Headers[k] = append(respHeader.Headers[k], values...)
		}
		forwarding.StripHopByHopHeaders(respHeader.Headers)

		if err := writer.WriteResponseHeader(respHeader); err != nil {
			_ = writer.WriteError(&protocol.Error{Code: 500, Message: "write response header: " + err.Error()})
			return
		}

		// Stream response body in chunks
		buf := make([]byte, 32*1024) // 32KB chunks
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				isFinal := readErr == io.EOF
				if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: buf[:n], IsFinal: isFinal}); err != nil {
					_ = writer.WriteError(&protocol.Error{Code: 500, Message: "write body chunk: " + err.Error()})
					return
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				_ = writer.WriteError(&protocol.Error{Code: 502, Message: "read upstream body: " + readErr.Error()})
				return
			}
		}
	}
}

// HandleClientRequest sends an HTTP request over a libp2p stream and reads the response.
// Supports streaming bodies for both request and response.
func HandleClientRequest(s network.Stream, method, path, query string, headers map[string][]string, body io.Reader) (*http.Response, error) {
	writer := protocol.NewStreamWriter(s)
	reader := protocol.NewStreamReader(s)

	// Determine content length hint
	var contentLengthHint int64 = -1 // streaming by default
	if lr, ok := body.(*io.LimitedReader); ok && body != nil {
		contentLengthHint = lr.N
	} else if body == nil {
		contentLengthHint = 0
	}

	// Write request header
	err := writer.WriteRequestHeader(&protocol.RequestHeader{
		Method:            method,
		Path:              path,
		Query:             query,
		Headers:           headers,
		ContentLengthHint: contentLengthHint,
	})
	if err != nil {
		return nil, fmt.Errorf("write request header: %w", err)
	}

	// Stream request body if present
	if body != nil {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := body.Read(buf)
			if n > 0 {
				isFinal := readErr == io.EOF
				if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: buf[:n], IsFinal: isFinal}); err != nil {
					return nil, fmt.Errorf("write request body chunk: %w", err)
				}
			}
			if readErr != nil {
				if readErr == io.EOF {
					break
				}
				return nil, fmt.Errorf("read request body: %w", readErr)
			}
		}
	} else if contentLengthHint == 0 {
		// Empty body — write a single empty final chunk
		if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: []byte{}, IsFinal: true}); err != nil {
			return nil, fmt.Errorf("write empty body chunk: %w", err)
		}
	}

	// Read response header or error frame
	respHeader, err := reader.ReadResponseHeader()
	if err != nil {
		// Check if it's an error frame instead
		errFrame, errErr := reader.ReadError()
		if errErr == nil && errFrame != nil {
			return nil, fmt.Errorf("server error (code %d): %s", errFrame.Code, errFrame.Message)
		}
		return nil, fmt.Errorf("read response header: %w", err)
	}

	// Build HTTP response with streaming body
	resp := &http.Response{
		Status:        respHeader.StatusText,
		StatusCode:    respHeader.StatusCode,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(map[string][]string, len(respHeader.Headers)),
		Body:          reader.BodyReader(),
		ContentLength: -1, // streaming
	}

	for k, values := range respHeader.Headers {
		resp.Header[k] = append(resp.Header[k], values...)
	}

	return resp, nil
}

// SendError sends an error frame over the stream.
func SendError(s network.Stream, code int, message string) error {
	writer := protocol.NewStreamWriter(s)
	return writer.WriteError(&protocol.Error{Code: code, Message: message})
}