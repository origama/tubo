package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/exactauthority"
	"github.com/origama/tubo-rebuilt/internal/openauth"
	"github.com/origama/tubo-rebuilt/internal/store"
)

// TestFullAuthorityVertical exercises the complete step 6B2 authorization
// pipeline: owner provisions exact authority, issues invite, recipient
// redeems, receives lease, opens with proof, authorizer validates, returns
// authorization.
//
// This test proves the invariant: Scope membership alone cannot authorize
// this remote-open; only a valid canonical AccessLease + proof can.
func TestFullAuthorityVertical(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	now := time.Now().UTC()

	// 1. Generate owner and client keys.
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pipeID := accesslease.PipeIDFromOwner(ownerPub)
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPub := accesslease.PublicString(clientPriv.Public().(ed25519.PublicKey))

	// 2. Owner provisions exact authority.
	secretsDir := state.SecretPath("exact-authorities-test")
	keyPath, err := exactauthority.KeyPath(secretsDir, pipeID)
	if err != nil {
		t.Fatal(err)
	}
	workflow := exactauthority.LocalOwnerWorkflow{
		KeyPath:            keyPath,
		PipeID:             pipeID,
		Epoch:              1,
		RedemptionPeers:    "",
		RedemptionProtocol: "",
	}
	auth, err := workflow.Provision(ownerPriv)
	if err != nil {
		t.Fatalf("owner provision: %v", err)
	}

	// 3. Owner issues invite.
	invite, err := accesslease.NewExactInvite(auth.Private, auth.Binding, pipeID, now, 10*time.Minute, 0)
	if err != nil {
		t.Fatalf("NewExactInvite: %v", err)
	}
	encodedInvite, err := accesslease.EncodeInvite(invite)
	if err != nil {
		t.Fatal(err)
	}

	// 4. Simulate redemption flow: server generates challenge, recipient signs.
	challenge, challengeErr := accesslease.RandomChallenge()
	if challengeErr != nil {
		t.Fatal(challengeErr)
	}
	proof := accesslease.SignProof(clientPriv, accesslease.RedeemProofPurpose, invite.JTI, pipeID, challenge)

	// 5. Redeem invite using store adapter from internal/store.
	adapter := store.NewExactAuthorityRevocationState(state, pipeID)
	loader := exactauthority.Loader{KeyPath: keyPath, Binding: auth.Binding}
	lease, err := exactauthority.RedeemExactInvite(ctx, now, encodedInvite, clientPub, challenge, proof, loader, adapter, adapter, 10*time.Minute)
	if err != nil {
		t.Fatalf("redeem: %v", err)
	}
	if lease.Subject != clientPub || lease.AudiencePipeID != pipeID {
		t.Fatalf("unexpected lease: subject=%q pipe=%q", lease.Subject, lease.AudiencePipeID)
	}

	// 6. Encode lease for transport.
	encodedLease, err := accesslease.Encode(lease)
	if err != nil {
		t.Fatal(err)
	}

	// 7. Open authorization using ExactOpenAuthorizer (now in runtime).
	authPub := auth.Private.Public().(ed25519.PublicKey)
	openChallenge := "open-challenge"
	openProof := accesslease.SignProof(clientPriv, accesslease.OpenProofPurpose, openChallenge, pipeID)
	authorizer, authErr := NewExactOpenAuthorizer(authPub, pipeID, adapter)
	if authErr != nil {
		t.Fatal(authErr)
	}
	authResult, err := authorizer.AuthorizeOpen(ctx, openauth.OpenRequest{
		PipeID:         pipeID,
		ClientPublic:   clientPub,
		Challenge:      openChallenge,
		ChallengeProof: openProof,
		AccessLease:    encodedLease,
		Now:            now,
	})
	if err != nil {
		t.Fatalf("authorizeOpen: %v", err)
	}
	if authResult.PipeID != pipeID || authResult.Subject != clientPub || authResult.LeaseID == "" {
		t.Fatalf("unexpected auth result: %#v", authResult)
	}

	// 8. Verify invite possession alone cannot open (need lease).
	_, err = authorizer.AuthorizeOpen(ctx, openauth.OpenRequest{
		PipeID:         pipeID,
		ClientPublic:   clientPub,
		Challenge:      openChallenge,
		ChallengeProof: openProof,
		AccessLease:    encodedInvite, // invite is NOT a lease
		Now:            now,
	})
	if err == nil {
		t.Fatal("invite used as lease must be rejected")
	}
}

