package runtime

// Engine integration tests for the receiving-boundary serve path (A0 Step 7).
// These tests exercise the Engine-based Pipe target forwarding through
// tunnelServer.serve with an in-memory resolver set, a deterministic fake
// OpenAuthorizer, and a recording fake DownstreamOpener. They use only net.Pipe
// pairs instead of a real libp2p host, so they are deterministic and sleep-free.

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/engine"
	"github.com/origama/tubo-rebuilt/internal/node"
	"github.com/origama/tubo-rebuilt/internal/openauth"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
)

// fakeStream adapts one side of a net.Pipe to the libp2p core.Stream
// interface with stubs for the opaque connection/scope surfaces. serve only
// uses Read/Write/Close/Reset/CloseWrite/SetDeadline, so the rest are no-ops.
type fakeStream struct {
	conn net.Conn
}

func (f *fakeStream) Read(p []byte) (int, error)                { return f.conn.Read(p) }
func (f *fakeStream) Write(p []byte) (int, error)               { return f.conn.Write(p) }
func (f *fakeStream) Close() error                              { return f.conn.Close() }
func (f *fakeStream) CloseWrite() error                         { return f.conn.Close() }
func (f *fakeStream) CloseRead() error                          { return nil }
func (f *fakeStream) Reset() error                              { return f.conn.Close() }
func (f *fakeStream) ResetWithError(core.StreamErrorCode) error { return f.conn.Close() }
func (f *fakeStream) SetDeadline(t time.Time) error             { return f.conn.SetDeadline(t) }
func (f *fakeStream) SetReadDeadline(t time.Time) error         { return f.conn.SetReadDeadline(t) }
func (f *fakeStream) SetWriteDeadline(t time.Time) error        { return f.conn.SetWriteDeadline(t) }
func (f *fakeStream) ID() string                                { return "fake-stream" }
func (f *fakeStream) Protocol() protocol.ID                     { return wire.TunnelID }
func (f *fakeStream) SetProtocol(protocol.ID) error             { return nil }
func (f *fakeStream) Stat() core.Stats                          { return core.Stats{} }
func (f *fakeStream) Conn() core.Conn                           { return nil }
func (f *fakeStream) Scope() core.StreamScope                   { return nil }

// fakeServiceConn returns a discard net.Conn for service dialing stubs.
func fakeServiceConn(_ domain.ServiceSpec) (net.Conn, error) {
	client, _ := net.Pipe()
	return client, nil
}

// fakeDownstreamRecorder records each DownstreamOpener invocation.
type fakeDownstreamRecorder struct {
	calls   int
	visited []string
	states  []engine.TraversalState
	fail    error
	half    bool // return an error on the next call
}

func (r *fakeDownstreamRecorder) open(ctx context.Context, nextSpec domain.PipeSpec, state engine.TraversalState) (node.Carrier, string, error) {
	r.calls++
	r.states = append(r.states, state)
	r.visited = append(r.visited, nextSpec.PipeID)
	if r.fail != nil {
		err := r.fail
		if r.half {
			return nil, "", err
		}
		return nil, "", err
	}
	client, _ := net.Pipe()
	return &fakeCarrier{conn: client}, "ses_" + nextSpec.PipeID, nil
}

type fakeCarrier struct {
	conn net.Conn
}

func (c *fakeCarrier) Read(p []byte) (int, error)    { return c.conn.Read(p) }
func (c *fakeCarrier) Write(p []byte) (int, error)   { return c.conn.Write(p) }
func (c *fakeCarrier) Close() error                  { return c.conn.Close() }
func (c *fakeCarrier) Reset() error                  { return c.conn.Close() }
func (c *fakeCarrier) CloseWrite() error             { return c.conn.Close() }
func (c *fakeCarrier) SetDeadline(t time.Time) error { return c.conn.SetDeadline(t) }

// okOpenAuthorizer always authorizes the open.
type okOpenAuthorizer struct{}

func (okOpenAuthorizer) AuthorizeOpen(context.Context, openauth.OpenRequest) (openauth.Authorization, error) {
	now := time.Now().UTC()
	return openauth.Authorization{
		PipeID:    "pip_a",
		Subject:   "client-public-key",
		Issuer:    "issuer",
		LeaseID:   "jti",
		ExpiresAt: now.Add(10 * time.Minute),
	}, nil
}

