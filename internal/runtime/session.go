package runtime

// Package runtime owns the minimum Session lifecycle the A0 path builds on.
//
// Issue #39 (fail-closed session-resume quarantine) removed the session-resume
// machinery from the A0 path: resume capability advertisement/offer/secret,
// negotiation/initiation/control handler and dependency, resume-only state,
// ACK/replay/candidate, planned switch, and abrupt carrier recovery with
// detached grace. Issue #28 (neutral Carrier seam) replaced Session's direct
// libp2p host/stream/peer and *store.Store coupling with a neutral Carrier
// contract realized at the host/L1 edge (internal/node). The engine below is
// the smallest non-resume Session foundation at the center of the production
// tunnel data plane: explicit NodeRuntime -> PipeRuntime -> SessionRegistry ->
// Session ownership, admission/work/close fencing/cancellation, and one active
// Carrier carrying raw full-duplex bytes with exact half-close. Do not
// reintroduce resume, control-stream, candidate, proof, retention, or detached
// recovery machinery without a real consumer and an architecture decision.

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/origama/tubo/internal/node"
)

const (
	// DefaultChunk is the copy chunk size for the raw duplex data plane.
	DefaultChunk = 32 << 10
)

// SessionState is the minimal non-resume session state machine.
type SessionState int

const (
	StateActive SessionState = iota
	StateClosing
	StateClosed
)

func (s SessionState) String() string {
	switch s {
	case StateActive:
		return "active"
	case StateClosing:
		return "closing"
	case StateClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// sessionPump is the core session state machine. It owns at most one active
// carrier and the local application endpoint, plus the explicit lifecycle
// fences from #27. There is no control stream, no candidate, no resume secret,
// no retention window, and no replay on this path.
type sessionPump struct {
	// Immutable identity
	SessionID string
	PipeID    string
	Scope     string

	// Epoch label retained for the Session shape. Non-resume sessions keep one
	// constant epoch; nothing advances it on this path.
	epoch uint64

	// Mutable transport
	mu            sync.Mutex
	state         SessionState
	activeCarrier node.Carrier

	// Target connection (the local application endpoint)
	targetConn io.ReadWriteCloser

	// Lifecycle
	ctx           context.Context
	cancel        context.CancelFunc
	carrierCtx    context.Context
	carrierCancel context.CancelFunc
	stopDone      chan struct{}
	stopping      bool
	workDone      chan struct{}
	workCount     int
	workClosed    bool

	// Session registry reference
	registry *sessionRegistry

	// isClient: true for the initiator side, false for the responder side.
	isClient bool

	// Byte accounting for store.Session telemetry relocation. The Session is
	// decoupled from *store.Store; these counters are read by the adapter
	// after pump completion.
	bytesIn  atomic.Int64
	bytesOut atomic.Int64

	// completedCleanly is set when both pump directions reach EOF (exact
	// both-FIN completion). On a clean completion the Session closes the
	// Carrier gracefully (Close, preserving mux FIN ordering) instead of
	// hard-resetting it — #43 finding 1 remediation at the Session/Carrier
	// boundary. Any error or forced-stop path still hard-resets fail-closed.
	completedCleanly atomic.Bool
}

// sessionRegistry is an isolated, PipeRuntime-owned session map.
type sessionRegistry struct {
	mu       sync.Mutex
	closed   bool
	sessions map[string]*sessionPump
}

func newSessionRegistry() *sessionRegistry {
	return &sessionRegistry{sessions: make(map[string]*sessionPump)}
}

func (r *sessionRegistry) get(id string) *sessionPump {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	return r.sessions[id]
}

func (r *sessionRegistry) register(id string, p *sessionPump) error {
	if id == "" {
		return errInvalidSessionID
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errRuntimeClosed
	}
	if _, exists := r.sessions[id]; exists {
		return errDuplicateSessionID
	}
	r.sessions[id] = p
	return nil
}

func (r *sessionRegistry) remove(id string, p *sessionPump) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[id] == p {
		delete(r.sessions, id)
	}
}

func (r *sessionRegistry) close() []*sessionPump {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	sessions := make([]*sessionPump, 0, len(r.sessions))
	for _, p := range r.sessions {
		sessions = append(sessions, p)
	}
	clear(r.sessions)
	return sessions
}

// sessionConfig holds configuration for a new session pump.
type sessionConfig struct {
	SessionID    string
	PipeID       string
	Scope        string
	TargetConn   io.ReadWriteCloser
	InitialEpoch uint64
	IsClient     bool
}

func newSessionPump(parent context.Context, registry *sessionRegistry, cfg sessionConfig) (*sessionPump, error) {
	ctx, cancel := context.WithCancel(parent)
	if cfg.InitialEpoch == 0 {
		cfg.InitialEpoch = 1
	}
	carrierCtx, carrierCancel := context.WithCancel(ctx)
	sp := &sessionPump{
		SessionID:     cfg.SessionID,
		PipeID:        cfg.PipeID,
		Scope:         cfg.Scope,
		targetConn:    cfg.TargetConn,
		epoch:         cfg.InitialEpoch,
		state:         StateActive,
		ctx:           ctx,
		cancel:        cancel,
		carrierCtx:    carrierCtx,
		carrierCancel: carrierCancel,
		stopDone:      make(chan struct{}),
		workDone:      make(chan struct{}),
		isClient:      cfg.IsClient,
	}
	sp.registry = registry
	if err := sp.registry.register(cfg.SessionID, sp); err != nil {
		sp.carrierCancel()
		sp.cancel()
		return nil, err
	}
	return sp, nil
}

// SetActiveCarrier sets the raw duplex carrier only while session remains owned.
func (sp *sessionPump) SetActiveCarrier(carrier node.Carrier) bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.stopping || sp.ctx.Err() != nil {
		return false
	}
	sp.activeCarrier = carrier
	return true
}

