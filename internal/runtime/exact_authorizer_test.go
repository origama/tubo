package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/exactauthority"
	"github.com/origama/tubo-rebuilt/internal/openauth"
)

type fakeRevocation struct {
	revoked   map[string]bool
	epochs    map[string]uint64
	epochErr  string
	revokeErr string
}

func (f *fakeRevocation) IsRevoked(_ context.Context, jti, subjectType string) (bool, error) {
	if f.revokeErr != "" {
		return false, errors.New(f.revokeErr)
	}
	key := subjectType + "/" + jti
	return f.revoked[key], nil
}

func (f *fakeRevocation) RevocationEpoch(_ context.Context, pipeID, subjectType string) (uint64, error) {
	if f.epochErr != "" {
		return 0, errors.New(f.epochErr)
	}
	key := subjectType + "/" + pipeID
	return f.epochs[key], nil
}

const testPipeID = "pip_n7s4bq312n_6be3_4e90_a8b4_a87ef9d4d7b5"

func newTestAuthorizerAndLease(t *testing.T, now time.Time) (*ExactOpenAuthorizer, ed25519.PrivateKey, string, ed25519.PrivateKey) {
	t.Helper()
	_, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authPub := authPriv.Public().(ed25519.PublicKey)
	clientPub := accesslease.PublicString(clientPriv.Public().(ed25519.PublicKey))

	lease, err := accesslease.NewAccessLease(authPriv, clientPub, testPipeID, now, 5*time.Minute, 0)
	if err != nil {
		t.Fatalf("NewAccessLease: %v", err)
	}
	enc, err := accesslease.Encode(lease)
	if err != nil {
		t.Fatal(err)
	}

	state := &fakeRevocation{
		revoked: map[string]bool{},
		epochs:  map[string]uint64{},
	}
	auth, err := NewExactOpenAuthorizer(authPub, testPipeID, state)
	if err != nil {
		t.Fatal(err)
	}
	return auth, clientPriv, enc, authPriv
}

func TestExactOpenAuthorizerFailsClosedForInvalidLease(t *testing.T) {
	now := time.Now().UTC()
	auth, clientPriv, _, authPriv := newTestAuthorizerAndLease(t, now)
	clientPub := accesslease.PublicString(clientPriv.Public().(ed25519.PublicKey))
	challenge := "test-challenge"
	proof := accesslease.SignProof(clientPriv, accesslease.OpenProofPurpose, challenge, testPipeID)

	genSignedLease := func(lifetime time.Duration) string {
		lease, _ := accesslease.NewAccessLease(authPriv, clientPub, testPipeID, now, lifetime, 0)
		enc, _ := accesslease.Encode(lease)
		return enc
	}

	cases := []struct {
		name  string
		lease string
	}{
		{name: "missing lease (empty)", lease: ""},
		{name: "malformed lease", lease: "!!!invalid-base64!!!"},
		{name: "non-canonical lease", lease: mustNonCanonicalLease()},
		{name: "wrong authority key", lease: mustWrongIssuerLease(clientPub)},
		{name: "wrong subject", lease: mustWrongSubjectLease(authPriv)},
		{name: "wrong pipe audience", lease: mustWrongPipeLease(authPriv, clientPub)},
		{name: "expired lease", lease: genSignedLease(-time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := openauth.OpenRequest{
				PipeID:         testPipeID,
				ClientPublic:   clientPub,
				Challenge:      challenge,
				ChallengeProof: proof,
				AccessLease:    tc.lease,
				Now:            now,
			}
			result, err := auth.AuthorizeOpen(context.Background(), req)
			if err == nil {
				t.Fatalf("expected fail-closed denial, got %#v", result)
			}
		})
	}
}

func TestExactOpenAuthorizerRejectsInvalidProof(t *testing.T) {
	now := time.Now().UTC()
	auth, clientPriv, encLease, _ := newTestAuthorizerAndLease(t, now)
	clientPub := accesslease.PublicString(clientPriv.Public().(ed25519.PublicKey))
	challenge := "test-challenge"

	// Wrong proof (signed with wrong purpose).
	wrongProof := accesslease.SignProof(clientPriv, "wrong-purpose", challenge, testPipeID)
	req := openauth.OpenRequest{
		PipeID:         testPipeID,
		ClientPublic:   clientPub,
		Challenge:      challenge,
		ChallengeProof: wrongProof,
		AccessLease:    encLease,
		Now:            now,
	}
	if _, err := auth.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("wrong proof accepted")
	}

	// Missing proof.
	req.ChallengeProof = ""
	if _, err := auth.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("empty proof accepted")
	}

	// Proof signed by wrong key.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherProof := accesslease.SignProof(otherPriv, accesslease.OpenProofPurpose, challenge, testPipeID)
	req.ChallengeProof = otherProof
	if _, err := auth.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("wrong key proof accepted")
	}
}

