package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	wire "github.com/origama/tubo-rebuilt/internal/protocol"
)

type ownershipTestStream struct {
	bytes.Buffer
	reset  bool
	closed bool
}

func (s *ownershipTestStream) Close() error                     { s.closed = true; return nil }
func (s *ownershipTestStream) CloseWrite() error                { return nil }
func (s *ownershipTestStream) CloseRead() error                 { return nil }
func (s *ownershipTestStream) Reset() error                     { s.reset = true; return nil }
func (s *ownershipTestStream) SetDeadline(time.Time) error      { return nil }
func (s *ownershipTestStream) SetReadDeadline(time.Time) error  { return nil }
func (s *ownershipTestStream) SetWriteDeadline(time.Time) error { return nil }
func (s *ownershipTestStream) ID() string                       { return "test" }
func (s *ownershipTestStream) Protocol() protocol.ID            { return wire.TunnelResumeID }
func (s *ownershipTestStream) SetProtocol(protocol.ID) error    { return nil }
func (s *ownershipTestStream) Stat() core.Stats                 { return core.Stats{} }
func (s *ownershipTestStream) Conn() core.Conn                  { return nil }
func (s *ownershipTestStream) Scope() core.StreamScope          { return nil }
func (s *ownershipTestStream) ResetWithError(core.StreamErrorCode) error {
	return s.Reset()
}

func newOwnershipSession(t testing.TB, runtime *PipeRuntime, id string) *sessionPump {
	t.Helper()
	session, err := runtime.NewSession(sessionConfig{
		SessionID:    id,
		PipeID:       "pip_test",
		Scope:        "test/default",
		TargetConn:   newTestConn(),
		ResumeSecret: bytes.Repeat([]byte("x"), 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

func roleStream(t testing.TB, role, id string) *ownershipTestStream {
	t.Helper()
	stream := &ownershipTestStream{}
	if err := wire.WriteResumeFrame(stream, wire.StreamRoleDeclaration{Role: role, SessionID: id}); err != nil {
		t.Fatal(err)
	}
	return stream
}

func TestPipeRuntimeBoundDispatchIsolatesSameSessionID(t *testing.T) {
	node := NewNodeRuntime(context.Background())
	defer node.Close()
	left, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	right, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	leftSession := newOwnershipSession(t, left, "same-id")
	rightSession := newOwnershipSession(t, right, "same-id")

	if got, err := left.sessionForRole(wire.StreamRoleDeclaration{Role: wire.StreamRoleResume, SessionID: "same-id"}); err != nil || got != leftSession {
		t.Fatalf("left owner selected %p, %v; want %p, nil", got, err, leftSession)
	}
	if got, err := right.sessionForRole(wire.StreamRoleDeclaration{Role: wire.StreamRoleResume, SessionID: "same-id"}); err != nil || got != rightSession {
		t.Fatalf("right owner selected %p, %v; want %p, nil", got, err, rightSession)
	}

	leftStream := roleStream(t, wire.StreamRoleResume, "same-id")
	left.HandleResumeStream(leftStream)
	if leftStream.reset {
		t.Fatal("left bound handler reset known session")
	}
	rightStream := roleStream(t, wire.StreamRoleResume, "same-id")
	right.HandleResumeStream(rightStream)
	if rightStream.reset {
		t.Fatal("right bound handler reset known session")
	}
}

func TestPipeRuntimeDispatchFailsClosed(t *testing.T) {
	node := NewNodeRuntime(context.Background())
	defer node.Close()
	owner, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	other, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	session := newOwnershipSession(t, owner, "known")

	for _, test := range []struct {
		name   string
		owner  *PipeRuntime
		stream *ownershipTestStream
	}{
		{name: "unknown", owner: owner, stream: roleStream(t, wire.StreamRoleResume, "unknown")},
		{name: "wrong owner", owner: other, stream: roleStream(t, wire.StreamRoleResume, "known")},
		{name: "unsupported role", owner: owner, stream: roleStream(t, "other", "known")},
		{name: "empty session", owner: owner, stream: roleStream(t, wire.StreamRoleResume, "")},
		{name: "malformed", owner: owner, stream: &ownershipTestStream{Buffer: *bytes.NewBufferString("bad")}},
		{name: "oversized", owner: owner, stream: oversizedRoleStream(t)},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.owner.HandleResumeStream(test.stream)
			if !test.stream.reset {
				t.Fatal("dispatch accepted invalid declaration")
			}
		})
	}

	session.Stop()
	closed := roleStream(t, wire.StreamRoleResume, "known")
	owner.HandleResumeStream(closed)
	if !closed.reset {
		t.Fatal("dispatch accepted stopped session")
	}
	owner.Close()
	closedOwner := roleStream(t, wire.StreamRoleResume, "known")
	owner.HandleResumeStream(closedOwner)
	if !closedOwner.reset {
		t.Fatal("dispatch accepted closed owner")
	}
}

func TestPipeRuntimeRegistrationAndParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	node := NewNodeRuntime(parent)
	owner, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	session := newOwnershipSession(t, owner, "owned")
	if _, err := owner.NewSession(sessionConfig{SessionID: "owned", TargetConn: newTestConn()}); !errorsIs(err, errDuplicateSessionID) {
		t.Fatalf("duplicate registration error = %v", err)
	}

	cancel()
	select {
	case <-node.done:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not close runtime tree")
	}
	select {
	case <-session.ctx.Done():
	default:
		t.Fatal("parent cancellation did not cancel session")
	}
	if got := owner.registry.get("owned"); got != nil {
		t.Fatal("parent cancellation retained session registry entry")
	}
	if _, err := owner.NewSession(sessionConfig{SessionID: "new", TargetConn: newTestConn()}); !errorsIs(err, errRuntimeClosed) {
		t.Fatalf("registration after close error = %v", err)
	}
	stream := roleStream(t, wire.StreamRoleControl, "owned")
	owner.HandleResumeStream(stream)
	if !stream.reset {
		t.Fatal("dispatch resurrected canceled session")
	}
	node.Close()
	owner.Close()
	session.Stop()
}

func TestPipeRuntimeConcurrentRegisterDispatchAndClose(t *testing.T) {
	node := NewNodeRuntime(context.Background())
	owner, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	defer node.Close()
	newOwnershipSession(t, owner, "stable")

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _ = owner.NewSession(sessionConfig{SessionID: fmt.Sprintf("session-%d", i), TargetConn: newTestConn()})
		}(i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner.HandleResumeStream(roleStream(t, wire.StreamRoleResume, "stable"))
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		owner.Close()
	}()
	wg.Wait()
	owner.Close()
}

