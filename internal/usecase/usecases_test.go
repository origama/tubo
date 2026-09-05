// Use-case tests — cases 7-11 against in-memory fakes and temp-dir localkey.
package usecase

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/localkey"
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
	u.ExactEpoch = newFakeExactEpochReader(0)

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
	u.ExactEpoch = newFakeExactEpochReader(0)
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
	u.ExactEpoch = newFakeExactEpochReader(0)

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
	u.Renewal = newFakeRenewalService(secretsDir)
	u.Bindings = newMemoryRemotePipeBindingRepo()
	u.LeaseCache = newMemoryAccessLeaseCacheRepo()
	u.ExactEpoch = newFakeExactEpochReader(0)

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

// ---------------------------------------------------------------------------
// Durable exact remote-Pipe binding lifecycle tests (schema 12)
// ---------------------------------------------------------------------------

// exactFixture is the L4 test composition: real localkey secrets in a temp
// dir, in-memory narrow repos, one-winner fake redemption and fake renewal.
type exactFixture struct {
	services   *memoryServiceRepo
	pipes      *memoryPipeRepo
	bindings   *memoryRemotePipeBindingRepo
	leaseCache *memoryAccessLeaseCacheRepo
	redemption *fakeRedemptionService
	renewal    *fakeRenewalService
	epoch      *fakeExactEpochReader
	u          UseCases
	secrets    string
}

func newExactFixture(t *testing.T) *exactFixture {
	t.Helper()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	bindings := newMemoryRemotePipeBindingRepo()
	leaseCache := newMemoryAccessLeaseCacheRepo()
	secrets := t.TempDir()
	redemption := newFakeRedemptionService(secrets)
	renewal := newFakeRenewalService(secrets)
	epoch := newFakeExactEpochReader(0)
	u := New(services, pipes)
	u.Secrets = secrets
	u.OwnerLocator = &fakeOwnerLocator{address: "/dns4/relay.test/tcp/4001/p2p/12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME/p2p-circuit/p2p/12D3KooWRJCtSj2fWsnyHYdpcmQoHwDzVzr39VmFrRwmJrwwW57G"}
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	u.Redemption = redemption
	u.Renewal = renewal
	u.Reach = newFakeReachability()
	u.Bindings = bindings
	u.LeaseCache = leaseCache
	u.ExactEpoch = epoch
	return &exactFixture{
		services: services, pipes: pipes, bindings: bindings, leaseCache: leaseCache,
		redemption: redemption, renewal: renewal, epoch: epoch, u: u, secrets: secrets,
	}
}

// makeShare attaches a test service/pipe on the owner side and mints an exact
// invite through the same UseCases composition.
func (f *exactFixture) makeShare(t *testing.T) (pipeID, invite string) {
	t.Helper()
	att, err := f.u.Attach(context.Background(), AttachInput{Name: "origin", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	share, err := f.u.ShareExact(context.Background(), ShareExactInput{PipeRef: att.PipeRef.String()})
	if err != nil {
		t.Fatalf("share: %v", err)
	}
	return share.PipeID, share.Invite
}

// pinClock drives the use case and both credential-minting fakes off the same
// deterministic clock so lease verification never races real time.
func (f *exactFixture) pinClock(at time.Time) {
	f.u.Now = fixedClock(at)
	f.redemption.setNow(fixedClock(at))
	f.renewal.setNow(fixedClock(at))
	f.leaseCache.setNow(fixedClock(at))
}

func TestAcceptExactInviteCreatesExactlyOneDurableBinding(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	pipeID, invite := f.makeShare(t)

	result, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if result.RemotePipeID != pipeID {
		t.Fatalf("accepted pipe %s != %s", result.RemotePipeID, pipeID)
	}
	b, err := f.bindings.Get(ctx, pipeID)
	if err != nil {
		t.Fatalf("binding must persist: %v", err)
	}
	if b.RemotePipeID != pipeID || b.RecipientPublic == "" || b.AuthorityPublic == "" || b.OwnerBinding == "" || b.Locator == "" {
		t.Fatalf("binding missing required fields: %#v", b)
	}
	if b.Source != "invite" {
		t.Fatalf("binding source = %q", b.Source)
	}
	if len(f.bindings.items) != 1 {
		t.Fatalf("expected exactly one binding row, got %d", len(f.bindings.items))
	}
	// Recipient identity key persisted and reusable.
	keyFile := filepath.Join(f.secrets, "pipes", "connect-"+pipeID+".key")
	if _, err := os.Stat(keyFile); err != nil {
		t.Fatalf("recipient key not persisted: %v", err)
	}
	private, err := localkey.Load(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	if accesslease.PublicString(private.Public().(ed25519.PublicKey)) != b.RecipientPublic {
		t.Fatal("recipient key does not match binding recipient")
	}
}

func TestAcceptExactInviteSameRecipientRetryDeterministic(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	_, invite := f.makeShare(t)

	first, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	second, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite})
	if err != nil {
		t.Fatalf("same-recipient retry must be deterministic: %v", err)
	}
	if first.RemotePipeID != second.RemotePipeID || first.RecipientPublic != second.RecipientPublic {
		t.Fatal("retry must reuse the same accepted recipient identity")
	}
	b, _ := f.bindings.Get(ctx, first.RemotePipeID)
	if b.RecipientPublic != first.RecipientPublic {
		t.Fatal("binding must reuse the original recipient")
	}
	if len(f.bindings.items) != 1 {
		t.Fatalf("no duplicate binding rows, got %d", len(f.bindings.items))
	}
}

func TestAcceptExactInviteDifferentRecipientCannotConsume(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	_, invite := f.makeShare(t)

	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite}); err != nil {
		t.Fatalf("first accept: %v", err)
	}
	// A second machine has its own recipient key but shares the authority-side
	// one-winner redemption contract.
	other := newExactFixture(t)
	other.u.Redemption = f.redemption
	other.u.Renewal = f.renewal
	if _, err := other.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite}); err == nil {
		t.Fatal("different recipient must be denied")
	} else if !strings.Contains(err.Error(), "authorization_denied") {
		t.Fatalf("expected authorization_denied, got %v", err)
	}
}

