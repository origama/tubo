// L3 post-acceptance exact renewal tests (durable remote-Pipe binding model).
// All failures fail closed; no sleeps; deterministic clocks.
package exactauthority

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

// fakeRevocationState drives revocation/epoch/persistence fail-closed paths.
type fakeRevocationState struct {
	revokedJTIs map[string]bool // subjectType+"/"+jti -> true
	epoch       uint64
	err         error
}

func (s *fakeRevocationState) IsRevoked(_ context.Context, jti, subjectType string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	return s.revokedJTIs[subjectType+"/"+jti] == true, nil
}

func (s *fakeRevocationState) RevocationEpoch(_ context.Context, pipeID, subjectType string) (uint64, error) {
	if s.err != nil {
		return 0, s.err
	}
	return s.epoch, nil
}

func (s *fakeRevocationState) revokeInvite(jti string) {
	s.revokedJTIs["exact.invite/"+jti] = true
}

// fakeAcceptedStore seeds the authority-side accepted relationship. Exactly
// one current authoritative relationship per (pipe, recipient): the
// deterministic AcceptedBindingStore contract.
type fakeAcceptedStore struct {
	accepted map[string]string // pipeID+"/"+clientPublic -> jti
	epochs   map[string]uint64 // pipeID+"/"+clientPublic -> accepted epoch
	err      error
}

func (s *fakeAcceptedStore) AcceptedRelationship(_ context.Context, pipeID, clientPublic string) (string, uint64, bool, error) {
	if s.err != nil {
		return "", 0, false, s.err
	}
	key := pipeID + "/" + clientPublic
	jti, ok := s.accepted[key]
	if !ok {
		return "", 0, false, nil
	}
	return jti, s.epochs[key], true, nil
}

func (s *fakeAcceptedStore) accept(pipeID, clientPublic, jti string, epoch uint64) {
	key := pipeID + "/" + clientPublic
	s.accepted[key] = jti
	s.epochs[key] = epoch
}

// renewalFixture provisions an owner + dedicated Exact Authority, mints an
// invite, and seeds an accepted relationship for client one.
type renewalFixture struct {
	pipeID          string
	auth            Authority
	loader          Loader
	encodedBinding  string
	accepted        *fakeAcceptedStore
	state           *fakeRevocationState
	clientOne       ed25519.PrivateKey
	clientTwo       ed25519.PrivateKey
	clientOnePublic string
}

func newRenewalFixture(t *testing.T) *renewalFixture {
	t.Helper()
	secretsDir := t.TempDir()
	_, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pipeID := accesslease.PipeIDFromOwner(ownerPriv.Public().(ed25519.PublicKey))
	keyPath, err := KeyPath(secretsDir, pipeID)
	if err != nil {
		t.Fatal(err)
	}
	workflow := LocalOwnerWorkflow{
		KeyPath:            keyPath,
		PipeID:             pipeID,
		Epoch:              1,
		RedemptionPeers:    "/dns4/relay.test/tcp/4001/p2p/peer",
		RedemptionProtocol: "/tubo/exact-redemption/1",
	}
	auth, err := workflow.Provision(ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := accesslease.EncodeOwnerBinding(auth.Binding)
	if err != nil {
		t.Fatal(err)
	}
	_, clientOne, _ := ed25519.GenerateKey(rand.Reader)
	_, clientTwo, _ := ed25519.GenerateKey(rand.Reader)
	clientOnePublic := accesslease.PublicString(clientOne.Public().(ed25519.PublicKey))
	accepted := &fakeAcceptedStore{accepted: make(map[string]string), epochs: make(map[string]uint64)}
	accepted.accept(pipeID, clientOnePublic, "jti_accepted", 0)
	return &renewalFixture{
		pipeID:          pipeID,
		auth:            auth,
		loader:          Loader{KeyPath: keyPath, Binding: auth.Binding},
		encodedBinding:  binding,
		accepted:        accepted,
		state:           &fakeRevocationState{revokedJTIs: make(map[string]bool)},
		clientOne:       clientOne,
		clientTwo:       clientTwo,
		clientOnePublic: clientOnePublic,
	}
}

func renewHelper(f *renewalFixture, ctx context.Context, now time.Time, pipeID, recipientPublic, binding, challenge, proof string, loader Loader, state RevocationState, accepted AcceptedBindingStore, ttl time.Duration) (accesslease.AccessLease, error) {
	return RenewAcceptedAccess(ctx, now, pipeID, recipientPublic, binding, challenge, proof, loader, state, accepted, ttl)
}

func TestRenewAcceptedAccessValidRecipientGetsFreshShortLivedLease(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, challenge)
	lease, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute)
	if err != nil {
		t.Fatalf("valid renewal: %v", err)
	}
	// Lease subject = accepted recipient, audience = exact Pipe, issuer =
	// owner-bound Exact Authority, short-lived.
	if lease.Subject != f.clientOnePublic {
		t.Fatalf("lease subject %q", lease.Subject)
	}
	if lease.AudiencePipeID != f.pipeID {
		t.Fatalf("lease audience %q", lease.AudiencePipeID)
	}
	wantIssuer := accesslease.PublicString(f.auth.Private.Public().(ed25519.PublicKey))
	if lease.Issuer != wantIssuer {
		t.Fatalf("lease issuer %q != %q", lease.Issuer, wantIssuer)
	}
	if lease.ExpiresAt.Sub(lease.IssuedAt) != 10*time.Minute {
		t.Fatalf("renewed lease not short-lived: %v", lease.ExpiresAt.Sub(lease.IssuedAt))
	}
	issuer, _ := accesslease.PublicFromString(wantIssuer)
	if err := lease.Verify(issuer, f.clientOnePublic, f.pipeID, now); err != nil {
		t.Fatalf("renewed lease must verify: %v", err)
	}
}