func oversizedRoleStream(t testing.TB) *ownershipTestStream {
	t.Helper()
	stream := &ownershipTestStream{}
	declaration := wire.StreamRoleDeclaration{Role: wire.StreamRoleResume, SessionID: strings.Repeat("x", wire.MaxResumeFrame)}
	if err := wire.WriteFrame(stream, declaration); err != nil {
		t.Fatal(err)
	}
	if stream.Len() <= wire.MaxResumeFrame+4 {
		t.Fatal("test role declaration did not exceed resume frame bound")
	}
	return stream
}

type blockingResumeStream struct {
	mu             sync.Mutex
	input          *bytes.Reader
	blocked        chan struct{}
	release        chan struct{}
	writes         bytes.Buffer
	reset          bool
	once           sync.Once
	releaseOnce    sync.Once
	closeStarted   chan struct{}
	closeRelease   chan struct{}
	closeStartOnce sync.Once
}

func newBlockingResumeStream(t testing.TB, session *sessionPump) *blockingResumeStream {
	t.Helper()
	var input bytes.Buffer
	if err := wire.WriteResumeFrame(&input, wire.StreamRoleDeclaration{Role: wire.StreamRoleResume, SessionID: session.SessionID}); err != nil {
		t.Fatal(err)
	}
	hello := wire.ResumeHello{Version: "2.1", PipeID: session.PipeID, SessionID: session.SessionID, NextEpoch: 2, Nonce: "nonce", Proof: session.proofForHello(2, "nonce")}
	if err := wire.WriteResumeFrame(&input, hello); err != nil {
		t.Fatal(err)
	}
	return &blockingResumeStream{input: bytes.NewReader(input.Bytes()), blocked: make(chan struct{}), release: make(chan struct{})}
}