func TestExactOpenAuthorizerChecksRevocation(t *testing.T) {
	now := time.Now().UTC()
	auth, clientPriv, encLease, _ := newTestAuthorizerAndLease(t, now)
	clientPub := accesslease.PublicString(clientPriv.Public().(ed25519.PublicKey))
	challenge := "test-challenge"
	proof := accesslease.SignProof(clientPriv, accesslease.OpenProofPurpose, challenge, testPipeID)

	req := openauth.OpenRequest{
		PipeID:         testPipeID,
		ClientPublic:   clientPub,
		Challenge:      challenge,
		ChallengeProof: proof,
		AccessLease:    encLease,
		Now:            now,
	}

	// First verify success with the fake state that has no revocations.
	result, err := auth.AuthorizeOpen(context.Background(), req)
	if err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	if result.PipeID != testPipeID || result.Subject != clientPub {
		t.Fatalf("unexpected authorization result: %#v", result)
	}

	// Revoke the lease JTI.
	auth.state.(*fakeRevocation).revoked["access-lease/"+result.LeaseID] = true
	if _, err := auth.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("revoked lease accepted")
	}

	// Remove revocation, set stale epoch.
	auth.state.(*fakeRevocation).revoked = map[string]bool{}
	auth.state.(*fakeRevocation).epochs["pipe-access/"+testPipeID] = 999
	if _, err := auth.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("stale epoch lease accepted")
	}

	// Revocation read error.
	auth.state.(*fakeRevocation).revoked = map[string]bool{}
	auth.state.(*fakeRevocation).epochs = map[string]uint64{"pipe-access/" + testPipeID: 1}
	auth.state.(*fakeRevocation).revokeErr = "database unavailable"
	if _, err := auth.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("revocation read error must fail closed")
	}

	// Epoch read error.
	auth.state.(*fakeRevocation).revokeErr = ""
	auth.state.(*fakeRevocation).epochErr = "database unavailable"
	if _, err := auth.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("epoch read error must fail closed")
	}
}

func TestExactOpenAuthorizerHappyPath(t *testing.T) {
	now := time.Now().UTC()
	auth, clientPriv, encLease, _ := newTestAuthorizerAndLease(t, now)
	clientPub := accesslease.PublicString(clientPriv.Public().(ed25519.PublicKey))
	challenge := "test-challenge"
	proof := accesslease.SignProof(clientPriv, accesslease.OpenProofPurpose, challenge, testPipeID)

	req := openauth.OpenRequest{
		PipeID:         testPipeID,
		ClientPublic:   clientPub,
		Challenge:      challenge,
		ChallengeProof: proof,
		AccessLease:    encLease,
		Now:            now,
	}
	result, err := auth.AuthorizeOpen(context.Background(), req)
	if err != nil {
		t.Fatalf("happy authorization failed: %v", err)
	}
	if result.PipeID != testPipeID || result.Subject != clientPub || result.LeaseID == "" || result.ExpiresAt.Before(now) || result.Issuer == "" {
		t.Fatalf("unexpected authorization result: %#v", result)
	}
}

func TestExactOpenAuthorizerNoRevocationState(t *testing.T) {
	now := time.Now().UTC()
	_, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authPub := authPriv.Public().(ed25519.PublicKey)
	clientPub := accesslease.PublicString(clientPriv.Public().(ed25519.PublicKey))

	lease, err := accesslease.NewAccessLease(authPriv, clientPub, testPipeID, now, 5*time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	enc, err := accesslease.Encode(lease)
	if err != nil {
		t.Fatal(err)
	}

	// Revocation state is a mandatory security dependency: construction
	// without it must fail closed. There is no revocation-free configuration
	// in which a valid lease authorizes an open.
	if auth, err := NewExactOpenAuthorizer(authPub, testPipeID, nil); err == nil {
		t.Fatal("authorizer constructed without revocation state")
		_ = auth
	} else {
		t.Logf("nil state correctly rejected: %v", err)
	}
	// And the corresponding denial path: even a fully valid lease can never
	// authorize when no revocation-fenced authorizer exists to check it.
	state := &fakeRevocation{revoked: map[string]bool{}, epochs: map[string]uint64{}}
	auth, authErr := NewExactOpenAuthorizer(authPub, testPipeID, state)
	if authErr != nil {
		t.Fatal(authErr)
	}
	challenge := "test-challenge"
	proof := accesslease.SignProof(clientPriv, accesslease.OpenProofPurpose, challenge, testPipeID)
	req := openauth.OpenRequest{
		PipeID:         testPipeID,
		ClientPublic:   clientPub,
		Challenge:      challenge,
		ChallengeProof: proof,
		AccessLease:    enc,
		Now:            now,
	}
	result, err := auth.AuthorizeOpen(context.Background(), req)
	if err != nil {
		t.Fatalf("authorization with revocation state failed: %v", err)
	}
	if result.PipeID != testPipeID {
		t.Fatal("unexpected result")
	}
}

// --- Test helpers ---

func mustNonCanonicalLease() string {
	_, authPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, clientPriv, _ := ed25519.GenerateKey(rand.Reader) //nolint:govet
	_ = clientPriv
	lease, _ := accesslease.NewAccessLease(authPriv, "testsub", testPipeID, time.Now().UTC(), 5*time.Minute, 0)
	enc, _ := accesslease.Encode(lease)
	return enc + "="
}

func mustWrongIssuerLease(clientPub string) string {
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	lease, _ := accesslease.NewAccessLease(otherPriv, clientPub, testPipeID, time.Now().UTC(), 5*time.Minute, 0)
	enc, _ := accesslease.Encode(lease)
	return enc
}

func mustWrongSubjectLease(authPriv ed25519.PrivateKey) string {
	lease, _ := accesslease.NewAccessLease(authPriv, "wrong-subject", testPipeID, time.Now().UTC(), 5*time.Minute, 0)
	enc, _ := accesslease.Encode(lease)
	return enc
}

func mustWrongPipeLease(authPriv ed25519.PrivateKey, clientPub string) string {
	lease, _ := accesslease.NewAccessLease(authPriv, clientPub, "pip_wrong", time.Now().UTC(), 5*time.Minute, 0)
	enc, _ := accesslease.Encode(lease)
	return enc
}

// Ensure fakeRevocation satisfies exactauthority.RevocationState at compile time.
var _ exactauthority.RevocationState = (*fakeRevocation)(nil)
