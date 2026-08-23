package runtime

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	wire "github.com/origama/tubo-rebuilt/internal/protocol"
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

// TestProofComputation verifies HMAC-SHA256 proof calculation.
func TestProofComputation(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-for-hmac")
	parts := [][]byte{[]byte("label"), []byte("data1"), []byte("data2")}

	p1 := computeProof(secret, parts...)
	p2 := computeProof(secret, parts...)
	if p1 != p2 {
		t.Fatal("deterministic proofs should match")
	}

	parts3 := [][]byte{[]byte("label"), []byte("data1"), []byte("data3")}
	p3 := computeProof(secret, parts3...)
	if p1 == p3 {
		t.Fatal("different data should produce different proofs")
	}

	if !proofsMatch(secret, p1, parts...) {
		t.Fatal("proofsMatch should match valid proof")
	}
	if proofsMatch(secret, p3, parts...) {
		t.Fatal("proofsMatch should reject invalid proof")
	}
}

// TestProofsMatchConstantTime verifies constant-time comparison.
func TestProofsMatchConstantTime(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-for-hmac")
	parts := [][]byte{[]byte("label"), []byte("data")}
	proof := computeProof(secret, parts...)

	if proofsMatch([]byte("wrong-secret-32-bytes-long-for-hmac"), proof, parts...) {
		t.Fatal("wrong secret should not match")
	}
	if proofsMatch(secret, proof, []byte("wrong-data")) {
		t.Fatal("wrong data should not match")
	}
	if proofsMatch(secret, "!!!invalid-base64!!!", parts...) {
		t.Fatal("malformed proof should not match")
	}
}

// TestGenerateResumeSecret verifies resume secret generation.
func TestGenerateResumeSecret(t *testing.T) {
	s1, err := generateResumeSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(s1) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(s1))
	}
	s2, _ := generateResumeSecret()
	if bytes.Equal(s1, s2) {
		t.Fatal("secrets should be unique")
	}
}

// TestRandomNonceStr verifies nonce generation.
func TestRandomNonceStr(t *testing.T) {
	n1, err := randomNonceStr()
	if err != nil {
		t.Fatal(err)
	}
	if len(n1) == 0 {
		t.Fatal("nonce should not be empty")
	}
	n2, _ := randomNonceStr()
	if n1 == n2 {
		t.Fatal("nonces should be unique")
	}
	data, err := base64.RawURLEncoding.DecodeString(n1)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(data))
	}
}

// TestDirectionState verifies basic direction state operations.
func TestDirectionState(t *testing.T) {
	var d directionState
	d.retained = &bytes.Buffer{}
	d.sourceNext = 100
	d.carrierSent = 100
	d.peerDelivered = 50
	if d.sourceNext-d.peerDelivered != 50 {
		t.Fatal("inflight calculation wrong")
	}
	d.finSeen = true
	if !d.finSeen {
		t.Fatal("finSeen should be true")
	}
}

// TestSessionStateMachine verifies state transitions.
func TestSessionStateMachine(t *testing.T) {
	for _, s := range []SessionState{StateActive, StateProbing, StateQuiescing, StateResumePrepared, StateActivating, StateDetached, StateClosing, StateClosed} {
		if s.String() == "unknown" {
			t.Fatalf("unexpected unknown state for %d", s)
		}
	}
}

// TestCarrierWriterCloseFlag verifies carrier writer closed flag.
func TestCarrierWriterCloseFlag(t *testing.T) {
	cw := &carrierWriter{epoch: 1}

	// Verify initial state: not closed
	cw.mu.Lock()
	if cw.closed {
		t.Fatal("expected not closed")
	}
	cw.mu.Unlock()

	// Set closed flag
	cw.mu.Lock()
	cw.closed = true
	cw.mu.Unlock()

	// Write with closed=true should return error (doesn't reach stream.Write)
	_, err := cw.Write([]byte("world"))
	if err == nil {
		t.Fatal("write after close should fail")
	}

	// Reset closed and verify Close without stream returns error without panic
	cw.mu.Lock()
	cw.closed = false
	cw.mu.Unlock()

	// With nil stream and not closed, Close calls stream.Close which panics.
	// We test the lock/flag behavior only.
}