func (s *blockingResumeStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.input.Len() > 0 {
		n, err := s.input.Read(p)
		s.mu.Unlock()
		return n, err
	}
	s.mu.Unlock()
	s.once.Do(func() { close(s.blocked) })
	<-s.release
	return 0, io.ErrClosedPipe
}
func (s *blockingResumeStream) Write(p []byte) (int, error) { return s.writes.Write(p) }
func (s *blockingResumeStream) Close() error {
	s.once.Do(func() { close(s.blocked) })
	s.releaseOnce.Do(func() { close(s.release) })
	if s.closeStarted != nil {
		s.closeStartOnce.Do(func() { close(s.closeStarted) })
		<-s.closeRelease
	}
	return nil
}
func (s *blockingResumeStream) CloseWrite() error { return nil }
func (s *blockingResumeStream) CloseRead() error  { return nil }
func (s *blockingResumeStream) Reset() error {
	s.mu.Lock()
	s.reset = true
	s.mu.Unlock()
	s.releaseOnce.Do(func() { close(s.release) })
	return nil
}
func (s *blockingResumeStream) SetDeadline(time.Time) error               { return nil }
func (s *blockingResumeStream) SetReadDeadline(time.Time) error           { return nil }
func (s *blockingResumeStream) SetWriteDeadline(time.Time) error          { return nil }
func (s *blockingResumeStream) ID() string                                { return "blocking" }
func (s *blockingResumeStream) Protocol() protocol.ID                     { return wire.TunnelResumeID }
func (s *blockingResumeStream) SetProtocol(protocol.ID) error             { return nil }
func (s *blockingResumeStream) Stat() core.Stats                          { return core.Stats{} }
func (s *blockingResumeStream) Conn() core.Conn                           { return nil }
func (s *blockingResumeStream) Scope() core.StreamScope                   { return nil }
func (s *blockingResumeStream) ResetWithError(core.StreamErrorCode) error { return s.Reset() }
func (s *blockingResumeStream) wasReset() bool                            { s.mu.Lock(); defer s.mu.Unlock(); return s.reset }

func TestAcceptedResumeCannotOutliveOwnerClose(t *testing.T) {
	for _, test := range []struct {
		name  string
		close func(*NodeRuntime, *PipeRuntime, *sessionPump)
	}{
		{name: "session", close: func(_ *NodeRuntime, _ *PipeRuntime, session *sessionPump) { session.Stop() }},
		{name: "pipe", close: func(_ *NodeRuntime, runtime *PipeRuntime, _ *sessionPump) { runtime.Close() }},
		{name: "node", close: func(node *NodeRuntime, _ *PipeRuntime, _ *sessionPump) { node.Close() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := NewNodeRuntime(context.Background())
			defer node.Close()
			runtime, err := node.NewPipeRuntime()
			if err != nil {
				t.Fatal(err)
			}
			session := newOwnershipSession(t, runtime, "resume")
			stream := newBlockingResumeStream(t, session)
			handlerDone := make(chan struct{})
			go func() { runtime.HandleResumeStream(stream); close(handlerDone) }()
			select {
			case <-stream.blocked:
			case <-time.After(time.Second):
				t.Fatal("resume handler did not reach post-hello barrier")
			}
			test.close(node, runtime, session)
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Fatal("owner close did not unblock accepted resume")
			}
			if !stream.wasReset() {
				t.Fatal("owner close did not reset accepted resume stream")
			}
			session.mu.Lock()
			state, candidate := session.state, session.candidateStream
			session.mu.Unlock()
			if state != StateClosed || candidate != nil {
				t.Fatalf("stale accepted resume revived stopped Session: state=%s candidate=%v", state, candidate != nil)
			}
			if runtime.registry.get(session.SessionID) != nil {
				t.Fatal("closed session remained registered")
			}
		})
	}
}