// buildPipeForwardServer creates a tunnelServer whose Pipe A targets Pipe B
// (which targets a Service), wired through an in-memory Engine. The returned
// server owns a fresh PipeRuntime that the caller must close.
func buildPipeForwardServer(t *testing.T, recorder *fakeDownstreamRecorder, pipeID string, targetRef domain.Ref) (*tunnelServer, *NodeRuntime, *PipeRuntime) {
	t.Helper()
	pipeA := domain.PipeSpec{
		Ref:        mustRuntimeRef(t, "pipe/a"),
		PipeID:     pipeID,
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"},
		Target:     targetRef,
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
	}
	pipeB := domain.PipeSpec{
		Ref:        mustRuntimeRef(t, "pipe/b"),
		PipeID:     "pip_b",
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"},
		Target:     mustRuntimeRef(t, "service/s1"),
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
	}
	service := domain.ServiceSpec{Ref: mustRuntimeRef(t, "service/s1"), Kind: domain.ServiceHTTP, Target: "http://127.0.0.1:8080"}

	pipes := map[string]domain.PipeSpec{pipeA.Ref.String(): pipeA, pipeB.Ref.String(): pipeB}
	services := map[string]domain.ServiceSpec{service.Ref.String(): service}

	eng := engine.New(
		func(ref domain.Ref) (domain.ServiceSpec, error) {
			if s, ok := services[ref.String()]; ok {
				return s, nil
			}
			return domain.ServiceSpec{}, domain.ErrNotFound
		},
		func(ref domain.Ref) (domain.PipeSpec, error) {
			if p, ok := pipes[ref.String()]; ok {
				return p, nil
			}
			return domain.PipeSpec{}, domain.ErrNotFound
		},
		fakeServiceConn,
	)

	nodeRuntime := NewNodeRuntime(context.Background())
	pipeRuntime, err := nodeRuntime.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}

	s := &tunnelServer{
		spec:             pipeA,
		service:          service,
		authorizer:       okOpenAuthorizer{},
		dialFunc:         fakeServiceConn,
		engine:           eng,
		downstreamOpener: recorder.open,
		runtime:          pipeRuntime,
	}
	return s, nodeRuntime, pipeRuntime
}

func mustRuntimeRef(t *testing.T, raw string) domain.Ref {
	t.Helper()
	ref, err := domain.ParseRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

// driveServe runs tunnelServer.serve on the server end of a net.Pipe while the
// client end performs the tunnel handshake. Returns the client-side OpenResult.
func driveServe(t *testing.T, s *tunnelServer, clientSend wire.OpenPipe) wire.OpenResult {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	sr := &fakeStream{conn: serverConn}
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- s.serve(sr)
	}()

	if err := wire.WriteFrame(clientConn, wire.ClientHello{Version: "2.0", ClientPublic: "client-public-key", Capabilities: []string{"raw-duplex", "half-close"}}); err != nil {
		t.Fatalf("client hello: %v", err)
	}
	var server wire.ServerHello
	if err := wire.ReadFrame(clientConn, &server); err != nil {
		t.Fatalf("server hello: %v", err)
	}
	if server.PipeID != s.spec.PipeID {
		t.Fatalf("unexpected server pipe id %q", server.PipeID)
	}
	if err := wire.WriteFrame(clientConn, clientSend); err != nil {
		t.Fatalf("client open: %v", err)
	}
	var openResult wire.OpenResult
	if err := wire.ReadFrame(clientConn, &openResult); err != nil {
		t.Fatalf("open result: %v", err)
	}
	// The server may still be running the session; drain by reading until
	// EOF or error from the serve goroutine after a successful open (the
	// session pumps until both FINs).
	select {
	case <-resultCh:
	case <-time.After(2 * time.Second):
	}
	return openResult
}

