package usecase

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/exactauthority"
	"github.com/origama/tubo-rebuilt/internal/localkey"
)

// ShareExact loads an owned Pipe, proves ownership, provisions-or-loads the
// dedicated Exact Authority key, and mints a canonical one-time ExactInvite.
// The exact path requires no Scope, membership, or discovery.
//
// Failure semantics:
//   - Missing Pipe -> not_found, no state change.
//   - Owner key unreadable -> persistence_failed, no state change.
//   - Owner key does not derive the PipeID -> authorization_denied. A foreign
//     or non-owned Pipe can never be shared.
//   - Owner runtime locator unavailable -> runtime_unavailable, no Exact
//     Authority provisioning and no invite.
//   - Exact Authority provisioning failure -> persistence_failed, no invite.
//
// Repeated shares provision the same dedicated authority key (create-or-load)
// and never change the Pipe definition's identity.
func (u UseCases) ShareExact(ctx context.Context, in ShareExactInput) (ShareExactResult, error) {
	spec, ownerKeyFile, err := u.Pipes.Get(ctx, in.PipeRef)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ShareExactResult{}, fmt.Errorf("not_found: %w", err)
		}
		return ShareExactResult{}, persistenceError("pipe", err)
	}
	owner, err := localkey.Load(ownerKeyFile)
	if err != nil {
		return ShareExactResult{}, persistenceError("owner key", err)
	}
	ownerPublic := owner.Public().(ed25519.PublicKey)
	if accesslease.PipeIDFromOwner(ownerPublic) != spec.PipeID {
		return ShareExactResult{}, errors.New("authorization_denied: pipe is not owned by this identity")
	}
	if u.OwnerLocator == nil {
		return ShareExactResult{}, errors.New("runtime_unavailable: owner exact-redemption locator is unavailable")
	}
	redemptionAddress, err := u.OwnerLocator.LocateOwner(ctx, spec.Ref, spec.PipeID)
	if err != nil {
		return ShareExactResult{}, err
	}
	redemptionAddress = strings.TrimSpace(redemptionAddress)
	if redemptionAddress == "" {
		return ShareExactResult{}, errors.New("runtime_unavailable: owner exact-redemption locator is unavailable")
	}
	if strings.TrimSpace(u.RedemptionProtocol) == "" {
		return ShareExactResult{}, errors.New("runtime_unavailable: owner exact-redemption protocol is unavailable")
	}
	if u.ExactEpoch == nil {
		return ShareExactResult{}, errors.New("runtime_unavailable: exact authorization epoch is unavailable")
	}
	// Mint against the CURRENT authoritative pipe-access epoch: after a
	// revocation the epoch is N+1, so the fresh invite is valid again while
	// every pre-revocation invite is stale (redemption and renewal both
	// require epoch equality).
	epoch, err := u.ExactEpoch.PipeAccessEpoch(ctx, spec.PipeID)
	if err != nil {
		return ShareExactResult{}, persistenceError("exact authorization epoch", err)
	}
	keyPath, err := exactauthority.KeyPath(u.Secrets, spec.PipeID)
	if err != nil {
		return ShareExactResult{}, persistenceError("exact authority path", err)
	}
	workflow := exactauthority.LocalOwnerWorkflow{
		KeyPath:            keyPath,
		PipeID:             spec.PipeID,
		Epoch:              epoch,
		RedemptionPeers:    redemptionAddress,
		RedemptionProtocol: u.RedemptionProtocol,
	}
	auth, err := workflow.Provision(owner)
	if err != nil {
		return ShareExactResult{}, persistenceError("exact authority", err)
	}
	now := u.now()
	invite, err := accesslease.NewExactInvite(auth.Private, auth.Binding, spec.PipeID, now, DefaultInviteTTL, epoch)
	if err != nil {
		return ShareExactResult{}, authorizationDenied(err)
	}
	encoded, err := accesslease.EncodeInvite(invite)
	if err != nil {
		return ShareExactResult{}, authorizationDenied(err)
	}
	return ShareExactResult{
		PipeRef:   spec.Ref,
		PipeID:    spec.PipeID,
		Invite:    encoded,
		ExpiresAt: invite.ExpiresAt,
	}, nil
}
