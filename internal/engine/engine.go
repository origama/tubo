// Package engine is the L2 Pipe connectivity composition engine (A0 Step 7).
//
// Engine composes Pipe traversal: it resolves the logical path from a starting
// Pipe through intermediate Pipe hops to a terminal Service, then opens each
// hop sequentially. For intermediate Pipe targets it delegates to an injected
// DownstreamOpener that performs per-hop authorization, reachability, and
// tunnel handshake. For terminal Service targets it resolves the ServiceSpec
// and opens the local endpoint.
//
// Dependency rules:
//   - Depends on L0 domain (Ref, PipeSpec, ServiceSpec, ResolveLogicalPath)
//   - Depends on L1 node (Carrier interface)
//   - Depends on stdlib only for core logic
//   - Does NOT import internal/discovery, internal/store, internal/authority,
//     internal/openauth, or any L3 package
//   - Traversal state and hop enforcement are pure L0-style logic
package engine

import (
	"context"
	"errors"
	"net"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/node"
)

var (
	// ErrHopLimitExceeded is returned when the traversal exceeds the max hop
	// budget. This is a deterministic fail-closed outcome.
	ErrHopLimitExceeded = errors.New("hop limit exceeded")

	// ErrLoopDetected is returned when a Pipe ID appears more than once in the
	// traversal visited set. This is a deterministic fail-closed outcome.
	ErrLoopDetected = errors.New("pipe traversal loop detected")

	// ErrInvalidTargetKind is returned when a Pipe's target is neither
	// KindService nor KindPipe.
	ErrInvalidTargetKind = errors.New("invalid target kind in pipe spec")

	// ErrNoPath is returned when the logical path cannot be resolved (e.g. start
	// Ref is not a Pipe, or an intermediate hop cannot be loaded).
	ErrNoPath = errors.New("no logical path to terminal service")

	// ErrMissingPipeTarget is returned when a Pipe target Ref cannot be resolved.
	ErrMissingPipeTarget = errors.New("missing pipe target in traversal")

	// ErrMissingServiceTarget is returned when a Service target Ref cannot be
	// resolved.
	ErrMissingServiceTarget = errors.New("missing service target in traversal")

	// ErrInvalidPipeDefinition is returned when a PipeSpec.Validate() fails
	// during traversal resolution.
	ErrInvalidPipeDefinition = errors.New("invalid pipe definition")

	// ErrInvalidServiceDefinition is returned when a ServiceSpec.Validate()
	// fails during traversal resolution.
	ErrInvalidServiceDefinition = errors.New("invalid service definition")
)

// TraversalState is the bounded, immutable-copy hop/loop enforcement state.
// Each hop advances the state by appending the current PipeID and decrementing
// the remaining budget. The state is copied on advance so the original is
// never mutated.
type TraversalState struct {
	VisitedIDs []string `json:"visited_ids"`
	Remaining  int      `json:"remaining"`
	MaxHops    int      `json:"max_hops"`
}

// NewTraversal creates an initial TraversalState with the given starting
// PipeID visited and the default hop budget.
func NewTraversal(pipeID string) TraversalState {
	return TraversalState{
		VisitedIDs: []string{pipeID},
		Remaining:  domain.DefaultMaxPipeHops,
		MaxHops:    domain.DefaultMaxPipeHops,
	}
}

// Advance returns a new TraversalState with the given PipeID appended to the
// visited set and the remaining budget decremented by one. It returns an error
// if the PipeID is already in the visited set (loop) or the remaining budget
// is exhausted (hop limit). The original state is never mutated.
func (s TraversalState) Advance(pipeID string) (TraversalState, error) {
	if s.Remaining <= 0 {
		return s, ErrHopLimitExceeded
	}
	for _, id := range s.VisitedIDs {
		if id == pipeID {
			return s, ErrLoopDetected
		}
	}
	visited := make([]string, len(s.VisitedIDs)+1)
	copy(visited, s.VisitedIDs)
	visited[len(s.VisitedIDs)] = pipeID
	return TraversalState{
		VisitedIDs: visited,
		Remaining:  s.Remaining - 1,
		MaxHops:    s.MaxHops,
	}, nil
}

// FromWire builds a TraversalState from wire OpenPipe fields. It validates
// that the wire fields are bounded and consistent, then returns the initial
// state. The returned state has the visited list from the wire, and remaining
// computed as hopLimit - len(visited).
func FromWire(visited []string, hopLimit int) (TraversalState, error) {
	if hopLimit <= 0 || hopLimit > domain.DefaultMaxPipeHops {
		return TraversalState{}, ErrHopLimitExceeded
	}
	visited = append([]string(nil), visited...)
	seen := make(map[string]struct{}, len(visited))
	for _, id := range visited {
		if _, exists := seen[id]; exists {
			return TraversalState{}, ErrLoopDetected
		}
		seen[id] = struct{}{}
	}
	if len(visited) >= hopLimit {
		return TraversalState{}, ErrHopLimitExceeded
	}
	return TraversalState{
		VisitedIDs: visited,
		Remaining:  hopLimit - len(visited),
		MaxHops:    hopLimit,
	}, nil
}

