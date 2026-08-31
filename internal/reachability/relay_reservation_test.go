package reachability

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// resFake is a deterministic stand-in for the L1 maintainer's transport
// seams: it controls relay connectedness, reservation success/failure, the
// relay-provided reservation expiration (TTL), and a manually advanced clock,
// and counts every maintenance (reservation) attempt.
type resFake struct {
	mu            sync.Mutex
	connected     bool
	failNext      bool
	maintainCalls int
	now           time.Time
	ttl           time.Duration
	ticks         chan time.Time
}

func newResFake(t *testing.T, ttl time.Duration) (*RelayReservation, *resFake) {
	t.Helper()
	f := &resFake{
		now:   time.Unix(0, 0),
		ttl:   ttl,
		ticks: make(chan time.Time, 16),
	}
	r := &RelayReservation{
		margin:   DefaultReservationMargin,
		minDelay: minimumRenewDelay,
		tick:     0,
		connected: func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.connected
		},
		maintain: func(context.Context) (time.Time, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.maintainCalls++
			if f.failNext {
				f.failNext = false
				return time.Time{}, errors.New("reserve relay circuit failed")
			}
			return f.now.Add(f.ttl), nil
		},
		now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.now
		},
		newTicks: func() (<-chan time.Time, func()) {
			return f.ticks, func() {}
		},
	}
	return r, f
}

func (f *resFake) setConnected(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.connected = v
}

func (f *resFake) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *resFake) setTTL(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ttl = d
}

func (f *resFake) setFail() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = true
}

func (f *resFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.maintainCalls
}

// pump pushes one maintenance-evaluation tick and lets the Serve goroutine
// process it. The tiny sleep is only to yield to the background goroutine; no
// real scheduling time is involved.
func pump(t *testing.T, f *resFake) {
	t.Helper()
	f.ticks <- time.Time{}
	time.Sleep(20 * time.Millisecond)
}

// serveRun records how many onReady/onFailure callbacks an owner received.
type serveRun struct {
	ready     int
	failures  int
	lastError error
	mu        sync.Mutex
}

func startServe(t *testing.T, r *RelayReservation) *serveRun {
	t.Helper()
	sr := &serveRun{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = r.Serve(ctx,
			func() { sr.mu.Lock(); sr.ready++; sr.mu.Unlock() },
			func(err error) { sr.mu.Lock(); sr.failures++; sr.lastError = err; sr.mu.Unlock() },
		)
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done })
	return sr
}

func (s *serveRun) readyCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ready
}

func (s *serveRun) failureCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures
}

// ---- Scheduling (relay-provided expiration drives renewal) ----

// TestReservationExpirationDrivesNextRenewal proves the relay-provided
// expiration, not a fixed renewal interval, is the scheduling fact: with a
// 5-minute TTL and a 1-minute margin, the next renewal is due at 4 minutes
// (expiration minus margin), and no reserve happens before that point.
func TestReservationExpirationDrivesNextRenewal(t *testing.T) {
	const ttl = 5 * time.Minute
	r, f := newResFake(t, ttl)
	f.setConnected(true)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	if got := f.calls(); got != 1 {
		t.Fatalf("initial maintenance calls = %d, want 1", got)
	}
	sr := startServe(t, r)

	// Well before expiration-minus-margin: no renewal.
	f.advance(3 * time.Minute)
	pump(t, f)
	if got := f.calls(); got != 1 {
		t.Fatalf("renewed at 3m before a 5m TTL (margin 1m): calls = %d, want 1", got)
	}
	if sr.readyCount() != 0 {
		t.Fatalf("no onReady expected before renewal, got %d", sr.readyCount())
	}

	// At expiration-minus-margin: renewal is due.
	f.advance(1 * time.Minute)
	pump(t, f)
	if got := f.calls(); got != 2 {
		t.Fatalf("renewal must fire at expiration-margin: calls = %d, want 2", got)
	}
	if sr.readyCount() != 1 {
		t.Fatalf("expected one onReady after renewal, got %d", sr.readyCount())
	}
}

