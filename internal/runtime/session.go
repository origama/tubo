// Package runtime manages pipe session lifecycle and live path migration.
//
// sessionPump implements a bounded, epoch-fenced, receiver-committed byte
// transport that replaces the uncoordinated copyDuplex. It supports:
//   - continuous ACKs with retained replay window
//   - planned switch barrier (relay <-> direct)
//   - abrupt carrier recovery with epoch + 1 relay fallback
//   - authenticated resume handshake with HMAC-SHA256 proof
//   - sequenced half-close (FIN after final bytes delivered)
//   - bounded 1 MiB per direction, 64 MiB aggregate
//   - one active carrier per epoch (epoch fencing)
package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
)

const (
	// DefaultMaxInflightPerDir is the default unacked byte buffer per direction.
	DefaultMaxInflightPerDir = 1 << 20 // 1 MiB
	// DefaultMaxAggregateMemory is the default aggregate session memory limit.
	DefaultMaxAggregateMemory = 64 << 20 // 64 MiB
	// DefaultAckBytes triggers an ACK every N bytes received.
	DefaultAckBytes = 64 << 10 // 64 KiB
	// DefaultAckInterval is the maximum interval between ACKs.
	DefaultAckInterval = 25 * time.Millisecond
	// DefaultDetachedGrace is how long a session lives after carrier loss.
	DefaultDetachedGrace = 10 * time.Second
	// DefaultResumeHandshakeTimeout is the max time for a resume handshake.
	DefaultResumeHandshakeTimeout = 10 * time.Second
	// DefaultMaxControlFrameSize is the max size of a control/resume message.
	DefaultMaxControlFrameSize = 4 << 10 // 4 KiB
	// DefaultDirectDialTimeout is the timeout for DCUtR direct dial.
	DefaultDirectDialTimeout = 5 * time.Second
)

// directionLabel is used for proof calculation.
const (
	dirC2S = "c2s"
	dirS2C = "s2c"
)

// SessionState represents the state machine for a session pump.
type SessionState int

const (
	StateActive SessionState = iota
	StateProbing
	StateQuiescing
	StateResumePrepared
	StateActivating
	StateDetached
	StateClosing
	StateClosed
)

func (s SessionState) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateProbing:
		return "probing"
	case StateQuiescing:
		return "quiescing"
	case StateResumePrepared:
		return "resume_prepared"
	case StateActivating:
		return "activating"
	case StateDetached:
		return "detached"
	case StateClosing:
		return "closing"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// directionState tracks per-direction byte state.
type directionState struct {
	mu sync.Mutex

	// source_next is the next byte offset to read from the application source.
	sourceNext uint64
	// carrierSent is the highest offset submitted to the current carrier.
	carrierSent uint64
	// peerDelivered is the highest offset the peer has committed to its app.
	peerDelivered uint64
	// retained is the buffer of bytes [peerDelivered, sourceNext).
	retained *bytes.Buffer
	// finSeen is true when the source has signaled end of data.
	finSeen bool
	// finDelivered is true when the source FIN has been delivered to the peer.
	finDelivered bool
	// currentTransferComplete marks the end of a planned barrier transfer.
	currentTransferComplete bool
	// oldEOFDrained is true when old carrier has been drained during quiesce.
	oldEOFDrained bool
}

// carrierWriter wraps a carrier stream with epoch fencing.
type carrierWriter struct {
	mu     sync.Mutex
	stream core.Stream
	epoch  uint64
	closed bool
}

func (cw *carrierWriter) Write(p []byte) (int, error) {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.closed {
		return 0, errors.New("carrier closed")
	}
	return cw.stream.Write(p)
}

func (cw *carrierWriter) Close() error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.closed {
		return nil
	}
	cw.closed = true
	return cw.stream.Close()
}

func (cw *carrierWriter) CloseWrite() error {
	cw.mu.Lock()
	defer cw.mu.Unlock()
	if cw.closed {
		return nil
	}
	cw.closed = true
	return cw.stream.CloseWrite()
}

// sessionPump is the core session state machine.
type sessionPump struct {
	// Immutable identity
	SessionID    string
	PipeID       string
	Scope        string
	ClientPeerID peer.ID
	ServerPeerID peer.ID
	Host         host.Host
	State        *store.Store

	// Resume secret (memory-only, never persisted)
	resumeSecret []byte // 32 bytes

	// Epoch tracking
	mu            sync.Mutex
	epoch         uint64
	state         SessionState
	activeCarrier core.Stream // current active carrier (raw duplex)
	controlStream core.Stream // control stream for ACKs/barriers
	controlSeq    uint64

	// Per-direction state
	c2s directionState
	s2c directionState

	// Candidate carrier for resume
	candidateStream core.Stream
	candidateEpoch  uint64
	candidateNonce  string
	candidateServer string // server nonce for handshake

	// Path tracking
	fromPath string
	toPath   string

	// Target connection (the local application endpoint)
	targetConn io.ReadWriteCloser

	// Lifecycle
	ctx           context.Context
	cancel        context.CancelFunc
	carrierCtx    context.Context
	carrierCancel context.CancelFunc
	detachedTimer *time.Timer

	// ACK ticker
	ackTicker *time.Ticker

	// Inflight limits
	maxInflightPerDir int
	maxAggregate      int64
	currentAggregate  atomic.Int64

	// Resume registry reference
	registry *sessionRegistry

	// Role: true for client (initiator), false for server (responder)
	isClient bool
}

// sessionRegistry is a map of sessionID -> *sessionPump for resume dispatch.
type sessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*sessionPump
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[string]*sessionPump)}
}

