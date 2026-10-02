package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/origama/tubo/internal/store"
)

func TestSyncControllerElectsOneOwnerWithoutDuplicateAttempts(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	called := make(chan struct{}, 1)
	controller := func(ref, identity string) SyncController {
		return SyncController{State: state, Scope: "solar/default", HolderRef: ref, ProcessIdentity: identity, Cadence: time.Hour, LeaseTTL: 100 * time.Millisecond, RenewInterval: 20 * time.Millisecond, AttemptTimeout: 50 * time.Millisecond, Attempt: func(context.Context, store.ScopeSyncLease) error {
			attempts.Add(1)
			select {
			case called <- struct{}{}:
			default:
			}
			return nil
		}}
	}
	doneA, doneB := make(chan struct{}), make(chan struct{})
	go func() { defer close(doneA); _ = controller("pipe/a", "proc-a").Run(ctx) }()
	go func() { defer close(doneB); _ = controller("pipe/b", "proc-b").Run(ctx) }()
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("owner did not attempt")
	}
	cancel()
	<-doneA
	<-doneB
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts=%d, want one elected owner", got)
	}
}

func TestSyncControllerHonorsPersistedRetryDeadline(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	waiting := make(chan time.Duration, 1)
	controller := SyncController{State: state, Scope: "solar/default", HolderRef: "pipe/a", ProcessIdentity: "proc-a", Cadence: time.Second, LeaseTTL: time.Minute, RenewInterval: time.Second, AttemptTimeout: 10 * time.Second, Jitter: func(time.Duration) time.Duration { return 0 }, Wait: func(ctx context.Context, delay time.Duration) error {
		waiting <- delay
		<-ctx.Done()
		return ctx.Err()
	}, Attempt: func(ctx context.Context, lease store.ScopeSyncLease) error {
		if attempts.Add(1) != 1 {
			return errors.New("unexpected retry")
		}
		next := time.Now().UTC().Add(time.Hour)
		return state.RecordSnapshotFailureWithLease(ctx, lease, store.SnapshotSourcePinnedAuthority, "authority_unreachable", &next)
	}}
	done := make(chan struct{})
	go func() { defer close(done); _ = controller.Run(ctx) }()
	select {
	case delay := <-waiting:
		if delay != time.Second {
			t.Fatalf("retry wait=%s, want renewal interval", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("controller did not wait for persisted retry deadline")
	}
	cancel()
	<-done
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts=%d, retry deadline did not suppress dial", got)
	}
}

func TestSyncControllerSchedulesCadenceAfterSlowSuccess(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := time.Now().UTC()
	current := base
	var clockMu sync.Mutex
	now := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return current }
	advance := func(d time.Duration) { clockMu.Lock(); current = current.Add(d); clockMu.Unlock() }
	started, release := make(chan struct{}), make(chan struct{})
	waiting := make(chan time.Duration, 1)
	var attempts atomic.Int32
	controller := SyncController{State: state, Scope: "solar/default", HolderRef: "pipe/a", ProcessIdentity: "proc-a", Cadence: 10 * time.Second, LeaseTTL: 2 * time.Minute, RenewInterval: 5 * time.Second, AttemptTimeout: time.Minute, Now: now, Jitter: func(time.Duration) time.Duration { return 0 }, Wait: func(ctx context.Context, delay time.Duration) error {
		waiting <- delay
		<-ctx.Done()
		return ctx.Err()
	}, Attempt: func(context.Context, store.ScopeSyncLease) error {
		if attempts.Add(1) != 1 {
			return errors.New("hot loop after slow success")
		}
		close(started)
		<-release
		return nil
	}}
	done := make(chan struct{})
	go func() { defer close(done); _ = controller.Run(ctx) }()
	<-started
	advance(30 * time.Second)
	close(release)
	select {
	case delay := <-waiting:
		if delay != 5*time.Second {
			t.Fatalf("first post-success wait=%s, want renewal interval", delay)
		}
	case <-time.After(time.Second):
		t.Fatal("slow success caused immediate second attempt")
	}
	cancel()
	<-done
	if got := attempts.Load(); got != 1 {
		t.Fatalf("attempts=%d, want one", got)
	}
}