// TestReservationRenewsBeforeExpiration proves renewal is scheduled before the
// relay-provided expiration, leaving the safety margin.
func TestReservationRenewsBeforeExpiration(t *testing.T) {
	const ttl = 5 * time.Minute
	r, f := newResFake(t, ttl)
	f.setConnected(true)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	sr := startServe(t, r)

	// Advance to just before the margin deadline; expiring cannot have passed.
	f.advance(4*time.Minute - 1*time.Second)
	pump(t, f)
	if got := f.calls(); got != 1 {
		t.Fatalf("renewed before the margin deadline: calls = %d, want 1", got)
	}

	// At the deadline, renew occurs with time to spare before expiration.
	f.advance(1 * time.Second)
	pump(t, f)
	if got := f.calls(); got != 2 {
		t.Fatalf("renewal must fire by expiration-margin: calls = %d, want 2", got)
	}
	if f.now.After(r.expires) {
		t.Fatalf("renewal happened after reservation had already expired (now=%v expires=%v)", f.now, r.expires)
	}
	if sr.readyCount() != 1 {
		t.Fatalf("expected one onReady after renewal, got %d", sr.readyCount())
	}
}

// TestReservationShorterTTLRenewsEarlier proves a shorter relay-provided TTL
// moves the renewal earlier (expiration minus margin), not earlier/later than
// the TTL difference would imply.
func TestReservationShorterTTLRenewsEarlier(t *testing.T) {
	// A 3-minute TTL must renew at 2 minutes (expiration minus 1-minute margin),
	// earlier than a 5-minute TTL which renews at 4 minutes.
	t.Helper()
	r3, f3 := newResFake(t, 3*time.Minute)
	f3.setConnected(true)
	if err := r3.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire 3m failed: %v", err)
	}
	sr3 := startServe(t, r3)
	f3.advance(2 * time.Minute) // 3m TTL margin point
	pump(t, f3)
	if got := sr3.readyCount(); got != 1 {
		t.Fatalf("3m TTL must renew at 2m: readyCount = %d, want 1", got)
	}

	r5, f5 := newResFake(t, 5*time.Minute)
	f5.setConnected(true)
	if err := r5.Acquire(context.Background()); err != nil {
		t.Fatalf("acquire 5m failed: %v", err)
	}
	sr5 := startServe(t, r5)
	// At the 3m-TTL margin point the 5m TTL must not yet renew.
	f5.advance(2 * time.Minute)
	pump(t, f5)
	if got := sr5.readyCount(); got != 0 {
		t.Fatalf("5m TTL must not renew at 2m: readyCount = %d, want 0", got)
	}
	// At its own margin point the 5m TTL renews.
	f5.advance(2 * time.Minute)
	pump(t, f5)
	if got := sr5.readyCount(); got != 1 {
		t.Fatalf("5m TTL must renew at 4m: readyCount = %d, want 1", got)
	}
}

// TestReservationReacquisitionUpdatesDeadline proves a reacquire that returns
// a different expiration replaces the previous scheduling deadline: after a
// 5-minute reservation is renewed at 4m to a 3-minute reservation, the next
// renewal is at 8m (4m + 3m - margin), not the old longer cadence.
func TestReservationReacquisitionUpdatesDeadline(t *testing.T) {
	r, f := newResFake(t, 5*time.Minute)
	f.setConnected(true)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	sr := startServe(t, r)

	// Renew at the 5m margin point, but the relay now returns a 3m TTL.
	f.advance(4 * time.Minute)
	f.setTTL(3 * time.Minute)
	pump(t, f)
	if got := f.calls(); got != 2 {
		t.Fatalf("expected one renewal at 4m, got %d calls", got)
	}

	// Old 5m schedule would renew next at 8m (4m + 4m); the new 3m TTL makes
	// the next renewal at 6m (renew at 4m + 3m - margin). Nothing happens in
	// between even though the old cadence is not yet reached.
	f.advance(2 * time.Minute) // now 6m: new deadline
	pump(t, f)
	if got := f.calls(); got != 3 {
		t.Fatalf("reacquisition must update the next deadline: calls = %d, want 3", got)
	}
	if sr.readyCount() != 2 {
		t.Fatalf("expected onReady after reacquisition-driven renewal, got %d", sr.readyCount())
	}
}