func (r *sessionRegistry) get(id string) *sessionPump {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sessions[id]
}

func (r *sessionRegistry) put(id string, p *sessionPump) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[id] = p
}

func (r *sessionRegistry) remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, id)
}

// instanceRegistry holds the per-pipe-runtime registry for resume dispatch.
// Set by the pipe runtime before handling resume streams.
var instanceRegistry = newSessionRegistry()

// SetInstanceRegistry sets the global instance registry for resume dispatch.
func SetInstanceRegistry(reg *sessionRegistry) {
	instanceRegistry = reg
}

// sessionConfig holds configuration for a new session pump.
type sessionConfig struct {
	SessionID         string
	PipeID            string
	Scope             string
	ClientPeerID      peer.ID
	ServerPeerID      peer.ID
	Host              host.Host
	State             *store.Store
	TargetConn        io.ReadWriteCloser
	MaxInflightPerDir int
	InitialEpoch      uint64
	FromPath          string
	ToPath            string
	ResumeSecret      []byte
}

func newSessionPump(cfg sessionConfig) *sessionPump {
	ctx, cancel := context.WithCancel(context.Background())
	if cfg.MaxInflightPerDir <= 0 {
		cfg.MaxInflightPerDir = DefaultMaxInflightPerDir
	}
	if cfg.InitialEpoch == 0 {
		cfg.InitialEpoch = 1
	}
	if cfg.FromPath == "" {
		cfg.FromPath = "relayed"
	}
	if cfg.ToPath == "" {
		cfg.ToPath = "relayed"
	}
	carrierCtx, carrierCancel := context.WithCancel(ctx)
	sp := &sessionPump{
		SessionID:         cfg.SessionID,
		PipeID:            cfg.PipeID,
		Scope:             cfg.Scope,
		ClientPeerID:      cfg.ClientPeerID,
		ServerPeerID:      cfg.ServerPeerID,
		Host:              cfg.Host,
		State:             cfg.State,
		targetConn:        cfg.TargetConn,
		resumeSecret:      cfg.ResumeSecret,
		epoch:             cfg.InitialEpoch,
		state:             StateActive,
		fromPath:          cfg.FromPath,
		toPath:            cfg.ToPath,
		maxInflightPerDir: cfg.MaxInflightPerDir,
		ctx:               ctx,
		cancel:            cancel,
		carrierCtx:        carrierCtx,
		carrierCancel:     carrierCancel,
		ackTicker:         time.NewTicker(DefaultAckInterval),
		isClient:          cfg.Host != nil, // client has a host for outbound streams
	}
	sp.c2s.retained = &bytes.Buffer{}
	sp.s2c.retained = &bytes.Buffer{}
	sp.registry = instanceRegistry
	sp.registry.put(cfg.SessionID, sp)
	return sp
}

// SetActiveCarrier sets the raw duplex carrier stream for this session.
func (sp *sessionPump) SetActiveCarrier(stream core.Stream) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.activeCarrier = stream
}

// SetControlStream sets the control stream for ACKs/barriers.
func (sp *sessionPump) SetControlStream(stream core.Stream) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	sp.controlStream = stream
}

// Stop terminates the session pump and releases resources.
func (sp *sessionPump) Stop() {
	sp.mu.Lock()
	if sp.state == StateClosed {
		sp.mu.Unlock()
		return
	}
	sp.state = StateClosed
	sp.mu.Unlock()

	sp.cancel()
	sp.ackTicker.Stop()

	sp.mu.Lock()
	if sp.detachedTimer != nil {
		sp.detachedTimer.Stop()
		sp.detachedTimer = nil
	}
	if sp.carrierCancel != nil {
		sp.carrierCancel()
	}
	sp.mu.Unlock()

	sp.registry.remove(sp.SessionID)
}

// generateResumeSecret creates a random 32-byte resume secret.
func generateResumeSecret() ([]byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	return secret, nil
}

// randomNonceStr generates a random 32-byte nonce as base64.
func randomNonceStr() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// writeResumeFrame writes a frame bounded by MaxResumeFrame (4 KiB).
func writeResumeFrame(w io.Writer, v any) error {
	return wire.WriteResumeFrame(w, v)
}

