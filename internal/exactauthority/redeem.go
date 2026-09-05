// Package exactauthority owns the Exact Pipe Authority key lifecycle (step
// 6B1b) and the remote redemption handler (step 6B2). The concrete
// OpenAuthorizer adapter lives in internal/runtime (composition root) so
// exactauthority imports only accesslease + localkey + stdlib.
// Redemption receives only read-only Loader capability; LocalOwnerWorkflow
// stays in trusted local owner code paths.
package exactauthority

import (
	"context"
	"errors"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

// RedeemExactInvite implements the mandatory ordering for remote exact-invite
// redemption (step 6B2). It receives only a Loader (read-only exact authority
// key access), never a LocalOwnerWorkflow, so remote redemption cannot
// provision a new key or escalate itself.
//
// Mandatory ordering:
//
//  1. Bound frame size (caller responsibility, enforced by wire.ReadFrame)
//  2. Canonical ExactInvite decode + fixed type/version/shape
//  3. OwnerBinding verify (self-certification, owner sig, bound dedicated key, exact Pipe)
//  4. Loader.Load EXISTING key only
//  5. Invite signature verify vs bound key
//  6. Time window / one_time / epoch checks
//  7. Recipient public key validation
//  8. Fresh server challenge generation
//  9. Recipient possession proof verify
//  10. ONLY NOW: atomic redemption mutation
//  11. Mint canonical AccessLease (recipient subject, exact Pipe, dedicated issuer, current epoch, short expiry)
//  12. Return lease
//
// Steps 4-9 produce zero DB mutations, zero generated keys, zero secret files,
// zero provisioning. Before step 10: no state changes at all.
func RedeemExactInvite(
	ctx context.Context,
	now time.Time,
	encodedInvite string,
	recipientPublic string,
	serverChallenge string,
	challengeProof string,
	loader Loader,
	state RevocationState,
	redemption RedemptionStore,
	leaseLifetime time.Duration,
) (accesslease.AccessLease, error) {
	var zero accesslease.AccessLease

	// Step 2: Decode the invite.
	invite, err := accesslease.DecodeInvite(encodedInvite)
	if err != nil {
		return zero, err
	}

	// Step 3: Verify the embedded OwnerBinding.
	binding, err := accesslease.DecodeOwnerBinding(invite.Binding)
	if err != nil {
		return zero, errors.New("exactauthority: redeeming invite has invalid owner binding")
	}
	if _, err := binding.VerifyOwnerBinding(); err != nil {
		return zero, errors.New("exactauthority: redeeming invite owner binding invalid")
	}
	ownerPub, err := accesslease.PublicFromString(binding.Owner)
	if err != nil {
		return zero, errors.New("exactauthority: redeeming invite invalid owner key")
	}
	if !accesslease.VerifyPipeOwner(invite.AudiencePipeID, ownerPub) {
		return zero, errors.New("exactauthority: redeeming invite owner is not the Pipe owner")
	}
	if binding.AuthorityKey != invite.Issuer {
		return zero, errors.New("exactauthority: redeeming invite issuer not bound by owner")
	}
	if binding.PipeID != invite.AudiencePipeID {
		return zero, errors.New("exactauthority: redeeming invite binding pipe mismatch")
	}

	// Step 4: Load the EXISTING exact authority key. Must already exist.
	auth, err := loader.Load()
	if err != nil {
		return zero, errors.New("exactauthority: redeeming invite authority key unavailable")
	}

	// Step 5: Verify invite signature vs the bound exact authority key.
	if err := invite.Verify(now, invite.AudiencePipeID); err != nil {
		return zero, err
	}

	// Step 6: one_time already checked by invite.Verify.
	if !invite.OneTime {
		return zero, errors.New("exactauthority: invite must be one-time")
	}

	// Step 7: Validate recipient public key is a canonical Ed25519 key.
	recipientPubKey, err := accesslease.PublicFromString(recipientPublic)
	if err != nil {
		return zero, errors.New("exactauthority: invalid recipient public key")
	}

	// Step 8 & 9: The caller (redemption server) must have performed the
	// challenge-response exchange: server generates challenge, recipient signs
	// it, server verifies. The serverChallenge is the challenge that was
	// generated and sent to the recipient; challengeProof is what came back.
	if serverChallenge == "" {
		return zero, errors.New("exactauthority: missing server challenge")
	}
	if !accesslease.VerifyProof(
		recipientPubKey,
		challengeProof,
		accesslease.RedeemProofPurpose,
		invite.JTI,
		invite.AudiencePipeID,
		serverChallenge,
	) {
		return zero, errors.New("exactauthority: invalid challenge proof in redemption")
	}

	// Steps 6B: Check revocation and epoch before mutation. An invite is
	// acceptable only while the authoritative pipe-access epoch equals the
	// epoch the invite was minted at: a revoked (advanced) epoch makes every
	// earlier invite stale, and a valid invite minted at a future epoch is
	// equally refused (fail closed, epoch rollback protection).
	if state != nil {
		revoked, revErr := state.IsRevoked(ctx, invite.JTI, "exact.invite")
		if revErr != nil {
			return zero, errors.New("exactauthority: revocation state unavailable")
		}
		if revoked {
			return zero, errors.New("exactauthority: invite revoked")
		}
		currentEpoch, epochErr := state.RevocationEpoch(ctx, invite.AudiencePipeID, "pipe-access")
		if epochErr != nil {
			return zero, errors.New("exactauthority: epoch state unavailable")
		}
		if currentEpoch != invite.AccessEpoch {
			return zero, errors.New("exactauthority: invite epoch stale")
		}
	}

	// Step 10: ONLY NOW atomic redemption mutation + authoritative accepted
	// relationship write. The accepted relationship records the invite epoch;
	// later renewals are authorized against exactly that epoch.
	sessionID, err := accesslease.NewSessionID()
	if err != nil {
		return zero, err
	}
	if redemption != nil {
		ok, redeemErr := redemption.RedeemInviteAndAccept(ctx, invite.JTI, invite.AudiencePipeID, sessionID, recipientPublic, invite.AccessEpoch, now)
		if redeemErr != nil {
			return zero, errors.New("exactauthority: redemption persistence failed")
		}
		if !ok {
			return zero, errors.New("exactauthority: invite already redeemed by another")
		}
	}

	// Step 11: Mint canonical AccessLease. The server-authoritative lifetime
	// (leaseLifetime, bounded to MaxTTL) is further capped by the remaining
	// invite lifetime so the bootstrap lease never outlives the credential
	// that authorized it. Post-acceptance access no longer depends on this
	// initial lease: renewal through the durable binding mints fresh ones.
	if leaseLifetime <= 0 || leaseLifetime > accesslease.MaxTTL {
		leaseLifetime = accesslease.MaxTTL
	}
	if lifetime := invite.ExpiresAt.Sub(now); lifetime < leaseLifetime && lifetime > 0 {
		leaseLifetime = lifetime
	}
	if leaseLifetime <= 0 {
		leaseLifetime = accesslease.MaxTTL
	}
	lease, err := accesslease.NewAccessLease(
		auth.Private,
		recipientPublic,
		invite.AudiencePipeID,
		now,
		leaseLifetime,
		invite.AccessEpoch,
	)
	if err != nil {
		return zero, err
	}

	return lease, nil
}

// randomChallenge is re-exported for clarity.
func randomChallenge() (string, error) { return accesslease.RandomChallenge() }

// NewAccessLeaseForReissuance creates a fresh lease for an accepted
// relationship, minted at the accepted pipe-access epoch. It is used by the
// initial redemption retry path and by post-acceptance renewal.
func NewAccessLeaseForReissuance(
	auth Authority,
	recipientPublic string,
	pipeID string,
	now time.Time,
	leaseLifetime time.Duration,
	accessEpoch uint64,
) (accesslease.AccessLease, error) {
	if leaseLifetime <= 0 || leaseLifetime > accesslease.MaxTTL {
		leaseLifetime = accesslease.MaxTTL
	}
	return accesslease.NewAccessLease(auth.Private, recipientPublic, pipeID, now, leaseLifetime, accessEpoch)
}