func TestAcceptExactInviteRejectsMalformedExpiredTampered(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	_, invite := f.makeShare(t)

	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite[:len(invite)-1] + "X"}); err == nil {
		t.Fatal("tampered invite accepted")
	}
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: "not-an-invite"}); err == nil {
		t.Fatal("malformed invite accepted")
	}
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: ""}); err == nil {
		t.Fatal("empty invite accepted")
	}

	// Expired before accept: share at real time, then advance the accept clock
	// past the 10-minute invite TTL.
	f2 := newExactFixture(t)
	t0 := time.Now().UTC()
	_, invite2 := f2.makeShare(t)
	f2.pinClock(t0.Add(20 * time.Minute))
	if _, err := f2.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite2}); err == nil {
		t.Fatal("expired-before-accept invite accepted")
	} else if !strings.Contains(err.Error(), "authorization_denied") {
		t.Fatalf("expected authorization_denied, got %v", err)
	}
}

func TestAcquireAcceptedPipeAccessUsesUnexpiredCachedLease(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	pipeID, invite := f.makeShare(t)
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite}); err != nil {
		t.Fatal(err)
	}
	access, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if f.renewal.callCount(pipeID) != 0 {
		t.Fatalf("unexpired cached lease must not renew, calls=%d", f.renewal.callCount(pipeID))
	}
	issuer, _ := accesslease.PublicFromString(access.AuthorityPublic)
	if err := access.AccessLease.Verify(issuer, access.AccessLease.Subject, pipeID, time.Now().UTC()); err != nil {
		t.Fatalf("acquired lease does not verify: %v", err)
	}
	if access.AccessLease.Subject == "" || access.AccessLease.AudiencePipeID != pipeID {
		t.Fatalf("lease subject/audience wrong: %#v", access.AccessLease)
	}
}

