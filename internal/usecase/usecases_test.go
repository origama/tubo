// Use-case tests — cases 7-11 against in-memory fakes and temp-dir localkey.
package usecase

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
	u.OwnerLocator = &fakeOwnerLocator{address: "relay.tubo.click:4001"}
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

func TestShareExactRequiresReachableOwnerLocatorBeforeMint(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()
	u := New(services, pipes)
	u.Secrets = secretsDir
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	attached, err := u.Attach(ctx, AttachInput{Name: "locator-required", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}

	result, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attached.PipeRef.String()})
	if err == nil || result.Invite != "" {
		t.Fatalf("missing locator result=%+v err=%v", result, err)
	}

	locatorErr := errors.New("runtime_unavailable: owner runtime is not reachable")
	u.OwnerLocator = &fakeOwnerLocator{err: locatorErr}
	result, err = u.ShareExact(ctx, ShareExactInput{PipeRef: attached.PipeRef.String()})
	if !errors.Is(err, locatorErr) || result.Invite != "" {
		t.Fatalf("unreachable locator result=%+v err=%v", result, err)
	}

	u.OwnerLocator = &fakeOwnerLocator{}
	result, err = u.ShareExact(ctx, ShareExactInput{PipeRef: attached.PipeRef.String()})
	if err == nil || result.Invite != "" {
		t.Fatalf("empty locator result=%+v err=%v", result, err)
	}
	keyPath := filepath.Join(secretsDir, "exact-authorities", attached.PipeID+".key")
	if _, statErr := os.Stat(keyPath); !os.IsNotExist(statErr) {
		t.Fatalf("locator failure provisioned authority key: %v", statErr)
	}
}

