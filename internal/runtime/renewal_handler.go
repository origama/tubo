// Package runtime owns the exact renewal handler for the durable accepted
// remote-Pipe binding model. The handler implements the
// /tubo/exact-renewal/1 protocol: bounded request (Pipe, accepted recipient,
// owner-bound binding) -> fresh challenge -> recipient possession proof ->
// exactauthority.RenewAcceptedAccess -> fresh short-lived AccessLease.
//
// The original invite is never required again after acceptance. The handler
// serves only its own pipe, loads only the EXISTING dedicated authority key
// (read-only Loader), requires an accepted authority-side relationship, and
// fails closed on revocation/epoch/persistence errors.
package runtime

import (
	"context"
	"time"

	core "github.com/libp2p/go-libp2p/core/network"

	"github.com/origama/tubo/internal/accesslease"
	"github.com/origama/tubo/internal/exactauthority"
	wire "github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/store"
)

// exactRenewalHandler handles /tubo/exact-renewal/1 for one exact Pipe.
type exactRenewalHandler struct {
	state    *store.Store
	pipeID   string
	leaseTTL time.Duration
}

// newExactRenewalHandler creates the renewal handler for the given pipe.
// leaseTTL is the server-authoritative short-lived lease lifetime (bounded to
// MaxTTL), matching the redemption handler's facility.
func newExactRenewalHandler(state *store.Store, pipeID string, leaseTTL time.Duration) *exactRenewalHandler {
	if leaseTTL <= 0 || leaseTTL > accesslease.MaxTTL {
		leaseTTL = accesslease.MaxTTL
	}
	return &exactRenewalHandler{state: state, pipeID: pipeID, leaseTTL: leaseTTL}
}

// handle is the stream handler for /tubo/exact-renewal/1.
func (h *exactRenewalHandler) handle(stream core.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

	fail := func(message string) {
		_ = wire.WriteFrame(stream, wire.ExactRenewalResponse{OK: false, Error: message})
	}

	// Step 1: bounded request frame.
	var req wire.ExactRenewalRequest
	if err := wire.ReadFrame(stream, &req); err != nil {
		fail("invalid_request")
		return
	}
	if len(req.PipeID) == 0 || len(req.PipeID) > 8192 || len(req.RecipientPublic) == 0 || len(req.RecipientPublic) > 8192 || len(req.OwnerBinding) == 0 || len(req.OwnerBinding) > 49152 {
		fail("invalid_request: field too large or empty")
		return
	}
	// The handler serves only the pipe identified by h.pipeID.
	if req.PipeID != h.pipeID {
		fail("not_found")
		return
	}
	// Step 2: build the Loader from the owner-bound binding the durable
	// relationship carries; RenewAcceptedAccess re-verifies it cryptographically.
	keyPath, keyErr := exactauthority.KeyPath(h.state.SecretPath(), h.pipeID)
	if keyErr != nil {
		fail("authority_unavailable")
		return
	}
	binding, bindErr := accesslease.DecodeOwnerBinding(req.OwnerBinding)
	if bindErr != nil {
		fail("invalid_binding")
		return
	}
	loader := exactauthority.Loader{KeyPath: keyPath, Binding: binding}
	adapter := store.NewExactAuthorityRevocationState(h.state, h.pipeID)

	// Step 3: fresh server challenge.
	challenge, challengeErr := accesslease.RandomChallenge()
	if challengeErr != nil {
		fail("internal_error")
		return
	}
	if err := wire.WriteFrame(stream, wire.ExactRenewalChallenge{Challenge: challenge}); err != nil {
		return
	}

	// Step 4: read the bounded possession proof.
	var proof wire.ExactRenewalProof
	if err := wire.ReadFrame(stream, &proof); err != nil {
		fail("invalid_proof")
		return
	}
	if len(proof.ChallengeProof) == 0 || len(proof.ChallengeProof) > 8192 {
		fail("invalid_proof: field too large or empty")
		return
	}

	// Step 5: authority-side ordering (accepted relationship, revocation,
	// possession proof, fresh lease minting). All failures are generic.
	lease, redeemErr := exactauthority.RenewAcceptedAccess(
		context.Background(),
		time.Now().UTC(),
		req.PipeID,
		req.RecipientPublic,
		req.OwnerBinding,
		challenge,
		proof.ChallengeProof,
		loader,
		adapter,
		adapter,
		h.leaseTTL,
	)
	if redeemErr != nil {
		fail("renewal_failed")
		return
	}

	// Step 6: encode and return the fresh short-lived AccessLease.
	encodedLease, encErr := accesslease.Encode(lease)
	if encErr != nil {
		fail("internal_error")
		return
	}
	if err := wire.WriteFrame(stream, wire.ExactRenewalResponse{OK: true, AccessLease: encodedLease, ExpiresAt: lease.ExpiresAt}); err != nil {
		return
	}
}
