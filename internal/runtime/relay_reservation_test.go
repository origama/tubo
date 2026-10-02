package runtime

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/origama/tubo/internal/store"
)

// mustPutRun persists a run row and returns a pointer to a fresh in-memory
// snapshot so tests can observe the persisted lifecycle status.
func mustPutRun(t *testing.T, state *store.Store, status string) *store.Run {
	t.Helper()
	run := &store.Run{PrimaryRef: "pipe/test", Kind: "pipe", Status: status, PeerID: "test-peer"}
	if err := state.PutRun(context.Background(), *run); err != nil {
		t.Fatal(err)
	}
	return run
}

func currentRunStatus(t *testing.T, state *store.Store) string {
	t.Helper()
	run, err := state.Run(context.Background(), "pipe/test")
	if err != nil {
		t.Fatal(err)
	}
	return run.Status
}

// TestPublishedPipeLifecycleFailureToDegraded proves the runtime's wiring maps
// an L1 maintenance failure to the existing degraded Run status.
func TestPublishedPipeLifecycleFailureToDegraded(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	run := mustPutRun(t, state, "ready")

	_, onFailure := publishedPipeLifecycle(state, run, "pipe/test", "relay-id", nil)
	onFailure(errors.New("reserve relay circuit failed"))

	if got := currentRunStatus(t, state); got != "degraded" {
		t.Fatalf("after maintenance failure run status = %q, want degraded", got)
	}
	if run.Status != "degraded" {
		t.Fatalf("in-memory run status = %q, want degraded", run.Status)
	}
}

// TestPublishedPipeLifecycleRecoveryToReady proves a later successful
// maintenance restores ready after a failure (degraded -> ready).
func TestPublishedPipeLifecycleRecoveryToReady(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	run := mustPutRun(t, state, "ready")

	onReady, onFailure := publishedPipeLifecycle(state, run, "pipe/test", "relay-id", nil)
	onFailure(errors.New("reserve relay circuit failed"))
	if got := currentRunStatus(t, state); got != "degraded" {
		t.Fatalf("expected degraded after failure, got %q", got)
	}
	onReady()
	if got := currentRunStatus(t, state); got != "ready" {
		t.Fatalf("expected recovery to ready, got %q", got)
	}
}

// TestPublishedPipeLifecyclePublishFailureStaysDegraded proves that when the
// scoped record-publication hook fails on a maintenance ready, the pipe is not
// marked ready (it degrades instead), preserving fail-closed in this path.
func TestPublishedPipeLifecyclePublishFailureStaysDegraded(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	run := mustPutRun(t, state, "ready")

	onReady, _ := publishedPipeLifecycle(state, run, "pipe/test", "relay-id", func() error {
		return errors.New("publish grant expired")
	})
	onReady()
	if got := currentRunStatus(t, state); got != "degraded" {
		t.Fatalf("publish failure must degrade, got %q", got)
	}
}

// TestPublishedPipeRepublishOnlyOnReadyCallback proves the runtime wiring only
// republishes (or otherwise mutates scoped state) in response to the abstract
// "maintenance succeeded" event: onFailure never republishes, onReady
// republishes exactly once per event. Combined with the L1 guarantee that a
// no-op maintenance tick emits neither callback (TestReservationNoUnnecessary-
// Renewal), a no-op L1 tick can never trigger scoped republishing. L2 never
// inspects L1 internals; it only maps the two abstract lifecycle events.
func TestPublishedPipeRepublishOnlyOnReadyCallback(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	run := mustPutRun(t, state, "ready")

	published := 0
	onReady, onFailure := publishedPipeLifecycle(state, run, "pipe/test", "relay-id", func() error {
		published++
		return nil
	})

	// Maintenance failure: no republish, degraded.
	onFailure(errors.New("reserve relay circuit failed"))
	if published != 0 {
		t.Fatalf("onFailure must not republish, got %d publishes", published)
	}
	if got := currentRunStatus(t, state); got != "degraded" {
		t.Fatalf("expected degraded after failure, got %q", got)
	}

	// Successful maintenance event: republish exactly once, restore ready.
	onReady()
	if published != 1 {
		t.Fatalf("each successful maintenance must republish exactly once, got %d", published)
	}
	if got := currentRunStatus(t, state); got != "ready" {
		t.Fatalf("expected ready after recovery, got %q", got)
	}
}

// TestPublishedPipeRuntimeHasNoReservationPolicy pins the architecture
// invariant that L2 holds no relay-reservation policy: pipe.go must consume the
// L1 RelayReservation primitive (Acquire + Serve) and must not independently
// decide reservation renewal. Any return of PeerConnected/EnsureRelay/
// ReserveRelay or a renewal interval into pipe.go breaks this test.
func TestPublishedPipeRuntimeHasNoReservationPolicy(t *testing.T) {
	src, err := os.ReadFile("pipe.go")
	if err != nil {
		t.Fatal(err)
	}
	content := string(src)

	for _, must := range []string{
		"reachability.NewRelayReservation",
		"reservation.Acquire(ctx)",
		"reservation.Serve(ctx",
		"publishedPipeLifecycle",
	} {
		if !strings.Contains(content, must) {
			t.Fatalf("pipe.go must consume L1 RelayReservation lifecycle via %q", must)
		}
	}

	for _, forbid := range []string{
		"EnsureRelay",
		"PeerConnected",
		"ReserveRelay",
		"lastRenewed",
	} {
		if strings.Contains(content, forbid) {
			t.Fatalf("pipe.go must not contain L1 reservation policy token %q", forbid)
		}
	}
}

// TestPublishedPipeLifecycleIsSharedProves both published-Pipe paths consume
// the same L1 lifecycle primitive with no per-path duplication.
func TestPublishedPipeLifecycleIsShared(t *testing.T) {
	src, err := os.ReadFile("pipe.go")
	if err != nil {
		t.Fatal(err)
	}
	content := string(src)
	if count := strings.Count(content, "publishedPipeLifecycle(state"); count < 2 {
		t.Fatalf("expected both scoped and scope-less paths to call publishedPipeLifecycle, saw %d invocations", count)
	}
}
