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

// errPipeIDDuplicate mirrors the Store's UNIQUE(pipe_id) constraint: a new
// ref may never reuse an identity another ref already owns.
var errPipeIDDuplicate = errors.New("pipe_id duplicate")

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

// ---- RemotePipeBinding fake ----

// memoryRemotePipeBindingRepo is an in-memory fake for RemotePipeBindingRepo.
type memoryRemotePipeBindingRepo struct {
	mu    sync.Mutex
	items map[string]domain.RemotePipeBinding
}

func newMemoryRemotePipeBindingRepo() *memoryRemotePipeBindingRepo {
	return &memoryRemotePipeBindingRepo{items: make(map[string]domain.RemotePipeBinding)}
}

func (r *memoryRemotePipeBindingRepo) Get(_ context.Context, remotePipeID string) (domain.RemotePipeBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.items[remotePipeID]
	if !ok {
		return domain.RemotePipeBinding{}, domain.ErrNotFound
	}
	return b, nil
}

func (r *memoryRemotePipeBindingRepo) Put(_ context.Context, b domain.RemotePipeBinding) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := b.Validate(); err != nil {
		return err
	}
	// Accepting twice must not create a second competing binding row.
	r.items[b.RemotePipeID] = b
	return nil
}

func (r *memoryRemotePipeBindingRepo) Delete(_ context.Context, remotePipeID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.items, remotePipeID)
	return nil
}

// memoryAccessLeaseCacheRepo is an in-memory fake for AccessLeaseCacheRepo.
// Expired cached material fails closed (treated as absent) exactly like the
// Store adapter; the cache is never the source of truth for the relationship.
type memoryAccessLeaseCacheRepo struct {
	mu    sync.Mutex
	items map[string]domain.AccessLeaseCache
	now   func() time.Time
}

func newMemoryAccessLeaseCacheRepo() *memoryAccessLeaseCacheRepo {
	return &memoryAccessLeaseCacheRepo{items: make(map[string]domain.AccessLeaseCache), now: time.Now}
}

func (r *memoryAccessLeaseCacheRepo) setNow(now func() time.Time) {
	r.mu.Lock()
	r.now = now
	r.mu.Unlock()
}

func (r *memoryAccessLeaseCacheRepo) Get(_ context.Context, remotePipeID string) (domain.AccessLeaseCache, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.items[remotePipeID]
	if !ok {
		return domain.AccessLeaseCache{}, domain.ErrNotFound
	}
	if !c.ExpiresAt.After(r.now().UTC()) {
		delete(r.items, remotePipeID)
		return domain.AccessLeaseCache{}, domain.ErrNotFound
	}
	return c, nil
}

func (r *memoryAccessLeaseCacheRepo) Put(_ context.Context, c domain.AccessLeaseCache) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c.RemotePipeID == "" || c.AccessLease == "" || c.ExpiresAt.IsZero() {
		return errors.New("invalid access lease cache: missing required fields")
	}
	r.items[c.RemotePipeID] = c
	return nil
}

