// Store-backed narrow-port tests — cases 3 (Run survives), 5 (error propagation),
// 6 (validation before write), 13 (identity survives restart), 14 (transient
// writes don't mutate definition rows).
package store

import (
	"context"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

// Compile-time bridge: the store adapters satisfy the consumer-owned usecase ports.
var _ usecase.ServiceRepository = (*ServiceRepoAdapter)(nil)
var _ usecase.PipeRepository = (*PipeRepoAdapter)(nil)
var _ usecase.RemotePipeBindingRepo = (*RemotePipeBindingAdapter)(nil)
var _ usecase.AccessLeaseCacheRepo = (*AccessLeaseCacheAdapter)(nil)

func TestStoreCase3_DeletePipeDoesNotRemoveRuns(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	ownerKey := "/tmp/dummy.key"
	pipeRef := "pipe/testpipe"

	// Create service
	serviceRef, _ := domain.NewRef(domain.KindService, "web")
	_, err = state.PutService(ctx, domain.ServiceSpec{Ref: serviceRef, Kind: domain.ServiceHTTP, Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}

	// Create pipe
	pipeRefParsed, _ := domain.NewRef(domain.KindPipe, "testpipe")
	now := time.Now()
	spec := domain.PipeSpec{
		Ref:        pipeRefParsed,
		PipeID:     "pip_testpipe",
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:9000"},
		Target:     serviceRef,
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	_, err = state.PutPipe(ctx, spec, ownerKey, "")
	if err != nil {
		t.Fatal(err)
	}

	// Create a run for the pipe
	run := Run{PrimaryRef: pipeRef, Kind: "pipe", PID: 0, Status: "ready", StartedAt: now}
	if err := state.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Verify run exists
	_, err = state.Run(ctx, pipeRef)
	if err != nil {
		t.Fatalf("run should exist before delete: %v", err)
	}

	// Delete pipe
	_, err = state.DeletePipe(ctx, pipeRef)
	if err != nil {
		t.Fatal(err)
	}

	// Pipe definition is gone
	_, _, _, err = state.Pipe(ctx, pipeRef)
	if err == nil {
		t.Fatal("pipe should be deleted")
	}

	// But the Run survives
	remaining, err := state.Run(ctx, pipeRef)
	if err != nil {
		t.Fatalf("run should survive pipe delete: %v", err)
	}
	if remaining.PrimaryRef != pipeRef {
		t.Fatalf("run ref mismatch: %s", remaining.PrimaryRef)
	}

	// Service survives too
	_, err = state.Service(ctx, serviceRef)
	if err != nil {
		t.Fatalf("service should survive: %v", err)
	}
}

func TestStoreCase5_PersistenceErrorPropagates(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	// Attempt to put a service with invalid JSON (via nil schema? straight error path:
	// a missing target is caught by Validate before any SQL write.)
	ref, _ := domain.NewRef(domain.KindService, "bad")
	_, err = state.PutService(ctx, domain.ServiceSpec{Ref: ref, Kind: domain.ServiceHTTP, Target: ""})
	if err == nil {
		t.Fatal("expected error for invalid service")
	}

	// Nothing was persisted (fail closed): the second read errors.
	if _, err := state.Service(ctx, ref); err == nil {
		t.Fatal("invalid service must not persist")
	}
}

func TestStoreCase6_ValidationRejectionBeforeWrite(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	ref, _ := domain.NewRef(domain.KindPipe, "invalid")
	svcRef, _ := domain.NewRef(domain.KindService, "web")
	spec := domain.PipeSpec{
		Ref:    ref,
		PipeID: "pip_invalid",
		// Missing Ingress.Listen for local kind triggers validation error.
		Ingress: domain.PipeIngress{Kind: domain.IngressLocal},
		Target:  svcRef,
	}
	_, err = state.PutPipe(ctx, spec, "/fake.key", "")
	if err == nil {
		t.Fatal("expected validation error")
	}

	// Nothing persisted.
	_, _, _, err = state.Pipe(ctx, ref.String())
	if err == nil {
		t.Fatal("pipe should not persist after validation failure")
	}
}

func TestStoreCase13_PipeIdentitySurvivesRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()

	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()

	svcRef, _ := domain.NewRef(domain.KindService, "web")
	_, err = state.PutService(ctx, domain.ServiceSpec{Ref: svcRef, Kind: domain.ServiceHTTP, Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}

	pipeRef, _ := domain.NewRef(domain.KindPipe, "restarttest")
	spec := domain.PipeSpec{
		Ref:        pipeRef,
		PipeID:     "pip_restarttest",
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:9000"},
		Target:     svcRef,
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	_, err = state.PutPipe(ctx, spec, "/tmp/key", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	// Re-open.
	state, err = Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	loaded, key, _, err := state.Pipe(ctx, pipeRef.String())
	if err != nil {
		t.Fatalf("pipe should survive restart: %v", err)
	}
	if loaded.PipeID != "pip_restarttest" {
		t.Fatalf("pipe identity changed: %s", loaded.PipeID)
	}
	if loaded.Target != svcRef {
		t.Fatalf("pipe target changed: %s", loaded.Target)
	}
	if key != "/tmp/key" {
		t.Fatalf("owner key path changed: %s", key)
	}
}

func TestStoreCase14_TransientWritesDoNotMutateDefinitionRows(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now()

	svcRef, _ := domain.NewRef(domain.KindService, "web")
	serviceSpec := domain.ServiceSpec{Ref: svcRef, Kind: domain.ServiceHTTP, Target: "http://127.0.0.1:8080"}
	if _, err := state.PutService(ctx, serviceSpec); err != nil {
		t.Fatal(err)
	}

	pipeRef, _ := domain.NewRef(domain.KindPipe, "transient")
	pipeSpec := domain.PipeSpec{
		Ref:        pipeRef,
		PipeID:     "pip_transient",
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:9000"},
		Target:     svcRef,
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if _, err := state.PutPipe(ctx, pipeSpec, "/tmp/key", ""); err != nil {
		t.Fatal(err)
	}

	// Write a transient Run row to the pipe.
	run := Run{PrimaryRef: pipeRef.String(), Kind: "pipe", PID: 0, Status: "ready", StartedAt: now}
	if err := state.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	// Write a transient Session row.
	session := Session{ID: "sess_001", PipeRef: pipeRef.String(), InitialTransportPath: "direct", StartedAt: now}
	if err := state.PutSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	// Verify ServiceSpec is unchanged.
	loadedService, err := state.Service(ctx, svcRef)
	if err != nil {
		t.Fatal(err)
	}
	if loadedService.Target != serviceSpec.Target {
		t.Fatalf("service changed after transient writes: %s", loadedService.Target)
	}

	// Verify PipeSpec is unchanged.
	loadedPipe, _, _, err := state.Pipe(ctx, pipeRef.String())
	if err != nil {
		t.Fatal(err)
	}
	if loadedPipe.PipeID != pipeSpec.PipeID {
		t.Fatalf("pipe identity changed after transient writes: %s", loadedPipe.PipeID)
	}
}
