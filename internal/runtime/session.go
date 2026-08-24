package runtime

// Package runtime owns the minimum Session lifecycle the A0 path builds on.
//
// Issue #39 (fail-closed session-resume quarantine) removed the session-resume
// machinery from the A0 path: resume capability advertisement/offer/secret,
// negotiation/initiation/control handler and dependency, resume-only state,
// ACK/replay/candidate, planned switch, and abrupt carrier recovery with
// detached grace. Production sessions now run the plain raw-duplex data plane
// (copyDuplex) with no active Session engine wiring. The engine below is the
// smallest non-resume Session foundation #28 re-grounds behind a Carrier seam:
// explicit NodeRuntime -> PipeRuntime -> SessionRegistry -> Session ownership,
// admission/work/close fencing/cancellation, and one active transport. Do not
// reintroduce resume, control-stream, candidate, proof, retention, or detached
// recovery machinery without a real consumer and an architecture decision.

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo-rebuilt/internal/store"
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
	SessionID    string
	PipeID       string
	Scope        string
	ClientPeerID peer.ID
	ServerPeerID peer.ID
	Host         host.Host
	State        *store.Store

	// Epoch label retained for the #28 Carrier seam. Non-resume sessions keep
	// one constant epoch; nothing advances it on this path.
	epoch uint64

	// Mutable transport
	mu            sync.Mutex
	state         SessionState
	activeCarrier core.Stream

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
	outbound      map[core.Stream]struct{}

	// Session registry reference
	registry *sessionRegistry

	// Role: true for client (initiator), false for server (responder). There
	// is no c2s/s2c ACK distinction on the non-resume path; retained for the
	// #28 session shape.
	isClient bool
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
	ClientPeerID peer.ID
	ServerPeerID peer.ID
	Host         host.Host
	State        *store.Store
	TargetConn   io.ReadWriteCloser
	InitialEpoch uint64
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
		ClientPeerID:  cfg.ClientPeerID,
		ServerPeerID:  cfg.ServerPeerID,
		Host:          cfg.Host,
		State:         cfg.State,
		targetConn:    cfg.TargetConn,
		epoch:         cfg.InitialEpoch,
		state:         StateActive,
		ctx:           ctx,
		cancel:        cancel,
		carrierCtx:    carrierCtx,
		carrierCancel: carrierCancel,
		stopDone:      make(chan struct{}),
		workDone:      make(chan struct{}),
		outbound:      make(map[core.Stream]struct{}),
		isClient:      cfg.Host != nil,
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
func (sp *sessionPump) SetActiveCarrier(stream core.Stream) bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.stopping || sp.ctx.Err() != nil {
		return false
	}
	sp.activeCarrier = stream
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

func (sp *sessionPump) trackOutbound(stream core.Stream) bool {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if sp.stopping || sp.ctx.Err() != nil {
		return false
	}
	sp.outbound[stream] = struct{}{}
	return true
}

func (sp *sessionPump) untrackOutbound(stream core.Stream) {
	sp.mu.Lock()
	delete(sp.outbound, stream)
	sp.mu.Unlock()
}

type sessionStopResources struct {
	active   core.Stream
	target   io.ReadWriteCloser
	outbound []core.Stream
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
		outbound: make([]core.Stream, 0, len(sp.outbound)),
	}
	sp.activeCarrier = nil
	for stream := range sp.outbound {
		resources.outbound = append(resources.outbound, stream)
	}
	sp.closeWorkDoneLocked()
	return resources, true
}

func (sp *sessionPump) completeStop(resources sessionStopResources) {
	sp.cancel()
	sp.carrierCancel()
	for _, stream := range resources.outbound {
		_ = stream.Reset()
	}
	if resources.active != nil {
		_ = resources.active.Close()
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

// runServerSession starts the minimal non-resume session for the server side.
func (sp *sessionPump) runServerSession() {
	if !sp.admitWork() {
		return
	}
	defer sp.Stop()
	defer sp.finishWork()
	sp.pumpCarrier()
}

// runClientSession starts the minimal non-resume session for the client side.
func (sp *sessionPump) runClientSession() {
	sp.runServerSession()
}

// pumpCarrier moves raw bytes both directions between the active carrier and
// the local target. A carrier read/write failure or EOF terminates the session
// (carrier failure terminates Session; no resume, no detached grace).
func (sp *sessionPump) pumpCarrier() {
	sp.mu.Lock()
	carrier := sp.activeCarrier
	sp.mu.Unlock()
	if carrier == nil {
		sp.requestStop()
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		sp.pumpCarrierToTarget(carrier, sp.targetConn)
	}()
	go func() {
		defer wg.Done()
		sp.pumpTargetToCarrier(sp.targetConn, carrier)
	}()

	wg.Wait()
}

// pumpCarrierToTarget reads carrier bytes and writes them to the local target.
func (sp *sessionPump) pumpCarrierToTarget(carrier io.Reader, local io.Writer) {
	buf := make([]byte, DefaultChunk)
	for {
		n, err := carrier.Read(buf)
		if n > 0 {
			written := 0
			for written < n {
				w, werr := local.Write(buf[written:n])
				written += w
				if werr != nil && written < n {
					sp.requestStop()
					return
				}
				if werr != nil {
					break
				}
			}
			continue
		}
		if err == nil {
			continue
		}
		// EOF or failure ends the session.
		sp.requestStop()
		return
	}
}

// pumpTargetToCarrier reads target bytes and writes them to the carrier.
func (sp *sessionPump) pumpTargetToCarrier(source io.Reader, carrier io.Writer) {
	buf := make([]byte, DefaultChunk)
	for {
		n, err := source.Read(buf)
		if n > 0 {
			written := 0
			for written < n {
				w, werr := carrier.Write(buf[written:n])
				written += w
				if werr != nil && written < n {
					sp.requestStop()
					return
				}
				if werr != nil {
					break
				}
			}
			continue
		}
		if err == nil {
			continue
		}
		// EOF or EOF terminates the session.
		sp.requestStop()
		return
	}
}