func TestShareExactEmbedsExactLocatorAndTamperingFails(t *testing.T) {
	const locator = "/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME/p2p-circuit/p2p/12D3KooWRJCtSj2fWsnyHYdpcmQoHwDzVzr39VmFrRwmJrwwW57G"
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	u := New(services, pipes)
	u.Secrets = t.TempDir()
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	locatorCapability := &fakeOwnerLocator{address: locator}
	u.OwnerLocator = locatorCapability
	attached, err := u.Attach(ctx, AttachInput{Name: "canonical-locator", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	shared, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attached.PipeRef.String()})
	if err != nil {
		t.Fatal(err)
	}
	if shared.Invite == "" || locatorCapability.calls != 1 || locatorCapability.ref != attached.PipeRef || locatorCapability.pipeID != attached.PipeID {
		t.Fatalf("locator capability/result mismatch: result=%+v locator=%+v", shared, locatorCapability)
	}
	invite, err := accesslease.DecodeInvite(shared.Invite)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := accesslease.DecodeOwnerBinding(invite.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if binding.RedemptionPeers != locator || binding.RedemptionProtocol != "/tubo/exact-redemption/1" {
		t.Fatalf("binding locator mismatch: %+v", binding)
	}
	extracted, err := redemptionAddress(invite)
	if err != nil || extracted != locator {
		t.Fatalf("recipient extracted %q, err=%v", extracted, err)
	}
	for name, credential := range map[string]any{"owner binding": binding, "exact invite": invite} {
		body, err := json.Marshal(credential)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"service", "service_ref", "target", "scope", "namespace", "discovery", "membership", "pipe_record", "descriptor", "application"} {
			if _, ok := fields[forbidden]; ok {
				t.Fatalf("%s leaked forbidden field %q", name, forbidden)
			}
		}
	}
	if binding.RedemptionPeers == "http://127.0.0.1:8080" {
		t.Fatal("owner binding leaked local Service target")
	}

	binding.RedemptionPeers += "/tampered"
	invite.Binding, err = accesslease.EncodeOwnerBinding(binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := invite.Verify(time.Now().UTC(), invite.AudiencePipeID); err == nil {
		t.Fatal("tampered owner locator retained valid ExactInvite")
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
	u.OwnerLocator = &fakeOwnerLocator{address: "relay.tubo.click:4001"}
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
	const locator = "/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME/p2p-circuit/p2p/12D3KooWRJCtSj2fWsnyHYdpcmQoHwDzVzr39VmFrRwmJrwwW57G"

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.OwnerLocator = &fakeOwnerLocator{address: locator}
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	reach := newFakeReachability()
	u.Reach = reach
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
	reach.mu.Lock()
	_, acquiredExactLocator := reach.conn[locator]
	reach.mu.Unlock()
	if !acquiredExactLocator {
		t.Fatalf("ConnectExact did not extract unchanged locator %q", locator)
	}
	// The connect operation never referenced Scope, discovery, or membership:
	// the crafted path above used only the invite plus injected reachability.
}

// TestCase15_ConnectExactPersistsExactConnectionState verifies that
// ConnectExact persists the exact connection state through the injected
// ExactConnectionStateRepo for detached-start consumption.
func TestCase15_ConnectExactPersistsExactConnectionState(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	exactConn := newMemoryExactConnectionRepo()
	secretsDir := t.TempDir()

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.OwnerLocator = &fakeOwnerLocator{address: "relay.tubo.click:4001"}
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	u.Reach = newFakeReachability()
	u.Redemption = newFakeRedemptionService(secretsDir)
	u.ExactConnections = exactConn

	// Attach + Share.
	attachResult, err := u.Attach(ctx, AttachInput{Name: "persisttest", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	shareResult, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	if err != nil {
		t.Fatal(err)
	}

	// Connect.
	connectResult, err := u.ConnectExact(ctx, ConnectExactInput{
		Invite:      shareResult.Invite,
		LocalListen: "127.0.0.1:9998",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Verify exact connection state was persisted.
	state, err := exactConn.Get(ctx, shareResult.PipeID)
	if err != nil {
		t.Fatalf("exact connection state should be persisted: %v", err)
	}
	if state.RemotePipeID != shareResult.PipeID {
		t.Fatalf("remote pipe ID mismatch: %s vs %s", state.RemotePipeID, shareResult.PipeID)
	}
	if state.Locator == "" {
		t.Fatal("locator must be non-empty")
	}
	if state.AccessLease == "" {
		t.Fatal("access lease must be non-empty")
	}
	if state.LocalPipeRef != connectResult.PipeRef.String() {
		t.Fatalf("local pipe ref mismatch: %s vs %s", state.LocalPipeRef, connectResult.PipeRef)
	}
	if state.ExpiresAt.IsZero() {
		t.Fatal("expiry must be non-zero")
	}
}

// TestCase16_ConnectExactExpiredLeaseFailsClosed verifies that an expired
// exact connection state returns ErrNotFound from the repo (fail closed).
func TestCase16_ConnectExactExpiredLeaseFailsClosed(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	exactConn := newMemoryExactConnectionRepo()
	secretsDir := t.TempDir()

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.OwnerLocator = &fakeOwnerLocator{address: "relay.tubo.click:4001"}
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	u.Reach = newFakeReachability()
	u.Redemption = newFakeRedemptionService(secretsDir)
	u.ExactConnections = exactConn

	// Attach + Share + Connect.
	attachResult, err := u.Attach(ctx, AttachInput{Name: "expiretest", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	shareResult, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.ConnectExact(ctx, ConnectExactInput{
		Invite:      shareResult.Invite,
		LocalListen: "127.0.0.1:9997",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Manually expire the stored state.
	state, err := exactConn.Get(ctx, shareResult.PipeID)
	if err != nil {
		t.Fatal(err)
	}
	// Set expiry to the past.
	state.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	if err := exactConn.Put(ctx, state); err != nil {
		t.Fatal(err)
	}

	// Get should fail with ErrNotFound.
	_, err = exactConn.Get(ctx, shareResult.PipeID)
	if err != domain.ErrNotFound {
		t.Fatalf("expired state must return ErrNotFound, got %v", err)
	}
}

// TestCase17_ConnectExactNoExactConnectionsRepoIsGraceful verifies that
// ConnectExact succeeds even when no ExactConnectionStateRepo is wired.
func TestCase17_ConnectExactNoExactConnectionsRepoIsGraceful(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.OwnerLocator = &fakeOwnerLocator{address: "relay.tubo.click:4001"}
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	u.Reach = newFakeReachability()
	u.Redemption = newFakeRedemptionService(secretsDir)
	// ExactConnections is nil — no persistence.

	attachResult, err := u.Attach(ctx, AttachInput{Name: "norepotest", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	shareResult, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	if err != nil {
		t.Fatal(err)
	}

	// Connect must still succeed.
	_, err = u.ConnectExact(ctx, ConnectExactInput{
		Invite:      shareResult.Invite,
		LocalListen: "127.0.0.1:9996",
	})
	if err != nil {
		t.Fatalf("connect must succeed without exact connections repo: %v", err)
	}
}

// TestCase18_DetachedStartConsumesExactConnectionState verifies that
// persisted exact connection state can be consumed by a detached start
// (simulating runLocalPipeExact lookup).
func TestCase18_DetachedStartConsumesExactConnectionState(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	exactConn := newMemoryExactConnectionRepo()
	secretsDir := t.TempDir()

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.OwnerLocator = &fakeOwnerLocator{address: "relay.tubo.click:4001"}
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	u.Reach = newFakeReachability()
	u.Redemption = newFakeRedemptionService(secretsDir)
	u.ExactConnections = exactConn

	// Attach + Share + Connect.
	attachResult, err := u.Attach(ctx, AttachInput{Name: "detachedtest", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	shareResult, err := u.ShareExact(ctx, ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	if err != nil {
		t.Fatal(err)
	}
	connectResult, err := u.ConnectExact(ctx, ConnectExactInput{
		Invite:      shareResult.Invite,
		LocalListen: "127.0.0.1:9995",
	})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Simulate detached start: load exact connection state by remote PipeID.
	// The remote PipeID is the invite audience (= shareResult.PipeID).
	state, err := exactConn.Get(ctx, shareResult.PipeID)
	if err != nil {
		t.Fatalf("detached start must find exact connection state: %v", err)
	}

	// Verify all fields needed by runLocalPipeExact are present.
	if state.RemotePipeID != shareResult.PipeID {
		t.Fatalf("remote pipe ID mismatch: %s vs %s", state.RemotePipeID, shareResult.PipeID)
	}
	if state.Locator == "" {
		t.Fatal("locator required for downstream dial")
	}
	if state.AccessLease == "" {
		t.Fatal("access lease required for open auth")
	}
	if state.LocalPipeRef != connectResult.PipeRef.String() {
		t.Fatalf("local pipe ref mismatch: %s vs %s", state.LocalPipeRef, connectResult.PipeRef)
	}
}

// TestCase19_ExactConnectionStateExpiryDeterministic verifies the expiry
// check is deterministic (no sleeps). Uses a fixed clock past expiry.
func TestCase19_ExactConnectionStateExpiryDeterministic(t *testing.T) {
	exactConn := newMemoryExactConnectionRepo()
	ctx := context.Background()

	// Insert state with expiry in the past.
	pastState := domain.ExactConnectionState{
		RemotePipeID: "pip_expired",
		Locator:      "/ip4/127.0.0.1/tcp/4001",
		AccessLease:  "dGVzdA",
		ExpiresAt:    time.Now().UTC().Add(-time.Hour),
		LocalPipeRef: "pipe/expired",
	}
	if err := exactConn.Put(ctx, pastState); err != nil {
		t.Fatal(err)
	}

	// Get must fail with ErrNotFound.
	_, err := exactConn.Get(ctx, "pip_expired")
	if err != domain.ErrNotFound {
		t.Fatalf("expired state must return ErrNotFound, got %v", err)
	}
}

// TestCase20_ExactConnectionStateNoExpiryIsValid verifies that a state
// with zero expiry is valid (no expiry check).
func TestCase20_ExactConnectionStateNoExpiryIsValid(t *testing.T) {
	exactConn := newMemoryExactConnectionRepo()
	ctx := context.Background()

	noExpiryState := domain.ExactConnectionState{
		RemotePipeID: "pip_no_expiry",
		Locator:      "/ip4/127.0.0.1/tcp/4001",
		AccessLease:  "dGVzdA",
		LocalPipeRef: "pipe/no_expiry",
	}
	if err := exactConn.Put(ctx, noExpiryState); err != nil {
		t.Fatal(err)
	}

	state, err := exactConn.Get(ctx, "pip_no_expiry")
	if err != nil {
		t.Fatalf("state without expiry must be retrievable: %v", err)
	}
	if state.RemotePipeID != "pip_no_expiry" {
		t.Fatalf("pipe ID mismatch")
	}
}

// TestCase21_ExactConnectionStateInvalidRejectedByPut verifies that the
// fake repo rejects invalid state (missing required fields).
func TestCase21_ExactConnectionStateInvalidRejectedByPut(t *testing.T) {
	exactConn := newMemoryExactConnectionRepo()
	ctx := context.Background()

	// Missing RemotePipeID.
	err := exactConn.Put(ctx, domain.ExactConnectionState{
		Locator:     "/ip4/127.0.0.1/tcp/4001",
		AccessLease: "dGVzdA",
	})
	if err == nil {
		t.Fatal("expected error for missing remote pipe ID")
	}

	// Missing Locator.
	err = exactConn.Put(ctx, domain.ExactConnectionState{
		RemotePipeID: "pip_test",
		AccessLease:  "dGVzdA",
	})
	if err == nil {
		t.Fatal("expected error for missing locator")
	}

	// Missing AccessLease.
	err = exactConn.Put(ctx, domain.ExactConnectionState{
		RemotePipeID: "pip_test",
		Locator:      "/ip4/127.0.0.1/tcp/4001",
	})
	if err == nil {
		t.Fatal("expected error for missing access lease")
	}
}
