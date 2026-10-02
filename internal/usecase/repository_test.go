package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/origama/tubo/internal/domain"
)

// Cases 1-6: Narrow persistence ports via in-memory fakes.
//
// Case 1: Service put/get/upsert round-trip — durable identity stable.
// Case 2: Pipe put/get/round-trip with owner-key path.
// Case 3: Delete pipe cascades only definition (no unrelated resources).
// Case 4: Get by ref and by pipe_id both work.
// Case 5: Unknown ref returns ErrNotFound (no zero-value success).
// Case 6: Validation rejection propagates before write (fail closed).

func TestCase1_ServicePutGetUpsert(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryServiceRepo()
	ref, _ := domain.NewRef(domain.KindService, "echo")
	spec := domain.ServiceSpec{Ref: ref, Kind: domain.ServiceHTTP, Target: "http://127.0.0.1:8080"}

	// First insert.
	created, err := repo.Upsert(ctx, spec)
	if err != nil || !created {
		t.Fatalf("first upsert: created=%v err=%v", created, err)
	}

	// Idempotent same content.
	created, err = repo.Upsert(ctx, spec)
	if err != nil || created {
		t.Fatalf("idempotent upsert: created=%v err=%v", created, err)
	}

	// Get
	loaded, err := repo.Get(ctx, ref)
	if err != nil || loaded.Target != spec.Target || loaded.Kind != spec.Kind {
		t.Fatalf("get: %+v err=%v", loaded, err)
	}

	// Conflict: different target
	diff := spec
	diff.Target = "http://127.0.0.1:8081"
	_, err = repo.Upsert(ctx, diff)
	if err == nil {
		t.Fatal("expected conflict error for different target")
	}

	// Original unchanged after conflict.
	loaded, err = repo.Get(ctx, ref)
	if err != nil || loaded.Target != "http://127.0.0.1:8080" {
		t.Fatalf("original unchanged after conflict: %+v", loaded)
	}
}

func TestCase2_PipePutGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryPipeRepo()
	now := time.Now()

	ref, _ := domain.NewRef(domain.KindPipe, "testpipe")
	target, _ := domain.NewRef(domain.KindService, "echo")
	spec := domain.PipeSpec{
		Ref:        ref,
		PipeID:     "pip_testpipe",
		Ingress:    domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:9000"},
		Target:     target,
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	keyFile := "/tmp/test-owner.key"

	created, err := repo.Upsert(ctx, spec, keyFile)
	if err != nil || !created {
		t.Fatalf("pipe upsert: created=%v err=%v", created, err)
	}

	// Idempotent same content.
	created, err = repo.Upsert(ctx, spec, "")
	if err != nil || created {
		t.Fatalf("idempotent: created=%v err=%v", created, err)
	}

	// Get by ref.
	loaded, key, err := repo.Get(ctx, ref.String())
	if err != nil || loaded.PipeID != spec.PipeID || key != keyFile {
		t.Fatalf("get by ref: pipe=%v key=%s err=%v", loaded, key, err)
	}

	// Get by pipe_id.
	loaded, key, err = repo.Get(ctx, spec.PipeID)
	if err != nil || loaded.Ref != ref || key != keyFile {
		t.Fatalf("get by pipe_id: ref=%v key=%s err=%v", loaded.Ref, key, err)
	}
}

func TestCase3_DeletePipeDefinitionOnly(t *testing.T) {
	ctx := context.Background()
	pipeRepo := newMemoryPipeRepo()
	serviceRepo := newMemoryServiceRepo()

	// Create a service and two pipes.
	svcRef, _ := domain.NewRef(domain.KindService, "web")
	mustService(ctx, t, serviceRepo, "web", "http://127.0.0.1:8080")

	p1Ref, _ := domain.NewRef(domain.KindPipe, "pipe-a")
	p2Ref, _ := domain.NewRef(domain.KindPipe, "pipe-b")
	mustPipe(ctx, t, pipeRepo, p1Ref, svcRef)
	mustPipe(ctx, t, pipeRepo, p2Ref, svcRef)

	// Delete pipe-a.
	if err := pipeRepo.Delete(ctx, p1Ref.String()); err != nil {
		t.Fatal(err)
	}

	// Pipe-a is gone.
	_, _, err := pipeRepo.Get(ctx, p1Ref.String())
	if err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for deleted pipe, got %v", err)
	}

	// Pipe-b survives.
	_, _, err = pipeRepo.Get(ctx, p2Ref.String())
	if err != nil {
		t.Fatalf("pipe-b should survive: %v", err)
	}

	// Service is not in pipe repo — the interface has no Run/Session surface.
	// The definition-only cascade is structural: the fake removed only the
	// pipe entry.
}