func (r *memoryAccessLeaseCacheRepo) Delete(_ context.Context, remotePipeID string) error {
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
	// Mirror the Store's UNIQUE(pipe_id) constraint: a new ref may not reuse
	// an identity another ref already owns.
	if spec.PipeID != "" {
		if ownerRef, taken := r.pipeIDs[spec.PipeID]; taken && ownerRef != ref {
			return false, errPipeIDDuplicate
		}
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

// ---- Fake owner locator ----

type fakeOwnerLocator struct {
	address string
	err     error
	calls   int
	ref     domain.Ref
	pipeID  string
}

func (l *fakeOwnerLocator) LocateOwner(_ context.Context, ref domain.Ref, pipeID string) (string, error) {
	l.calls++
	l.ref = ref
	l.pipeID = pipeID
	return l.address, l.err
}

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

// errInviteRedeemed mirrors the store's one-winner redemption denial: a
// different recipient can never consume an already-accepted invite.
var errInviteRedeemed = errors.New("invite already redeemed by another client")

// fakeRedemptionService mints an AccessLease by loading the authority key
// that ShareExact already provisioned at the canonical key path. It enforces
// the one-winner atomic redemption contract: first recipient wins; a different
// recipient is denied; same-recipient retry is idempotent (mints a fresh
// lease).
type fakeRedemptionService struct {
	secretsDir string
	mu         sync.Mutex
	winners    map[string]string // jti/pipe -> recipientPublic
	calls      int
	now        func() time.Time
}

func newFakeRedemptionService(secretsDir string) *fakeRedemptionService {
	return &fakeRedemptionService{secretsDir: secretsDir, winners: make(map[string]string), now: time.Now}
}

func (s *fakeRedemptionService) setNow(now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

func (s *fakeRedemptionService) Redeem(_ context.Context, _ Carrier, invite accesslease.ExactInvite, recipientPublic string) (accesslease.AccessLease, error) {
	key := invite.JTI + "/" + invite.AudiencePipeID
	s.mu.Lock()
	s.calls++
	if winner, ok := s.winners[key]; ok && winner != recipientPublic {
		s.mu.Unlock()
		return accesslease.AccessLease{}, errInviteRedeemed
	}
	s.winners[key] = recipientPublic
	now := s.now
	s.mu.Unlock()
	keyPath := filepath.Join(s.secretsDir, "exact-authorities", invite.AudiencePipeID+".key")
	authPrivate, err := localkey.Provision(keyPath)
	if err != nil {
		return accesslease.AccessLease{}, err
	}
	lease, err := accesslease.NewAccessLease(authPrivate, recipientPublic, invite.AudiencePipeID, now().UTC(), 10*time.Minute)
	if err != nil {
		return accesslease.AccessLease{}, err
	}
	return lease, nil
}

// ---- Fake renewal service ----

// fakeRenewalService mints a fresh short-lived lease from the same authority
// key as redemption (post-acceptance reissuance). Tests inject failures via
// err to cover revocation/epoch/possession failures at the use-case boundary.
type fakeRenewalService struct {
	secretsDir string
	mu         sync.Mutex
	err        error
	calls      map[string]int
	leaseTTL   time.Duration
	now        func() time.Time
	// subjectOverride, when non-empty, mints leases for a different subject
	// than requested (tests the local verification fail-closed path).
	subjectOverride string
}

func newFakeRenewalService(secretsDir string) *fakeRenewalService {
	return &fakeRenewalService{secretsDir: secretsDir, calls: make(map[string]int), leaseTTL: 10 * time.Minute, now: time.Now}
}

func (s *fakeRenewalService) setNow(now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

func (s *fakeRenewalService) fail(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}

func (s *fakeRenewalService) callCount(pipeID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[pipeID]
}

func (s *fakeRenewalService) setTTL(ttl time.Duration) {
	s.mu.Lock()
	s.leaseTTL = ttl
	s.mu.Unlock()
}

func (s *fakeRenewalService) Renew(_ context.Context, _ Carrier, req RenewalRequest) (accesslease.AccessLease, error) {
	s.mu.Lock()
	s.calls[req.PipeID]++
	err := s.err
	ttl := s.leaseTTL
	subject := s.subjectOverride
	now := s.now
	s.mu.Unlock()
	if err != nil {
		return accesslease.AccessLease{}, err
	}
	if subject == "" {
		subject = req.RecipientPublic
	}
	keyPath := filepath.Join(s.secretsDir, "exact-authorities", req.PipeID+".key")
	authPrivate, err := localkey.Provision(keyPath)
	if err != nil {
		return accesslease.AccessLease{}, err
	}
	return accesslease.NewAccessLease(authPrivate, subject, req.PipeID, now().UTC(), ttl)
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
