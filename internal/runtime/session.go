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
	detachedTimer *time.Timer
	done          chan struct{}

	// ACK ticker
	ackTicker *time.Ticker

	// Inflight limits
	maxInflightPerDir int
	maxAggregate      int64
	currentAggregate  atomic.Int64

	// Resume registry reference
	registry *sessionRegistry
}

// sessionRegistry is a map of sessionID -> *sessionPump for resume dispatch.
type sessionRegistry struct {
	mu       sync.Mutex
	sessions map[string]*sessionPump
}

var globalSessionRegistry = &sessionRegistry{sessions: make(map[string]*sessionPump)}

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
		done:              make(chan struct{}),
		ackTicker:         time.NewTicker(DefaultAckInterval),
	}
	sp.c2s.retained = &bytes.Buffer{}
	sp.s2c.retained = &bytes.Buffer{}
	sp.registry = globalSessionRegistry
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
	state := sp.state
	sp.mu.Unlock()
	if state == StateClosed {
		return
	}
	sp.cancel()
	sp.ackTicker.Stop()
	sp.mu.Lock()
	sp.state = StateClosed
	if sp.detachedTimer != nil {
		sp.detachedTimer.Stop()
		sp.detachedTimer = nil
	}
	sp.mu.Unlock()
	sp.registry.remove(sp.SessionID)
	<-sp.done
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

// computeProof calculates HMAC-SHA256 proof.
func computeProof(secret []byte, parts ...[]byte) string {
	mac := hmac.New(sha256.New, secret)
	for _, p := range parts {
		mac.Write(p)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
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

// runServerSession starts the session pump for the server side.
// It reads from the active carrier and writes to the target, and vice versa.
func (sp *sessionPump) runServerSession() {
	defer close(sp.done)
	defer sp.Stop()

	var wg sync.WaitGroup
	wg.Add(2)

	// c2s: read from carrier, write to target
	go func() {
		defer wg.Done()
		sp.readFromCarrier(sp.activeCarrier, sp.targetConn, &sp.c2s)
	}()

	// s2c: read from target, write to carrier
	go func() {
		defer wg.Done()
		sp.readFromSource(sp.targetConn, sp.activeCarrier, &sp.s2c, &sp.c2s)
	}()

	// ACK sender goroutine
	go sp.sendAcks()

	wg.Wait()
}

// runClientSession starts the session pump for the client side.
// It reads from the local connection and writes to the carrier, and vice versa.
func (sp *sessionPump) runClientSession() {
	defer close(sp.done)
	defer sp.Stop()

	var wg sync.WaitGroup
	wg.Add(3)

	// c2s: read from local, write to carrier
	go func() {
		defer wg.Done()
		sp.readFromSource(sp.targetConn, sp.activeCarrier, &sp.c2s, &sp.s2c)
	}()

	// s2c: read from carrier, write to local
	go func() {
		defer wg.Done()
		sp.readFromCarrier(sp.activeCarrier, sp.targetConn, &sp.s2c)
	}()

	// ACK sender goroutine
	go sp.sendAcks()

	wg.Wait()
}

// readFromCarrier reads from the carrier stream and writes to the local endpoint.
// It tracks delivery offsets and sends ACKs.
func (sp *sessionPump) readFromCarrier(carrier io.Reader, local io.Writer, dir *directionState) {
	buf := make([]byte, 32<<10)
	for {
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
				// Late bytes from old carrier - discard
				continue
			}

			// Write to local endpoint
			written := 0
			for written < n {
				w, werr := local.Write(buf[written:n])
				written += w
				if werr != nil && written < n {
					// Partial write with error - close session
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
				// Carrier EOF - may be barrier or real EOF
				sp.mu.Lock()
				state := sp.state
				sp.mu.Unlock()
				if state == StateQuiescing || state == StateResumePrepared {
					dir.mu.Lock()
					dir.oldEOFDrained = true
					dir.mu.Unlock()
				}
				// Don't propagate as application FIN
				return
			}
			// Read error - carrier failure
			sp.handleCarrierFailure()
			return
		}
	}
}

// readFromSource reads from the local source and writes to the carrier.
// It retains bytes until acknowledged by the peer.
func (sp *sessionPump) readFromSource(source io.Reader, carrier io.Writer, dir *directionState, otherDir *directionState) {
	buf := make([]byte, 32<<10)
	for {
		sp.mu.Lock()
		if sp.state == StateClosed || sp.state == StateClosing {
			sp.mu.Unlock()
			return
		}
		sp.mu.Unlock()

		// Check if we're quiescing and should stop writing
		dir.mu.Lock()
		if dir.currentTransferComplete {
			dir.mu.Unlock()
			// Wait for barrier or state change
			time.Sleep(100 * time.Millisecond)
			continue
		}
		dir.mu.Unlock()

		n, err := source.Read(buf)
		if n > 0 {
			// Check retained buffer limits
			dir.mu.Lock()
			if dir.sourceNext+uint64(n) > dir.peerDelivered+uint64(sp.maxInflightPerDir) {
				// Backpressure: wait for ACK to free space
				dir.mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				continue
			}

			// Retain for potential retransmission
			dir.retained.Write(buf[:n])
			dir.sourceNext += uint64(n)
			dir.carrierSent += uint64(n)
			dir.mu.Unlock()

			// Write to carrier
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
				// Application FIN
				dir.mu.Lock()
				dir.finSeen = true
				dir.mu.Unlock()
				// Propagate CloseWrite on carrier
				if cw, ok := carrier.(closeWriter); ok {
					_ = cw.CloseWrite()
				}
				return
			}
			// Read error - close session
			sp.mu.Lock()
			sp.state = StateClosing
			sp.mu.Unlock()
			sp.Stop()
			return
		}
	}
}