func TestCase4_GetByRefAndByPipeID(t *testing.T) {
	ctx := context.Background()
	repo := newMemoryPipeRepo()
	svcRef, _ := domain.NewRef(domain.KindService, "svc1")
	svcRef2, _ := domain.NewRef(domain.KindService, "svc2")

	p1Ref, _ := domain.NewRef(domain.KindPipe, "p1")
	_ = mustPipe(ctx, t, repo, p1Ref, svcRef)

	p2Ref, _ := domain.NewRef(domain.KindPipe, "p2")
	p2 := mustPipe(ctx, t, repo, p2Ref, svcRef2)

	// By ref
	loaded, key, err := repo.Get(ctx, p1Ref.String())
	if err != nil || loaded.Ref != p1Ref {
		t.Fatalf("get by ref: %v key=%s err=%v", loaded, key, err)
	}

	// By pipe_id
	loaded, key, err = repo.Get(ctx, p2.PipeID)
	if err != nil || loaded.Ref != p2Ref {
		t.Fatalf("get by pipe_id %s: ref=%v err=%v", p2.PipeID, loaded.Ref, err)
	}
}

func TestCase5_UnknownRefReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	serviceRepo := newMemoryServiceRepo()
	pipeRepo := newMemoryPipeRepo()

	_, err := serviceRepo.Get(ctx, domain.Ref{Kind: domain.KindService, Name: "nonexistent"})
	if err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for service, got %v", err)
	}

	_, _, err = pipeRepo.Get(ctx, "pipe/nonexistent")
	if err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for pipe, got %v", err)
	}

	err = serviceRepo.Delete(ctx, domain.Ref{Kind: domain.KindService, Name: "nonexistent"})
	if err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for delete, got %v", err)
	}

	err = pipeRepo.Delete(ctx, "pipe/nonexistent")
	if err != domain.ErrNotFound {
		t.Fatalf("expected ErrNotFound for delete, got %v", err)
	}
}

func TestCase6_ValidationRejectionBeforeWrite(t *testing.T) {
	ctx := context.Background()
	serviceRepo := newMemoryServiceRepo()
	pipeRepo := newMemoryPipeRepo()

	// Invalid service: missing target.
	invalidService := domain.ServiceSpec{
		Ref:    domain.Ref{Kind: domain.KindService, Name: "bad"},
		Kind:   domain.ServiceHTTP,
		Target: "",
	}
	_, err := serviceRepo.Upsert(ctx, invalidService)
	if err == nil {
		t.Fatal("expected validation error for empty service target")
	}
	// Nothing was stored.
	_, err = serviceRepo.Get(ctx, invalidService.Ref)
	if err != domain.ErrNotFound {
		t.Fatalf("nothing should be stored after validation error: %v", err)
	}

	// Invalid pipe: no ingress listen for local kind.
	svcRef, _ := domain.NewRef(domain.KindService, "exists")
	_ = mustService(ctx, t, serviceRepo, "exists", "http://127.0.0.1:8080")
	invalidPipe := domain.PipeSpec{
		Ref:     domain.Ref{Kind: domain.KindPipe, Name: "bad"},
		PipeID:  "pip_bad",
		Ingress: domain.PipeIngress{Kind: domain.IngressLocal}, // missing Listen
		Target:  svcRef,
	}
	_, err = pipeRepo.Upsert(ctx, invalidPipe, "/fake")
	if err == nil {
		t.Fatal("expected validation error for missing listen")
	}
	// Nothing was stored.
	_, _, err = pipeRepo.Get(ctx, invalidPipe.Ref.String())
	if err != domain.ErrNotFound {
		t.Fatalf("nothing should be stored: %v", err)
	}
}
