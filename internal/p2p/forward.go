package p2p

import (
	"bufio"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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

type StreamStatsRecorder interface {
	Begin()
	AddRx(int64)
	AddTx(int64)
	Observe(statusCode int, latency time.Duration)
	Finish(err error)
}

// readCloser wraps an io.Reader to satisfy io.ReadCloser.
type readCloser struct {
	io.Reader
}

func (rc *readCloser) Close() error { return nil }

type countingReadCloser struct {
	rc      io.ReadCloser
	onRead  func(int64)
	onClose func()
	once    sync.Once
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 && c.onRead != nil {
		c.onRead(int64(n))
	}
	return n, err
}

func (c *countingReadCloser) Close() error {
	c.once.Do(func() {
		if c.onClose != nil {
			c.onClose()
		}
	})
	return c.rc.Close()
}

func tcpTargetAddress(localTarget string) (string, error) {
	u, err := url.Parse(localTarget)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(u.Scheme, "tcp") {
		return "", fmt.Errorf("raw tcp requires tcp:// target, got %q", localTarget)
	}
	if strings.TrimSpace(u.Host) == "" {
		return "", fmt.Errorf("tcp target requires host:port")
	}
	return u.Host, nil
}

func closeWriteIfPossible(v any) error {
	type closeWriter interface{ CloseWrite() error }
	if cw, ok := v.(closeWriter); ok {
		return cw.CloseWrite()
	}
	return nil
}

func firstStatsRecorder(recorders ...StreamStatsRecorder) StreamStatsRecorder {
	if len(recorders) == 0 {
		return nil
	}
	return recorders[0]
}

func ProxyTCPStream(left net.Conn, right network.Stream) (int64, int64, error) {
	errCh := make(chan error, 1)
	var sent int64
	go func() {
		var err error
		sent, err = io.Copy(right, left)
		closeErr := closeWriteIfPossible(right)
		log.Printf("tcp proxy left->right done left=%T right=%T bytes=%d copy_err=%v close_write_err=%v", left, right, sent, err, closeErr)
		errCh <- err
	}()
	received, recvErr := io.Copy(left, right)
	closeErr := closeWriteIfPossible(left)
	log.Printf("tcp proxy right->left done left=%T right=%T bytes=%d copy_err=%v close_write_err=%v", left, right, received, recvErr, closeErr)
	sendErr := <-errCh
	if recvErr != nil && !errors.Is(recvErr, net.ErrClosed) {
		return sent, received, recvErr
	}
	if sendErr != nil && !errors.Is(sendErr, net.ErrClosed) {
		return sent, received, sendErr
	}
	return sent, received, nil
}

// HandleServiceStream handles an incoming libp2p stream as a service (server side).
// It reads the HTTP request from the peer, forwards it to the local upstream target,
// and streams the response back. Supports streaming bodies for large responses.
func HandleServiceStream(localTarget string, connectAuth *ConnectProofValidation, recorders ...StreamStatsRecorder) func(network.Stream) {
	recorder := firstStatsRecorder(recorders...)
	return func(s network.Stream) {
		var opErr error
		if recorder != nil {
			recorder.Begin()
			defer func() { recorder.Finish(opErr) }()
		}
		defer s.Close()
		start := time.Now()
		remotePeer := peer.ID("unknown")
		remoteAddr := "unknown"
		if conn := s.Conn(); conn != nil {
			remotePeer = conn.RemotePeer()
			remoteAddr = conn.RemoteMultiaddr().String()
		}
		log.Printf("service stream opened peer=%s remote_addr=%s stream_protocol_id=%s target=%s", remotePeer, remoteAddr, s.Protocol(), localTarget)

		reader := protocol.NewStreamReader(s)
		writer := protocol.NewStreamWriter(s)

		var peerHello *protocol.Hello
		var err error
		if streamUsesHello(s) {
			peerHello, err = reader.ReadHello()
			if err != nil {
				opErr = err
				log.Printf("service stream read hello failed peer=%s err=%v duration=%s", remotePeer, err, time.Since(start))
				_ = writer.WriteError(&protocol.Error{Code: 400, Message: "decode hello: " + err.Error()})
				return
			}
			if err := validatePeerHello(peerHello); err != nil {
				opErr = err
				log.Printf("service stream incompatible hello peer=%s role=%s err=%v duration=%s", remotePeer, peerHello.Role, err, time.Since(start))
				_ = writer.WriteError(&protocol.Error{Code: 426, Message: err.Error()})
				return
			}
			negotiated := protocol.NegotiateCapabilities(peerHello.Capabilities)
			if err := writer.WriteHello(localHello("service")); err != nil {
				opErr = err
				log.Printf("service stream write hello failed peer=%s err=%v duration=%s", remotePeer, err, time.Since(start))
				return
			}
			remoteProtocolVersion := fmt.Sprintf("%d.%d", peerHello.ProtocolMajor, peerHello.ProtocolMinor)
			RecordNegotiation("service", peerHello.Role, string(s.Protocol()), remoteProtocolVersion, negotiated)
			log.Printf("service protocol negotiated peer=%s remote_role=%s local_role=service stream_protocol_id=%s protocol=%s peer_protocol=%s capabilities=%v", remotePeer, peerHello.Role, s.Protocol(), protocol.ProtocolVersion, remoteProtocolVersion, negotiated)
		}

		if connectAuth != nil && connectAuth.Require {
			if !hasCapability(peerHello.Capabilities, protocol.CapabilityConnectProofV1) {
				opErr = errors.New("connect proof required")
				_ = writer.WriteError(&protocol.Error{Code: 426, Message: "connect proof required"})
				return
			}
			proof, err := reader.ReadConnectProof()
			if err != nil {
				opErr = err
				log.Printf("service stream read connect proof failed peer=%s err=%v duration=%s", remotePeer, err, time.Since(start))
				code := 400
				msg := "decode connect proof: " + err.Error()
				if strings.Contains(err.Error(), "expected ConnectProof") {
					code = 428
					msg = "connect proof required"
				}
				_ = writer.WriteError(&protocol.Error{Code: code, Message: msg})
				return
			}
			if err := connectAuth.Validate(remotePeer, s.Conn().RemotePublicKey(), proof); err != nil {
				opErr = err
				log.Printf("service stream connect proof rejected peer=%s err=%v duration=%s", remotePeer, err, time.Since(start))
				_ = writer.WriteError(&protocol.Error{Code: 403, Message: err.Error()})
				return
			}
			log.Printf("service stream connect proof accepted peer=%s service=%s namespace=%s/%s duration=%s", remotePeer, connectAuth.ServiceID, connectAuth.ClusterID, connectAuth.NamespaceID, time.Since(start))
		}

		// Read request header
		reqHeader, err := reader.ReadRequestHeader()
		if err != nil {
			opErr = err
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

		if isWebSocketUpgrade(reqHeader.Headers) {
			if err := handleServiceWebSocketUpgrade(s, reader, writer, reqHeader, upURL, recorder); err != nil {
				opErr = err
				log.Printf("service websocket upgrade failed peer=%s url=%s err=%v duration=%s", remotePeer, upURL, err, time.Since(start))
				_ = writer.WriteError(&protocol.Error{Code: 502, Message: "websocket upgrade failed: " + err.Error()})
			}
			return
		}

		// Create upstream request — body will be streamed via BodyReader
		upReq, err := http.NewRequest(reqHeader.Method, upURL, nil) // body set below
		if err != nil {
			opErr = err
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
		if recorder != nil {
			bodyReader = &countingReadCloser{rc: bodyReader, onRead: recorder.AddRx}
		}
		if reqHeader.ContentLengthHint > 0 {
			lr := &io.LimitedReader{R: bodyReader, N: int64(reqHeader.ContentLengthHint)}
			upReq.Body = &readCloser{lr}
		} else {
			upReq.Body = bodyReader
		}

		// Forward to upstream
		resp, err := http.DefaultClient.Do(upReq)
		if err != nil {
			opErr = err
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
			opErr = err
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
					opErr = err
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
							opErr = err
							log.Printf("service stream write final empty chunk failed peer=%s status=%d bytes=%d err=%v duration=%s", remotePeer, resp.StatusCode, bytesWritten, err, time.Since(start))
							_ = writer.WriteError(&protocol.Error{Code: 500, Message: "write final body chunk: " + err.Error()})
							return
						}
					}
					break
				}
				log.Printf("service upstream body read failed peer=%s status=%d bytes=%d err=%v duration=%s", remotePeer, resp.StatusCode, bytesWritten, readErr, time.Since(start))
				opErr = readErr
				_ = writer.WriteError(&protocol.Error{Code: 502, Message: "read upstream body: " + readErr.Error()})
				return
			}
		}
		log.Printf("service stream completed peer=%s method=%s path=%s status=%d bytes=%d duration=%s", remotePeer, reqHeader.Method, reqHeader.Path, resp.StatusCode, bytesWritten, time.Since(start))
		if recorder != nil {
			recorder.AddTx(bytesWritten)
			recorder.Observe(resp.StatusCode, time.Since(start))
		}
		if err := s.CloseWrite(); err != nil {
			opErr = err
			log.Printf("service stream close-write failed peer=%s err=%v", remotePeer, err)
			return
		}
		_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = io.Copy(io.Discard, s)
	}
}

