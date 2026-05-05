package p2p

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/origama/tubo/internal/forwarding"
	"github.com/origama/tubo/internal/protocol"
)

const streamChunkSize = 32 * 1024

func streamUsesHello(s network.Stream) bool {
	return string(s.Protocol()) == protocol.ProtocolID
}

func localHello(role string) *protocol.Hello {
	return &protocol.Hello{
		ProtocolMajor: uint16(protocol.ProtocolMajor),
		ProtocolMinor: uint16(protocol.ProtocolMinor),
		Role:          role,
		Capabilities:  protocol.SupportedCapabilities(),
	}
}

func validatePeerHello(peerHello *protocol.Hello) error {
	if peerHello == nil {
		return fmt.Errorf("missing hello")
	}
	if int(peerHello.ProtocolMajor) != protocol.ProtocolMajor {
		return fmt.Errorf("incompatible protocol major: local=%d remote=%d", protocol.ProtocolMajor, peerHello.ProtocolMajor)
	}
	return nil
}

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
		start := time.Now()
		remotePeer := s.Conn().RemotePeer()
		remoteAddr := s.Conn().RemoteMultiaddr()
		log.Printf("service stream opened peer=%s remote_addr=%s stream_protocol_id=%s target=%s", remotePeer, remoteAddr, s.Protocol(), localTarget)

		reader := protocol.NewStreamReader(s)
		writer := protocol.NewStreamWriter(s)

		if streamUsesHello(s) {
			peerHello, err := reader.ReadHello()
			if err != nil {
				log.Printf("service stream read hello failed peer=%s err=%v duration=%s", remotePeer, err, time.Since(start))
				_ = writer.WriteError(&protocol.Error{Code: 400, Message: "decode hello: " + err.Error()})
				return
			}
			if err := validatePeerHello(peerHello); err != nil {
				log.Printf("service stream incompatible hello peer=%s role=%s err=%v duration=%s", remotePeer, peerHello.Role, err, time.Since(start))
				_ = writer.WriteError(&protocol.Error{Code: 426, Message: err.Error()})
				return
			}
			negotiated := protocol.NegotiateCapabilities(peerHello.Capabilities)
			if err := writer.WriteHello(localHello("service")); err != nil {
				log.Printf("service stream write hello failed peer=%s err=%v duration=%s", remotePeer, err, time.Since(start))
				return
			}
			remoteProtocolVersion := fmt.Sprintf("%d.%d", peerHello.ProtocolMajor, peerHello.ProtocolMinor)
			RecordNegotiation("service", peerHello.Role, string(s.Protocol()), remoteProtocolVersion, negotiated)
			log.Printf("service protocol negotiated peer=%s remote_role=%s local_role=service stream_protocol_id=%s protocol=%s peer_protocol=%s capabilities=%v", remotePeer, peerHello.Role, s.Protocol(), protocol.ProtocolVersion, remoteProtocolVersion, negotiated)
		}

		// Read request header
		reqHeader, err := reader.ReadRequestHeader()
		if err != nil {
			log.Printf("service stream decode request failed peer=%s err=%v duration=%s", remotePeer, err, time.Since(start))
			_ = writer.WriteError(&protocol.Error{Code: 400, Message: "decode request header: " + err.Error()})
			return
		}

		// Build upstream URL
		upURL := strings.TrimRight(localTarget, "/") + reqHeader.Path
		if reqHeader.Query != "" {
			upURL += "?" + reqHeader.Query
		}
		log.Printf("service upstream request peer=%s method=%s path=%s query=%q url=%s content_length_hint=%d", remotePeer, reqHeader.Method, reqHeader.Path, reqHeader.Query, upURL, reqHeader.ContentLengthHint)

		// Create upstream request — body will be streamed via BodyReader
		upReq, err := http.NewRequest(reqHeader.Method, upURL, nil) // body set below
		if err != nil {
			log.Printf("service upstream build request failed peer=%s url=%s err=%v duration=%s", remotePeer, upURL, err, time.Since(start))
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
			log.Printf("service upstream failed peer=%s url=%s err=%v duration=%s", remotePeer, upURL, err, time.Since(start))
			_ = writer.WriteError(&protocol.Error{Code: 502, Message: "upstream failed: " + err.Error()})
			return
		}
		defer resp.Body.Close()
		log.Printf("service upstream response peer=%s url=%s status=%d", remotePeer, upURL, resp.StatusCode)

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
			log.Printf("service stream write response header failed peer=%s status=%d err=%v duration=%s", remotePeer, resp.StatusCode, err, time.Since(start))
			_ = writer.WriteError(&protocol.Error{Code: 500, Message: "write response header: " + err.Error()})
			return
		}

		// Stream response body in chunks
		buf := make([]byte, streamChunkSize)
		var bytesWritten int64
		finalSent := false
		for {
			n, readErr := resp.Body.Read(buf)
			if n > 0 {
				isFinal := readErr == io.EOF
				if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: buf[:n], IsFinal: isFinal}); err != nil {
					log.Printf("service stream write body chunk failed peer=%s status=%d bytes=%d err=%v duration=%s", remotePeer, resp.StatusCode, bytesWritten, err, time.Since(start))
					_ = writer.WriteError(&protocol.Error{Code: 500, Message: "write body chunk: " + err.Error()})
					return
				}
				bytesWritten += int64(n)
				finalSent = isFinal
			}
			if readErr != nil {
				if readErr == io.EOF {
					if !finalSent {
						if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: []byte{}, IsFinal: true}); err != nil {
							log.Printf("service stream write final empty chunk failed peer=%s status=%d bytes=%d err=%v duration=%s", remotePeer, resp.StatusCode, bytesWritten, err, time.Since(start))
							_ = writer.WriteError(&protocol.Error{Code: 500, Message: "write final body chunk: " + err.Error()})
							return
						}
					}
					break
				}
				log.Printf("service upstream body read failed peer=%s status=%d bytes=%d err=%v duration=%s", remotePeer, resp.StatusCode, bytesWritten, readErr, time.Since(start))
				_ = writer.WriteError(&protocol.Error{Code: 502, Message: "read upstream body: " + readErr.Error()})
				return
			}
		}
		log.Printf("service stream completed peer=%s method=%s path=%s status=%d bytes=%d duration=%s", remotePeer, reqHeader.Method, reqHeader.Path, resp.StatusCode, bytesWritten, time.Since(start))
		if err := s.CloseWrite(); err != nil {
			log.Printf("service stream close-write failed peer=%s err=%v", remotePeer, err)
			return
		}
		_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(io.Discard, s)
	}
}