// computeProof calculates HMAC-SHA256 proof and returns base64 string.
func computeProof(secret []byte, parts ...[]byte) string {
	mac := hmac.New(sha256.New, secret)
	for _, p := range parts {
		mac.Write(p)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// computeProofRaw returns the raw HMAC-SHA256 digest for constant-time comparison.
func computeProofRaw(secret []byte, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, secret)
	for _, p := range parts {
		mac.Write(p)
	}
	return mac.Sum(nil)
}

// proofsMatch compares proof strings using constant-time HMAC comparison.
func proofsMatch(secret []byte, expectedProof string, parts ...[]byte) bool {
	expectedRaw, err := base64.RawURLEncoding.DecodeString(expectedProof)
	if err != nil {
		return false
	}
	actualRaw := computeProofRaw(secret, parts...)
	return hmac.Equal(expectedRaw, actualRaw)
}

// uint64Bytes encodes a uint64 as big-endian bytes.
func uint64Bytes(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// uint64Str returns canonical big-endian bytes for a uint64.
func uint64Str(v uint64) string {
	return string(uint64Bytes(v))
}

// canonicalProofParts builds the canonical byte sequence for proof calculation.
func (sp *sessionPump) canonicalProofParts(epoch uint64, clientNonce, serverNonce string, extra ...string) [][]byte {
	parts := [][]byte{
		[]byte("tubo-resume-v1"),
		[]byte(sp.PipeID),
		[]byte(sp.SessionID),
		uint64Bytes(epoch),
		[]byte(sp.ClientPeerID.String()),
		[]byte(sp.ServerPeerID.String()),
		[]byte(clientNonce),
		[]byte(serverNonce),
	}
	for _, e := range extra {
		parts = append(parts, []byte(e))
	}
	return parts
}

// proofForHello computes the proof for a ResumeHello message.
func (sp *sessionPump) proofForHello(nextEpoch uint64, nonce string) string {
	parts := sp.canonicalProofParts(nextEpoch, nonce, "")
	return computeProof(sp.resumeSecret, parts...)
}

// proofForChallenge computes the proof for a ResumeChallenge message.
func (sp *sessionPump) proofForChallenge(nextEpoch uint64, clientNonce, serverNonce string) string {
	parts := sp.canonicalProofParts(nextEpoch, clientNonce, serverNonce)
	return computeProof(sp.resumeSecret, parts...)
}

// proofForCommit computes the proof for a ResumeCommit message.
func (sp *sessionPump) proofForCommit(nextEpoch uint64, clientNonce, serverNonce string, c2sOldSent, s2cDelivered uint64, c2sFinSeen, s2cFinDelivered bool) string {
	extra := []string{uint64Str(c2sOldSent), uint64Str(s2cDelivered)}
	if c2sFinSeen {
		extra = append(extra, "fin")
	}
	if s2cFinDelivered {
		extra = append(extra, "fin_delivered")
	}
	parts := sp.canonicalProofParts(nextEpoch, clientNonce, serverNonce, extra...)
	return computeProof(sp.resumeSecret, parts...)
}

// proofForResult computes the proof for a ResumeResult message.
func (sp *sessionPump) proofForResult(epoch uint64, clientNonce, serverNonce string, c2sDelivered, s2cOldSent, c2sResumeAt, s2cResumeAt uint64, c2sFinDelivered, s2cFinSeen bool) string {
	extra := []string{
		uint64Str(c2sDelivered), uint64Str(s2cOldSent),
		uint64Str(c2sResumeAt), uint64Str(s2cResumeAt),
	}
	if c2sFinDelivered {
		extra = append(extra, "fin_delivered")
	}
	if s2cFinSeen {
		extra = append(extra, "fin_seen")
	}
	parts := sp.canonicalProofParts(epoch, clientNonce, serverNonce, extra...)
	return computeProof(sp.resumeSecret, parts...)
}

// proofForActivate computes the proof for a ResumeActivate/ResumeActivated message.
func (sp *sessionPump) proofForActivate(epoch uint64, clientNonce, serverNonce, transcriptDigest string) string {
	parts := sp.canonicalProofParts(epoch, clientNonce, serverNonce, transcriptDigest)
	return computeProof(sp.resumeSecret, parts...)
}

// pumpCarrier spawns the carrier goroutines. It uses sp.activeCarrier directly
// from the struct so that carrier swaps take effect on the next call.
func (sp *sessionPump) pumpCarrier() {
	sp.mu.Lock()
	carrier := sp.activeCarrier
	sp.mu.Unlock()
	if carrier == nil {
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		sp.readFromCarrier(carrier, sp.targetConn, &sp.c2s)
	}()

	go func() {
		defer wg.Done()
		sp.readFromSource(sp.targetConn, carrier, &sp.s2c, &sp.c2s)
	}()

	wg.Wait()
}

// runServerSession starts the session pump for the server side.
func (sp *sessionPump) runServerSession() {
	defer sp.Stop()

	go sp.sendAcks()
	sp.pumpCarrier()
}

// runClientSession starts the session pump for the client side.
func (sp *sessionPump) runClientSession() {
	defer sp.Stop()

	go sp.sendAcks()

	sp.mu.Lock()
	carrier := sp.activeCarrier
	sp.mu.Unlock()

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		sp.readFromSource(sp.targetConn, carrier, &sp.c2s, &sp.s2c)
	}()

	go func() {
		defer wg.Done()
		sp.readFromCarrier(carrier, sp.targetConn, &sp.s2c)
	}()

	wg.Wait()
}

// readFromCarrier reads from the carrier stream and writes to the local endpoint.
// It checks the carrier context to detect carrier swaps.
func (sp *sessionPump) readFromCarrier(carrier io.Reader, local io.Writer, dir *directionState) {
	buf := make([]byte, 32<<10)
	for {
		select {
		case <-sp.carrierCtx.Done():
			// Carrier was swapped; this goroutine is stale
			return
		default:
		}

		sp.mu.Lock()
		epoch := sp.epoch
		state := sp.state
		sp.mu.Unlock()

		if state == StateClosed {
			return
		}

		n, err := carrier.Read(buf)
		if n > 0 {
			// Epoch fence: discard if epoch changed (late old-carrier bytes)
			sp.mu.Lock()
			currentEpoch := sp.epoch
			sp.mu.Unlock()
			if epoch != currentEpoch {
				continue
			}

			// Write to local endpoint
			written := 0
			for written < n {
				w, werr := local.Write(buf[written:n])
				written += w
				if werr != nil && written < n {
					sp.mu.Lock()
					sp.state = StateClosing
					sp.mu.Unlock()
					sp.Stop()
					return
				}
				if werr != nil {
					break
				}
			}

			dir.mu.Lock()
			dir.peerDelivered += uint64(written)
			dir.mu.Unlock()
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				sp.mu.Lock()
				state := sp.state
				sp.mu.Unlock()
				if state == StateQuiescing || state == StateResumePrepared {
					dir.mu.Lock()
					dir.oldEOFDrained = true
					dir.mu.Unlock()
				}
				return
			}
			sp.handleCarrierFailure()
			return
		}
	}
}

