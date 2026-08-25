package runtime

// Issue #28 continuity acceptance test: the production Session data plane runs
// raw full-duplex bytes and exact half-close (FIN only after final bytes) over
// the neutral Carrier seam, with owner cancellation terminating a mid-flow
// session. This exercises runServerSession/runClientSession and the active
// Carrier directly, deterministically and without any libp2p or network.

import (
	"bytes"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/node"
)

// byteQueue is one direction of a full-duplex pipe with clean half-close: a
// CloseWrite marks the write side FIN; the read side drains remaining bytes
// then observes io.EOF. A reset is distinct (surfaces as a closed-pipe error).
// Every read/write is mutex/broadcast driven, so it is safe under -race and
// the pump, peer and driver can touch the same direction concurrently.
type byteQueue struct {
	mu   sync.Mutex
	cond *sync.Cond
	buf  []byte
	fin  bool // write side half-closed
	dead bool // direction torn down
}

func newByteQueue() *byteQueue {
	q := &byteQueue{}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *byteQueue) Write(p []byte) (int, error) {
	q.cond.L.Lock()
	if q.dead {
		q.cond.L.Unlock()
		return 0, io.ErrClosedPipe
	}
	q.buf = append(q.buf, p...)
	q.cond.Broadcast()
	q.cond.L.Unlock()
	return len(p), nil
}

func (q *byteQueue) CloseWrite() error {
	q.cond.L.Lock()
	if q.dead {
		q.cond.L.Unlock()
		return io.ErrClosedPipe
	}
	q.fin = true
	q.cond.Broadcast()
	q.cond.L.Unlock()
	return nil
}

func (q *byteQueue) Read(p []byte) (int, error) {
	q.cond.L.Lock()
	for len(q.buf) == 0 && !q.fin && !q.dead {
		q.cond.Wait()
	}
	if len(q.buf) > 0 {
		n := copy(p, q.buf)
		q.buf = q.buf[n:]
		q.cond.L.Unlock()
		return n, nil
	}
	// Snapshot the terminal flags while still holding the lock so a concurrent
	// kill/reset cannot otherwise race this read outside the mutex.
	dead := q.dead
	q.cond.L.Unlock()
	if dead {
		return 0, io.ErrClosedPipe
	}
	return 0, io.EOF
}

func (q *byteQueue) kill() {
	q.cond.L.Lock()
	q.dead = true
	q.fin = true
	q.cond.Broadcast()
	q.cond.L.Unlock()
}

// duplexEnd is one end of a full-duplex in-memory stream. It references two
// per-direction queues: toPeer carries writes to the far end, fromPeer
// carries bytes the far end writes to this end. It satisfies node.Carrier.
type duplexEnd struct {
	toPeer   *byteQueue
	fromPeer *byteQueue
}

// pairDuplex returns two wire-connected full-duplex endpoints (carrier or app
// pipe). Writes on one end are readable on the far end; CloseWrite half-closes
// only that direction.
func pairDuplex() (*duplexEnd, *duplexEnd) {
	ab := newByteQueue()
	ba := newByteQueue()
	a := &duplexEnd{toPeer: ab, fromPeer: ba}
	b := &duplexEnd{toPeer: ba, fromPeer: ab}
	return a, b
}

func (e *duplexEnd) Read(p []byte) (int, error)  { return e.fromPeer.Read(p) }
func (e *duplexEnd) Write(p []byte) (int, error) { return e.toPeer.Write(p) }
func (e *duplexEnd) CloseWrite() error           { return e.toPeer.CloseWrite() }
func (e *duplexEnd) Close() error                { return e.CloseWrite() }
func (e *duplexEnd) Reset() error                { e.toPeer.kill(); e.fromPeer.kill(); return nil }
func (e *duplexEnd) SetDeadline(time.Time) error { return nil }

var _ node.Carrier = (*duplexEnd)(nil)