// TestReservationTooShortFailsClosedNotAfterExpiry proves the invariant that a
// renewal is never scheduled at or after the relay-provided expiration: a very
// short reservation (5s TTL, below the 30s renewal floor) fails closed with
// ErrReservationTooShort instead of scheduling a renewal that would land after
// the reservation expired — which would reintroduce NO_RESERVATION.
func TestReservationTooShortFailsClosedNotAfterExpiry(t *testing.T) {
	const tiny = 5 * time.Second
	r, f := newResFake(t, tiny)
	f.setConnected(true)

	err := r.Acquire(context.Background())
	if err == nil {
		t.Fatal("a reservation too short to renew before expiry must fail closed during acquire")
	}
	if !errors.Is(err, ErrReservationTooShort) {
		t.Fatalf("expected ErrReservationTooShort, got %v", err)
	}
	if r.Ready() {
		t.Fatal("must not be ready when renewal cannot be scheduled before expiration")
	}
	if got := f.calls(); got != 1 {
		t.Fatalf("acquire must attempt exactly one maintenance, got %d", got)
	}

	// Simulate the relay improving: a schedulable reservation is accepted and
	// its renewal is strictly before expiration.
	f.setTTL(5 * time.Minute)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("healthy reservation must acquire, got %v", err)
	}
	if !r.Ready() {
		t.Fatal("expected ready after healthy acquire")
	}
}

// TestReservationTooShortMidLifeDegradesNotAfterExpiry proves that when a
// connected relay starts returning reservations too short to renew before
// expiry, the maintainer surfaces a maintenance failure (degraded) instead of
// scheduling renewal after expiration, and recovers once the relay returns a
// schedulable reservation again.
func TestReservationTooShortMidLifeDegradesNotAfterExpiry(t *testing.T) {
	r, f := newResFake(t, 5*time.Minute)
	f.setConnected(true)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	sr := startServe(t, r)

	// Renewal due at 4m, but the relay now returns a 5s reservation.
	f.advance(4 * time.Minute)
	f.setTTL(5 * time.Second)
	pump(t, f)

	if sr.failureCount() != 1 {
		t.Fatalf("too-short reservation must surface onFailure, got %d", sr.failureCount())
	}
	if sr.lastError == nil || !errors.Is(sr.lastError, ErrReservationTooShort) {
		t.Fatalf("expected ErrReservationTooShort surfaced, got %v", sr.lastError)
	}
	if sr.readyCount() != 0 {
		t.Fatalf("no onReady expected for too-short reservation, got %d", sr.readyCount())
	}
	if r.Ready() {
		t.Fatal("ready must be revoked when renewal cannot be scheduled before expiry")
	}

	// No further attempts until the min-delay floor: no tight failure loop.
	pump(t, f)
	if got := f.calls(); got != 2 {
		t.Fatalf("no maintenance expected before min-delay floor, got %d calls", got)
	}

	// Relay returns a schedulable reservation; recovery restores ready.
	f.advance(30 * time.Second)
	f.setTTL(5 * time.Minute)
	pump(t, f)
	if sr.failureCount() != 1 {
		t.Fatalf("no new failure expected, got %d", sr.failureCount())
	}
	if sr.readyCount() != 1 {
		t.Fatalf("expected recovery onReady, got %d", sr.readyCount())
	}
	if !r.Ready() {
		t.Fatal("expected ready after recovery")
	}
}

// ---- Core connected-but-due regression ----

// TestReservationRenewsWhileStillConnected is the core regression: a relay
// that remains connected but whose reservation is due for renewal must have
// reservation acquisition attempted again. Relay connectedness is not treated
// as proof of reservation validity.
func TestReservationRenewsWhileStillConnected(t *testing.T) {
	const ttl = 5 * time.Minute
	r, f := newResFake(t, ttl)
	f.setConnected(true)
	ctx := context.Background()
	if err := r.Acquire(ctx); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	if got := f.calls(); got != 1 {
		t.Fatalf("initial maintenance calls = %d, want 1", got)
	}
	if !r.Ready() {
		t.Fatal("expected ready after successful acquire")
	}
	sr := startServe(t, r)

	// Tick while connected but renewal not due: no maintenance operation.
	pump(t, f)
	if got := f.calls(); got != 1 {
		t.Fatalf("unexpected maintenance while not due: calls = %d, want 1", got)
	}
	if sr.readyCount() != 0 {
		t.Fatalf("no onReady expected while not due, got %d", sr.readyCount())
	}
	if sr.failureCount() != 0 {
		t.Fatalf("no onFailure expected while not due, got %d", sr.failureCount())
	}

	// Relay still connected, but renewal is now due: acquisition is repeated.
	f.advance(4 * time.Minute)
	pump(t, f)
	if got := f.calls(); got != 2 {
		t.Fatalf("connected relay with due reservation must renew: calls = %d, want 2", got)
	}
	if sr.readyCount() != 1 {
		t.Fatalf("expected one onReady after renewal, got %d", sr.readyCount())
	}
	if !r.Ready() {
		t.Fatal("expected ready after successful renewal")
	}
}

