package runtime

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/store"
)

const (
	ScopeSyncCadence        = time.Minute
	ScopeSyncLeaseTTL       = 45 * time.Second
	ScopeSyncRenewInterval  = 10 * time.Second
	ScopeSyncAttemptTimeout = 20 * time.Second
)

// SyncController owns periodic authority-2.1 cache reconciliation for one
// membership-backed runtime. Lease ownership is local scheduling only.
type SyncController struct {
	State           *store.Store
	Scope           string
	HolderRef       string
	ProcessIdentity string

	Cadence        time.Duration
	LeaseTTL       time.Duration
	RenewInterval  time.Duration
	AttemptTimeout time.Duration
	Now            func() time.Time
	Wait           func(context.Context, time.Duration) error
	Jitter         func(time.Duration) time.Duration
	Attempt        func(context.Context, store.ScopeSyncLease) error
}

func (c SyncController) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func waitScopeSync(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func boundedScopeSyncJitter(limit time.Duration) time.Duration {
	if limit <= 0 {
		return 0
	}
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return 0
	}
	return time.Duration(binary.LittleEndian.Uint64(raw[:]) % uint64(limit+1))
}

func (c SyncController) settings() (cadence, ttl, renew, timeout time.Duration, wait func(context.Context, time.Duration) error, jitter func(time.Duration) time.Duration, attempt func(context.Context, store.ScopeSyncLease) error, err error) {
	cadence, ttl, renew, timeout = c.Cadence, c.LeaseTTL, c.RenewInterval, c.AttemptTimeout
	if cadence == 0 {
		cadence = ScopeSyncCadence
	}
	if ttl == 0 {
		ttl = ScopeSyncLeaseTTL
	}
	if renew == 0 {
		renew = ScopeSyncRenewInterval
	}
	if timeout == 0 {
		timeout = ScopeSyncAttemptTimeout
	}
	if cadence <= 0 || ttl <= timeout || renew <= 0 || renew >= ttl || timeout <= 0 {
		err = errors.New("invalid scope sync controller bounds")
		return
	}
	wait, jitter = c.Wait, c.Jitter
	if wait == nil {
		wait = waitScopeSync
	}
	if jitter == nil {
		jitter = boundedScopeSyncJitter
	}
	attempt = c.Attempt
	if attempt == nil {
		attempt = func(ctx context.Context, lease store.ScopeSyncLease) error {
			_, err := FetchRecordsWithOptions(ctx, c.State, c.Scope, FetchRecordsOptions{ScopeSyncLease: &lease})
			return err
		}
	}
	if c.State == nil || c.Scope == "" || c.HolderRef == "" || c.ProcessIdentity == "" {
		err = errors.New("invalid scope sync controller identity")
	}
	return
}

// Run does not retain owner state before a successful lease commit. It runs no
// concurrent attempts and conditionally releases only its own fenced token.
func (c SyncController) Run(ctx context.Context) error {
	cadence, ttl, renew, timeout, wait, jitter, attempt, err := c.settings()
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		lease, acquired, acquireErr := c.State.AcquireScopeSyncLease(ctx, c.Scope, c.HolderRef, c.ProcessIdentity, c.now(), ttl)
		if acquireErr != nil {
			if err := wait(ctx, renew+jitter(renew/4)); err != nil {
				return nil
			}
			continue
		}
		if !acquired {
			delay := renew
			if observed, err := c.State.ScopeSyncLease(ctx, c.Scope); err == nil && observed.Active {
				remaining := observed.ExpiresAt.Sub(c.now())
				if remaining > 0 && remaining < delay {
					delay = remaining
				}
			}
			if err := wait(ctx, delay+jitter(renew/4)); err != nil {
				return nil
			}
			continue
		}
		owned := c.runOwner(ctx, lease, cadence, ttl, renew, timeout, wait, jitter, attempt)
		_, _ = c.State.ReleaseScopeSyncLease(context.Background(), lease)
		if !owned || ctx.Err() != nil {
			return nil
		}
	}
	return nil
}

func (c SyncController) runOwner(ctx context.Context, lease store.ScopeSyncLease, cadence, ttl, renew, timeout time.Duration, wait func(context.Context, time.Duration) error, jitter func(time.Duration) time.Duration, attempt func(context.Context, store.ScopeSyncLease) error) bool {
	due := c.now()
	for ctx.Err() == nil {
		now := c.now()
		if !now.Before(due) {
			if c.attempt(ctx, lease, ttl, renew, timeout, attempt) {
				return false
			}
			// Cadence begins after completion, not attempt start. A slow success
			// must never make due remain in past and trigger a hot retry loop.
			due = c.nextDue(ctx, c.now().Add(cadence+jitter(cadence/10)))
			continue
		}
		delay := due.Sub(now)
		if delay > renew {
			delay = renew
		}
		if err := wait(ctx, delay); err != nil {
			return false
		}
		if c.now().Before(due) {
			ok, err := c.State.RenewScopeSyncLease(ctx, lease, c.now(), ttl)
			if err != nil || !ok {
				return false
			}
		}
	}
	return false
}

// attempt returns true when controller lost lease and must stop before another dial.
func (c SyncController) attempt(parent context.Context, lease store.ScopeSyncLease, ttl, renew, timeout time.Duration, call func(context.Context, store.ScopeSyncLease) error) bool {
	attemptCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- call(attemptCtx, lease) }()
	ticker := time.NewTicker(renew)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return errors.Is(err, store.ErrScopeSyncLeaseLost)
		case <-parent.Done():
			cancel()
			<-done
			return true
		case <-attemptCtx.Done():
			<-done
			return false
		case <-ticker.C:
			ok, err := c.State.RenewScopeSyncLease(parent, lease, c.now(), ttl)
			if err != nil || !ok {
				cancel()
				<-done
				return true
			}
		}
	}
}

func (c SyncController) nextDue(ctx context.Context, fallback time.Time) time.Time {
	freshness, err := c.State.ScopeFreshness(ctx, c.Scope)
	if err == nil && freshness.NextRetry != nil && freshness.NextRetry.After(fallback) {
		return *freshness.NextRetry
	}
	return fallback
}

// StartScopeSync starts only a real membership holder. Local authorities never
// self-sync or mint membership tokens; authority state remains source-of-truth.
func StartScopeSync(ctx context.Context, state *store.Store, scope, holderRef string) (func(), error) {
	if _, err := state.Membership(ctx, scope); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return func() {}, nil
		}
		return nil, err
	}
	run, err := state.Run(ctx, holderRef)
	if err != nil {
		return nil, err
	}
	controllerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = (SyncController{State: state, Scope: scope, HolderRef: holderRef, ProcessIdentity: run.ProcessIdentity}).Run(controllerCtx)
	}()
	return func() {
		cancel()
		<-done
	}, nil
}