func TestRenewAcceptedAccessAfterInviteExpirySucceeds(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	t0 := time.Now().UTC()
	// The invite that seeded the relationship expired 20 minutes ago; renewal
	// must not require it.
	later := t0.Add(20 * time.Minute)
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, challenge)
	if _, err := renewHelper(f, ctx, later, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute); err != nil {
		t.Fatalf("renewal after invite expiry: %v", err)
	}
}

func TestRenewAcceptedAccessWithoutAcceptedBindingFails(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, challenge)
	empty := &fakeAcceptedStore{accepted: make(map[string]string), epochs: make(map[string]uint64)}
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, proof, f.loader, f.state, empty, 10*time.Minute); err == nil {
		t.Fatal("renewal without accepted binding must fail")
	}
}

func TestRenewAcceptedAccessDifferentRecipientFails(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	// Client two proves possession but was never accepted.
	proof := accesslease.SignProof(f.clientTwo, accesslease.RenewProofPurpose, f.pipeID, challenge)
	clientTwoPublic := accesslease.PublicString(f.clientTwo.Public().(ed25519.PublicKey))
	if _, err := renewHelper(f, ctx, now, f.pipeID, clientTwoPublic, f.encodedBinding, challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("different recipient must fail")
	}
}

func TestRenewAcceptedAccessInvalidPossessionProofFails(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	// Replay: proof bound to a different challenge.
	stale := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, "stale-challenge")
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, stale, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("replayed/incorrect proof must fail")
	}
	// Wrong purpose: an open proof cannot renew.
	wrongPurpose := accesslease.SignProof(f.clientOne, accesslease.OpenProofPurpose, challenge, f.pipeID)
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, wrongPurpose, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("wrong-purpose proof must fail")
	}
}

func TestRenewAcceptedAccessWrongPipeFails(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, challenge)
	if _, err := renewHelper(f, ctx, now, "pip_other", f.clientOnePublic, f.encodedBinding, challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("wrong pipe must fail")
	}
}

func TestRenewAcceptedAccessStaleRevocationEpochFails(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, challenge)
	f.state.epoch = 1 // pipe-access revoked: further issuance denied
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("stale revocation epoch must deny renewal")
	}
}

func TestRenewAcceptedAccessRevokedInviteFails(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, challenge)
	f.state.revokeInvite("jti_accepted")
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("revoked accepted relationship must deny renewal")
	}
}

func TestRenewAcceptedAccessRevocationPersistenceErrorFailsClosed(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, challenge)
	f.state.err = errors.New("revocation store unavailable")
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("revocation persistence error must fail closed")
	}
	f.state.err = nil
	f.accepted.err = errors.New("accepted binding store unavailable")
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("accepted-state persistence error must fail closed")
	}
}

func TestRenewAcceptedAccessBadBindingOrKeyFailsClosed(t *testing.T) {
	f := newRenewalFixture(t)
	ctx := context.Background()
	now := time.Now().UTC()
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(f.clientOne, accesslease.RenewProofPurpose, f.pipeID, challenge)
	// Tampered binding bytes fail closed.
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, "tampered-bindng", challenge, proof, f.loader, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("tampered binding must fail")
	}
	// Missing authority key (Loader) fails closed.
	missing := Loader{KeyPath: "/nonexistent/renew-key", Binding: f.auth.Binding}
	if _, err := renewHelper(f, ctx, now, f.pipeID, f.clientOnePublic, f.encodedBinding, challenge, proof, missing, f.state, f.accepted, 10*time.Minute); err == nil {
		t.Fatal("missing authority key must fail")
	}
}