// TestEngineServePipeTargetForwardsDownstreamExactlyOnce proves the receiving
// boundary advances traversal state and invokes the downstream-opener
// capability exactly once for a Pipe target, and that the OpenPipe hop/visited
// propagation reaches the opener with the current PipeID appended to the wire
// visited list and the downstream PipeID appended by the Engine.
func TestEngineServePipeTargetForwardsDownstreamExactlyOnce(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	recorder := &fakeDownstreamRecorder{}
	s, nodeRuntime, _ := buildPipeForwardServer(t, recorder, "pip_a", mustRuntimeRef(t, "pipe/b"))
	defer nodeRuntime.Close()
	// Session bookkeeping needs the state adapter; inject it.
	s.state = state

	result := driveServe(t, s, wire.OpenPipe{
		PipeID:         "pip_a",
		AccessLease:    "lease",
		HopLimit:       3,
		VisitedPipeIDs: []string{"pip_client"},
	})
	if !result.OK {
		t.Fatalf("expected open OK, got error %s: %s", result.ErrorCode, result.Message)
	}
	if recorder.calls != 1 {
		t.Fatalf("downstream opener called %d times, want exactly 1", recorder.calls)
	}
	// The opener must receive nextSpec Pipe B and a traversal state whose
	// visited list is wire visited + the current pipe (the downstream PipeID
	// is appended only by its own receiving boundary once entered).
	if len(recorder.states) != 1 {
		t.Fatalf("expected 1 recorded state, got %d", len(recorder.states))
	}
	got := recorder.states[0].VisitedIDs
	want := []string{"pip_client", "pip_a"}
	if len(got) != len(want) {
		t.Fatalf("visited length = %d (%v), want %d (%v)", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visited[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
	if recorder.visited[0] != "pip_b" {
		t.Fatalf("downstream opener next spec pipe = %q, want pip_b", recorder.visited[0])
	}
	if recorder.states[0].Remaining != 1 {
		t.Fatalf("remaining hops = %d, want 1 (hopLimit 3 minus 2 visited)", recorder.states[0].Remaining)
	}
}

// TestEngineServePipeTargetDeniedDownstreamNoBytes proves a failed downstream
// authorization produces an OpenError (denial) and no downstream open bytes:
// the opener is the only authority path for the downstream hop, and its error
// short-circuits the open with no carrier handed to a Session.
func TestEngineServePipeTargetDeniedDownstreamNoBytes(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	recorder := &fakeDownstreamRecorder{fail: errors.New("downstream authorization denied")}
	s, nodeRuntime, _ := buildPipeForwardServer(t, recorder, "pip_a", mustRuntimeRef(t, "pipe/b"))
	defer nodeRuntime.Close()
	s.state = state

	result := driveServe(t, s, wire.OpenPipe{
		PipeID:         "pip_a",
		AccessLease:    "lease",
		HopLimit:       3,
		VisitedPipeIDs: []string{"pip_client"},
	})
	if result.OK {
		t.Fatal("expected open failure when downstream denied")
	}
	if result.ErrorCode != "target_unreachable" {
		t.Fatalf("expected target_unreachable error code, got %q", result.ErrorCode)
	}
	if recorder.calls != 1 {
		t.Fatalf("downstream opener called %d times, want exactly 1", recorder.calls)
	}
}

// TestEngineServeServiceTargetNoDownstreamCall proves that when the target is
// a Service, the downstream opener is never invoked; the existing service dial
// path completes the open.
func TestEngineServeServiceTargetNoDownstreamCall(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	recorder := &fakeDownstreamRecorder{}
	s, nodeRuntime, _ := buildPipeForwardServer(t, recorder, "pip_a", mustRuntimeRef(t, "service/s1"))
	defer nodeRuntime.Close()
	s.state = state

	result := driveServe(t, s, wire.OpenPipe{
		PipeID:         "pip_a",
		AccessLease:    "lease",
		HopLimit:       3,
		VisitedPipeIDs: []string{"pip_client"},
	})
	if !result.OK {
		t.Fatalf("expected open OK, got error %s: %s", result.ErrorCode, result.Message)
	}
	if recorder.calls != 0 {
		t.Fatalf("downstream opener must not be called for a Service target, called %d times", recorder.calls)
	}
}

// TestEngineServeLoopDetectedBeforeDownstream proves the receiving boundary
// rejects a repeated PipeID in wire visited before any downstream open.
func TestEngineServeLoopDetectedBeforeDownstream(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	recorder := &fakeDownstreamRecorder{}
	s, nodeRuntime, _ := buildPipeForwardServer(t, recorder, "pip_a", mustRuntimeRef(t, "pipe/b"))
	defer nodeRuntime.Close()
	s.state = state

	result := driveServe(t, s, wire.OpenPipe{
		PipeID:         "pip_a",
		AccessLease:    "lease",
		HopLimit:       3,
		VisitedPipeIDs: []string{"pip_a"}, // current pipe already visited
	})
	if result.OK {
		t.Fatal("expected open failure on repeated pipe id")
	}
	if result.ErrorCode != "loop_detected" {
		t.Fatalf("expected loop_detected error code, got %q", result.ErrorCode)
	}
	if recorder.calls != 0 {
		t.Fatalf("downstream opener called %d times, want 0 for loop rejection", recorder.calls)
	}
}

// TestEngineServeHopLimitExceededBeforeDownstream proves the receiving
// boundary rejects an exhausted hop budget before any downstream open.
func TestEngineServeHopLimitExceededBeforeDownstream(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	recorder := &fakeDownstreamRecorder{}
	s, nodeRuntime, _ := buildPipeForwardServer(t, recorder, "pip_a", mustRuntimeRef(t, "pipe/b"))
	defer nodeRuntime.Close()
	s.state = state

	result := driveServe(t, s, wire.OpenPipe{
		PipeID:         "pip_a",
		AccessLease:    "lease",
		HopLimit:       1,
		VisitedPipeIDs: []string{"pip_client"},
	})
	if result.OK {
		t.Fatal("expected open failure when hop limit exceeded")
	}
	if result.ErrorCode != "hop_limit_exceeded" {
		t.Fatalf("expected hop_limit_exceeded error code, got %q", result.ErrorCode)
	}
	if recorder.calls != 0 {
		t.Fatalf("downstream opener called %d times, want 0 for hop-limit rejection", recorder.calls)
	}
}

var _ io.ReadWriteCloser = (*fakeCarrier)(nil)
var _ core.Stream = (*fakeStream)(nil)
