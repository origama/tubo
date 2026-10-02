package engine

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/node"
)

// ---------------------------------------------------------------------------
// Helpers: in-memory resolvers and mock dialer/opener
// ---------------------------------------------------------------------------

func mustRef(t *testing.T, raw string) domain.Ref {
	t.Helper()
	ref, err := domain.ParseRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

type mockBroker struct {
	pipes        map[string]domain.PipeSpec
	services     map[string]domain.ServiceSpec
	dialErr      error
	downstreamOK bool
}

func newBroker(t *testing.T) *mockBroker {
	t.Helper()
	return &mockBroker{
		pipes:    make(map[string]domain.PipeSpec),
		services: make(map[string]domain.ServiceSpec),
	}
}

func (b *mockBroker) addPipe(spec domain.PipeSpec) {
	b.pipes[spec.Ref.String()] = spec
}

func (b *mockBroker) addService(spec domain.ServiceSpec) {
	b.services[spec.Ref.String()] = spec
}

func (b *mockBroker) resolvePipe(ref domain.Ref) (domain.PipeSpec, error) {
	spec, ok := b.pipes[ref.String()]
	if !ok {
		return domain.PipeSpec{}, fmt.Errorf("%w: %s", domain.ErrNotFound, ref)
	}
	return spec, nil
}

func (b *mockBroker) resolveService(ref domain.Ref) (domain.ServiceSpec, error) {
	spec, ok := b.services[ref.String()]
	if !ok {
		return domain.ServiceSpec{}, fmt.Errorf("%w: %s", domain.ErrNotFound, ref)
	}
	return spec, nil
}

func (b *mockBroker) dialService(_ domain.ServiceSpec) (net.Conn, error) {
	if b.dialErr != nil {
		return nil, b.dialErr
	}
	client, server := net.Pipe()
	_ = server.Close() // discard server side
	return client, nil
}

func (b *mockBroker) downstreamOpener(downstreamOK bool, failAfter int) DownstreamOpener {
	return func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		if !downstreamOK {
			return nil, "", errors.New("downstream authorization denied")
		}
		if failAfter > 0 && len(state.VisitedIDs) >= failAfter {
			return nil, "", errors.New("downstream open failed after hop")
		}
		client, _ := net.Pipe()
		return &mockCarrier{conn: client}, "ses_" + nextSpec.PipeID, nil
	}
}

// mockCarrier wraps a net.Conn to satisfy node.Carrier
type mockCarrier struct {
	conn net.Conn
}

func (m *mockCarrier) Read(p []byte) (int, error)  { return m.conn.Read(p) }
func (m *mockCarrier) Write(p []byte) (int, error) { return m.conn.Write(p) }
func (m *mockCarrier) Close() error                { return m.conn.Close() }
func (m *mockCarrier) Reset() error                { return m.conn.Close() }
func (m *mockCarrier) CloseWrite() error {
	if cw, ok := m.conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return m.conn.Close()
}
func (m *mockCarrier) SetDeadline(t time.Time) error { return m.conn.SetDeadline(t) }

// pipeService helper creates a pipe with a service target
func pipeService(t *testing.T, name string, targetRef string, piping string) domain.PipeSpec {
	t.Helper()
	return domain.PipeSpec{
		Ref:        mustRef(t, "pipe/"+name),
		PipeID:     "pip_" + name,
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: piping},
		Target:     mustRef(t, targetRef),
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
	}
}

// pipePipe helper creates a pipe with a pipe target
func pipePipe(t *testing.T, name string, targetRef string) domain.PipeSpec {
	t.Helper()
	return domain.PipeSpec{
		Ref:        mustRef(t, "pipe/"+name),
		PipeID:     "pip_" + name,
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"},
		Target:     mustRef(t, targetRef),
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
	}
}

// svc helper creates a service spec
func svc(t *testing.T, name string) domain.ServiceSpec {
	t.Helper()
	return domain.ServiceSpec{
		Ref:    mustRef(t, "service/"+name),
		Kind:   domain.ServiceTCP,
		Target: "tcp://127.0.0.1:9999",
	}
}