// HandleClientRequest sends an HTTP request over a libp2p stream and reads the response.
// Supports streaming bodies for both request and response.
func HandleServiceTCPStream(localTarget string, connectAuth *ConnectProofValidation, recorders ...StreamStatsRecorder) func(network.Stream) {
	recorder := firstStatsRecorder(recorders...)
	return func(s network.Stream) {
		var opErr error
		if recorder != nil {
			recorder.Begin()
			defer func() { recorder.Finish(opErr) }()
		}
		defer s.Close()
		start := time.Now()
		remotePeer := peer.ID("unknown")
		if conn := s.Conn(); conn != nil {
			remotePeer = conn.RemotePeer()
		}
		reader := protocol.NewStreamReader(s)
		writer := protocol.NewStreamWriter(s)

		var peerHello *protocol.Hello
		var err error
		if streamUsesHello(s) {
			peerHello, err = reader.ReadHello()
			if err != nil {
				opErr = err
				_ = writer.WriteError(&protocol.Error{Code: 400, Message: "decode hello: " + err.Error()})
				return
			}
			if err := validatePeerHello(peerHello); err != nil {
				opErr = err
				_ = writer.WriteError(&protocol.Error{Code: 426, Message: err.Error()})
				return
			}
			if !hasCapability(peerHello.Capabilities, protocol.CapabilityRawTCPV1) {
				opErr = errors.New("raw tcp not supported by remote peer")
				_ = writer.WriteError(&protocol.Error{Code: 426, Message: "raw tcp not supported by remote peer"})
				return
			}
			if err := writer.WriteHello(localHello("service")); err != nil {
				opErr = err
				return
			}
		}

		if connectAuth != nil && connectAuth.Require {
			if !hasCapability(peerHello.Capabilities, protocol.CapabilityConnectProofV1) {
				opErr = errors.New("connect proof required")
				_ = writer.WriteError(&protocol.Error{Code: 426, Message: "connect proof required"})
				return
			}
			proof, err := reader.ReadConnectProof()
			if err != nil {
				opErr = err
				code := 400
				msg := "decode connect proof: " + err.Error()
				if strings.Contains(err.Error(), "expected ConnectProof") {
					code = 428
					msg = "connect proof required"
				}
				_ = writer.WriteError(&protocol.Error{Code: code, Message: msg})
				return
			}
			if err := connectAuth.Validate(remotePeer, s.Conn().RemotePublicKey(), proof); err != nil {
				opErr = err
				_ = writer.WriteError(&protocol.Error{Code: 403, Message: err.Error()})
				return
			}
		}

		req, err := reader.ReadTunnelRequest()
		if err != nil {
			opErr = err
			_ = writer.WriteError(&protocol.Error{Code: 400, Message: "decode tunnel request: " + err.Error()})
			return
		}
		if req.Kind != "tcp" {
			opErr = fmt.Errorf("unsupported tunnel kind: %s", req.Kind)
			_ = writer.WriteError(&protocol.Error{Code: 400, Message: "unsupported tunnel kind: " + req.Kind})
			return
		}
		targetAddr, err := tcpTargetAddress(localTarget)
		if err != nil {
			opErr = err
			_ = writer.WriteError(&protocol.Error{Code: 500, Message: err.Error()})
			return
		}
		upstream, err := (&net.Dialer{Timeout: 5 * time.Second}).Dial("tcp", targetAddr)
		if err != nil {
			opErr = err
			_ = writer.WriteError(&protocol.Error{Code: 502, Message: "tcp target dial failed: " + err.Error()})
			return
		}
		defer upstream.Close()
		if err := writer.WriteTunnelReady(&protocol.TunnelReady{Kind: "tcp"}); err != nil {
			opErr = err
			return
		}
		sent, received, err := ProxyTCPStream(upstream, s)
		if recorder != nil {
			recorder.AddRx(received)
			recorder.AddTx(sent)
			recorder.Observe(http.StatusOK, time.Since(start))
		}
		if err != nil {
			opErr = err
			log.Printf("service tcp tunnel closed peer=%s target=%s bytes_in=%d bytes_out=%d err=%v duration=%s", remotePeer, targetAddr, received, sent, err, time.Since(start))
			return
		}
		log.Printf("service tcp tunnel completed peer=%s target=%s bytes_in=%d bytes_out=%d duration=%s", remotePeer, targetAddr, received, sent, time.Since(start))
	}
}