// HandleClientRequest sends an HTTP request over a libp2p stream and reads the response.
// Supports streaming bodies for both request and response.
func HandleClientRequest(s network.Stream, role, method, path, query string, headers map[string][]string, body io.Reader) (*http.Response, error) {
	writer := protocol.NewStreamWriter(s)
	reader := protocol.NewStreamReader(s)

	if streamUsesHello(s) {
		if err := writer.WriteHello(localHello(role)); err != nil {
			return nil, fmt.Errorf("write hello: %w", err)
		}
		peerHello, err := reader.ReadHello()
		if err != nil {
			return nil, fmt.Errorf("read hello: %w", err)
		}
		if err := validatePeerHello(peerHello); err != nil {
			return nil, err
		}
		negotiated := protocol.NegotiateCapabilities(peerHello.Capabilities)
		remoteProtocolVersion := fmt.Sprintf("%d.%d", peerHello.ProtocolMajor, peerHello.ProtocolMinor)
		RecordNegotiation(role, peerHello.Role, string(s.Protocol()), remoteProtocolVersion, negotiated)
		log.Printf("client protocol negotiated remote_role=%s local_role=%s stream_protocol_id=%s protocol=%s peer_protocol=%s capabilities=%v", peerHello.Role, role, s.Protocol(), protocol.ProtocolVersion, remoteProtocolVersion, negotiated)
	}

	// Determine content length hint
	var contentLengthHint int64 = -1 // streaming by default
	if lr, ok := body.(*io.LimitedReader); ok && body != nil {
		contentLengthHint = lr.N
	} else if body == nil || body == http.NoBody {
		contentLengthHint = 0
	} else if vals, ok := headers["Content-Length"]; ok && len(vals) > 0 {
		if n, err := strconv.ParseInt(vals[0], 10, 64); err == nil && n >= 0 {
			contentLengthHint = n
		}
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
	if body != nil && body != http.NoBody {
		buf := make([]byte, streamChunkSize)
		finalSent := false
		for {
			n, readErr := body.Read(buf)
			if n > 0 {
				isFinal := readErr == io.EOF
				if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: buf[:n], IsFinal: isFinal}); err != nil {
					return nil, fmt.Errorf("write request body chunk: %w", err)
				}
				finalSent = isFinal
			}
			if readErr != nil {
				if readErr == io.EOF {
					if !finalSent {
						if err := writer.WriteBodyChunk(&protocol.BodyChunk{Data: []byte{}, IsFinal: true}); err != nil {
							return nil, fmt.Errorf("write request body final chunk: %w", err)
						}
					}
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
	respHeader, errFrame, err := reader.ReadResponseHeaderOrError()
	if err != nil {
		return nil, fmt.Errorf("read response header: %w", err)
	}
	if errFrame != nil {
		return nil, fmt.Errorf("server error (code %d): %s", errFrame.Code, errFrame.Message)
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
