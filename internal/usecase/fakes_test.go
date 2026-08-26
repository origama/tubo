// Package usecase test helpers — in-memory fakes and deterministic test utilities.
package usecase

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/localkey"
)

var errConflict = errors.New("conflict: resource already exists with different specification")

// ---- Service repo fake ----

type memoryServiceRepo struct {
	mu    sync.Mutex
	items map[string]domain.ServiceSpec
}

func newMemoryServiceRepo() *memoryServiceRepo {
	return &memoryServiceRepo{items: make(map[string]domain.ServiceSpec)}
}

func (r *memoryServiceRepo) Upsert(_ context.Context, spec domain.ServiceSpec) (bool, error) {
	if err := spec.Validate(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := spec.Ref.String()
	existing, ok := r.items[key]
	if ok {
		if reflect.DeepEqual(existing, spec) {
			return false, nil
		}
		return false, errConflict
	}
	r.items[key] = spec
	return true, nil
}

func (r *memoryServiceRepo) Get(_ context.Context, ref domain.Ref) (domain.ServiceSpec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	spec, ok := r.items[ref.String()]
	if !ok {
		return domain.ServiceSpec{}, domain.ErrNotFound
	}
	return spec, nil
}

func (r *memoryServiceRepo) Delete(_ context.Context, ref domain.Ref) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := ref.String()
	if _, ok := r.items[key]; !ok {
		return domain.ErrNotFound
	}
	delete(r.items, key)
	return nil
}

// ---- Pipe repo fake ----

// memoryExactConnectionRepo is an in-memory fake for ExactConnectionStateRepo.
type memoryExactConnectionRepo struct {
	mu    sync.Mutex
	items map[string]domain.ExactConnectionState
}

func newMemoryExactConnectionRepo() *memoryExactConnectionRepo {
	return &memoryExactConnectionRepo{items: make(map[string]domain.ExactConnectionState)}
}

func (r *memoryExactConnectionRepo) Get(_ context.Context, remotePipeID string) (domain.ExactConnectionState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	state, ok := r.items[remotePipeID]
	if !ok {
		return domain.ExactConnectionState{}, domain.ErrNotFound
	}
	// Expired material fails closed.
	if !state.ExpiresAt.IsZero() && !state.ExpiresAt.After(time.Now().UTC()) {
		delete(r.items, remotePipeID)
		return domain.ExactConnectionState{}, domain.ErrNotFound
	}
	return state, nil
}

func (r *memoryExactConnectionRepo) Put(_ context.Context, state domain.ExactConnectionState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if state.RemotePipeID == "" || state.AccessLease == "" || state.Locator == "" {
		return errors.New("invalid exact connection state: missing required fields")
	}
	r.items[state.RemotePipeID] = state
	return nil
}

func (r *memoryExactConnectionRepo) Delete(_ context.Context, remotePipeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, remotePipeID)
	return nil
}

// ---- Pipe repo fake ----

type memoryPipeRepo struct {
	mu        sync.Mutex
	specs     map[string]domain.PipeSpec
	ownerKeys map[string]string // ref -> key file path
	pipeIDs   map[string]string // pipeID -> ref
}

func newMemoryPipeRepo() *memoryPipeRepo {
	return &memoryPipeRepo{
		specs:     make(map[string]domain.PipeSpec),
		ownerKeys: make(map[string]string),
		pipeIDs:   make(map[string]string),
	}
}