// TestReservationNoUnnecessaryRenewal proves the inverse: a connected relay
// whose reservation is not due performs no reservation operation across ticks.
func TestReservationNoUnnecessaryRenewal(t *testing.T) {
	const ttl = 5 * time.Minute
	r, f := newResFake(t, ttl)
	f.setConnected(true)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	sr := startServe(t, r)

	// Several ticks pass, relay stays connected, renewal never becomes due.
	for i := 0; i < 5; i++ {
		pump(t, f)
		if f.calls() != 1 {
			t.Fatalf("maintenance calls = %d after %d ticks, want 1 (no renewal due)", f.calls(), i+1)
		}
	}
	if sr.readyCount() != 0 {
		t.Fatalf("no onReady expected, got %d", sr.readyCount())
	}
	if sr.failureCount() != 0 {
		t.Fatalf("no onFailure expected, got %d", sr.failureCount())
	}
}

// TestReservationDisconnectedReconnectsAndReacquires proves that a lost relay
// connection triggers reconnect and reservation reacquisition.
func TestReservationDisconnectedReconnectsAndReacquires(t *testing.T) {
	const ttl = time.Hour // huge: only connectivity can force maintenance
	r, f := newResFake(t, ttl)
	f.setConnected(true)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	if got := f.calls(); got != 1 {
		t.Fatalf("initial calls = %d, want 1", got)
	}
	sr := startServe(t, r)

	// Relay disconnects; next tick must reconnect + reacquire.
	f.setConnected(false)
	pump(t, f)
	if got := f.calls(); got != 2 {
		t.Fatalf("disconnected relay must reconnect+reacquire: calls = %d, want 2", got)
	}
	if sr.readyCount() != 1 {
		t.Fatalf("expected onReady after reconnect+reacquire, got %d", sr.readyCount())
	}
	if !r.Ready() {
		t.Fatal("expected ready after successful reconnect")
	}
}

// ---- Failure / recovery ----

// TestReservationFailureSurfacedToOwner proves a failed reservation operation
// is passed to the owner via onFailure, and ready is revoked.
func TestReservationFailureSurfacedToOwner(t *testing.T) {
	const ttl = 5 * time.Minute
	r, f := newResFake(t, ttl)
	f.setConnected(true)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	sr := startServe(t, r)

	f.advance(4 * time.Minute)
	f.setFail()
	pump(t, f)

	if sr.failureCount() != 1 {
		t.Fatalf("expected one onFailure, got %d", sr.failureCount())
	}
	if sr.lastError == nil || sr.lastError.Error() != "reserve relay circuit failed" {
		t.Fatalf("unexpected surfaced error: %v", sr.lastError)
	}
	if sr.readyCount() != 0 {
		t.Fatalf("no onReady expected on failure, got %d", sr.readyCount())
	}
	if r.Ready() {
		t.Fatal("ready must be false after failed maintenance")
	}
}

// TestReservationRecoveryAfterFailure proves that after a failed maintenance
// attempt, a later successful attempt restores usable reachability (onReady).
func TestReservationRecoveryAfterFailure(t *testing.T) {
	const ttl = 5 * time.Minute
	r, f := newResFake(t, ttl)
	f.setConnected(true)
	if err := r.Acquire(context.Background()); err != nil {
		t.Fatalf("initial acquire failed: %v", err)
	}
	sr := startServe(t, r)

	// First attempt fails.
	f.advance(4 * time.Minute)
	f.setFail()
	pump(t, f)
	if sr.failureCount() != 1 {
		t.Fatalf("expected initial failure, got %d", sr.failureCount())
	}

	// Later attempt succeeds: restoration to ready.
	pump(t, f)
	if sr.failureCount() != 1 {
		t.Fatalf("no new failure expected, got %d", sr.failureCount())
	}
	if sr.readyCount() != 1 {
		t.Fatalf("expected recovery onReady, got %d", sr.readyCount())
	}
	if !r.Ready() {
		t.Fatal("expected ready after recovery")
	}
}

// TestReservationAcquireFailureFailsClosed proves an owner cannot reach a
// ready state when the initial reservation acquisition fails.
func TestReservationAcquireFailureFailsClosed(t *testing.T) {
	const ttl = 5 * time.Minute
	r, f := newResFake(t, ttl)
	f.setConnected(true)
	f.setFail()
	if err := r.Acquire(context.Background()); err == nil {
		t.Fatal("expected acquire to fail when initial reservation fails")
	}
	if r.Ready() {
		t.Fatal("must not be ready when initial reservation acquisition failed")
	}
}