func TestCloseWaitsForAcceptedDispatchExit(t *testing.T) {
	for _, test := range []struct {
		name  string
		close func(*NodeRuntime, *PipeRuntime, *sessionPump)
	}{
		{name: "session", close: func(_ *NodeRuntime, _ *PipeRuntime, session *sessionPump) { session.Stop() }},
		{name: "pipe", close: func(_ *NodeRuntime, runtime *PipeRuntime, _ *sessionPump) { runtime.Close() }},
		{name: "node", close: func(node *NodeRuntime, _ *PipeRuntime, _ *sessionPump) { node.Close() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := NewNodeRuntime(context.Background())
			defer node.Close()
			runtime, err := node.NewPipeRuntime()
			if err != nil {
				t.Fatal(err)
			}
			session := newOwnershipSession(t, runtime, "dispatch-barrier")
			stream := newBlockingResumeStream(t, session)
			stream.closeStarted = make(chan struct{})
			stream.closeRelease = make(chan struct{})
			handlerDone := make(chan struct{})
			go func() { runtime.HandleResumeStream(stream); close(handlerDone) }()
			select {
			case <-stream.blocked:
			case <-time.After(time.Second):
				t.Fatal("dispatch did not reach barrier")
			}
			closeDone := make(chan struct{})
			go func() { test.close(node, runtime, session); close(closeDone) }()
			select {
			case <-stream.closeStarted:
			case <-time.After(time.Second):
				t.Fatal("dispatch did not begin local close")
			}
			select {
			case <-closeDone:
				t.Fatal("owner close returned before accepted dispatch exited")
			default:
			}
			close(stream.closeRelease)
			select {
			case <-handlerDone:
			case <-time.After(time.Second):
				t.Fatal("dispatch did not exit")
			}
			select {
			case <-closeDone:
			case <-time.After(time.Second):
				t.Fatal("owner close did not wait dispatch exit")
			}
		})
	}
}

type activationBarrierStream struct {
	replayStarted    chan struct{}
	replayAfterClose chan struct{}
	releaseWrite     chan struct{}
	pumpRead         chan struct{}
	closed           chan struct{}
	closeOnce        sync.Once
	replayOnce       sync.Once
	afterOnce        sync.Once
	pumpOnce         sync.Once
}

func newActivationBarrierStream() *activationBarrierStream {
	return &activationBarrierStream{replayStarted: make(chan struct{}), replayAfterClose: make(chan struct{}), releaseWrite: make(chan struct{}), pumpRead: make(chan struct{}), closed: make(chan struct{})}
}
func (s *activationBarrierStream) Read([]byte) (int, error) {
	s.pumpOnce.Do(func() { close(s.pumpRead) })
	<-s.closed
	return 0, io.ErrClosedPipe
}
func (s *activationBarrierStream) Write(p []byte) (int, error) {
	s.replayOnce.Do(func() { close(s.replayStarted) })
	<-s.closed
	s.afterOnce.Do(func() { close(s.replayAfterClose) })
	<-s.releaseWrite
	return 0, io.ErrClosedPipe
}
func (s *activationBarrierStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}
func (s *activationBarrierStream) CloseWrite() error                         { return s.Close() }
func (s *activationBarrierStream) CloseRead() error                          { return s.Close() }
func (s *activationBarrierStream) Reset() error                              { return s.Close() }
func (s *activationBarrierStream) SetDeadline(time.Time) error               { return nil }
func (s *activationBarrierStream) SetReadDeadline(time.Time) error           { return nil }
func (s *activationBarrierStream) SetWriteDeadline(time.Time) error          { return nil }
func (s *activationBarrierStream) ID() string                                { return "activation" }
func (s *activationBarrierStream) Protocol() protocol.ID                     { return wire.TunnelResumeID }
func (s *activationBarrierStream) SetProtocol(protocol.ID) error             { return nil }
func (s *activationBarrierStream) Stat() core.Stats                          { return core.Stats{} }
func (s *activationBarrierStream) Conn() core.Conn                           { return nil }
func (s *activationBarrierStream) Scope() core.StreamScope                   { return nil }
func (s *activationBarrierStream) ResetWithError(core.StreamErrorCode) error { return s.Reset() }