func StartClientTCPTunnel(s network.Stream, role string, connectProof *protocol.ConnectProof) error {
	writer := protocol.NewStreamWriter(s)
	reader := protocol.NewStreamReader(s)
	peerHello, err := negotiateClientHello(s, reader, writer, role)
	if err != nil {
		return err
	}
	if peerHello == nil || !hasCapability(peerHello.Capabilities, protocol.CapabilityRawTCPV1) {
		return fmt.Errorf("remote peer does not support raw tcp")
	}
	if connectProof != nil {
		if !hasCapability(peerHello.Capabilities, protocol.CapabilityConnectProofV1) {
			return fmt.Errorf("remote peer does not support connect proof")
		}
		if err := writer.WriteConnectProof(connectProof); err != nil {
			return fmt.Errorf("write connect proof: %w", err)
		}
	}
	if err := writer.WriteTunnelRequest(&protocol.TunnelRequest{Kind: "tcp"}); err != nil {
		return fmt.Errorf("write tunnel request: %w", err)
	}
	ready, errFrame, err := reader.ReadTunnelReadyOrError()
	if err != nil {
		return fmt.Errorf("read tunnel ready: %w", err)
	}
	if errFrame != nil {
		return fmt.Errorf("server error (code %d): %s", errFrame.Code, errFrame.Message)
	}
	if ready.Kind != "tcp" {
		return fmt.Errorf("tunnel kind mismatch: got %q", ready.Kind)
	}
	return nil
}

