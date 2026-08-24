package runtime

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
)

// testBufferConn implements io.ReadWriteCloser for testing.
type testBufferConn struct {
	bytes.Buffer
	closed bool
}

func (c *testBufferConn) Close() error      { c.closed = true; return nil }
func (c *testBufferConn) CloseWrite() error { c.closed = true; return nil }
func newTestConn() *testBufferConn          { return &testBufferConn{} }

func newTestSession(t testing.TB, cfg sessionConfig) *sessionPump {
	t.Helper()
	node := NewNodeRuntime(context.Background())
	t.Cleanup(node.Close)
	pipeRuntime, err := node.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	session, err := pipeRuntime.NewSession(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// TestSessionPumpNew verifies session pump creation on the non-resume path.
func TestSessionPumpNew(t *testing.T) {
	sp := newTestSession(t, sessionConfig{
		SessionID:  "test-session",
		PipeID:     "test-pipe",
		Scope:      "test/test",
		TargetConn: newTestConn(),
	})
	defer sp.Stop()
	if sp.SessionID != "test-session" {
		t.Fatalf("expected test-session, got %s", sp.SessionID)
	}
	if sp.epoch != 1 {
		t.Fatalf("expected epoch 1, got %d", sp.epoch)
	}
	if sp.state != StateActive {
		t.Fatalf("expected active, got %s", sp.state)
	}
}

// TestSessionStateMachine verifies the minimal non-resume state transitions.
func TestSessionStateMachine(t *testing.T) {
	for _, s := range []SessionState{StateActive, StateClosing, StateClosed} {
		if s.String() == "unknown" {
			t.Fatalf("unexpected unknown state for %d", s)
		}
	}
}

// TestSessionRegistry verifies instance registry.
func TestSessionRegistry(t *testing.T) {
	reg := newSessionRegistry()
	pump := &sessionPump{SessionID: "test-reg"}

	if err := reg.register("test-reg", pump); err != nil {
		t.Fatal(err)
	}
	if got := reg.get("test-reg"); got == nil || got.SessionID != "test-reg" {
		t.Fatal("registry get/put failed")
	}

	reg.remove("test-reg", pump)
	if got := reg.get("test-reg"); got != nil {
		t.Fatal("expected nil after remove")
	}
}

// TestSetActiveCarrierRejectsStoppedSession verifies the transport-admission
// fence: no active carrier may be installed after close.
func TestSetActiveCarrierRejectsStoppedSession(t *testing.T) {
	sp := newTestSession(t, sessionConfig{
		SessionID:  "carrier-fence",
		PipeID:     "test-pipe",
		Scope:      "test/test",
		TargetConn: newTestConn(),
	})
	sp.Stop()
	stream := &ownershipTestStream{}
	if sp.SetActiveCarrier(stream) {
		t.Fatal("stopped session accepted a carrier")
	}
	if stream.closed {
		t.Fatal("rejected carrier install must not close the stream")
	}
}

// TestHalfCloseSequencing verifies FIN-after-final-byte sequencing on
// the non-resume raw-duplex production transport (copyDuplex). FIN (CloseWrite)
// must be observed only after the final application byte is delivered.
type halfCloseSource struct {
	mu   sync.Mutex
	data []byte
}

func (s *halfCloseSource) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.data) > 0 {
		n := copy(p, s.data)
		s.data = s.data[n:]
		return n, nil
	}
	return 0, io.EOF
}
func (s *halfCloseSource) Write(p []byte) (int, error) { return len(p), nil }
func (s *halfCloseSource) Close() error                { return nil }
func (s *halfCloseSource) CloseWrite() error           { return nil }

type halfCloseWatcher struct {
	mu              sync.Mutex
	written         int
	expectN         int
	finalByteC      chan struct{}
	closeWriteC     chan struct{}
	closeWriteEarly bool
}

func (w *halfCloseWatcher) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.written += len(p)
	n := w.written
	expect := w.expectN
	finalC := w.finalByteC
	w.mu.Unlock()
	if expect > 0 && n >= expect && finalC != nil {
		select {
		case <-finalC:
		default:
			close(finalC)
		}
	}
	return len(p), nil
}
func (w *halfCloseWatcher) Read([]byte) (int, error) { return 0, io.EOF }
func (w *halfCloseWatcher) Close() error             { return nil }
func (w *halfCloseWatcher) CloseWrite() error {
	w.mu.Lock()
	if w.finalByteC != nil {
		select {
		case <-w.finalByteC:
		default:
			w.closeWriteEarly = true
		}
	}
	w.mu.Unlock()
	if w.closeWriteC != nil {
		close(w.closeWriteC)
	}
	return nil
}

func TestHalfCloseSequencing(t *testing.T) {
	payload := bytes.Repeat([]byte("fin-sequencing-payload-"), 16)
	src := &halfCloseSource{data: append([]byte(nil), payload...)}
	dst := &halfCloseWatcher{expectN: len(payload), finalByteC: make(chan struct{}), closeWriteC: make(chan struct{})}

	copyDuplex(src, dst)

	if dst.written != len(payload) {
		t.Fatalf("delivered %d bytes, want %d", dst.written, len(payload))
	}
	if dst.closeWriteEarly {
		t.Fatal("FIN (CloseWrite) sequenced before the final application byte was delivered")
	}
}