func TestCloseWaitsForActivationWorkAndFencesSideEffects(t *testing.T) {
	for _, test := range []struct {
		name  string
		close func(*NodeRuntime, *PipeRuntime, *sessionPump)
	}{
		{name: "session", close: func(_ *NodeRuntime, _ *PipeRuntime, session *sessionPump) { session.Stop() }},
		{name: "pipe", close: func(_ *NodeRuntime, runtime *PipeRuntime, _ *sessionPump) { runtime.Close() }},
		{name: "node", close: func(node *NodeRuntime, _ *PipeRuntime, _ *sessionPump) { node.Close() }},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := NewNodeRuntime(context.Background())
			defer node.Close()
			runtime, err := node.NewPipeRuntime()
			if err != nil {
				t.Fatal(err)
			}
			session := newOwnershipSession(t, runtime, "activation-barrier")
			session.c2s.mu.Lock()
			session.c2s.sourceNext = 1
			session.c2s.retained.WriteByte('x')
			session.c2s.mu.Unlock()
			stream := newActivationBarrierStream()
			activated := make(chan bool, 1)
			go func() { activated <- session.activateResume(stream, 2, "client", "server", 0, 0) }()
			select {
			case <-stream.replayStarted:
			case <-time.After(time.Second):
				t.Fatal("activation did not enter replay")
			}
			closeDone := make(chan struct{})
			go func() { test.close(node, runtime, session); close(closeDone) }()
			select {
			case <-stream.replayAfterClose:
			case <-time.After(time.Second):
				t.Fatal("close did not cancel replay")
			}
			select {
			case <-closeDone:
				t.Fatal("owner close returned before activation work exited")
			default:
			}
			close(stream.releaseWrite)
			select {
			case ok := <-activated:
				if ok {
					t.Fatal("closing session completed activation")
				}
			case <-time.After(time.Second):
				t.Fatal("activation did not exit")
			}
			select {
			case <-closeDone:
			case <-time.After(time.Second):
				t.Fatal("owner close did not wait activation exit")
			}
			select {
			case <-stream.pumpRead:
				t.Fatal("closing session created pump work")
			default:
			}
		})
	}
}

func TestActivateResumeRejectsStoppedSession(t *testing.T) {
	node := NewNodeRuntime(context.Background())
	defer node.Close()
	runtime, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	session := newOwnershipSession(t, runtime, "stopped")
	stream := &ownershipTestStream{}
	session.Stop()
	if err := session.StartResumeHandshake(context.Background(), "", "relayed"); err == nil || err.Error() != "session closed" {
		t.Fatalf("stopped session resume start error = %v", err)
	}
	if session.activateResume(stream, 2, "client", "server", 0, 0) {
		t.Fatal("stopped session activated")
	}
	if !stream.reset {
		t.Fatal("stopped session did not reset activation carrier")
	}
	session.mu.Lock()
	state := session.state
	session.mu.Unlock()
	if state != StateClosed {
		t.Fatalf("state=%s, want closed", state)
	}
}

type blockingCloseConn struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *blockingCloseConn) Read([]byte) (int, error)    { return 0, io.EOF }
func (c *blockingCloseConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *blockingCloseConn) Close() error {
	c.once.Do(func() { close(c.started) })
	<-c.release
	return nil
}

func TestCloseWaitsForOwnedSessionCleanup(t *testing.T) {
	node := NewNodeRuntime(context.Background())
	runtime, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	target := &blockingCloseConn{started: make(chan struct{}), release: make(chan struct{})}
	session, err := runtime.NewSession(sessionConfig{SessionID: "cleanup", TargetConn: target})
	if err != nil {
		t.Fatal(err)
	}
	pipeDone := make(chan struct{})
	go func() { runtime.Close(); close(pipeDone) }()
	select {
	case <-target.started:
	case <-time.After(time.Second):
		t.Fatal("session cleanup did not start")
	}
	nodeDone := make(chan struct{})
	go func() { node.Close(); close(nodeDone) }()
	select {
	case <-nodeDone:
		t.Fatal("NodeRuntime.Close returned before in-flight PipeRuntime cleanup finished")
	default:
	}
	close(target.release)
	select {
	case <-pipeDone:
	case <-time.After(time.Second):
		t.Fatal("pipe close did not finish cleanup")
	}
	select {
	case <-nodeDone:
	case <-time.After(time.Second):
		t.Fatal("node close did not observe pipe cleanup completion")
	}
	if runtime.registry.get(session.SessionID) != nil {
		t.Fatal("closed session remained registered")
	}
}

type lateOutboundHost struct {
	host.Host
	started chan struct{}
	stream  core.Stream
	once    sync.Once
}

func (h *lateOutboundHost) NewStream(ctx context.Context, _ peer.ID, _ ...protocol.ID) (core.Stream, error) {
	h.once.Do(func() { close(h.started) })
	<-ctx.Done()
	return h.stream, nil
}

