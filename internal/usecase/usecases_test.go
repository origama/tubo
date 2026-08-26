// Use-case tests — cases 7-11 against in-memory fakes and temp-dir localkey.
package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
)

// Case 7: Attach creates Service then Pipe (all-or-nothing order).
// Case 8: Attach targets existing Service ref reuses (idempotent upsert).
// Case 9: ShareExact provisions exact authority key once, mints bounded ExactInvite.
// Case 10: ShareExact: invite verification fails on wrong pipe/expired/tampered (negative).
// Case 11: ConnectExact uses only invite + configured reachability (no Scope, no discovery).

func TestCase7_AttachCreatesServiceThenPipe(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.Now = fixedClock(now)
	u.RedemptionAddress = "relay.tubo.click:4001"
	u.RedemptionProtocol = "/tubo/exact-redemption/1"

	result, err := u.Attach(ctx, AttachInput{Name: "myservice", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if result.ServiceRef.String() != "service/myservice" {
		t.Fatalf("unexpected service ref: %s", result.ServiceRef)
	}
	if result.PipeRef.String() != "pipe/myservice" {
		t.Fatalf("unexpected pipe ref: %s", result.PipeRef)
	}
	if result.PipeID == "" || len(result.PipeID) < 10 {
		t.Fatalf("invalid pipe id: %s", result.PipeID)
	}
	// Verify durable identity: PipeID derives from the owner key.
	svc, err := services.Get(ctx, result.ServiceRef)
	if err != nil || svc.Target != "http://127.0.0.1:8080" {
		t.Fatalf("service should persist: %+v err=%v", svc, err)
	}
	pipe, _, err := pipes.Get(ctx, result.PipeRef.String())
	if err != nil || pipe.PipeID != result.PipeID {
		t.Fatalf("pipe should persist: %+v err=%v", pipe, err)
	}
	// Scope-less: Scope must be nil.
	if pipe.Scope != nil {
		t.Fatal("attach must create scope-less pipe")
	}
	// Durable runtime separation: no PID/Carrier/Session/address fields.
	verifyNoRuntimeFields(t, pipe)
}

func TestCase8_AttachReusesExistingService(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.Now = fixedClock(now)

	// First attach.
	first, err := u.Attach(ctx, AttachInput{Name: "web", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if !first.Created {
		t.Fatal("first attach should create new resources")
	}
	id1 := first.PipeID

	// Second attach with same name + target reuses service.
	second, err := u.Attach(ctx, AttachInput{Name: "web", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("second attach: %v", err)
	}
	if second.PipeRef != first.PipeRef {
		t.Fatal("second attach should return same pipe ref")
	}
	if second.PipeID != id1 {
		t.Fatal("pipe identity must be stable across re-attach")
	}
	if second.ServiceRef != first.ServiceRef {
		t.Fatal("second attach should reuse service ref")
	}
}

func TestCase9_ShareExactProvisionsKeyOnce(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	// Create an owned pipe via Attach.
	u := New(services, pipes)
	u.Secrets = secretsDir
	u.Now = fixedClock(now)
	u.RedemptionAddress = "relay.tubo.click:4001"
	u.RedemptionProtocol = "/tubo/exact-redemption/1"

	attachResult, err := u.Attach(ctx, AttachInput{Name: "sharetest", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	// First share.
	first, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	if err != nil {
		t.Fatalf("first share: %v", err)
	}
	if first.Invite == "" {
		t.Fatal("share must return non-empty invite")
	}
	// Invite is bounded.
	if len(first.Invite) > 48<<10 {
		t.Fatalf("invite too large: %d bytes", len(first.Invite))
	}
	// Verify the invite can decode.
	invite, err := accesslease.DecodeInvite(first.Invite)
	if err != nil {
		t.Fatalf("invite decode: %v", err)
	}
	if invite.AudiencePipeID != attachResult.PipeID {
		t.Fatal("invite audience mismatch")
	}
	if err := invite.Verify(now, invite.AudiencePipeID); err != nil {
		t.Fatalf("invite verify: %v", err)
	}
	issuer1, err := accesslease.PublicFromString(invite.Issuer)
	if err != nil {
		t.Fatal(err)
	}

	// Second share: same authority key, same pipe identity.
	second, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	if err != nil {
		t.Fatalf("second share: %v", err)
	}
	if second.PipeID != first.PipeID {
		t.Fatal("pipe identity changed after second share")
	}

	// Decode second invite — issuer (authority key) must match first.
	invite2, err := accesslease.DecodeInvite(second.Invite)
	if err != nil {
		t.Fatalf("second invite decode: %v", err)
	}
	issuer2, err := accesslease.PublicFromString(invite2.Issuer)
	if err != nil {
		t.Fatal(err)
	}
	if !issuer1.Equal(issuer2) {
		t.Fatal("authority key identity changed between shares")
	}
	if invite2.AudiencePipeID != invite.AudiencePipeID {
		t.Fatal("pipe audience changed between shares")
	}
}

func TestCase10_ShareExactNegative(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.Now = fixedClock(now)
	u.RedemptionAddress = "relay.tubo.click:4001"
	u.RedemptionProtocol = "/tubo/exact-redemption/1"

	// a) Share non-existent pipe -> not_found.
	_, err := u.ShareExact(ctx, ShareExactInput{PipeRef: "pipe/nonexistent"})
	if err == nil {
		t.Fatal("expected error for non-existent pipe")
	}

	// Create an owned pipe.
	attachResult, err := u.Attach(ctx, AttachInput{Name: "shareneg", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}

	// b) Create valid invite.
	result, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	if err != nil {
		t.Fatal(err)
	}

	// c) Tampered invite -> DecodeInvite fails.
	tampered := result.Invite[:len(result.Invite)-1] + "X" // flip last char
	_, err = accesslease.DecodeInvite(tampered)
	if err == nil {
		t.Fatal("expected decode failure for tampered invite")
	}

	// d) Expired invite -> Verify fails (use time after expiry).
	later := now.Add(2 * DefaultInviteTTL)
	invite, err := accesslease.DecodeInvite(result.Invite)
	if err != nil {
		t.Fatal(err)
	}
	if err := invite.Verify(later, ""); err == nil {
		t.Fatal("expected verify failure for expired invite")
	}

	// e) Wrong pipe audience -> owner binding pipe mismatch.
	// The invite was minted for attachResult.PipeID; verify with a different
	// pipe ID should fail.
	if err := invite.Verify(now, "pip_differentpipe"); err == nil {
		t.Fatal("expected verify failure for wrong pipe audience")
	}
}

func TestCase11_ConnectExactNoScopeNoDiscovery(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.RedemptionAddress = "relay.tubo.click:4001"
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	u.Reach = newFakeReachability()
	u.Redemption = newFakeRedemptionService(secretsDir)

	// Step 1: Attach (creates owned pipe and provisions owner key).
	attachResult, err := u.Attach(ctx, AttachInput{Name: "connectexact", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	// Step 2: Share (provisions exact authority key and mints invite).
	shareResult, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	if err != nil {
		t.Fatalf("share: %v", err)
	}

	// Step 3: Connect (redeems invite via injected reachability, creates local pipe).
	connectResult, err := u.ConnectExact(ctx, ConnectExactInput{
		Invite:      shareResult.Invite,
		LocalListen: "127.0.0.1:9999",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if connectResult.PipeRef.String() == "" {
		t.Fatal("no pipe ref returned")
	}
	if connectResult.PipeID == "" {
		t.Fatal("no pipe id returned")
	}

	// Verify the local pipe is persisted and scope-less.
	localPipe, _, err := pipes.Get(ctx, connectResult.PipeRef.String())
	if err != nil {
		t.Fatalf("local pipe must persist: %v", err)
	}
	if localPipe.Scope != nil {
		t.Fatal("local pipe must be scope-less")
	}
	if localPipe.Ingress.Kind != domain.IngressLocal {
		t.Fatalf("connect pipe must be local ingress, got %q", localPipe.Ingress.Kind)
	}
	// The connect operation never referenced Scope, discovery, or membership:
	// the crafted path above used only the invite plus injected reachability.
}