// DownstreamOpener opens an authorized carrier to a remote Pipe hop. It is
// injected at the composition root, where L3 access lease acquisition and L1
// reachability are available. The Engine calls this for each remote Pipe hop
// in the traversal.
//
// The opener must perform the full tunnel handshake (ClientHello, ServerHello,
// OpenPipe with traversal state and AccessLease, OpenResult) and return the
// established carrier and session ID on success. The Engine does not perform
// any I/O itself.
type DownstreamOpener func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error)

// ForwardResult captures the outcome of forwarding through one Pipe hop.
// If IsService is true, the target is a terminal Service and ServiceConn is
// set. If IsService is false, the target is a downstream Pipe and
// DownstreamCarrier and DownstreamSessionID are set.
type ForwardResult struct {
	IsService           bool
	ServiceConn         net.Conn
	DownstreamCarrier   node.Carrier
	DownstreamSessionID string
}

// Engine composes Pipe connectivity traversal. It resolves the logical path
// from a starting Pipe and orchestrates per-hop resolution through injected
// capabilities.
type Engine struct {
	resolveService func(domain.Ref) (domain.ServiceSpec, error)
	resolvePipe    func(domain.Ref) (domain.PipeSpec, error)
	dialService    func(domain.ServiceSpec) (net.Conn, error)
}

// New creates an Engine with the injected resolution and dialing capabilities.
// The Engine owns pure composition logic; all I/O and discovery is delegated
// to the injected functions.
func New(
	resolveService func(domain.Ref) (domain.ServiceSpec, error),
	resolvePipe func(domain.Ref) (domain.PipeSpec, error),
	dialService func(domain.ServiceSpec) (net.Conn, error),
) *Engine {
	return &Engine{
		resolveService: resolveService,
		resolvePipe:    resolvePipe,
		dialService:    dialService,
	}
}

// ResolvePath resolves the full logical path from a starting Pipe Ref through
// intermediate Pipe hops to a terminal Service. It delegates to
// domain.ResolveLogicalPath, reusing L0 cycle detection, hop limit, and
// Service terminal detection.
func (e *Engine) ResolvePath(start domain.Ref, maxHops int) (domain.LogicalPath, error) {
	return domain.ResolveLogicalPath(start, maxHops, e.resolvePipe, e.resolveService)
}

// ResolvePipeSpec resolves a PipeSpec by Ref.
func (e *Engine) ResolvePipeSpec(ref domain.Ref) (domain.PipeSpec, error) {
	return e.resolvePipe(ref)
}

// ResolveServiceSpec resolves a ServiceSpec by Ref.
func (e *Engine) ResolveServiceSpec(ref domain.Ref) (domain.ServiceSpec, error) {
	return e.resolveService(ref)
}

// Forward advances the traversal through one Pipe hop. For a Pipe target, it
// resolves the downstream PipeSpec and calls openDownstream to establish the
// authorized carrier. For a Service target, it resolves the ServiceSpec and
// dials the local endpoint.
//
// The caller must provide openDownstream for remote Pipe hops. For terminal
// Service targets, openDownstream is not called (may be nil).
func (e *Engine) Forward(ctx context.Context, current domain.PipeSpec, state TraversalState, openDownstream DownstreamOpener) (ForwardResult, error) {
	switch current.Target.Kind {
	case domain.KindService:
		return e.forwardServiceTarget(ctx, current.Target)
	case domain.KindPipe:
		return e.forwardPipeTarget(ctx, current.Target, state, openDownstream)
	default:
		return ForwardResult{}, ErrInvalidTargetKind
	}
}

func (e *Engine) forwardServiceTarget(_ context.Context, target domain.Ref) (ForwardResult, error) {
	svc, err := e.resolveService(target)
	if err != nil {
		return ForwardResult{}, errors.Join(ErrMissingServiceTarget, err)
	}
	if err := svc.Validate(); err != nil {
		return ForwardResult{}, errors.Join(ErrInvalidServiceDefinition, err)
	}
	conn, err := e.dialService(svc)
	if err != nil {
		return ForwardResult{}, err
	}
	return ForwardResult{
		IsService:   true,
		ServiceConn: conn,
	}, nil
}

func (e *Engine) forwardPipeTarget(ctx context.Context, target domain.Ref, state TraversalState, openDownstream DownstreamOpener) (ForwardResult, error) {
	nextSpec, err := e.resolvePipe(target)
	if err != nil {
		return ForwardResult{}, errors.Join(ErrMissingPipeTarget, err)
	}
	if err := nextSpec.Validate(); err != nil {
		return ForwardResult{}, errors.Join(ErrInvalidPipeDefinition, err)
	}
	newState, err := state.Advance(nextSpec.PipeID)
	if err != nil {
		return ForwardResult{}, err
	}
	if openDownstream == nil {
		return ForwardResult{}, errors.New("downstream opener required for pipe target")
	}
	carrier, sessionID, err := openDownstream(ctx, nextSpec, newState)
	if err != nil {
		return ForwardResult{}, err
	}
	return ForwardResult{
		IsService:           false,
		DownstreamCarrier:   carrier,
		DownstreamSessionID: sessionID,
	}, nil
}