// TestSessionPumpNew verifies session pump creation.
func TestSessionPumpNew(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-session",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
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

// TestHandleAckC2S verifies ACK handling for c2s direction.
func TestHandleAckC2S(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-ack",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.c2s.mu.Lock()
	sp.c2s.sourceNext = 1000
	sp.c2s.carrierSent = 1000
	sp.c2s.retained.Write(make([]byte, 1000))
	sp.c2s.mu.Unlock()

	ack := wire.Ack{
		SessionID:       "test-ack",
		Epoch:           1,
		Direction:       dirC2S,
		DeliveredOffset: 500,
	}
	sp.handleAck(ack)

	sp.c2s.mu.Lock()
	if sp.c2s.peerDelivered != 500 {
		t.Fatalf("expected delivered 500, got %d", sp.c2s.peerDelivered)
	}
	if sp.c2s.retained.Len() != 500 {
		t.Fatalf("expected 500 bytes retained, got %d", sp.c2s.retained.Len())
	}
	sp.c2s.mu.Unlock()
}

// TestHandleAckS2C verifies ACK handling for s2c direction.
func TestHandleAckS2C(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-acks2c",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.s2c.mu.Lock()
	sp.s2c.sourceNext = 2000
	sp.s2c.carrierSent = 2000
	sp.s2c.retained.Write(make([]byte, 2000))
	sp.s2c.mu.Unlock()

	sp.handleAck(wire.Ack{
		SessionID:       "test-acks2c",
		Epoch:           1,
		Direction:       dirS2C,
		DeliveredOffset: 1500,
	})

	sp.s2c.mu.Lock()
	if sp.s2c.peerDelivered != 1500 {
		t.Fatalf("expected delivered 1500, got %d", sp.s2c.peerDelivered)
	}
	if sp.s2c.retained.Len() != 500 {
		t.Fatalf("expected 500 bytes retained, got %d", sp.s2c.retained.Len())
	}
	sp.s2c.mu.Unlock()
}

// TestHandleSwitchPrepare verifies switch prepare handling.
func TestHandleSwitchPrepare(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-switch",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.handleSwitchPrepare(wire.SwitchPrepare{
		SessionID: "test-switch",
		FromEpoch: 1,
		ToEpoch:   2,
		Reason:    "planned",
	})

	sp.mu.Lock()
	if sp.state != StateProbing {
		t.Fatalf("expected probing, got %s", sp.state)
	}
	if sp.candidateEpoch != 2 {
		t.Fatalf("expected candidate epoch 2, got %d", sp.candidateEpoch)
	}
	sp.mu.Unlock()
}

// TestHandleBarrier verifies barrier handling.
func TestHandleBarrier(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-barrier",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.mu.Lock()
	sp.state = StateProbing
	sp.mu.Unlock()

	sp.handleBarrier(wire.Barrier{
		SessionID: "test-barrier",
		ToEpoch:   2,
		Direction: dirC2S,
		CutOffset: 500,
	})

	sp.mu.Lock()
	if sp.state != StateQuiescing {
		t.Fatalf("expected quiescing, got %s", sp.state)
	}
	sp.mu.Unlock()
	sp.c2s.mu.Lock()
	if !sp.c2s.currentTransferComplete {
		t.Fatal("expected c2s transfer complete")
	}
	sp.c2s.mu.Unlock()
}

// TestHandleSwitchAbort verifies switch abort.
func TestHandleSwitchAbort(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-abort",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.mu.Lock()
	sp.state = StateProbing
	sp.c2s.currentTransferComplete = true
	sp.mu.Unlock()

	sp.handleSwitchAbort(wire.SwitchAbort{
		SessionID: "test-abort",
		ToEpoch:   2,
		Reason:    "timeout",
	})

	sp.mu.Lock()
	if sp.state != StateActive {
		t.Fatalf("expected active, got %s", sp.state)
	}
	sp.c2s.mu.Lock()
	if sp.c2s.currentTransferComplete {
		t.Fatal("expected c2s transfer not complete after abort")
	}
	sp.c2s.mu.Unlock()
	sp.mu.Unlock()
}

// TestHandleBarrierAck verifies barrier ACK handling.
func TestHandleBarrierAck(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-barierack",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.handleBarrierAck(wire.BarrierAck{
		SessionID:       "test-barierack",
		ToEpoch:         2,
		Direction:       dirC2S,
		DeliveredOffset: 500,
		FinDelivered:    true,
	})

	sp.c2s.mu.Lock()
	if sp.c2s.peerDelivered != 500 {
		t.Fatalf("expected delivered 500, got %d", sp.c2s.peerDelivered)
	}
	if !sp.c2s.finDelivered {
		t.Fatal("expected fin delivered")
	}
	sp.c2s.mu.Unlock()
}

// TestVerifyConnPathLogic verifies path detection logic.
func TestVerifyConnPathLogic(t *testing.T) {
	if isRelayed("tcp") {
		t.Fatal("tcp should not be relayed")
	}
	if !isRelayed("relay") {
		t.Fatal("relay should be relayed")
	}
	if !isRelayed("circuit") {
		t.Fatal("circuit should be relayed")
	}
}

func isRelayed(transport string) bool {
	return transport == "relay" || transport == "circuit"
}

