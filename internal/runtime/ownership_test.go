package runtime

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	wire "github.com/origama/tubo/internal/protocol"
)

// ownershipTestStream is a minimal in-process core.Stream for #27 ownership
// and carrier tests. It carries the authorized /tubo/tunnel/2.0 protocol.
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
func (s *ownershipTestStream) Protocol() protocol.ID            { return wire.TunnelID }
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
		SessionID:  id,
		PipeID:     "pip_test",
		Scope:      "test/default",
		TargetConn: newTestConn(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// TestPipeRuntimeSessionsAreIsolatedAcrossOwners verifies that the same
// SessionID under two sibling PipeRuntimes remains isolated: each registry
// holds only its own session.
func TestPipeRuntimeSessionsAreIsolatedAcrossOwners(t *testing.T) {
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
	if leftSession == rightSession {
		t.Fatal("sibling registries returned the same Session")
	}
	if left.registry.get("same-id") != leftSession {
		t.Fatal("left owner lost its registered session")
	}
	if right.registry.get("same-id") != rightSession {
		t.Fatal("right owner lost its registered session")
	}
}

// TestPipeRuntimeDuplicateSessionRejected verifies duplicate ID rejection
// within one PipeRuntime.
func TestPipeRuntimeDuplicateSessionRejected(t *testing.T) {
	node := NewNodeRuntime(context.Background())
	defer node.Close()
	owner, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	newOwnershipSession(t, owner, "owned")
	if _, err := owner.NewSession(sessionConfig{SessionID: "owned", TargetConn: newTestConn()}); !errorsIs(err, errDuplicateSessionID) {
		t.Fatalf("duplicate registration error = %v", err)
	}
}

// TestPipeRuntimeRegistrationAndParentCancellation verifies parent
// cancellation closes owned Sessions and clears the registry.
func TestPipeRuntimeRegistrationAndParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	node := NewNodeRuntime(parent)
	owner, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	newOwnershipSession(t, owner, "owned")

	cancel()
	select {
	case <-node.done:
	case <-time.After(time.Second):
		t.Fatal("parent cancellation did not close runtime tree")
	}
	if got := owner.registry.get("owned"); got != nil {
		t.Fatal("parent cancellation retained session registry entry")
	}
	if _, err := owner.NewSession(sessionConfig{SessionID: "new", TargetConn: newTestConn()}); !errorsIs(err, errRuntimeClosed) {
		t.Fatalf("registration after close error = %v", err)
	}
	// Registry close was idempotent by construction (require no resurrection).
	if got := owner.registry.get("owned"); got != nil {
		t.Fatal("closed owner resurrected registry entry")
	}
	node.Close()
	owner.Close()
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
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		owner.Close()
	}()
	wg.Wait()
	owner.Close()
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

// TestCloseCancelsAndJoinsBlockedOutboundOp verifies that owner close cancels
// and joins admitted outbound work so no post-close work remains admitted.
func TestCloseCancelsAndJoinsBlockedOutboundOp(t *testing.T) {
	for _, closer := range []struct {
		name  string
		close func(*NodeRuntime, *PipeRuntime, *sessionPump)
	}{
		{name: "session", close: func(_ *NodeRuntime, _ *PipeRuntime, session *sessionPump) { session.Stop() }},
		{name: "pipe", close: func(_ *NodeRuntime, runtime *PipeRuntime, _ *sessionPump) { runtime.Close() }},
		{name: "node", close: func(node *NodeRuntime, _ *PipeRuntime, _ *sessionPump) { node.Close() }},
	} {
		t.Run(closer.name, func(t *testing.T) {
			node := NewNodeRuntime(context.Background())
			defer node.Close()
			runtime, err := node.NewPipeRuntime()
			if err != nil {
				t.Fatal(err)
			}
			sp := newOwnershipSession(t, runtime, "outbound")
			opDone := make(chan error, 1)
			go func() {
				ctx, finish, err := sp.beginOutbound(context.Background())
				if err != nil {
					opDone <- err
					return
				}
				<-ctx.Done()
				finish()
				opDone <- nil
			}()
			// Let the outbound op reach its blocking ctx.Done().
			select {
			case err := <-opDone:
				t.Fatalf("outbound op ended before close: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			closeDone := make(chan struct{})
			go func() { closer.close(node, runtime, sp); close(closeDone) }()
			select {
			case <-opDone:
			case <-time.After(time.Second):
				t.Fatal("owner close did not cancel blocked outbound op")
			}
			select {
			case <-closeDone:
			case <-time.After(time.Second):
				t.Fatal("owner close did not join outbound work")
			}
			sp.mu.Lock()
			work := sp.workCount
			sp.mu.Unlock()
			if work != 0 {
				t.Fatal("closed session retained admitted outbound work")
			}
		})
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
		t.Fatal("set active carrier")
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