// acceptDispatch returns true only while the session remains owned and live.
func (sp *sessionPump) acceptDispatch() bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	return !sp.stopping && sp.ctx.Err() == nil
}

// admitWork is the Session lifecycle admission fence. Close changes state to
// closed before waiting, so no later work can be admitted.
func (sp *sessionPump) admitWork() bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.stopping || sp.ctx.Err() != nil {
		return false
	}
	sp.workCount++
	return true
}

func (sp *sessionPump) finishWork() {
	sp.mu.Lock()
	sp.workCount--
	if sp.workCount < 0 {
		sp.mu.Unlock()
		panic("session lifecycle work underflow")
	}
	sp.closeWorkDoneLocked()
	sp.mu.Unlock()
}

func (sp *sessionPump) closeWorkDoneLocked() {
	if sp.stopping && sp.workCount == 0 && !sp.workClosed {
		sp.workClosed = true
		close(sp.workDone)
	}
}

func (sp *sessionPump) startPump() bool {
	if !sp.admitWork() {
		return false
	}
	go func() {
		defer sp.finishWork()
		sp.pumpCarrier()
	}()
	return true
}

// beginOutbound admits a blocking outbound operation under Session ownership.
// Its context is canceled by either caller or Session close.
func (sp *sessionPump) beginOutbound(caller context.Context) (context.Context, func(), error) {
	if caller == nil {
		caller = context.Background()
	}
	if !sp.admitWork() {
		return nil, nil, errors.New("session closed")
	}
	ctx, cancel := context.WithCancel(sp.ctx)
	stopCaller := context.AfterFunc(caller, cancel)
	return ctx, func() {
		stopCaller()
		cancel()
		sp.finishWork()
	}, nil
}

type sessionStopResources struct {
	active   node.Carrier
	target   io.ReadWriteCloser
	workDone <-chan struct{}
}

// beginStop is lifecycle state independent of protocol state transitions.
// Exactly one caller becomes closer; all other callers wait stopDone.
func (sp *sessionPump) beginStop() (sessionStopResources, bool) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.stopping {
		return sessionStopResources{}, false
	}
	sp.stopping = true
	sp.state = StateClosed
	resources := sessionStopResources{
		active:   sp.activeCarrier,
		target:   sp.targetConn,
		workDone: sp.workDone,
	}
	sp.activeCarrier = nil
	sp.closeWorkDoneLocked()
	return resources, true
}

func (sp *sessionPump) completeStop(resources sessionStopResources) {
	sp.cancel()
	sp.carrierCancel()
	if resources.active != nil {
		// Clean both-FIN completion closes the Carrier gracefully so FIN bytes are
		// not preempted by a hard reset; any other completion hard-resets.
		if sp.completedCleanly.Load() {
			_ = resources.active.Close()
		} else {
			_ = resources.active.Reset()
		}
	}
	if resources.target != nil {
		_ = resources.target.Close()
	}
	<-resources.workDone
	sp.registry.remove(sp.SessionID, sp)
	close(sp.stopDone)
}