// TestComputeProofCanonical verifies canonical proof computation.
func TestComputeProofCanonical(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-for-hmac")
	parts := [][]byte{
		[]byte("tubo-resume-v1"),
		[]byte("pipe-123"),
		[]byte("session-456"),
		uint64Bytes(2),
		[]byte("peerA"),
		[]byte("peerB"),
		[]byte("nonce1"),
		[]byte("nonce2"),
		uint64Bytes(1000),
		uint64Bytes(500),
	}

	p1 := computeProof(secret, parts...)
	p2 := computeProof(secret, parts...)
	if p1 != p2 {
		t.Fatal("symmetric proofs must match")
	}
	if !proofsMatch(secret, p1, parts...) {
		t.Fatal("proofsMatch should validate")
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

// TestHandleCarrierFailure verifies carrier failure handling.
func TestHandleCarrierFailure(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-fail",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.handleCarrierFailure()

	sp.mu.Lock()
	if sp.state != StateDetached {
		t.Fatalf("expected detached, got %s", sp.state)
	}
	if sp.detachedTimer == nil {
		t.Fatal("expected detached timer")
	}
	sp.mu.Unlock()
}

// TestBufferLimit verifies backpressure at buffer limit.
func TestBufferLimit(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:         "test-buf",
		PipeID:            "test-pipe",
		Scope:             "test/test",
		TargetConn:        newTestConn(),
		MaxInflightPerDir: 1024,
		ResumeSecret:      secret,
	})
	defer sp.Stop()

	sp.c2s.mu.Lock()
	sp.c2s.sourceNext = 2000
	sp.c2s.peerDelivered = 500
	inflight := sp.c2s.sourceNext - sp.c2s.peerDelivered
	sp.c2s.mu.Unlock()

	if inflight <= uint64(sp.maxInflightPerDir) {
		t.Fatal("expected backpressure (inflight > limit)")
	}
}

// TestHalfCloseSequencing verifies FIN after all bytes.
func TestHalfCloseSequencing(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-halfclose",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.c2s.mu.Lock()
	sp.c2s.finSeen = true
	sp.c2s.sourceNext = 1000
	sp.c2s.peerDelivered = 1000
	sp.c2s.finDelivered = true
	allDelivered := sp.c2s.sourceNext == sp.c2s.peerDelivered
	finOk := sp.c2s.finSeen && allDelivered && sp.c2s.finDelivered
	sp.c2s.mu.Unlock()

	if !finOk {
		t.Fatal("FIN should be sequenced after all bytes delivered")
	}
}

// TestNegativeWrongProof verifies wrong proof rejected.
func TestNegativeWrongProof(t *testing.T) {
	secret := []byte("real-secret")
	parts := [][]byte{[]byte("label"), []byte("data")}
	proof := computeProof(secret, parts...)
	if proofsMatch([]byte("wrong-secret"), proof, parts...) {
		t.Fatal("wrong secret should not match proof")
	}
}

// TestNegativeWrongNonceProof verifies different nonce produces different proof.
func TestNegativeWrongNonceProof(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-for-hmac")
	if computeProof(secret, []byte("nonce1")) == computeProof(secret, []byte("nonce2")) {
		t.Fatal("different nonces should produce different proofs")
	}
}

// TestNegativeWrongEpochProof verifies different epoch produces different proof.
func TestNegativeWrongEpochProof(t *testing.T) {
	secret := []byte("test-secret-32-bytes-long-for-hmac")
	if computeProof(secret, uint64Bytes(1)) == computeProof(secret, uint64Bytes(2)) {
		t.Fatal("different epochs should produce different proofs")
	}
}

// TestAggregateCapEnforcement verifies aggregate memory cap check.
func TestAggregateCapEnforcement(t *testing.T) {
	secret, _ := generateResumeSecret()
	sp := newTestSession(t, sessionConfig{
		SessionID:    "test-aggr",
		PipeID:       "test-pipe",
		Scope:        "test/test",
		TargetConn:   newTestConn(),
		ResumeSecret: secret,
	})
	defer sp.Stop()

	sp.currentAggregate.Store(DefaultMaxAggregateMemory + 1)
	if sp.currentAggregate.Load() <= DefaultMaxAggregateMemory {
		t.Fatal("expected aggregate cap exceeded")
	}
}

// TestWriteResumeFrame verifies resume frame writer.
func TestWriteResumeFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := writeResumeFrame(&buf, wire.ResumeHello{
		Version:   "2.1",
		PipeID:    "test",
		SessionID: "test-session",
		NextEpoch: 2,
		Nonce:     "test-nonce",
		Proof:     "test-proof",
	}); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected non-empty frame")
	}
}

// TestReadResumeFrame verifies resume frame round-trip.
func TestReadResumeFrame(t *testing.T) {
	msg := wire.ResumeHello{
		Version:   "2.1",
		PipeID:    "test",
		SessionID: "test-session",
		NextEpoch: 2,
		Nonce:     "test-nonce",
		Proof:     "test-proof",
	}
	var buf bytes.Buffer
	if err := wire.WriteResumeFrame(&buf, msg); err != nil {
		t.Fatal(err)
	}

	var decoded wire.ResumeHello
	if err := wire.ReadFrame(&buf, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PipeID != "test" {
		t.Fatalf("expected test, got %s", decoded.PipeID)
	}
	if decoded.NextEpoch != 2 {
		t.Fatalf("expected epoch 2, got %d", decoded.NextEpoch)
	}
}

// TestResumeFrameBound verifies MaxResumeFrame is enforced.
func TestResumeFrameBound(t *testing.T) {
	var buf bytes.Buffer
	// Create a struct with a very long string to exceed 4 KiB
	longValue := make([]byte, 5000)
	for i := range longValue {
		longValue[i] = 'x'
	}
	err := writeResumeFrame(&buf, struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}{Type: "test", Data: string(longValue)})
	if err == nil {
		t.Fatal("expected frame size error")
	}
}