// TestAcquireAcceptedPipeAccessRenewsAfterCacheExpiry drives a deterministic
// clock past the cached lease expiry; renewal mints a fresh short-lived lease
// and the durable binding survives the cache churn (no sleep).
func TestAcquireAcceptedPipeAccessRenewsAfterCacheExpiry(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	t0 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	f.pinClock(t0)
	pipeID, invite := f.makeShare(t)
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite}); err != nil {
		t.Fatal(err)
	}
	// Advance well past the 10-minute lease; the expired cache fails closed.
	f.pinClock(t0.Add(time.Hour))
	access, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID)
	if err != nil {
		t.Fatalf("acquire after cache expiry: %v", err)
	}
	if f.renewal.callCount(pipeID) != 1 {
		t.Fatalf("expired cache must renew exactly once, calls=%d", f.renewal.callCount(pipeID))
	}
	if !access.AccessLease.ExpiresAt.After(f.u.now()) {
		t.Fatal("renewed lease must be fresh")
	}
	cached, err := f.leaseCache.Get(ctx, pipeID)
	if err != nil {
		t.Fatalf("renewed lease must be cached: %v", err)
	}
	if cached.ExpiresAt.Before(f.u.now()) {
		t.Fatal("cache must hold the fresh lease")
	}
	// The durable binding survives lease cache churn and carries no ingress.
	b, err := f.bindings.Get(ctx, pipeID)
	if err != nil {
		t.Fatal("binding must survive cache expiry")
	}
	if b.Locator == "" || b.RecipientPublic == "" {
		t.Fatal("binding independent of lease cache")
	}
}

func TestAcquireAcceptedPipeAccessWithoutBindingFails(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	_, err := f.u.AcquireAcceptedPipeAccess(ctx, "pip_never_accepted")
	if err == nil {
		t.Fatal("acquire without accepted binding must fail")
	}
	if !strings.HasPrefix(err.Error(), "authorization_denied") {
		t.Fatalf("expected authorization_denied, got %v", err)
	}
}

// TestAcquireAcceptedPipeAccessRenewalFailuresFailClosed covers revoked /
// epoch-stale / persistence failures and a renewed-lease subject mismatch at
// the L4 boundary: every one denies acquisition (fail closed).
func TestAcquireAcceptedPipeAccessRenewalFailuresFailClosed(t *testing.T) {
	ctx := context.Background()

	for _, failure := range []string{"revoked", "epoch stale", "accepted state unavailable"} {
		f := newExactFixture(t)
		pipeID, invite := f.makeShare(t)
		if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite}); err != nil {
			t.Fatal(err)
		}
		if err := f.leaseCache.Delete(ctx, pipeID); err != nil {
			t.Fatal(err)
		}
		f.renewal.fail(errors.New(failure))
		if _, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID); err == nil {
			t.Fatalf("renewal failure %q must fail closed", failure)
		}
	}

	// Renewal returning a lease bound to a different subject must fail closed.
	f := newExactFixture(t)
	pipeID, invite := f.makeShare(t)
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite}); err != nil {
		t.Fatal(err)
	}
	_ = f.leaseCache.Delete(ctx, pipeID)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub := accesslease.PublicString(otherPriv.Public().(ed25519.PublicKey))
	f.renewal.subjectOverride = otherPub
	if _, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID); err == nil {
		t.Fatal("renewed lease with mismatched subject must fail verification")
	}
}

// TestConnectCompatibilityTokenThenRemoteRefactor proves the legacy connect
// path consumes the new foundation: --token persists the binding and caches
// the initial lease; --remote reuses the accepted binding with no redemption.
func TestConnectCompatibilityTokenThenRemoteRefactor(t *testing.T) {
	ctx := context.Background()

	// Token path: connect --token <invite>.
	f := newExactFixture(t)
	pipeID, invite := f.makeShare(t)
	res, err := f.u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9991"})
	if err != nil {
		t.Fatalf("connect --token: %v", err)
	}
	if res.TargetPipeID != pipeID {
		t.Fatalf("target %s != %s", res.TargetPipeID, pipeID)
	}
	if _, err := f.bindings.Get(ctx, pipeID); err != nil {
		t.Fatal("token connect must persist the durable binding")
	}
	if _, err := f.leaseCache.Get(ctx, pipeID); err != nil {
		t.Fatal("token connect must cache the initial lease")
	}
	if _, _, err := f.pipes.Get(ctx, res.PipeRef.String()); err != nil {
		t.Fatal("token connect must create the legacy local Pipe")
	}
	if f.redemption.calls != 1 {
		t.Fatalf("expected exactly one redemption, got %d", f.redemption.calls)
	}

	// Remote path (e.g. after restart): no invite, no re-redemption, same
	// listener (one local connect Pipe per remote Pipe under current
	// compatibility semantics).
	res2, err := f.u.ConnectExact(ctx, ConnectExactInput{RemotePipeID: pipeID, LocalListen: "127.0.0.1:9991"})
	if err != nil {
		t.Fatalf("connect --remote: %v", err)
	}
	if res2.TargetPipeID != pipeID {
		t.Fatalf("remote target %s != %s", res2.TargetPipeID, pipeID)
	}
	if f.redemption.calls != 1 {
		t.Fatalf("remote reconnect must not re-commit redemption, calls=%d", f.redemption.calls)
	}
	if f.renewal.callCount(pipeID) != 0 {
		t.Fatalf("unexpired cached lease must be reused on remote reconnect, calls=%d", f.renewal.callCount(pipeID))
	}
}