// readFromSource reads from the local source and writes to the carrier.
func (sp *sessionPump) readFromSource(source io.Reader, carrier io.Writer, dir *directionState, otherDir *directionState) {
	buf := make([]byte, 32<<10)
	for {
		select {
		case <-sp.carrierCtx.Done():
			return
		default:
		}

		sp.mu.Lock()
		if sp.state == StateClosed || sp.state == StateClosing {
			sp.mu.Unlock()
			return
		}
		sp.mu.Unlock()

		dir.mu.Lock()
		if dir.currentTransferComplete {
			dir.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			continue
		}
		dir.mu.Unlock()

		// Aggregate cap check
		if sp.currentAggregate.Load() > DefaultMaxAggregateMemory {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		n, err := source.Read(buf)
		if n > 0 {
			dir.mu.Lock()
			if dir.sourceNext+uint64(n) > dir.peerDelivered+uint64(sp.maxInflightPerDir) {
				dir.mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				continue
			}

			dir.retained.Write(buf[:n])
			sp.currentAggregate.Add(int64(n))
			dir.sourceNext += uint64(n)
			dir.carrierSent += uint64(n)
			dir.mu.Unlock()

			written := 0
			for written < n {
				w, werr := carrier.Write(buf[written:n])
				written += w
				if werr != nil {
					sp.handleCarrierFailure()
					return
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				dir.mu.Lock()
				dir.finSeen = true
				dir.mu.Unlock()
				if cw, ok := carrier.(closeWriter); ok {
					_ = cw.CloseWrite()
				}
				return
			}
			sp.mu.Lock()
			sp.state = StateClosing
			sp.mu.Unlock()
			sp.Stop()
			return
		}
	}
}

// sendAcks periodically sends ACKs for received data.
// Client sends ACKs for s2c direction (data received from server).
// Server sends ACKs for c2s direction (data received from client).
func (sp *sessionPump) sendAcks() {
	// Determine which direction we ACK based on role
	ackDir := dirC2S // server acks c2s
	if sp.isClient {
		ackDir = dirS2C // client acks s2c
	}

	var dir *directionState
	if ackDir == dirC2S {
		dir = &sp.c2s
	} else {
		dir = &sp.s2c
	}

	for {
		select {
		case <-sp.ctx.Done():
			return
		case <-sp.ackTicker.C:
			sp.mu.Lock()
			cs := sp.controlStream
			epoch := sp.epoch
			sp.mu.Unlock()

			if cs == nil {
				continue
			}

			dir.mu.Lock()
			delivered := dir.peerDelivered
			finDelivered := dir.finDelivered
			dir.mu.Unlock()

			sp.mu.Lock()
			sp.controlSeq++
			seq := sp.controlSeq
			sp.mu.Unlock()

			type ackWire struct {
				Type string `json:"type"`
				wire.Ack
			}
			ack := ackWire{
				Type: "ack",
				Ack: wire.Ack{
					SessionID:       sp.SessionID,
					Epoch:           epoch,
					ControlSeq:      seq,
					Direction:       ackDir,
					DeliveredOffset: delivered,
					FinDelivered:    finDelivered,
					Proof:           sp.computeAckProof(epoch, ackDir, delivered, finDelivered),
				},
			}

			_ = writeResumeFrame(cs, ack)
		}
	}
}

func (sp *sessionPump) computeAckProof(epoch uint64, direction string, delivered uint64, finDelivered bool) string {
	parts := [][]byte{
		[]byte("tubo-resume-v1"),
		[]byte(sp.SessionID),
		uint64Bytes(epoch),
		[]byte(direction),
		uint64Bytes(delivered),
	}
	if finDelivered {
		parts = append(parts, []byte("fin"))
	}
	return computeProof(sp.resumeSecret, parts...)
}

// handleCarrierFailure initiates recovery or closes the session.
func (sp *sessionPump) handleCarrierFailure() {
	sp.mu.Lock()
	defer sp.mu.Unlock()

	if sp.state == StateClosed || sp.state == StateClosing {
		return
	}

	sp.state = StateDetached

	// Start detached grace timer
	if sp.detachedTimer != nil {
		sp.detachedTimer.Stop()
	}
	sp.detachedTimer = time.AfterFunc(DefaultDetachedGrace, func() {
		sp.mu.Lock()
		if sp.state == StateDetached {
			sp.state = StateClosing
			sp.mu.Unlock()
			sp.Stop()
		} else {
			sp.mu.Unlock()
		}
	})
}

// handleResumeStream handles an incoming resume candidate stream.
func (sp *sessionPump) handleResumeStream(stream core.Stream) {
	defer stream.Close()

	_ = stream.SetDeadline(time.Now().Add(DefaultResumeHandshakeTimeout))

	var hello wire.ResumeHello
	if err := wire.ReadFrame(stream, &hello); err != nil {
		return
	}
	if hello.SessionID != sp.SessionID || hello.PipeID != sp.PipeID {
		return
	}

	sp.mu.Lock()
	expectedEpoch := sp.epoch + 1
	state := sp.state
	sp.mu.Unlock()

	if hello.NextEpoch != expectedEpoch {
		return
	}
	if state != StateDetached && state != StateActive && state != StateProbing {
		return
	}

	// Verify proof with constant-time comparison
	helloParts := sp.canonicalProofParts(hello.NextEpoch, hello.Nonce, "")
	if !proofsMatch(sp.resumeSecret, hello.Proof, helloParts...) {
		return
	}

	serverNonce, err := randomNonceStr()
	if err != nil {
		return
	}

	challenge := wire.ResumeChallenge{
		SessionID: sp.SessionID,
		NextEpoch: hello.NextEpoch,
		Nonce:     serverNonce,
		Proof:     sp.proofForChallenge(hello.NextEpoch, hello.Nonce, serverNonce),
	}
	if err := writeResumeFrame(stream, challenge); err != nil {
		return
	}

	var commit wire.ResumeCommit
	if err := wire.ReadFrame(stream, &commit); err != nil {
		return
	}

	sp.mu.Lock()
	sp.candidateStream = stream
	sp.candidateEpoch = hello.NextEpoch
	sp.candidateNonce = hello.Nonce
	sp.candidateServer = serverNonce
	sp.mu.Unlock()

	// Verify commit proof
	sp.c2s.mu.Lock()
	s2cDelivered := sp.s2c.peerDelivered
	sp.c2s.mu.Unlock()
	sp.s2c.mu.Lock()
	s2cFinDelivered := sp.s2c.finDelivered
	sp.s2c.mu.Unlock()

	// Use the client-reported C2SOldSent for proof, not local carrierSent
	// (server is c2s receiver so its carrierSent is 0; client reported the real value)
	commitParts := sp.canonicalProofParts(hello.NextEpoch, hello.Nonce, serverNonce,
		uint64Str(commit.C2SOldSent), uint64Str(s2cDelivered))
	if commit.C2SFinSeen {
		commitParts = append(commitParts, []byte("fin"))
	}
	if commit.S2CFinDelivered {
		commitParts = append(commitParts, []byte("fin_delivered"))
	}
	if !proofsMatch(sp.resumeSecret, commit.Proof, commitParts...) {
		return
	}

	// Validate offsets
	sp.c2s.mu.Lock()
	c2sLocalDelivered := sp.c2s.peerDelivered
	sp.c2s.mu.Unlock()
	sp.s2c.mu.Lock()
	s2cCarrierSent := sp.s2c.carrierSent
	sp.s2c.mu.Unlock()

	if commit.C2SOldSent < c2sLocalDelivered {
		return
	}
	if commit.S2CDelivered > s2cCarrierSent {
		return
	}
	if commit.C2SFinSeen && !sp.c2s.finSeen {
		return
	}
	if commit.S2CFinDelivered && !s2cFinDelivered {
		return
	}

	sp.c2s.mu.Lock()
	c2sDelivered := sp.c2s.peerDelivered
	sp.c2s.mu.Unlock()
	sp.s2c.mu.Lock()
	s2cOldSent := sp.s2c.carrierSent
	s2cFinSeen := sp.s2c.finSeen
	sp.s2c.mu.Unlock()

	result := wire.ResumeResult{
		OK:              true,
		SessionID:       sp.SessionID,
		Epoch:           hello.NextEpoch,
		C2SDelivered:    c2sDelivered,
		S2COldSent:      s2cOldSent,
		C2SResumeAt:     c2sDelivered,
		S2CResumeAt:     commit.S2CDelivered,
		C2SFinDelivered: sp.c2s.finDelivered,
		S2CFinSeen:      s2cFinSeen,
		Proof:           sp.proofForResult(hello.NextEpoch, hello.Nonce, serverNonce, c2sDelivered, s2cOldSent, c2sDelivered, commit.S2CDelivered, sp.c2s.finDelivered, s2cFinSeen),
	}
	if err := writeResumeFrame(stream, result); err != nil {
		return
	}

	var activate wire.ResumeActivate
	if err := wire.ReadFrame(stream, &activate); err != nil {
		return
	}

	transcriptDigest := fmt.Sprintf("%s:%s:%d:%s:%s", sp.SessionID, hello.Nonce, hello.NextEpoch, serverNonce, "activate")
	activateParts := sp.canonicalProofParts(hello.NextEpoch, hello.Nonce, serverNonce, transcriptDigest)
	if !proofsMatch(sp.resumeSecret, activate.Proof, activateParts...) {
		return
	}

	activated := wire.ResumeActivated{
		SessionID: sp.SessionID,
		Epoch:     hello.NextEpoch,
		Proof:     sp.proofForActivate(hello.NextEpoch, hello.Nonce, serverNonce, transcriptDigest),
	}
	if err := writeResumeFrame(stream, activated); err != nil {
		return
	}

	// Activate: switch to new carrier
	sp.activateResume(stream, hello.NextEpoch, hello.Nonce, serverNonce, c2sDelivered, commit.S2CDelivered)
}

// activateResume installs the new carrier, replays retained bytes, and spawns
// fresh pump goroutines for the new carrier.
func (sp *sessionPump) activateResume(stream core.Stream, newEpoch uint64, clientNonce, serverNonce string, c2sResumeAt, s2cResumeAt uint64) {
	// Cancel old carrier goroutines
	sp.mu.Lock()
	if sp.carrierCancel != nil {
		sp.carrierCancel()
	}
	oldStream := sp.activeCarrier
	sp.activeCarrier = stream
	sp.epoch = newEpoch
	sp.state = StateActive
	// Create new carrier context for the new goroutines
	sp.carrierCtx, sp.carrierCancel = context.WithCancel(sp.ctx)
	sp.mu.Unlock()

	// Close old carrier without propagating FIN
	if oldStream != nil {
		_ = oldStream.Close()
	}

	// Replay retained bytes in c2s direction
	sp.c2s.mu.Lock()
	if sp.c2s.peerDelivered < sp.c2s.sourceNext {
		replay := sp.c2s.retained.Bytes()
		if uint64(len(replay)) > sp.c2s.sourceNext-sp.c2s.peerDelivered {
			replay = replay[:sp.c2s.sourceNext-sp.c2s.peerDelivered]
		}
		if len(replay) > 0 {
			_, _ = stream.Write(replay)
		}
	}
	sp.c2s.mu.Unlock()

	// Spawn fresh pump goroutines for the new carrier
	go sp.pumpCarrier()

	// Record path event
	event := store.PathEvent{
		SessionID:    sp.SessionID,
		Epoch:        newEpoch,
		FromPath:     sp.fromPath,
		ToPath:       sp.toPath,
		Reason:       "recovery",
		C2SCommitted: c2sResumeAt,
		S2CCommitted: s2cResumeAt,
		CommittedAt:  time.Now().UTC(),
	}
	_ = sp.State.PutPathEvent(context.Background(), event)

	_ = sp.State.PutSession(context.Background(), store.Session{
		ID:                   sp.SessionID,
		CurrentTransportPath: sp.toPath,
	})

	runtimeInfof("session resumed epoch=%d session=%s path=%s", newEpoch, sp.SessionID, sp.toPath)
}

// readControlFrame reads a framed control message and decodes it into the
// provided value. Unlike wire.ReadFrame, it does NOT use DisallowUnknownFields
// because control frames carry a "type" field alongside the message fields.
func readControlFrame(r io.Reader, value any) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > wire.MaxResumeFrame {
		return fmt.Errorf("invalid control frame size %d", n)
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return err
	}
	return nil
}

// controlFrameType extracts the "type" field from a raw JSON frame without
// decoding into a concrete struct.
func controlFrameType(r io.Reader) (string, []byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return "", nil, err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > wire.MaxResumeFrame {
		return "", nil, fmt.Errorf("invalid control frame size %d", n)
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(r, data); err != nil {
		return "", nil, err
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return "", nil, err
	}
	return header.Type, data, nil
}

// handleControlStream processes incoming control messages.
func (sp *sessionPump) handleControlStream(stream core.Stream) {
	defer stream.Close()

	var roleDecl wire.StreamRoleDeclaration
	if err := wire.ReadFrame(stream, &roleDecl); err != nil {
		return
	}
	if roleDecl.Role != wire.StreamRoleControl || roleDecl.SessionID != sp.SessionID {
		return
	}

	for {
		msgType, rawData, err := controlFrameType(stream)
		if err != nil {
			return
		}

		switch msgType {
		case "ack":
			var ack wire.Ack
			if err := json.Unmarshal(rawData, &ack); err != nil {
				return
			}
			sp.handleAck(ack)
		case "switch_prepare":
			var prep wire.SwitchPrepare
			if err := json.Unmarshal(rawData, &prep); err != nil {
				return
			}
			sp.handleSwitchPrepare(prep)
		case "barrier":
			var bar wire.Barrier
			if err := json.Unmarshal(rawData, &bar); err != nil {
				return
			}
			sp.handleBarrier(bar)
		case "barrier_ack":
			var back wire.BarrierAck
			if err := json.Unmarshal(rawData, &back); err != nil {
				return
			}
			sp.handleBarrierAck(back)
		case "switch_abort":
			var abort wire.SwitchAbort
			if err := json.Unmarshal(rawData, &abort); err != nil {
				return
			}
			sp.handleSwitchAbort(abort)
		default:
			return
		}
	}
}

func (sp *sessionPump) handleAck(ack wire.Ack) {
	if ack.Direction == dirC2S {
		// Peer ACK'd our c2s data
		sp.c2s.mu.Lock()
		if ack.DeliveredOffset > sp.c2s.peerDelivered {
			sp.c2s.peerDelivered = ack.DeliveredOffset
			// Trim retained buffer
			trim := int(ack.DeliveredOffset - (sp.c2s.sourceNext - uint64(sp.c2s.retained.Len())))
			if trim > 0 && trim <= sp.c2s.retained.Len() {
				sp.c2s.retained.Next(trim)
			}
		}
		sp.c2s.finDelivered = ack.FinDelivered
		sp.c2s.mu.Unlock()
	} else {
		sp.s2c.mu.Lock()
		if ack.DeliveredOffset > sp.s2c.peerDelivered {
			sp.s2c.peerDelivered = ack.DeliveredOffset
			trim := int(ack.DeliveredOffset - (sp.s2c.sourceNext - uint64(sp.s2c.retained.Len())))
			if trim > 0 && trim <= sp.s2c.retained.Len() {
				sp.s2c.retained.Next(trim)
			}
		}
		sp.s2c.finDelivered = ack.FinDelivered
		sp.s2c.mu.Unlock()
	}
}

// handleSwitchPrepare begins the planned switch process.
func (sp *sessionPump) handleSwitchPrepare(prep wire.SwitchPrepare) {
	sp.mu.Lock()
	if sp.state != StateActive {
		sp.mu.Unlock()
		return
	}
	sp.state = StateProbing
	sp.candidateEpoch = prep.ToEpoch
	sp.mu.Unlock()
}

// handleBarrier begins quiescing a direction.
func (sp *sessionPump) handleBarrier(bar wire.Barrier) {
	sp.mu.Lock()
	if sp.state != StateProbing && sp.state != StateQuiescing {
		sp.mu.Unlock()
		return
	}
	sp.state = StateQuiescing
	sp.mu.Unlock()

	if bar.Direction == dirC2S {
		sp.c2s.mu.Lock()
		sp.c2s.currentTransferComplete = true
		sp.c2s.mu.Unlock()
	} else {
		sp.s2c.mu.Lock()
		sp.s2c.currentTransferComplete = true
		sp.s2c.mu.Unlock()
	}
}

// handleBarrierAck processes barrier acknowledgment.
func (sp *sessionPump) handleBarrierAck(back wire.BarrierAck) {
	// Record the barrier acknowledgment
	if back.Direction == dirC2S {
		sp.c2s.mu.Lock()
		sp.c2s.peerDelivered = back.DeliveredOffset
		sp.c2s.finDelivered = back.FinDelivered
		sp.c2s.oldEOFDrained = true
		sp.c2s.mu.Unlock()
	} else {
		sp.s2c.mu.Lock()
		sp.s2c.peerDelivered = back.DeliveredOffset
		sp.s2c.finDelivered = back.FinDelivered
		sp.s2c.oldEOFDrained = true
		sp.s2c.mu.Unlock()
	}
}

// handleSwitchAbort cancels a planned switch.
func (sp *sessionPump) handleSwitchAbort(abort wire.SwitchAbort) {
	sp.mu.Lock()
	if sp.state != StateProbing && sp.state != StateQuiescing {
		sp.mu.Unlock()
		return
	}
	sp.state = StateActive
	sp.c2s.mu.Lock()
	sp.c2s.currentTransferComplete = false
	sp.c2s.mu.Unlock()
	sp.s2c.mu.Lock()
	sp.s2c.currentTransferComplete = false
	sp.s2c.mu.Unlock()
	sp.mu.Unlock()
}

// HandleResumeStreamID is the stream handler for incoming resume streams.
func HandleResumeStreamID(stream core.Stream) {
	var roleDecl wire.StreamRoleDeclaration
	if err := wire.ReadFrame(stream, &roleDecl); err != nil {
		_ = stream.Reset()
		return
	}

	pump := instanceRegistry.get(roleDecl.SessionID)
	if pump == nil {
		_ = stream.Reset()
		return
	}

	switch roleDecl.Role {
	case wire.StreamRoleControl:
		pump.SetControlStream(stream)
		pump.handleControlStream(stream)
	case wire.StreamRoleResume:
		pump.handleResumeStream(stream)
	default:
		_ = stream.Reset()
	}
}

// verifyConnPath checks that the actual libp2p connection matches the expected
// path type. It uses Conn.Stat().Limited to detect relayed connections.
// Returns true if the path matches, false if there's a mismatch.
func verifyConnPath(stream core.Stream, expectedPath string) bool {
	conn := stream.Conn()
	if conn == nil {
		return false
	}
	stat := conn.ConnState()
	// Transport indicates whether the connection uses a relay
	isRelayed := stat.Transport == "relay" || stat.Transport == "circuit"

	if expectedPath == "direct" && isRelayed {
		return false
	}
	if expectedPath == "relayed" && !isRelayed {
		// Allow relayed when expected, but direct is also fine
		// (we may have a direct connection available)
		return true
	}
	return true
}

// StartResumeHandshake initiates a resume handshake as the client.
// It opens a new stream, authenticates, and negotiates offsets.
func (sp *sessionPump) StartResumeHandshake(ctx context.Context, peerID peer.ID, newPath string) error {
	sp.mu.Lock()
	sp.state = StateProbing
	sp.fromPath = sp.toPath
	sp.toPath = newPath
	newEpoch := sp.epoch + 1
	sp.mu.Unlock()

	stream, err := sp.Host.NewStream(
		core.WithAllowLimitedConn(ctx, "tubo resume via relay"),
		peerID,
		protocol.ID(wire.TunnelResumeID),
	)
	if err != nil {
		return fmt.Errorf("open resume stream: %w", err)
	}

	// Verify the actual connection path matches expectations
	if !verifyConnPath(stream, newPath) {
		stream.Close()
		return fmt.Errorf("path mismatch: expected %s, got relayed", newPath)
	}

	_ = stream.SetDeadline(time.Now().Add(DefaultResumeHandshakeTimeout))

	// Send role declaration
	roleDecl := wire.StreamRoleDeclaration{
		Role:      wire.StreamRoleResume,
		SessionID: sp.SessionID,
	}
	if err := writeResumeFrame(stream, roleDecl); err != nil {
		stream.Close()
		return fmt.Errorf("write role decl: %w", err)
	}

	clientNonce, err := randomNonceStr()
	if err != nil {
		return err
	}

	hello := wire.ResumeHello{
		Version:   "2.1",
		PipeID:    sp.PipeID,
		SessionID: sp.SessionID,
		NextEpoch: newEpoch,
		Nonce:     clientNonce,
		Proof:     sp.proofForHello(newEpoch, clientNonce),
	}
	if err := writeResumeFrame(stream, hello); err != nil {
		return fmt.Errorf("write hello: %w", err)
	}

	var challenge wire.ResumeChallenge
	if err := wire.ReadFrame(stream, &challenge); err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}

	serverNonce := challenge.Nonce

	// Build commit with our observed offsets
	sp.c2s.mu.Lock()
	c2sSent := sp.c2s.carrierSent
	sp.c2s.mu.Unlock()
	sp.s2c.mu.Lock()
	s2cDelivered := sp.s2c.peerDelivered
	s2cFinDelivered := sp.s2c.finDelivered
	sp.s2c.mu.Unlock()

	commit := wire.ResumeCommit{
		SessionID:       sp.SessionID,
		NextEpoch:       newEpoch,
		ClientNonce:     clientNonce,
		ServerNonce:     serverNonce,
		C2SOldSent:      c2sSent,
		S2CDelivered:    s2cDelivered,
		C2SFinSeen:      sp.c2s.finSeen,
		S2CFinDelivered: s2cFinDelivered,
		Proof:           sp.proofForCommit(newEpoch, clientNonce, serverNonce, c2sSent, s2cDelivered, sp.c2s.finSeen, s2cFinDelivered),
	}
	if err := writeResumeFrame(stream, commit); err != nil {
		return fmt.Errorf("write commit: %w", err)
	}

	var result wire.ResumeResult
	if err := wire.ReadFrame(stream, &result); err != nil {
		return fmt.Errorf("read result: %w", err)
	}

	if !result.OK {
		return fmt.Errorf("resume rejected: %s", result.ErrorCode)
	}

	transcriptDigest := fmt.Sprintf("%s:%s:%d:%s:%s", sp.SessionID, clientNonce, newEpoch, serverNonce, "activate")
	activate := wire.ResumeActivate{
		SessionID:        sp.SessionID,
		Epoch:            result.Epoch,
		TranscriptDigest: transcriptDigest,
		Proof:            sp.proofForActivate(result.Epoch, clientNonce, serverNonce, transcriptDigest),
	}
	if err := writeResumeFrame(stream, activate); err != nil {
		return fmt.Errorf("write activate: %w", err)
	}

	var activated wire.ResumeActivated
	if err := wire.ReadFrame(stream, &activated); err != nil {
		return fmt.Errorf("read activated: %w", err)
	}

	// Activate: switch to new carrier
	sp.activateResume(stream, result.Epoch, clientNonce, serverNonce, result.C2SResumeAt, result.S2CResumeAt)

	return nil
}

// OpenControlStream opens a control stream for the session.
func (sp *sessionPump) OpenControlStream(ctx context.Context, peerID peer.ID) error {
	stream, err := sp.Host.NewStream(
		core.WithAllowLimitedConn(ctx, "tubo control stream"),
		peerID,
		protocol.ID(wire.TunnelResumeID),
	)
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}

	roleDecl := wire.StreamRoleDeclaration{
		Role:      wire.StreamRoleControl,
		SessionID: sp.SessionID,
	}
	if err := writeResumeFrame(stream, roleDecl); err != nil {
		stream.Close()
		return fmt.Errorf("write role decl: %w", err)
	}

	sp.SetControlStream(stream)
	return nil
}