func TestSyncControllerCrashExpiryTakeoverRecovers(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	// Simulate crashed owner: committed lease, no controller and no release.
	if _, acquired, err := state.AcquireScopeSyncLease(ctx, "solar/default", "pipe/crashed", "proc-crashed", time.Now().UTC(), 100*time.Millisecond); err != nil || !acquired {
		t.Fatalf("crashed owner lease: acquired=%t err=%v", acquired, err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	recovered := make(chan struct{})
	controller := SyncController{State: state, Scope: "solar/default", HolderRef: "pipe/successor", ProcessIdentity: "proc-successor", Cadence: time.Hour, LeaseTTL: time.Second, RenewInterval: 10 * time.Millisecond, AttemptTimeout: 100 * time.Millisecond, Attempt: func(context.Context, store.ScopeSyncLease) error {
		close(recovered)
		return nil
	}}
	done := make(chan struct{})
	go func() { defer close(done); _ = controller.Run(runCtx) }()
	select {
	case <-recovered:
		lease, err := state.ScopeSyncLease(ctx, "solar/default")
		if err != nil || !lease.Active || lease.HolderRef != "pipe/successor" {
			t.Fatalf("takeover status: %#v %v", lease, err)
		}
	case <-time.After(time.Second):
		t.Fatal("successor did not recover after crashed lease expiry")
	}
	cancel()
	<-done
}

func TestSyncControllerRenewalLossCancelsAttempt(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	lease, acquired, err := state.AcquireScopeSyncLease(ctx, "solar/default", "pipe/a", "proc-a", time.Now().UTC(), time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire: %t %v", acquired, err)
	}
	started, canceled := make(chan struct{}), make(chan struct{})
	controller := SyncController{State: state, Scope: lease.Scope, HolderRef: lease.HolderRef, ProcessIdentity: lease.ProcessIdentity}
	result := make(chan bool, 1)
	go func() {
		result <- controller.attempt(ctx, lease, time.Second, 10*time.Millisecond, time.Second, func(ctx context.Context, _ store.ScopeSyncLease) error {
			close(started)
			<-ctx.Done()
			close(canceled)
			return ctx.Err()
		})
	}()
	<-started
	if ok, err := state.ReleaseScopeSyncLease(ctx, lease); err != nil || !ok {
		t.Fatalf("release: %t %v", ok, err)
	}
	select {
	case lost := <-result:
		if !lost {
			t.Fatal("renewal loss did not stop owner")
		}
	case <-time.After(time.Second):
		t.Fatal("renewal loss did not cancel attempt")
	}
	select {
	case <-canceled:
	default:
		t.Fatal("attempt context not canceled")
	}
}

func TestStartScopeSyncSkipsLocalAuthorityWithoutMembership(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.PutScope(ctx, store.Scope{Name: "solar/default"}); err != nil {
		t.Fatal(err)
	}
	if err := state.PutRun(ctx, store.Run{PrimaryRef: "authority/solar", Kind: "authority", PID: 0, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	stop, err := StartScopeSync(ctx, state, "solar/default", "authority/solar")
	if err != nil {
		t.Fatal(err)
	}
	stop()
	lease, err := state.ScopeSyncLease(ctx, "solar/default")
	if err != nil || lease.Active {
		t.Fatalf("local authority self-sync lease: %#v %v", lease, err)
	}
}

func TestSyncControllerDeadlineBoundsAttempt(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := store.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	controller := SyncController{State: state, Scope: "solar/default", HolderRef: "pipe/a", ProcessIdentity: "proc-a", Cadence: time.Hour, LeaseTTL: 100 * time.Millisecond, RenewInterval: 20 * time.Millisecond, AttemptTimeout: 25 * time.Millisecond, Attempt: func(ctx context.Context, _ store.ScopeSyncLease) error {
		<-ctx.Done()
		close(finished)
		return ctx.Err()
	}}
	done := make(chan struct{})
	go func() { defer close(done); _ = controller.Run(ctx) }()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("attempt exceeded outer deadline")
	}
	cancel()
	<-done
}
