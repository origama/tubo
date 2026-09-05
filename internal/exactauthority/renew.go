// Package exactauthority owns the Exact Pipe Authority key lifecycle (step
// 6B1b), the remote redemption handler (step 6B2), and post-acceptance exact
// renewal/reissuance (durable remote-Pipe binding). The concrete OpenAuthorizer
// adapter lives in internal/runtime (composition root) so exactauthority imports
// only accesslease + localkey + stdlib.
package exactauthority

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

// RenewAcceptedAccess implements the mandatory ordering for post-acceptance
// exact renewal. It replaces the dead "resend the expired invite" pattern: the
// original ExactInvite is never required again after acceptance. The renewal
// exchange establishes the requested PipeID, the recipient identity, a fresh
// server challenge, recipient possession proof, the existing accepted
// relationship, current revocation/epoch validity, and the correct owner-bound
// Exact Authority, then returns a fresh short-lived AccessLease.
//
// Mandatory ordering (mirrors RedeemExactInvite's fail-closed discipline):
//
//  1. Bounded/canonical PipeID and recipient public key validation
//  2. OwnerBinding decode + owner-signature verify + exact-Pipe match
//  3. Loader.Load EXISTING dedicated key only (never provision)
//  4. Binding authority key must equal the loaded key (owner-bound issuer)
//  5. Accepted binding lookup: recipient X validly accepted for Pipe P
//  6. Revocation: originating invite JTI not revoked; pipe-access epoch not
//     advanced (any pipe-access revocation invalidates further issuance)
//  7. Fresh server challenge possession proof verify (renew purpose)
//  8. Mint fresh short-lived AccessLease under the bound issuer
//
// Steps 2-7 produce zero mutations. The remote caller can never provision
// authority keys, create arbitrary accepted bindings, switch subject or Pipe,
// bypass revocation, or renew a relationship that never existed.
func RenewAcceptedAccess(
	ctx context.Context,
	now time.Time,
	pipeID string,
	recipientPublic string,
	ownerBindingEncoded string,
	serverChallenge string,
	challengeProof string,
	loader Loader,
	state RevocationState,
	accepted AcceptedBindingStore,
	leaseLifetime time.Duration,
) (accesslease.AccessLease, error) {
	var zero accesslease.AccessLease

	// Step 1: bounded canonical PipeID and recipient key.
	if pipeID == "" || len(pipeID) > accesslease.MaxPipeIDLen {
		return zero, errors.New("exactauthority: renewing invalid pipe id")
	}
	recipientPubKey, err := accesslease.PublicFromString(recipientPublic)
	if err != nil {
		return zero, errors.New("exactauthority: renewing invalid recipient public key")
	}

	// Step 2: verify the owner-bound binding the durable relationship carries.
	binding, err := accesslease.DecodeOwnerBinding(ownerBindingEncoded)
	if err != nil {
		return zero, errors.New("exactauthority: renewing binding invalid")
	}
	if _, err := binding.VerifyOwnerBinding(); err != nil {
		return zero, errors.New("exactauthority: renewing binding owner signature invalid")
	}
	if binding.PipeID != pipeID {
		return zero, errors.New("exactauthority: renewing binding pipe mismatch")
	}
	ownerPub, err := accesslease.PublicFromString(binding.Owner)
	if err != nil {
		return zero, errors.New("exactauthority: renewing binding invalid owner key")
	}
	if !accesslease.VerifyPipeOwner(pipeID, ownerPub) {
		return zero, errors.New("exactauthority: renewing binding owner is not the Pipe owner")
	}

	// Step 3: load the EXISTING dedicated exact authority key.
	auth, err := loader.Load()
	if err != nil {
		return zero, errors.New("exactauthority: renewing authority key unavailable")
	}

	// Step 4: the binding must bind exactly this loaded authority key, so the
	// issuer of every renewed lease is the owner-bound Exact Authority.
	if binding.AuthorityKey != accesslease.PublicString(auth.Private.Public().(ed25519.PublicKey)) {
		return zero, errors.New("exactauthority: renewing binding authority mismatch")
	}

	// Step 5: the accepted relationship must exist for this exact recipient+pipe.
	jti, acceptedOK, err := accepted.AcceptedRedemption(ctx, pipeID, recipientPublic)
	if err != nil {
		return zero, errors.New("exactauthority: renewal accepted state unavailable")
	}
	if !acceptedOK {
		return zero, errors.New("exactauthority: no accepted binding for renewal")
	}

	// Step 6: revocation/epoch validity fails closed.
	if state != nil {
		revoked, revErr := state.IsRevoked(ctx, jti, "exact.invite")
		if revErr != nil {
			return zero, errors.New("exactauthority: renewal revocation state unavailable")
		}
		if revoked {
			return zero, errors.New("exactauthority: accepted relationship revoked")
		}
		currentEpoch, epochErr := state.RevocationEpoch(ctx, pipeID, "pipe-access")
		if epochErr != nil {
			return zero, errors.New("exactauthority: renewal epoch state unavailable")
		}
		// Any pipe-access revocation (epoch advanced past issuance baseline
		// zero) invalidates further lease issuance; a lease minted here would
		// be denied by the L2 authorizer's stale-epoch check.
		if currentEpoch > 0 {
			return zero, errors.New("exactauthority: renewal epoch stale")
		}
	}

	// Step 7: fresh server challenge possession proof by the accepted identity.
	if serverChallenge == "" {
		return zero, errors.New("exactauthority: missing renewal challenge")
	}
	if !accesslease.VerifyProof(
		recipientPubKey,
		challengeProof,
		accesslease.RenewProofPurpose,
		pipeID,
		serverChallenge,
	) {
		return zero, errors.New("exactauthority: invalid renewal possession proof")
	}

	// Step 8: mint a fresh short-lived lease under the bound issuer.
	return NewAccessLeaseForReissuance(auth, recipientPublic, pipeID, now, leaseLifetime)
}