func (r *memoryPipeRepo) Upsert(_ context.Context, spec domain.PipeSpec, ownerKeyFile string) (bool, error) {
	if err := spec.Validate(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ref := spec.Ref.String()
	existing, ok := r.specs[ref]
	if ok {
		if existing.PipeID != spec.PipeID || existing.Target != spec.Target || existing.Ingress != spec.Ingress {
			return false, errConflict
		}
		if ownerKeyFile != "" {
			r.ownerKeys[ref] = ownerKeyFile
		}
		return false, nil
	}
	r.specs[ref] = spec
	if ownerKeyFile != "" {
		r.ownerKeys[ref] = ownerKeyFile
	}
	if spec.PipeID != "" {
		r.pipeIDs[spec.PipeID] = ref
	}
	return true, nil
}

func (r *memoryPipeRepo) Get(_ context.Context, identifier string) (domain.PipeSpec, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	spec, ok := r.specs[identifier]
	if ok {
		return spec, r.ownerKeys[identifier], nil
	}
	ref, ok := r.pipeIDs[identifier]
	if ok {
		spec = r.specs[ref]
		return spec, r.ownerKeys[ref], nil
	}
	for storedRef, storedSpec := range r.specs {
		if storedSpec.PipeID == identifier {
			return storedSpec, r.ownerKeys[storedRef], nil
		}
	}
	return domain.PipeSpec{}, "", domain.ErrNotFound
}

func (r *memoryPipeRepo) Delete(_ context.Context, ref string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.specs[ref]
	if !ok {
		return domain.ErrNotFound
	}
	delete(r.specs, ref)
	delete(r.ownerKeys, ref)
	if existing.PipeID != "" {
		delete(r.pipeIDs, existing.PipeID)
	}
	return nil
}

// ---- Fake carrier (in-memory pipe) ----

type fakeCarrier struct {
	r chan []byte
	w chan []byte
}

func newFakeCarrier() *fakeCarrier {
	return &fakeCarrier{r: make(chan []byte, 64), w: make(chan []byte, 64)}
}

func (c *fakeCarrier) Read(p []byte) (int, error) {
	data := <-c.r
	n := copy(p, data)
	return n, nil
}

func (c *fakeCarrier) Write(p []byte) (int, error) {
	data := make([]byte, len(p))
	copy(data, p)
	c.w <- data
	return len(p), nil
}

func (c *fakeCarrier) Close() error { return nil }

// ---- Fake reachability ----

type fakeReachability struct {
	mu   sync.Mutex
	conn map[string]*fakeCarrier
}

func newFakeReachability() *fakeReachability {
	return &fakeReachability{conn: make(map[string]*fakeCarrier)}
}

func (r *fakeReachability) Acquire(_ context.Context, address string) (Carrier, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := newFakeCarrier()
	r.conn[address] = c
	return c, nil
}

// ---- Fake redemption service ----

// fakeRedemptionService mints an AccessLease by loading the authority key
// that ShareExact already provisioned at the canonical key path.
type fakeRedemptionService struct {
	secretsDir string
}

func newFakeRedemptionService(secretsDir string) *fakeRedemptionService {
	return &fakeRedemptionService{secretsDir: secretsDir}
}

func (s *fakeRedemptionService) Redeem(_ context.Context, _ Carrier, invite accesslease.ExactInvite, recipientPublic string) (accesslease.AccessLease, error) {
	keyPath := filepath.Join(s.secretsDir, "exact-authorities", invite.AudiencePipeID+".key")
	authPrivate, err := localkey.Provision(keyPath)
	if err != nil {
		return accesslease.AccessLease{}, err
	}
	lease, err := accesslease.NewAccessLease(authPrivate, recipientPublic, invite.AudiencePipeID, time.Now().UTC(), 10*time.Minute)
	if err != nil {
		return accesslease.AccessLease{}, err
	}
	return lease, nil
}

// ---- Fake pipe starter ----

type fakePipeStarter struct {
	mu      sync.Mutex
	started map[string]domain.PipeSpec
}

func newFakePipeStarter() *fakePipeStarter {
	return &fakePipeStarter{started: make(map[string]domain.PipeSpec)}
}

func (s *fakePipeStarter) Start(_ context.Context, spec domain.PipeSpec) (StartPipeResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started[spec.Ref.String()] = spec
	return StartPipeResult{PipeRef: spec.Ref, Status: "ready"}, nil
}

// ---- Clock helpers ----

func fixedClock(now time.Time) func() time.Time {
	return func() time.Time { return now.UTC() }
}

// ---- Must-create helper for tests ----

func mustService(ctx context.Context, t testTB, repo ServiceRepository, name, target string) domain.Ref {
	ref, _ := domain.NewRef(domain.KindService, name)
	spec, err := serviceSpecFromTarget(name, target)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.Upsert(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func mustPipe(ctx context.Context, t testTB, pipes PipeRepository, ref domain.Ref, target domain.Ref) domain.PipeSpec {
	now := time.Now()
	spec := domain.PipeSpec{Ref: ref, PipeID: "pip_" + ref.Name, Target: target, Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"}, Visibility: domain.VisibilityPrivate, Access: domain.AccessInviteOnly, CreatedAt: now, UpdatedAt: now}
	_, err := pipes.Upsert(ctx, spec, "/fake/key")
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// testTB is a subset of testing.TB for use by helper functions.
type testTB interface {
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	Helper()
}
