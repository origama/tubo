// Package runtime owns the exact redemption handler for Journey A.
// The handler implements the /tubo/exact-redemption/1 protocol: bounded
// request -> challenge -> recipient proof -> exactauthority.RedeemExactInvite
// -> AccessLease response. Server uses only Loader (read-only authority key
// access), never LocalOwnerWorkflow, so remote redemption cannot provision
// new keys or escalate itself.
package runtime

import (
	"context"
	"time"

	core "github.com/libp2p/go-libp2p/core/network"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/exactauthority"
	"github.com/origama/tubo-rebuilt/internal/localkey"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
)

// exactRedemptionHandler handles the /tubo/exact-redemption/1 protocol.
// It receives only a Loader (read-only), never a LocalOwnerWorkflow.
type exactRedemptionHandler struct {
	state    *store.Store
	pipeID   string
	leaseTTL time.Duration
}

// newExactRedemptionHandler creates a handler for the given pipe's exact
// redemption protocol. leaseTTL is the server-authoritative short-lived lease
// lifetime (bounded to MaxTTL); callers may shorten it via the documented
// TUBO_EXACT_LEASE_TTL facility.
func newExactRedemptionHandler(state *store.Store, pipeID string, leaseTTL time.Duration) *exactRedemptionHandler {
	if leaseTTL <= 0 || leaseTTL > accesslease.MaxTTL {
		leaseTTL = accesslease.MaxTTL
	}
	return &exactRedemptionHandler{state: state, pipeID: pipeID, leaseTTL: leaseTTL}
}

// handle is the stream handler for /tubo/exact-redemption/1.
// Protocol:
//
//  1. Client sends ExactInvite (bounded wire frame)
//  2. Server responds with challenge (random nonce)
//  3. Client sends challenge proof (signature of challenge + invite JTI + pipeID)
//  4. Server calls RedeemExactInvite, returns AccessLease or error
//
// Server never provisions keys, never accesses LocalOwnerWorkflow.
func (h *exactRedemptionHandler) handle(stream core.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

	// Step 1: Read the invite from the client.
	var req wire.ExactRedemptionRequest
	if err := wire.ReadFrame(stream, &req); err != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "invalid_request"})
		return
	}
	if len(req.Invite) > 8192 || len(req.Invite) == 0 {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "invalid_request: invite too large or empty"})
		return
	}

	// Decode the invite to get the audience pipe ID for Loader.
	invite, err := accesslease.DecodeInvite(req.Invite)
	if err != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "invalid_invite"})
		return
	}

	// The handler serves only the pipe identified by h.pipeID.
	if invite.AudiencePipeID != h.pipeID {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "not_found"})
		return
	}

	// Step 2: Generate a challenge.
	challenge, err := accesslease.RandomChallenge()
	if err != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "internal_error"})
		return
	}
	if err := wire.WriteFrame(stream, wire.ExactRedemptionChallenge{Challenge: challenge}); err != nil {
		return
	}

	// Step 3: Read the challenge proof from the client.
	var proof wire.ExactRedemptionProof
	if err := wire.ReadFrame(stream, &proof); err != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "invalid_proof"})
		return
	}
	if len(proof.RecipientPublic) > 8192 || len(proof.ChallengeProof) > 8192 {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "invalid_proof: field too large"})
		return
	}

	// Build the Loader for the exact authority key.
	// Note: SecretPath("exact-authorities") returns <home>/secrets/exact-authorities,
	// and KeyPath already appends "exact-authorities" to the secrets dir, so
	// we pass the base secrets dir (without the subdirectory).
	secretDir := h.state.SecretPath()
	keyPath, keyErr := exactauthority.KeyPath(secretDir, h.pipeID)
	if keyErr != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "authority_unavailable"})
		return
	}
	_, loadErr := localkey.Load(keyPath)
	if loadErr != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "authority_unavailable"})
		return
	}
	// Build the binding from the invite's embedded binding.
	binding, bindErr := accesslease.DecodeOwnerBinding(invite.Binding)
	if bindErr != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "invalid_invite"})
		return
	}

	// Loader reads the key internally; we just need to verify it exists.
	loader := exactauthority.Loader{
		KeyPath: keyPath,
		Binding: binding,
	}
	adapter := store.NewExactAuthorityRevocationState(h.state, h.pipeID)

	// Step 4: Redeem the invite.
	now := time.Now().UTC()
	lease, redeemErr := exactauthority.RedeemExactInvite(
		context.Background(),
		now,
		req.Invite,
		proof.RecipientPublic,
		challenge,
		proof.ChallengeProof,
		loader,
		adapter,
		adapter,
		h.leaseTTL,
	)
	if redeemErr != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "redemption_failed"})
		return
	}

	// Step 5: Encode and return the AccessLease.
	encodedLease, encErr := accesslease.Encode(lease)
	if encErr != nil {
		_ = wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: false, Error: "internal_error"})
		return
	}

	if err := wire.WriteFrame(stream, wire.ExactRedemptionResponse{OK: true, AccessLease: encodedLease, ExpiresAt: lease.ExpiresAt}); err != nil {
		return
	}
}