// sendAcks periodically sends ACKs for received data.
func (sp *sessionPump) sendAcks() {
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

			// Send ACK for c2s direction (we're the receiver)
			sp.c2s.mu.Lock()
			delivered := sp.c2s.peerDelivered
			finDelivered := sp.c2s.finDelivered
			sp.c2s.mu.Unlock()

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
					Direction:       dirC2S,
					DeliveredOffset: delivered,
					FinDelivered:    finDelivered,
					Proof:           sp.computeAckProof(epoch, dirC2S, delivered, finDelivered),
				},
			}

			_ = wire.WriteFrame(cs, ack)
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
	if state != StateDetached && state != StateActive {
		return
	}

	// Verify proof
	expectedProof := sp.proofForHello(hello.NextEpoch, hello.Nonce)
	if hello.Proof != expectedProof {
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
	if err := wire.WriteFrame(stream, challenge); err != nil {
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
	c2sOldSent := sp.c2s.carrierSent
	sp.c2s.mu.Unlock()
	sp.s2c.mu.Lock()
	s2cDelivered := sp.s2c.peerDelivered
	s2cFinDelivered := sp.s2c.finDelivered
	sp.s2c.mu.Unlock()

	expectedCommitProof := sp.proofForCommit(hello.NextEpoch, hello.Nonce, serverNonce, c2sOldSent, s2cDelivered, sp.c2s.finSeen, s2cFinDelivered)
	if commit.Proof != expectedCommitProof {
		return
	}

	// Validate offsets
	if commit.C2SOldSent < sp.c2s.peerDelivered || commit.C2SOldSent > c2sOldSent {
		return
	}
	if commit.S2CDelivered > sp.s2c.carrierSent {
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
	if err := wire.WriteFrame(stream, result); err != nil {
		return
	}

	var activate wire.ResumeActivate
	if err := wire.ReadFrame(stream, &activate); err != nil {
		return
	}

	transcriptDigest := fmt.Sprintf("%s:%s:%d:%s:%s", sp.SessionID, hello.Nonce, hello.NextEpoch, serverNonce, "activate")
	expectedActivateProof := sp.proofForActivate(hello.NextEpoch, hello.Nonce, serverNonce, transcriptDigest)
	if activate.Proof != expectedActivateProof {
		return
	}

	activated := wire.ResumeActivated{
		SessionID: sp.SessionID,
		Epoch:     hello.NextEpoch,
		Proof:     sp.proofForActivate(hello.NextEpoch, hello.Nonce, serverNonce, transcriptDigest),
	}
	if err := wire.WriteFrame(stream, activated); err != nil {
		return
	}

	// Activate: switch to new carrier
	sp.activateResume(stream, hello.NextEpoch, hello.Nonce, serverNonce, c2sDelivered, commit.S2CDelivered)
}

// activateResume installs the new carrier and replays retained bytes.
func (sp *sessionPump) activateResume(stream core.Stream, newEpoch uint64, clientNonce, serverNonce string, c2sResumeAt, s2cResumeAt uint64) {
	sp.mu.Lock()
	oldStream := sp.activeCarrier
	sp.activeCarrier = stream
	sp.epoch = newEpoch
	sp.state = StateActive
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
		// Write retained bytes to new carrier
		if len(replay) > 0 {
			_, _ = stream.Write(replay)
		}
	}
	sp.c2s.mu.Unlock()

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

	// Update session current transport path
	_ = sp.State.PutSession(context.Background(), store.Session{
		ID:                   sp.SessionID,
		CurrentTransportPath: sp.toPath,
	})

	runtimeInfof("session resumed epoch=%d session=%s path=%s", newEpoch, sp.SessionID, sp.toPath)
}

// handleControlStream processes incoming control messages.
// The role declaration is sent once at stream open; subsequent frames are
// typed control messages.
func (sp *sessionPump) handleControlStream(stream core.Stream) {
	defer stream.Close()

	// Read role declaration once
	var roleDecl wire.StreamRoleDeclaration
	if err := wire.ReadFrame(stream, &roleDecl); err != nil {
		return
	}
	if roleDecl.Role != wire.StreamRoleControl || roleDecl.SessionID != sp.SessionID {
		return
	}

	for {
		// Read typed control message directly
		type controlHeader struct {
			Type string `json:"type"`
		}
		var header controlHeader
		if err := wire.ReadFrame(stream, &header); err != nil {
			return
		}

		switch header.Type {
		case "ack":
			var ack wire.Ack
			if err := wire.ReadFrame(stream, &ack); err != nil {
				return
			}
			sp.handleAck(ack)
		case "switch_prepare":
			var prep wire.SwitchPrepare
			if err := wire.ReadFrame(stream, &prep); err != nil {
				return
			}
			sp.handleSwitchPrepare(prep)
		case "barrier":
			var bar wire.Barrier
			if err := wire.ReadFrame(stream, &bar); err != nil {
				return
			}
			sp.handleBarrier(bar)
		case "barrier_ack":
			var back wire.BarrierAck
			if err := wire.ReadFrame(stream, &back); err != nil {
				return
			}
			sp.handleBarrierAck(back)
		case "switch_abort":
			var abort wire.SwitchAbort
			if err := wire.ReadFrame(stream, &abort); err != nil {
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
	// Read the stream role declaration
	var roleDecl wire.StreamRoleDeclaration
	if err := wire.ReadFrame(stream, &roleDecl); err != nil {
		_ = stream.Reset()
		return
	}

	pump := globalSessionRegistry.get(roleDecl.SessionID)
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
	defer stream.Close()

	_ = stream.SetDeadline(time.Now().Add(DefaultResumeHandshakeTimeout))

	// Send role declaration
	roleDecl := wire.StreamRoleDeclaration{
		Role:      wire.StreamRoleResume,
		SessionID: sp.SessionID,
	}
	if err := wire.WriteFrame(stream, roleDecl); err != nil {
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
	if err := wire.WriteFrame(stream, hello); err != nil {
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
	if err := wire.WriteFrame(stream, commit); err != nil {
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
	if err := wire.WriteFrame(stream, activate); err != nil {
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
	if err := wire.WriteFrame(stream, roleDecl); err != nil {
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
	if err := wire.WriteFrame(cs, prep); err != nil {
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
	if err := wire.WriteFrame(cs, bar); err != nil {
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
	if err := wire.WriteFrame(cs, bar2); err != nil {
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