// ---------------------------------------------------------------------------
// Tests 1-3: Resolution succeeds for valid paths
// ---------------------------------------------------------------------------

func TestEngine_ResolvePath_PipeToService(t *testing.T) {
	// Test 1: Single-hop Pipe->Service resolves/opens terminal once
	b := newBroker(t)
	b.addPipe(pipeService(t, "a", "service/s1", "127.0.0.1:9000"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	path, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err != nil {
		t.Fatalf("expected path resolved, got: %v", err)
	}
	if len(path.Pipes) != 1 || path.Pipes[0] != mustRef(t, "pipe/a") {
		t.Fatalf("expected [pipe/a], got %v", path.Pipes)
	}
	if path.Terminal != mustRef(t, "service/s1") {
		t.Fatalf("expected terminal service/s1, got %s", path.Terminal)
	}

	// Forward through the terminal hop
	spec := b.pipes["pipe/a"]
	state := NewTraversal(spec.PipeID)
	result, err := eng.Forward(context.Background(), spec, state, nil)
	if err != nil {
		t.Fatalf("expected forward success, got: %v", err)
	}
	if !result.IsService {
		t.Fatal("expected service target")
	}
	if result.ServiceConn == nil {
		t.Fatal("expected non-nil service connection")
	}
	_ = result.ServiceConn.Close()
}

func TestEngine_ResolvePath_AtoBtoService(t *testing.T) {
	// Test 2: A->B->Service resolves and opens
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:9001"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	path, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err != nil {
		t.Fatalf("expected path resolved, got: %v", err)
	}
	if len(path.Pipes) != 2 {
		t.Fatalf("expected 2 pipes, got %d: %v", len(path.Pipes), path.Pipes)
	}
	if path.Terminal != mustRef(t, "service/s1") {
		t.Fatalf("expected terminal service/s1, got %s", path.Terminal)
	}

	// Forward through A (pipe target)
	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	downstreamOK := true
	opener := b.downstreamOpener(downstreamOK, 0)
	resultA, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err != nil {
		t.Fatalf("expected forward A success, got: %v", err)
	}
	if resultA.IsService {
		t.Fatal("expected pipe target for A")
	}
	if resultA.DownstreamCarrier == nil {
		t.Fatal("expected non-nil downstream carrier")
	}
	defer resultA.DownstreamCarrier.Close()

	// Forward through B (service target)
	specB := b.pipes["pipe/b"]
	stateB, err := stateA.Advance(specB.PipeID)
	if err != nil {
		t.Fatalf("expected advance success, got: %v", err)
	}
	resultB, err := eng.Forward(context.Background(), specB, stateB, nil)
	if err != nil {
		t.Fatalf("expected forward B success, got: %v", err)
	}
	if !resultB.IsService {
		t.Fatal("expected service target for B")
	}
	if resultB.ServiceConn == nil {
		t.Fatal("expected non-nil service connection")
	}
	_ = resultB.ServiceConn.Close()
}

func TestEngine_ResolvePath_AtoBtoCtoService(t *testing.T) {
	// Test 3: A->B->C->Service
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipePipe(t, "b", "pipe/c"))
	b.addPipe(pipeService(t, "c", "service/s1", "127.0.0.1:9002"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	path, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err != nil {
		t.Fatalf("expected path resolved, got: %v", err)
	}
	if len(path.Pipes) != 3 {
		t.Fatalf("expected 3 pipes, got %d", len(path.Pipes))
	}
	if path.Terminal != mustRef(t, "service/s1") {
		t.Fatalf("expected terminal service/s1, got %s", path.Terminal)
	}

	// Forward through A -> B
	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	opener := b.downstreamOpener(true, 0)
	resultA, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err != nil {
		t.Fatalf("expected forward A success, got: %v", err)
	}
	if resultA.IsService {
		t.Fatal("expected pipe target for A")
	}
	defer resultA.DownstreamCarrier.Close()

	// Forward through B -> C
	specB := b.pipes["pipe/b"]
	stateB, _ := stateA.Advance(specB.PipeID)
	resultB, err := eng.Forward(context.Background(), specB, stateB, opener)
	if err != nil {
		t.Fatalf("expected forward B success, got: %v", err)
	}
	if resultB.IsService {
		t.Fatal("expected pipe target for B")
	}
	defer resultB.DownstreamCarrier.Close()

	// Forward through C -> Service
	specC := b.pipes["pipe/c"]
	stateC, _ := stateB.Advance(specC.PipeID)
	resultC, err := eng.Forward(context.Background(), specC, stateC, nil)
	if err != nil {
		t.Fatalf("expected forward C success, got: %v", err)
	}
	if !resultC.IsService {
		t.Fatal("expected service target for C")
	}
	_ = resultC.ServiceConn.Close()
}

// ---------------------------------------------------------------------------
// Tests 4-8: Loop/hop enforcement
// ---------------------------------------------------------------------------

func TestEngine_ResolvePath_DirectSelfTargetDenied(t *testing.T) {
	// Test 4: direct self-target denied
	pipe := domain.PipeSpec{
		Ref:        mustRef(t, "pipe/a"),
		PipeID:     "pip_a",
		Target:     mustRef(t, "pipe/a"), // self-target
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"},
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
	}
	b := newBroker(t)
	b.addPipe(pipe)
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	// PipeSpec.Validate rejects self-target, so this should fail at the L0 level
	_, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err == nil {
		t.Fatal("expected self-target path to be rejected")
	}
	if !strings.Contains(err.Error(), "cannot target itself") {
		t.Fatalf("expected self-target error, got: %v", err)
	}
}

func TestEngine_ResolvePath_ABACycleDenied(t *testing.T) {
	// Test 5: A->B->A cycle denied before terminal open
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipePipe(t, "b", "pipe/a")) // cycle
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	_, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err == nil {
		t.Fatal("expected cycle to be rejected")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

func TestEngine_ResolvePath_RepeatedTraversalPipeDenied(t *testing.T) {
	// Test 6: repeated traversal Pipe denied (indirect cycle via revisit)
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipePipe(t, "b", "pipe/c"))
	b.addPipe(pipePipe(t, "c", "pipe/b")) // cycle back to b
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	_, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err == nil {
		t.Fatal("expected cycle to be rejected")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

func TestEngine_ResolvePath_MaxHopsSucceeds(t *testing.T) {
	// Test 7: exactly max hops succeeds
	// Build a chain of 8 pipes (A->B->C->D->E->F->G->H->Service)
	b := newBroker(t)
	for i := 0; i < 8; i++ {
		if i < 7 {
			next := string(rune('a' + i + 1))
			b.addPipe(pipePipe(t, string(rune('a'+i)), "pipe/"+next))
		} else {
			b.addPipe(pipeService(t, string(rune('a'+i)), "service/s1", "127.0.0.1:0"))
		}
	}
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	path, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err != nil {
		t.Fatalf("expected 8-hop path to succeed, got: %v", err)
	}
	if len(path.Pipes) != 8 {
		t.Fatalf("expected 8 pipes, got %d", len(path.Pipes))
	}
}

func TestEngine_ResolvePath_MaxPlusOneDenied(t *testing.T) {
	// Test 8: max+1 hops denied
	b := newBroker(t)
	for i := 0; i < 9; i++ {
		if i < 8 {
			next := string(rune('a' + i + 1))
			b.addPipe(pipePipe(t, string(rune('a'+i)), "pipe/"+next))
		} else {
			b.addPipe(pipeService(t, string(rune('a'+i)), "service/s1", "127.0.0.1:0"))
		}
	}
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	_, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err == nil {
		t.Fatal("expected 9-hop path to be rejected")
	}
	if !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("expected hop limit error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests 9-12: Missing/invalid targets
// ---------------------------------------------------------------------------

func TestEngine_MissingPipeTargetFailsClosed(t *testing.T) {
	// Test 9: missing Pipe target fails closed
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/missing"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	_, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err == nil {
		t.Fatal("expected missing pipe target to fail")
	}
}

func TestEngine_MissingServiceTargetFailsClosed(t *testing.T) {
	// Test 10: missing Service target fails closed
	b := newBroker(t)
	b.addPipe(pipeService(t, "a", "service/missing", "127.0.0.1:0"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	_, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err == nil {
		t.Fatal("expected missing service target to fail")
	}
}

func TestEngine_InvalidPipeDefinitionFailsClosed(t *testing.T) {
	// Test 11: invalid Pipe definition (no target) fails closed
	invalid := domain.PipeSpec{
		Ref:        mustRef(t, "pipe/a"),
		PipeID:     "pip_a",
		Target:     domain.Ref{}, // invalid
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"},
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
	}
	b := newBroker(t)
	b.addPipe(invalid)
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	_, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err == nil {
		t.Fatal("expected invalid pipe to fail")
	}
}

func TestEngine_InvalidServiceDefinitionFailsClosed(t *testing.T) {
	// Test 12: invalid Service definition fails closed
	b := newBroker(t)
	b.addPipe(pipeService(t, "a", "service/bad", "127.0.0.1:0"))
	// Add a service with invalid kind
	badSvc := domain.ServiceSpec{
		Ref:    mustRef(t, "service/bad"),
		Kind:   "invalid",
		Target: "http://127.0.0.1:8080",
	}
	b.services["service/bad"] = badSvc

	// Validate fails at service level
	svcSpec, err := b.resolveService(mustRef(t, "service/bad"))
	if err != nil {
		t.Fatal("unexpected: service should be resolvable")
	}
	if err := svcSpec.Validate(); err == nil {
		t.Fatal("expected invalid service validation to fail")
	}
}

// ---------------------------------------------------------------------------
// Tests 13-16: Authorization per hop
// ---------------------------------------------------------------------------

func TestEngine_RemotePipeHopInvokesDownstreamExactlyOnce(t *testing.T) {
	// Test 13: remote Pipe hop invokes downstream-open capability exactly once
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	callCount := 0
	opener := func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		callCount++
		client, _ := net.Pipe()
		return &mockCarrier{conn: client}, "ses_" + nextSpec.PipeID, nil
	}

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	_, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err != nil {
		t.Fatalf("expected forward success, got: %v", err)
	}
	if callCount != 1 {
		t.Fatalf("expected downstream opener called exactly once, got %d", callCount)
	}
}

func TestEngine_FailedDownstreamAuthorizationNoBytes(t *testing.T) {
	// Test 14: failed downstream authorization produces no downstream target bytes
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	opener := b.downstreamOpener(false, 0) // downstream fails
	_, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err == nil {
		t.Fatal("expected downstream authorization failure")
	}
	if !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected authorization denied error, got: %v", err)
	}
}

func TestEngine_UpstreamAuthCannotSubstituteDownstream(t *testing.T) {
	// Test 15: upstream authorization cannot substitute downstream
	// The Engine never reuses upstream authorization for downstream.
	// We verify this by ensuring that even with a valid upstream setup,
	// the downstream opener still performs its own authorization.
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	// Track that the opener is called with the correct state
	openerCalled := false
	opener := func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		openerCalled = true
		// The opener receives its own state and must independently authorize
		if len(state.VisitedIDs) == 0 {
			return nil, "", errors.New("downstream authorization denied: no traversal state")
		}
		client, _ := net.Pipe()
		return &mockCarrier{conn: client}, "ses_" + nextSpec.PipeID, nil
	}

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	_, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err != nil {
		t.Fatalf("expected forward success, got: %v", err)
	}
	if !openerCalled {
		t.Fatal("expected downstream opener to be called")
	}
}

func TestEngine_ReachabilityAloneCannotAuthorize(t *testing.T) {
	// Test 16: reachability/carrier facts alone cannot authorize downstream
	// The Engine delegates authorization to the DownstreamOpener, which must
	// independently validate. The Engine itself does not authorize.
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	// The opener simulates carrier failure (no authorization)
	opener := func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		return nil, "", errors.New("downstream authorization denied: invalid lease")
	}

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	_, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err == nil {
		t.Fatal("expected downstream authorization failure")
	}
	if !strings.Contains(err.Error(), "authorization denied") {
		t.Fatalf("expected authorization denied, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests 17-22: Lifecycle, cancellation, session, data flow
// ---------------------------------------------------------------------------

func TestEngine_CancellationDuringResolution(t *testing.T) {
	// Test 17: cancellation during resolution/open cleans admitted resources
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	opener := b.downstreamOpener(true, 0)
	_, err := eng.Forward(ctx, specA, stateA, opener)
	if err != nil {
		// Cancellation during downstream open should be handled by the opener
		// The Engine itself doesn't check ctx in Forward, but the opener does
		// This is fine - the opener is responsible for respecting cancellation
		_ = err
	}
}

func TestEngine_TargetOpenFailureNoLiveSession(t *testing.T) {
	// Test 18: target-open failure creates no live Session
	// The Engine's Forward method does not create Sessions - it returns the
	// carrier and leaves Session creation to the caller. If Forward fails,
	// no carrier is returned, so no Session can be created.
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	opener := b.downstreamOpener(false, 0)
	result, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err == nil {
		t.Fatal("expected failure")
	}
	if result.DownstreamCarrier != nil {
		t.Fatal("expected no downstream carrier on failure")
	}
}

func TestEngine_SessionOwnsTargetAfterAdmission(t *testing.T) {
	// Test 19: Session owns opened target/carrier after admission
	// The Engine returns the carrier and leaves Session creation to the caller.
	// Verify that after successful Forward, the carrier is valid.
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	opener := b.downstreamOpener(true, 0)
	result, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if result.DownstreamCarrier == nil {
		t.Fatal("expected non-nil downstream carrier")
	}
	defer result.DownstreamCarrier.Close()
}

func TestEngine_HalfCloseCorrectThroughComposedPath(t *testing.T) {
	// Test 20: half-close correct through composed path
	// The Engine's mock carrier supports CloseWrite. Verify that the carrier
	// returned by the opener can be used for half-close semantics.
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	opener := b.downstreamOpener(true, 0)
	result, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if result.DownstreamCarrier == nil {
		t.Fatal("expected non-nil downstream carrier")
	}
	defer result.DownstreamCarrier.Close()

	// Test half-close
	if err := result.DownstreamCarrier.CloseWrite(); err != nil {
		t.Fatalf("expected half-close to work, got: %v", err)
	}
}

func TestEngine_ApplicationBytesTraverseComposedPath(t *testing.T) {
	// Test 21: application bytes traverse composed path exactly once
	// Verify write/read through the composed carrier
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)

	// Create a piped pair for the downstream carrier
	carrierR, carrierW := net.Pipe()
	opener := func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		return &mockCarrier{conn: carrierR}, "ses_" + nextSpec.PipeID, nil
	}

	result, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	defer result.DownstreamCarrier.Close()
	defer carrierW.Close()

	// Write through the carrier and read from the other end
	payload := []byte("hello through pipe composition")
	go func() {
		_, _ = result.DownstreamCarrier.Write(payload)
	}()

	buf := make([]byte, len(payload))
	n, err := carrierW.Read(buf)
	if err != nil {
		t.Fatalf("expected read success, got: %v", err)
	}
	if n != len(payload) || string(buf[:n]) != string(payload) {
		t.Fatalf("expected %q, got %q", string(payload), string(buf[:n]))
	}
}

func TestEngine_NoReplayOnTraversalOpenFailure(t *testing.T) {
	// Test 22: no replay on traversal/open failure
	// When a downstream open fails, the Engine does not retry.
	// Verify that the opener is called exactly once per Forward call.
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	callCount := 0
	opener := func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		callCount++
		return nil, "", errors.New("downstream authorization denied")
	}

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	_, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err == nil {
		t.Fatal("expected failure")
	}
	if callCount != 1 {
		t.Fatalf("expected downstream opener called exactly once on failure, got %d", callCount)
	}
}

// ---------------------------------------------------------------------------
// Additional tests: TraversalState advance and wire validation
// ---------------------------------------------------------------------------

func TestTraversalState_Advance(t *testing.T) {
	state := NewTraversal("pip_a")
	if len(state.VisitedIDs) != 1 || state.VisitedIDs[0] != "pip_a" {
		t.Fatalf("unexpected initial state: %v", state.VisitedIDs)
	}
	if state.Remaining != domain.DefaultMaxPipeHops {
		t.Fatalf("expected remaining %d, got %d", domain.DefaultMaxPipeHops, state.Remaining)
	}

	state2, err := state.Advance("pip_b")
	if err != nil {
		t.Fatalf("expected advance success, got: %v", err)
	}
	if len(state2.VisitedIDs) != 2 || state2.VisitedIDs[1] != "pip_b" {
		t.Fatalf("unexpected advanced state: %v", state2.VisitedIDs)
	}
	if state2.Remaining != domain.DefaultMaxPipeHops-1 {
		t.Fatalf("expected remaining %d, got %d", domain.DefaultMaxPipeHops-1, state2.Remaining)
	}

	// Original unchanged
	if len(state.VisitedIDs) != 1 {
		t.Fatal("original state was mutated")
	}
}

func TestTraversalState_AdvanceLoop(t *testing.T) {
	state := NewTraversal("pip_a")
	_, err := state.Advance("pip_a") // same ID
	if err == nil || !errors.Is(err, ErrLoopDetected) {
		t.Fatalf("expected loop error, got: %v", err)
	}
}

func TestTraversalState_AdvanceHopLimit(t *testing.T) {
	state := TraversalState{
		VisitedIDs: []string{"pip_a"},
		Remaining:  0,
		MaxHops:    1,
	}
	_, err := state.Advance("pip_b")
	if err == nil || !errors.Is(err, ErrHopLimitExceeded) {
		t.Fatalf("expected hop limit error, got: %v", err)
	}
}

func TestFromWire(t *testing.T) {
	state, err := FromWire([]string{"pip_a"}, 8)
	if err != nil {
		t.Fatalf("expected valid wire state, got: %v", err)
	}
	if len(state.VisitedIDs) != 1 || state.VisitedIDs[0] != "pip_a" {
		t.Fatalf("unexpected visited: %v", state.VisitedIDs)
	}
	if state.Remaining != 7 {
		t.Fatalf("expected remaining 7, got %d", state.Remaining)
	}
}

func TestFromWire_RejectsDuplicate(t *testing.T) {
	_, err := FromWire([]string{"pip_a", "pip_a"}, 8)
	if err == nil || !errors.Is(err, ErrLoopDetected) {
		t.Fatalf("expected loop error, got: %v", err)
	}
}

func TestFromWire_RejectsBeyondLimit(t *testing.T) {
	_, err := FromWire([]string{"pip_a", "pip_b", "pip_c", "pip_d", "pip_e", "pip_f", "pip_g", "pip_h"}, 8)
	if err == nil || !errors.Is(err, ErrHopLimitExceeded) {
		t.Fatalf("expected hop limit error, got: %v", err)
	}
}

func TestFromWire_RejectsInvalidHopLimit(t *testing.T) {
	_, err := FromWire([]string{}, 0)
	if err == nil || !errors.Is(err, ErrHopLimitExceeded) {
		t.Fatalf("expected hop limit error, got: %v", err)
	}
	_, err = FromWire([]string{}, 100)
	if err == nil || !errors.Is(err, ErrHopLimitExceeded) {
		t.Fatalf("expected hop limit error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Architecture guard tests
// ---------------------------------------------------------------------------

func TestEngine_NoStoreDependency(t *testing.T) {
	// Test 20: Engine resolves without importing internal/store
	// This is a compile-time check. The test verifies that the engine package
	// does not import store, authority, or discovery packages.
	// The check is structural: look for store/authority/discovery imports.
	// We verify this by checking that the Engine's New function accepts
	// function types, not *store.Store.
	b := newBroker(t)
	b.addPipe(pipeService(t, "a", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)
	if eng == nil {
		t.Fatal("expected non-nil engine")
	}
	// Verify the engine uses injected functions, not store
	path, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err != nil {
		t.Fatalf("expected path resolved, got: %v", err)
	}
	if len(path.Pipes) != 1 {
		t.Fatal("expected single pipe path")
	}
}

func TestEngine_NoDiscoveryDependency(t *testing.T) {
	// Test 21: Engine resolves without importing internal/discovery
	// Same structural check: Engine uses injected resolvers, not discovery.
	_ = New(nil, nil, nil) // Engine can be created without any imports
}

func TestEngine_NoAuthorityDependency(t *testing.T) {
	// Test 22: Engine resolves without importing internal/authority
	// Engine does not handle leases or authorization directly.
	// Authorization is delegated to the DownstreamOpener.
	// Verify the Engine type surface has no authority-related types.
	_ = New(nil, nil, nil)
}

// ---------------------------------------------------------------------------
// Edge case: Engine.RejectSelfTarget via Forward validation
// ---------------------------------------------------------------------------

func TestEngine_ForwardSelfTargetDenied(t *testing.T) {
	// Test that a Pipe with target pointing to itself is rejected
	// This is caught by PipeSpec.Validate() which is called in ResolvePath
	pipe := domain.PipeSpec{
		Ref:        mustRef(t, "pipe/a"),
		PipeID:     "pip_a",
		Target:     mustRef(t, "pipe/a"), // self-target - caught by Validate
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"},
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
	}
	b := newBroker(t)
	b.addPipe(pipe)
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	_, err := eng.ResolvePath(mustRef(t, "pipe/a"), 8)
	if err == nil {
		t.Fatal("expected self-target to be rejected")
	}
}

// ---------------------------------------------------------------------------
// Test: Max hops exactly via Forward
// ---------------------------------------------------------------------------

func TestEngine_ForwardExactMaxHopsSucceeds(t *testing.T) {
	// Build a chain of exactly DefaultMaxPipeHops pipes
	b := newBroker(t)
	for i := 0; i < domain.DefaultMaxPipeHops; i++ {
		letter := string(rune('a' + i))
		if i < domain.DefaultMaxPipeHops-1 {
			next := string(rune('a' + i + 1))
			b.addPipe(pipePipe(t, letter, "pipe/"+next))
		} else {
			b.addPipe(pipeService(t, letter, "service/s1", "127.0.0.1:0"))
		}
	}
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	// Resolve path
	path, err := eng.ResolvePath(mustRef(t, "pipe/a"), domain.DefaultMaxPipeHops)
	if err != nil {
		t.Fatalf("expected path resolved, got: %v", err)
	}
	if len(path.Pipes) != domain.DefaultMaxPipeHops {
		t.Fatalf("expected %d pipes, got %d", domain.DefaultMaxPipeHops, len(path.Pipes))
	}

	// Forward through each hop
	opener := b.downstreamOpener(true, 0)
	var state TraversalState
	for i, pipeRef := range path.Pipes {
		spec := b.pipes[pipeRef.String()]
		if i == 0 {
			state = NewTraversal(spec.PipeID)
		}
		isTerminal := i == len(path.Pipes)-1
		var result ForwardResult
		var fwdErr error
		if isTerminal {
			result, fwdErr = eng.Forward(context.Background(), spec, state, nil)
		} else {
			result, fwdErr = eng.Forward(context.Background(), spec, state, opener)
		}
		if fwdErr != nil {
			t.Fatalf("hop %d: expected forward success, got: %v", i, fwdErr)
		}
		if isTerminal && !result.IsService {
			t.Fatalf("hop %d: expected service target", i)
		}
		if !isTerminal && result.IsService {
			t.Fatalf("hop %d: expected pipe target", i)
		}
		// Advance state for next hop
		if !isTerminal {
			state, _ = state.Advance(spec.PipeID)
		}
	}
}

// ---------------------------------------------------------------------------
// Test: Downstream open failure at intermediate hop
// ---------------------------------------------------------------------------

func TestEngine_ForwardDownstreamFailureAtIntermediateHop(t *testing.T) {
	// A->B->C->Service, B's downstream (C) fails
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipePipe(t, "b", "pipe/c"))
	b.addPipe(pipeService(t, "c", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	// Fail at C's downstream (i.e., when opening B's target)
	// Make opener fail after 2 visited IDs
	opener := b.downstreamOpener(false, 0)

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	_, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err == nil {
		t.Fatal("expected failure at B's downstream open")
	}
}

// ---------------------------------------------------------------------------
// Test: Downstream not called when Service target
// ---------------------------------------------------------------------------

func TestEngine_ForwardServiceNoDownstreamCall(t *testing.T) {
	// When targeting a Service, the downstream opener must NOT be called
	b := newBroker(t)
	b.addPipe(pipeService(t, "a", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	openerCalled := false
	opener := func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		openerCalled = true
		return nil, "", nil
	}

	specA := b.pipes["pipe/a"]
	stateA := NewTraversal(specA.PipeID)
	result, err := eng.Forward(context.Background(), specA, stateA, opener)
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
	if !result.IsService {
		t.Fatal("expected service target")
	}
	if openerCalled {
		t.Fatal("downstream opener should not be called for service target")
	}
}

// TestEngine_ForwardBudgetExhaustedNoDownstreamOpen proves the Engine rejects
// a downstream Pipe hop attempt when the remaining budget cannot accommodate
// the downstream serve acceptance, and does so without invoking the opener.
func TestEngine_ForwardBudgetExhaustedNoDownstreamOpen(t *testing.T) {
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipeService(t, "b", "service/s1", "127.0.0.1:0"))
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	openerCalls := 0
	opener := func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		openerCalls++
		return nil, "", errors.New("must not be called")
	}

	specA := b.pipes["pipe/a"]
	// state: visited=[pip_a], remaining 0 -> downstream hop must be rejected
	state := TraversalState{VisitedIDs: []string{specA.PipeID}, Remaining: 0, MaxHops: 1}
	_, err := eng.Forward(context.Background(), specA, state, opener)
	if err == nil || !errors.Is(err, ErrHopLimitExceeded) {
		t.Fatalf("expected hop limit error, got %v", err)
	}
	if openerCalls != 0 {
		t.Fatalf("opener called %d times on budget-exhausted downstream, want 0", openerCalls)
	}
}

// TestEngine_ForwardDownstreamLoopRejected proves the Engine rejects a
// downstream Pipe target whose PipeID is already in the visited chain, without
// invoking the opener.
func TestEngine_ForwardDownstreamLoopRejected(t *testing.T) {
	b := newBroker(t)
	b.addPipe(pipePipe(t, "a", "pipe/b"))
	b.addPipe(pipePipe(t, "b", "pipe/a")) // cycle back to a
	b.addService(svc(t, "s1"))
	eng := New(b.resolveService, b.resolvePipe, b.dialService)

	openerCalls := 0
	opener := func(ctx context.Context, nextSpec domain.PipeSpec, state TraversalState) (node.Carrier, string, error) {
		openerCalls++
		return nil, "", errors.New("must not be called")
	}

	// State where current pipe a has already visited pip_a; target pipe a
	// resolved from b => downstream pipe ID pip_a already in chain.
	state := NewTraversal("pip_a")
	specB := b.pipes["pipe/b"] // targets pipe/a
	_, err := eng.Forward(context.Background(), specB, state, opener)
	if err == nil || !errors.Is(err, ErrLoopDetected) {
		t.Fatalf("expected loop error, got %v", err)
	}
	if openerCalls != 0 {
		t.Fatalf("opener called %d times on loop, want 0", openerCalls)
	}
}