func TestConnectExactRequiresExactlyOneOfTokenOrRemote(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	if _, err := f.u.ConnectExact(ctx, ConnectExactInput{LocalListen: "127.0.0.1:9989"}); err == nil {
		t.Fatal("connect without token or remote must fail")
	}
	if _, err := f.u.ConnectExact(ctx, ConnectExactInput{Invite: "x", RemotePipeID: "pip_x", LocalListen: "127.0.0.1:9988"}); err == nil {
		t.Fatal("connect with both token and remote must fail")
	}
}

func TestConnectRemoteWithoutBindingFailsClosed(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	_, err := f.u.ConnectExact(ctx, ConnectExactInput{RemotePipeID: "pip_unknown", LocalListen: "127.0.0.1:9987"})
	if err == nil {
		t.Fatal("connect --remote without binding must fail")
	}
	if !strings.Contains(err.Error(), "authorization_denied") {
		t.Fatalf("expected authorization_denied, got %v", err)
	}
}

// TestRenewalAfterInviteExpiryFailsNoInvite proves the invite is bootstrap
// only: after acceptance the bond renews with the clock past invite expiry.
func TestRenewalAfterInviteExpirySucceeds(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	t0 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	f.pinClock(t0)
	pipeID, invite := f.makeShare(t)
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite}); err != nil {
		t.Fatal(err)
	}
	// Invite expires 10 min after issue; acquire 20 min later through the
	// binding. Renewal must succeed (no invite involved).
	f.pinClock(t0.Add(20 * time.Minute))
	if _, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID); err != nil {
		t.Fatalf("renewal after invite expiry: %v", err)
	}
}

// TestShareExactMintsInviteAtCurrentEpoch proves ShareExact reads the current
// authoritative pipe-access epoch and mints the owner binding + invite at it,
// so post-revocation shares re-authorize instead of staying stale.
func TestShareExactMintsInviteAtCurrentEpoch(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	f.epoch.setEpoch(1) // a pipe-access revocation already advanced the epoch
	att, err := f.u.Attach(ctx, AttachInput{Name: "ep", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}
	share, err := f.u.ShareExact(ctx, ShareExactInput{PipeRef: att.PipeRef.String()})
	if err != nil {
		t.Fatalf("share at epoch 1: %v", err)
	}
	invite, err := accesslease.DecodeInvite(share.Invite)
	if err != nil {
		t.Fatal(err)
	}
	if invite.AccessEpoch != 1 {
		t.Fatalf("invite must carry current epoch 1, got %d", invite.AccessEpoch)
	}
	// The invited binding epoch matches too (provenance).
	binding, err := accesslease.DecodeOwnerBinding(invite.Binding)
	if err != nil || binding.Epoch != 1 {
		t.Fatalf("owner binding epoch mismatch: %v", err)
	}
}

// TestPostRevokeReauthorization is the full use-case epoch lifecycle: accept
// at epoch 0 -> epoch advances to 1 -> renewal denied -> fresh share+accept at
// epoch 1 -> acquisition and renewal work again (no kill switch).
func TestPostRevokeReauthorization(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	t0 := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	f.pinClock(t0)
	pipeID, invite := f.makeShare(t)
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite}); err != nil {
		t.Fatal(err)
	}
	// Revoke -> epoch advances to 1; the accepted relationship (epoch 0) is
	// stale and the authority denies renewal (the L4 layer surfaces the
	// server-side denial; the fake stands in for the L3 epoch check).
	f.epoch.setEpoch(1)
	if err := f.leaseCache.Delete(ctx, pipeID); err != nil {
		t.Fatal(err)
	}
	f.renewal.fail(errors.New("exact_renewal: server denied: renewal_failed"))
	if _, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID); err == nil {
		t.Fatal("stale accepted relationship must not renew after revocation")
	}
	f.renewal.fail(nil)
	// A fresh share after the revoke mints an invite at epoch 1 and a new
	// accept replaces the authoritative relationship.
	f.epoch.setEpoch(1)
	att, _, _ := f.pipes.Get(ctx, "pipe/origin")
	share2, err := f.u.ShareExact(ctx, ShareExactInput{PipeRef: att.Ref.String()})
	if err != nil {
		t.Fatal(err)
	}
	invite2, _ := accesslease.DecodeInvite(share2.Invite)
	if invite2.AccessEpoch != 1 {
		t.Fatalf("post-revoke invite epoch: %d", invite2.AccessEpoch)
	}
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: share2.Invite}); err != nil {
		t.Fatalf("re-accept after revocation: %v", err)
	}
	// Acquisition works again through the fresh relationship.
	f.renewal.setTTL(10 * time.Minute)
	cached, _ := f.leaseCache.Get(ctx, pipeID)
	_ = cached
	if err := f.leaseCache.Delete(ctx, pipeID); err != nil {
		t.Fatal(err)
	}
	f.renewal.epoch = 1
	access, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID)
	if err != nil {
		t.Fatalf("acquire after re-authorization: %v", err)
	}
	if access.AccessLease.AccessEpoch != 1 {
		t.Fatalf("re-acquired lease must carry new epoch, got %d", access.AccessLease.AccessEpoch)
	}
}

