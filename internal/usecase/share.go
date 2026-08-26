package usecase

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"

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
	keyPath, err := exactauthority.KeyPath(u.Secrets, spec.PipeID)
	if err != nil {
		return ShareExactResult{}, persistenceError("exact authority path", err)
	}
	workflow := exactauthority.LocalOwnerWorkflow{
		KeyPath:            keyPath,
		PipeID:             spec.PipeID,
		Epoch:              1, // 8B wires the revocation authorization epoch.
		RedemptionPeers:    u.RedemptionAddress,
		RedemptionProtocol: u.RedemptionProtocol,
	}
	auth, err := workflow.Provision(owner)
	if err != nil {
		return ShareExactResult{}, persistenceError("exact authority", err)
	}
	now := u.now()
	invite, err := accesslease.NewExactInvite(auth.Private, auth.Binding, spec.PipeID, now, DefaultInviteTTL)
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