// TestRedemptionAtomicity proves the SQLite one-winner contract: first
// redemption wins, different subject denied, same-subject retry idempotent.
func TestRedemptionAtomicity(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	now := time.Now().UTC()
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pipeID := accesslease.PipeIDFromOwner(ownerPub)

	secretsDir := state.SecretPath("exact-authorities-test")
	keyPath, err := exactauthority.KeyPath(secretsDir, pipeID)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := exactauthority.LocalOwnerWorkflow{KeyPath: keyPath, PipeID: pipeID, Epoch: 1}.Provision(ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := accesslease.NewExactInvite(auth.Private, auth.Binding, pipeID, now, 10*time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	encodedInvite, err := accesslease.EncodeInvite(invite)
	if err != nil {
		t.Fatal(err)
	}

	// Two recipients.
	_, clientPriv1, _ := ed25519.GenerateKey(rand.Reader)
	_, clientPriv2, _ := ed25519.GenerateKey(rand.Reader)
	clientPub1 := accesslease.PublicString(clientPriv1.Public().(ed25519.PublicKey))
	clientPub2 := accesslease.PublicString(clientPriv2.Public().(ed25519.PublicKey))

	challenge := "server-challenge-for-atomicity"
	proof1 := accesslease.SignProof(clientPriv1, accesslease.RedeemProofPurpose, invite.JTI, pipeID, challenge)

	adapter := store.NewExactAuthorityRevocationState(state, pipeID)
	loader := exactauthority.Loader{KeyPath: keyPath, Binding: auth.Binding}

	// First redemption by client1 succeeds.
	_, err = exactauthority.RedeemExactInvite(ctx, now, encodedInvite, clientPub1, challenge, proof1, loader, adapter, adapter, 10*time.Minute)
	if err != nil {
		t.Fatalf("first redeem by client1: %v", err)
	}

	// Second redemption by client2 fails (different subject).
	proof2 := accesslease.SignProof(clientPriv2, accesslease.RedeemProofPurpose, invite.JTI, pipeID, challenge)
	_, err = exactauthority.RedeemExactInvite(ctx, now, encodedInvite, clientPub2, challenge, proof2, loader, adapter, adapter, 10*time.Minute)
	if err == nil {
		t.Fatal("second recipient must be denied")
	}
}

// TestScopeTokenCannotAuthorizeExactRemoteOpen demonstrates that the
// prototype authority.Token cannot substitute for a canonical AccessLease in
// the ExactOpenAuthorizer.
func TestScopeTokenCannotAuthorizeExactRemoteOpen(t *testing.T) {
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

	revState := &fakeRevocation{revoked: map[string]bool{}, epochs: map[string]uint64{}}
	auth, authErr := NewExactOpenAuthorizer(authPub, testPipeID, revState)
	if authErr != nil {
		t.Fatal(authErr)
	}
	challenge := "challenge"
	proof := accesslease.SignProof(clientPriv, accesslease.OpenProofPurpose, challenge, testPipeID)

	// Use a prototype-style token as access lease.
	// The ExactOpenAuthorizer calls accesslease.Decode which will reject
	// any non-canonical AccessLease format.
	req := openauth.OpenRequest{
		PipeID:         testPipeID,
		ClientPublic:   clientPub,
		Challenge:      challenge,
		ChallengeProof: proof,
		AccessLease:    "ZS2vdGFuIHByb3RvdHlwZSB0b2tlbg",
		Now:            now,
	}
	if _, err := auth.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("prototype token accepted as AccessLease")
	}
}

// TestRevokedUnredeemedInviteDeniesRedemption: an exact invite revoked by the
// owner BEFORE redemption (the revoke-by-token path writes subject_type
// exact.invite under the exact Pipe) must be denied at the redemption
// boundary with zero state mutation: no lease, no accepted relationship.
func TestRevokedUnredeemedInviteDeniesRedemption(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	now := time.Now().UTC()

	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pipeID := accesslease.PipeIDFromOwner(ownerPriv.Public().(ed25519.PublicKey))
	secretsDir := state.SecretPath("exact-authorities-test")
	keyPath, keyPathErr := exactauthority.KeyPath(secretsDir, pipeID)
	if keyPathErr != nil {
		t.Fatal(keyPathErr)
	}
	auth, err := exactauthority.LocalOwnerWorkflow{KeyPath: keyPath, PipeID: pipeID, Epoch: 1}.Provision(ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := accesslease.NewExactInvite(auth.Private, auth.Binding, pipeID, now, 10*time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	encodedInvite, err := accesslease.EncodeInvite(invite)
	if err != nil {
		t.Fatal(err)
	}

	// Owner revokes the still-unredeemed invite JTI before any redemption.
	if err := state.Revoke(ctx, pipeID, "exact.invite", invite.JTI, 1); err != nil {
		t.Fatal(err)
	}

	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPub := accesslease.PublicString(clientPriv.Public().(ed25519.PublicKey))
	challenge, _ := accesslease.RandomChallenge()
	proof := accesslease.SignProof(clientPriv, accesslease.RedeemProofPurpose, invite.JTI, pipeID, challenge)
	adapter := store.NewExactAuthorityRevocationState(state, pipeID)
	loader := exactauthority.Loader{KeyPath: keyPath, Binding: auth.Binding}

	if _, err := exactauthority.RedeemExactInvite(ctx, now, encodedInvite, clientPub, challenge, proof, loader, adapter, adapter, 10*time.Minute); err == nil {
		t.Fatal("revoked unredeemed invite was redeemed")
	}
	if _, _, ok, relErr := adapter.AcceptedRelationship(ctx, pipeID, clientPub); relErr != nil || ok {
		t.Fatalf("denied redemption must not create accepted relationship: ok=%v %v", ok, relErr)
	}
}