func HandleClientRequest(s network.Stream, role, method, path, query string, headers map[string][]string, body io.Reader, connectProof *protocol.ConnectProof) (*http.Response, error) {
	writer := protocol.NewStreamWriter(s)
	reader := protocol.NewStreamReader(s)

	peerHello, err := negotiateClientHello(s, reader, writer, role)
	if err != nil {
		return nil, err
	}
	if connectProof != nil {
		if peerHello == nil || !hasCapability(peerHello.Capabilities, protocol.CapabilityConnectProofV1) {
			return nil, fmt.Errorf("remote peer does not support connect proof")
		}
		if err := writer.WriteConnectProof(connectProof); err != nil {
			return nil, fmt.Errorf("write connect proof: %w", err)
		}
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
	err = writer.WriteRequestHeader(&protocol.RequestHeader{
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

func negotiateClientHello(s network.Stream, reader *protocol.StreamReader, writer *protocol.StreamWriter, role string) (*protocol.Hello, error) {
	if !streamUsesHello(s) {
		return nil, nil
	}
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
	return peerHello, nil
}

// StartClientWebSocketUpgrade sends a websocket upgrade request and leaves the
// libp2p stream in raw byte mode after the 101 response header.
func StartClientWebSocketUpgrade(s network.Stream, role, method, path, query string, headers map[string][]string, connectProof *protocol.ConnectProof) (*protocol.ResponseHeader, error) {
	writer := protocol.NewStreamWriter(s)
	reader := protocol.NewStreamReader(s)
	if _, err := negotiateClientHello(s, reader, writer, role); err != nil {
		return nil, err
	}
	if connectProof != nil {
		if err := writer.WriteConnectProof(connectProof); err != nil {
			return nil, fmt.Errorf("write connect proof: %w", err)
		}
	}
	if err := writer.WriteRequestHeader(&protocol.RequestHeader{Method: method, Path: path, Query: query, Headers: headers, ContentLengthHint: 0}); err != nil {
		return nil, fmt.Errorf("write websocket request header: %w", err)
	}
	respHeader, errFrame, err := reader.ReadResponseHeaderOrError()
	if err != nil {
		return nil, fmt.Errorf("read websocket response header: %w", err)
	}
	if errFrame != nil {
		return nil, fmt.Errorf("server error (code %d): %s", errFrame.Code, errFrame.Message)
	}
	return respHeader, nil
}

func hasCapability(caps []string, want string) bool {
	for _, cap := range caps {
		if cap == want {
			return true
		}
	}
	return false
}

func isWebSocketUpgrade(headers map[string][]string) bool {
	upgrade := false
	connectionUpgrade := false
	for k, values := range headers {
		for _, v := range values {
			if strings.EqualFold(k, "Upgrade") && strings.EqualFold(strings.TrimSpace(v), "websocket") {
				upgrade = true
			}
			if strings.EqualFold(k, "Connection") {
				for _, part := range strings.Split(v, ",") {
					if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
						connectionUpgrade = true
					}
				}
			}
		}
	}
	return upgrade && connectionUpgrade
}

func handleServiceWebSocketUpgrade(s network.Stream, _ *protocol.StreamReader, writer *protocol.StreamWriter, reqHeader *protocol.RequestHeader, upURL string, recorder StreamStatsRecorder) error {
	upReq, conn, br, err := dialUpgradeUpstream(reqHeader, upURL)
	if err != nil {
		return err
	}
	defer conn.Close()
	resp, err := http.ReadResponse(br, upReq)
	if err != nil {
		return fmt.Errorf("read upstream upgrade response: %w", err)
	}
	defer resp.Body.Close()
	respHeader := &protocol.ResponseHeader{StatusCode: resp.StatusCode, StatusText: http.StatusText(resp.StatusCode), Headers: make(map[string][]string, len(resp.Header))}
	for k, values := range resp.Header {
		respHeader.Headers[k] = append([]string(nil), values...)
	}
	if err := writer.WriteResponseHeader(respHeader); err != nil {
		return fmt.Errorf("write websocket response header: %w", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil
	}
	log.Printf("service websocket upgraded url=%s", upURL)
	proxyRawStream(s, conn, br, recorder)
	return nil
}

func dialUpgradeUpstream(reqHeader *protocol.RequestHeader, upURL string) (*http.Request, net.Conn, *bufio.Reader, error) {
	u, err := url.Parse(upURL)
	if err != nil {
		return nil, nil, nil, err
	}
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		if u.Scheme == "https" || u.Scheme == "wss" {
			host = net.JoinHostPort(host, "443")
		} else {
			host = net.JoinHostPort(host, "80")
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	var conn net.Conn
	if u.Scheme == "https" || u.Scheme == "wss" {
		conn, err = tls.DialWithDialer(dialer, "tcp", host, &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.Dial("tcp", host)
	}
	if err != nil {
		return nil, nil, nil, err
	}
	upReq, err := http.NewRequest(reqHeader.Method, upURL, nil)
	if err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}
	for k, values := range reqHeader.Headers {
		for _, v := range values {
			upReq.Header.Add(k, v)
		}
	}
	upReq.ContentLength = 0
	if err := upReq.Write(conn); err != nil {
		_ = conn.Close()
		return nil, nil, nil, err
	}
	return upReq, conn, bufio.NewReader(conn), nil
}

func proxyRawStream(s network.Stream, conn net.Conn, upstreamReader io.Reader, recorder StreamStatsRecorder) {
	done := make(chan struct{}, 2)
	var sent int64
	var received int64
	go func() { n, _ := io.Copy(conn, s); sent = n; done <- struct{}{} }()
	go func() { n, _ := io.Copy(s, upstreamReader); received = n; done <- struct{}{} }()
	<-done
	<-done
	if recorder != nil {
		recorder.AddRx(received)
		recorder.AddTx(sent)
		recorder.Observe(http.StatusOK, 0)
	}
	_ = conn.Close()
	_ = s.Close()
}

// SendError sends an error frame over the stream.
func SendError(s network.Stream, code int, message string) error {
	writer := protocol.NewStreamWriter(s)
	return writer.WriteError(&protocol.Error{Code: code, Message: message})
}