// TryPlannedSwitch attempts a planned relay->direct switch.
func (sp *sessionPump) TryPlannedSwitch(ctx context.Context, peerID peer.ID, newPath string) error {
	sp.mu.Lock()
	if sp.state != StateActive {
		sp.mu.Unlock()
		return errors.New("session not active")
	}
	sp.state = StateProbing
	sp.fromPath = sp.toPath
	sp.toPath = newPath
	newEpoch := sp.epoch + 1
	sp.mu.Unlock()

	// Send SwitchPrepare over control stream
	sp.mu.Lock()
	cs := sp.controlStream
	sp.mu.Unlock()
	if cs == nil {
		return errors.New("no control stream")
	}

	nonce, err := randomNonceStr()
	if err != nil {
		return err
	}

	// Send typed switch_prepare frame
	type prepWire struct {
		Type string `json:"type"`
		wire.SwitchPrepare
	}
	prep := prepWire{
		Type: "switch_prepare",
		SwitchPrepare: wire.SwitchPrepare{
			SessionID:      sp.SessionID,
			FromEpoch:      sp.epoch,
			ToEpoch:        newEpoch,
			CandidateNonce: nonce,
			Reason:         "planned",
			Proof:          sp.computeAckProof(newEpoch, "switch", 0, false),
		},
	}
	if err := writeResumeFrame(cs, prep); err != nil {
		return err
	}

	// Quiesce c2s direction
	type barWire struct {
		Type string `json:"type"`
		wire.Barrier
	}
	bar := barWire{
		Type: "barrier",
		Barrier: wire.Barrier{
			SessionID: sp.SessionID,
			ToEpoch:   newEpoch,
			Direction: dirC2S,
			CutOffset: sp.c2s.carrierSent,
			FinSeen:   sp.c2s.finSeen,
			Proof:     sp.computeAckProof(newEpoch, "barrier_c2s", sp.c2s.carrierSent, sp.c2s.finSeen),
		},
	}
	if err := writeResumeFrame(cs, bar); err != nil {
		return err
	}

	// Quiesce s2c direction
	bar2 := barWire{
		Type: "barrier",
		Barrier: wire.Barrier{
			SessionID: sp.SessionID,
			ToEpoch:   newEpoch,
			Direction: dirS2C,
			CutOffset: sp.s2c.carrierSent,
			FinSeen:   sp.s2c.finSeen,
			Proof:     sp.computeAckProof(newEpoch, "barrier_s2c", sp.s2c.carrierSent, sp.s2c.finSeen),
		},
	}
	if err := writeResumeFrame(cs, bar2); err != nil {
		return err
	}

	// Now open the direct candidate stream and perform resume handshake
	return sp.StartResumeHandshake(ctx, peerID, newPath)
}

// InitiateResumeRecovery handles abrupt carrier failure by initiating a relay resume.
func (sp *sessionPump) InitiateResumeRecovery(ctx context.Context, peerID peer.ID) error {
	sp.mu.Lock()
	if sp.state != StateDetached {
		sp.mu.Unlock()
		return errors.New("session not detached")
	}
	sp.mu.Unlock()

	// Stop detached timer
	sp.mu.Lock()
	if sp.detachedTimer != nil {
		sp.detachedTimer.Stop()
		sp.detachedTimer = nil
	}
	sp.mu.Unlock()

	return sp.StartResumeHandshake(ctx, peerID, "relayed")
}