func TestCloseCancelsAndJoinsBlockedOutboundOperations(t *testing.T) {
	for _, operation := range []struct {
		name string
		call func(*sessionPump) error
	}{
		{name: "control", call: func(session *sessionPump) error { return session.OpenControlStream(context.Background(), "") }},
		{name: "resume", call: func(session *sessionPump) error {
			return session.StartResumeHandshake(context.Background(), "", "relayed")
		}},
	} {
		for _, closer := range []struct {
			name  string
			close func(*NodeRuntime, *PipeRuntime, *sessionPump)
		}{
			{name: "session", close: func(_ *NodeRuntime, _ *PipeRuntime, session *sessionPump) { session.Stop() }},
			{name: "pipe", close: func(_ *NodeRuntime, runtime *PipeRuntime, _ *sessionPump) { runtime.Close() }},
			{name: "node", close: func(node *NodeRuntime, _ *PipeRuntime, _ *sessionPump) { node.Close() }},
		} {
			t.Run(operation.name+"/"+closer.name, func(t *testing.T) {
				node := NewNodeRuntime(context.Background())
				defer node.Close()
				runtime, err := node.NewPipeRuntime()
				if err != nil {
					t.Fatal(err)
				}
				session := newOwnershipSession(t, runtime, "outbound")
				stream := &ownershipTestStream{}
				session.Host = &lateOutboundHost{started: make(chan struct{}), stream: stream}
				opDone := make(chan error, 1)
				go func() { opDone <- operation.call(session) }()
				h := session.Host.(*lateOutboundHost)
				select {
				case <-h.started:
				case <-time.After(time.Second):
					t.Fatal("outbound operation did not enter NewStream")
				}
				closeDone := make(chan struct{})
				go func() { closer.close(node, runtime, session); close(closeDone) }()
				select {
				case <-opDone:
				case <-time.After(time.Second):
					t.Fatal("owner close did not cancel blocked NewStream")
				}
				select {
				case <-closeDone:
				case <-time.After(time.Second):
					t.Fatal("owner close did not join outbound work")
				}
				if stream.Len() != 0 || stream.reset == false {
					t.Fatal("closed outbound operation wrote or installed stream")
				}
				session.mu.Lock()
				control, active, work := session.controlStream, session.activeCarrier, session.workCount
				session.mu.Unlock()
				if control != nil || active != nil || work != 0 {
					t.Fatal("closed outbound operation retained ownership")
				}
			})
		}
	}
}

type errorPumpTarget struct {
	closed       chan struct{}
	writeStarted chan struct{}
	once         sync.Once
}

func (c *errorPumpTarget) Read([]byte) (int, error) { <-c.closed; return 0, io.ErrClosedPipe }
func (c *errorPumpTarget) Write([]byte) (int, error) {
	c.once.Do(func() { close(c.writeStarted) })
	return 0, errors.New("local write failure")
}
func (c *errorPumpTarget) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func TestPumpFailureStartsOneTerminalCloser(t *testing.T) {
	node := NewNodeRuntime(context.Background())
	defer node.Close()
	runtime, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	target := &errorPumpTarget{closed: make(chan struct{}), writeStarted: make(chan struct{})}
	session, err := runtime.NewSession(sessionConfig{SessionID: "pump-failure", TargetConn: target})
	if err != nil {
		t.Fatal(err)
	}
	carrier := &ownershipTestStream{Buffer: *bytes.NewBufferString("x")}
	if !session.SetActiveCarrier(carrier) {
		t.Fatal("set carrier")
	}
	runDone := make(chan struct{})
	go func() { session.runServerSession(); close(runDone) }()
	select {
	case <-target.writeStarted:
	case <-time.After(time.Second):
		t.Fatal("pump did not reach local write failure")
	}
	select {
	case <-session.stopDone:
	case <-time.After(time.Second):
		t.Fatal("pump failure did not complete close")
	}
	select {
	case <-runDone:
	case <-time.After(time.Second):
		t.Fatal("pump work did not exit")
	}
	session.mu.Lock()
	state, stopping, work := session.state, session.stopping, session.workCount
	session.mu.Unlock()
	if state != StateClosed || !stopping || work != 0 {
		t.Fatalf("terminal lifecycle lost: state=%s stopping=%v work=%d", state, stopping, work)
	}
	if runtime.registry.get(session.SessionID) != nil {
		t.Fatal("terminal session resurrected in registry")
	}
	session.Stop()
}

func errorsIs(got, want error) bool { return errors.Is(got, want) }