// TestMultipleInvitesSameRecipientDeterministic proves accepting a second
// invite for the same Pipe+recipient replaces the current relationship while
// the redemption history stays intact and the binding remains a single row
// with the identical recipient identity.
func TestMultipleInvitesSameRecipientDeterministic(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	pipeID, inviteA := f.makeShare(t)
	if _, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: inviteA}); err != nil {
		t.Fatal(err)
	}
	// Second independent share + accept by the same recipient.
	shareB, err := f.u.ShareExact(ctx, ShareExactInput{PipeRef: "pipe/origin"})
	if err != nil {
		t.Fatal(err)
	}
	if shareB.PipeID != pipeID {
		t.Fatalf("same pipe expected")
	}
	second, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: shareB.Invite})
	if err != nil {
		t.Fatalf("second accept: %v", err)
	}
	b, _ := f.bindings.Get(ctx, pipeID)
	if b.RecipientPublic != second.RecipientPublic || len(f.bindings.items) != 1 {
		t.Fatalf("binding must be one row with the same recipient: %d rows", len(f.bindings.items))
	}
	// Acquisition still works: the deterministic current relationship returns
	// usable access material.
	access, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID)
	if err != nil {
		t.Fatalf("acquire after second invite: %v", err)
	}
	if access.RemotePipeID != pipeID || access.AccessLease.AudiencePipeID != pipeID {
		t.Fatalf("acquired access mismatch: %#v", access)
	}
}

// TestSkewedIssuerClockStillAcquiresAccess is the short deterministic E2E
// for the bounded clock-skew fix: the issuer (redemption/renewal fake) mints
// credentials with a clock 25s AHEAD of the consumer. Issues within
// MaxClockSkew (30s) must verify and produce usable access; the whole
// accept -> acquire path completes end-to-end (in-process, no sleeps).
func TestSkewedIssuerClockStillAcquiresAccess(t *testing.T) {
	f := newExactFixture(t)
	ctx := context.Background()
	t0 := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	f.pinClock(t0)
	pipeID, invite := f.makeShare(t)

	// Now the issuer's clock runs 25s ahead of the consumer during ALL
	// credential minting (redemption lease + renewal lease).
	skewed := t0.Add(25 * time.Second)
	f.pinClock(skewed)
	f.u.Now = fixedClock(t0) // consumer verification clock stays at t0

	// Accept: redemption mints a lease with IssuedAt/NotBefore = t0+25s;
	// local Verify runs at t0 and must accept it under the 30s skew fence.
	accepted, err := f.u.AcceptExactInvite(ctx, AcceptExactInviteInput{Invite: invite})
	if err != nil {
		t.Fatalf("accept with skewed issuer: %v", err)
	}
	// Acquire from cache: the cached lease verifies at t0 despite NotBefore
	// being 25s in the "future".
	access, err := f.u.AcquireAcceptedPipeAccess(ctx, pipeID)
	if err != nil {
		t.Fatalf("acquire with skewed issuer: %v", err)
	}
	if access.RemotePipeID != pipeID || !access.AccessLease.ExpiresAt.After(t0) {
		t.Fatalf("acquired access invalid: %#v", access)
	}
	_ = accepted
}