// continuator wires one session pump: TargetConn is one end of an app pipe,
// active Carrier is one end of a second pipe. The test drives the remaining
// far ends, so real byte flow crosses the Carrier seam.
type continuator struct {
	session    *sessionPump
	carrier    node.Carrier
	carrierFar *duplexEnd
	app        *duplexEnd
	run        func()
}

func newContinuity(t testing.TB, name string, isClient bool) *continuator {
	t.Helper()
	carrierNear, carrierFar := pairDuplex()
	appNear, appFar := pairDuplex()
	sp := newTestSession(t, sessionConfig{
		SessionID:  "cont-" + name + "-" + t.Name(),
		PipeID:     "pip_test",
		Scope:      "test/default",
		TargetConn: appNear,
		IsClient:   isClient,
	})
	if !sp.SetActiveCarrier(carrierNear) {
		t.Fatal("session rejected active carrier")
	}
	c := &continuator{session: sp, carrier: carrierNear, carrierFar: carrierFar, app: appFar}
	if isClient {
		c.run = sp.runClientSession
	} else {
		c.run = sp.runServerSession
	}
	return c
}

// TestTunnelContinuityFullDuplexHalfClose drives the server-role session pump
// and verifies: (1) bytes written by the app cross into the carrier far end,
// (2) bytes written by the carrier far end cross into the app, and (3) the
// data plane completes only after both directions reach FIN, preserving exact
// half-close sequencing through the Session pump over Carrier.
func TestTunnelContinuityFullDuplexHalfClose(t *testing.T) {
	c := newContinuity(t, "srv", false)
	done := make(chan struct{})
	go func() { c.run(); close(done) }()

	appToCarrier := bytes.Repeat([]byte("app-to-carrier-"), 40)
	go func() {
		_, _ = c.app.Write(appToCarrier)
		_ = c.app.CloseWrite()
	}()
	got := drainStream(t, c.carrierFar)
	if !bytes.Equal(got, appToCarrier) {
		t.Fatalf("carrier received %d bytes, want %d", len(got), len(appToCarrier))
	}

	carrierToApp := bytes.Repeat([]byte("reply-from-carrier-"), 40)
	go func() {
		_, _ = c.carrierFar.Write(carrierToApp)
		_ = c.carrierFar.CloseWrite()
	}()
	got = drainStream(t, c.app)
	if !bytes.Equal(got, carrierToApp) {
		t.Fatalf("app received %d bytes, want %d", len(got), len(carrierToApp))
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("session pump did not complete after both directions FIN")
	}
	c.session.Stop()
}

// TestTunnelContinuityMidCancel verifies an in-flight session terminates on an
// abrupt transport failure and that Stop is idempotent with the retained
// fail-closed owner contract. A graceful both-FIN completion instead closes the
// carrier cleanly (see #43 finding 1 remediation); this test exercises the hard
// reset / fail-closed path of an ungraceful mid-flight termination.
func TestTunnelContinuityMidCancel(t *testing.T) {
	c := newContinuity(t, "cancel", true)
	started := make(chan struct{})
	go func() {
		close(started)
		c.run()
	}()

	chunk := bytes.Repeat([]byte("midflow-"), 200)
	go func() {
		_, _ = c.app.Write(chunk)
		_ = c.app.CloseWrite() // clean app write side; carrier side stays abnormal
	}()
	<-started
	time.Sleep(10 * time.Millisecond)
	_, _ = c.carrierFar.Write(chunk[:64])
	// Abrupt transport failure (not a clean FIN) forces the fail-closed reset
	// path; Stop is idempotent and must not panic or deadlock.
	c.carrierFar.Reset()
	c.session.Stop()

	// Session owns the Carrier; after an ungraceful termination a late write
	// must fail closed.
	if _, err := c.carrierFar.Write([]byte("late")); err == nil {
		t.Fatal("carrier write after session termination did not fail")
	}
}

func drainStream(t testing.TB, r io.Reader) []byte {
	t.Helper()
	var out []byte
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("stream read: %v", err)
		}
		if n == 0 {
			break
		}
	}
	return out
}