// Stop terminates Session-owned work and waits exact one closer completion.
func (sp *sessionPump) Stop() {
	resources, closer := sp.beginStop()
	if !closer {
		<-sp.stopDone
		return
	}
	sp.completeStop(resources)
}

// requestStop is used from tracked pump work. It first fences lifecycle state,
// then completes close outside that work to avoid self-wait deadlock.
func (sp *sessionPump) requestStop() {
	resources, closer := sp.beginStop()
	if closer {
		go sp.completeStop(resources)
	}
}

// runServerSession starts the minimal non-resume session for the responder
// (server) side. It blocks until the tunnel data plane completes.
func (sp *sessionPump) runServerSession() {
	if !sp.admitWork() {
		return
	}
	defer sp.Stop()
	defer sp.finishWork()
	sp.pumpCarrier()
}

// runClientSession starts the minimal non-resume session for the initiator
// (client) side.
func (sp *sessionPump) runClientSession() {
	sp.runServerSession()
}

func (sp *sessionPump) ByteCounts() (int64, int64) {
	return sp.bytesIn.Load(), sp.bytesOut.Load()
}

// pumpCarrier moves raw bytes both directions between the active carrier and
// the local target with exact half-close (FIN only after final bytes). A
// carrier or local transport failure terminates the Session (fail closed; no
// resume, no detached grace). A clean one-direction EOF half-closes that
// direction and lets the opposite direction finish; the pump completes when
// both directions have FIN-ed, then the session ends cleanly.
func (sp *sessionPump) pumpCarrier() {
	sp.mu.Lock()
	carrier := sp.activeCarrier
	sp.mu.Unlock()
	if carrier == nil {
		sp.requestStop()
		return
	}

	var wg sync.WaitGroup
	var cleanA, cleanB bool
	wg.Add(2)
	go func() {
		defer wg.Done()
		cleanA = sp.carrierToTarget(carrier, sp.targetConn)
	}()
	go func() {
		defer wg.Done()
		cleanB = sp.targetToCarrier(sp.targetConn, carrier)
	}()
	wg.Wait()
	sp.completedCleanly.Store(cleanA && cleanB)
}

// carrierToTarget copies carrier bytes to the local target. On clean carrier
// EOF it half-closes the target write side and reports clean; on a transport
// error it fails the session closed.
func (sp *sessionPump) carrierToTarget(carrier io.Reader, target io.Writer) (clean bool) {
	buf := make([]byte, DefaultChunk)
	for {
		n, err := carrier.Read(buf)
		if n > 0 {
			if werr := sp.writeAllToTarget(target, buf[:n]); werr != nil {
				sp.requestStop()
				return false
			}
			sp.bytesIn.Add(int64(n))
			if err == nil {
				continue
			}
		}
		if err == io.EOF {
			if cw, ok := target.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			return true
		}
		if err != nil {
			sp.requestStop()
			return false
		}
	}
}

// targetToCarrier reads target bytes and writes them to the carrier. On target
// EOF it half-closes the carrier write side and returns clean.
func (sp *sessionPump) targetToCarrier(source io.Reader, carrier io.Writer) (clean bool) {
	buf := make([]byte, DefaultChunk)
	for {
		n, err := source.Read(buf)
		if n > 0 {
			if werr := sp.writeAllToCarrier(carrier, buf[:n]); werr != nil {
				sp.requestStop()
				return false
			}
			sp.bytesOut.Add(int64(n))
			if err == nil {
				continue
			}
		}
		if err == io.EOF {
			if cw, ok := carrier.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			return true
		}
		if err != nil {
			sp.requestStop()
			return false
		}
	}
}

func (sp *sessionPump) writeAllToTarget(target io.Writer, p []byte) error {
	written := 0
	for written < len(p) {
		w, werr := target.Write(p[written:])
		written += w
		if werr != nil {
			if written < len(p) {
				return werr
			}
			return nil
		}
	}
	return nil
}

func (sp *sessionPump) writeAllToCarrier(carrier io.Writer, p []byte) error {
	written := 0
	for written < len(p) {
		w, werr := carrier.Write(p[written:])
		written += w
		if werr != nil {
			if written < len(p) {
				return werr
			}
			return nil
		}
	}
	return nil
}
