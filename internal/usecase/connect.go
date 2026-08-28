package usecase

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/localkey"
)

// ConnectExact decodes and verifies an exact invite, acquires a carrier via
// the injected reachability capability (never Scope/discovery/membership),
// redeems the invite for the canonical short-lived Access Lease, verifies the
// lease binds this recipient to the exact Pipe under the bound issuer, and
// creates a durable local ingress Pipe. The local Pipe is never started here;
// StartPipe (8B) owns runtime launch.
//
// Failure semantics:
//   - Invalid/expired/tampered invite -> authorization_denied, no state change.
//   - Missing local listen -> invalid_input, no state change.
//   - Reachability not wired -> reachability_unavailable, no state change.
//   - Carrier acquisition failure -> pipe_unreachable, no durable definition.
//   - Redemption failure or lease mismatch -> authorization_denied, no
//     durable definition and no misleading ready state.
//   - Local Pipe upsert failure -> persistence_failed; the recipient identity
//     key may remain on disk (harmless, deterministic, reused next attempt).
func (u UseCases) ConnectExact(ctx context.Context, in ConnectExactInput) (ConnectExactResult, error) {
	if strings.TrimSpace(in.LocalListen) == "" {
		return ConnectExactResult{}, errors.New("invalid_input: connect requires a local listen address")
	}
	now := u.now()
	invite, err := accesslease.DecodeInvite(in.Invite)
	if err != nil {
		return ConnectExactResult{}, authorizationDenied(fmt.Errorf("invalid exact invite: %w", err))
	}
	if err := invite.Verify(now, invite.AudiencePipeID); err != nil {
		return ConnectExactResult{}, authorizationDenied(err)
	}
	// Recipient identity is durable and deterministic per invite audience, so
	// a re-connect reuses the same identity across restarts. A friendly --name
	// names the local Pipe resource only: the identity key stays the canonical
	// per-audience path that redemption and the detached runtime expect.
	recipientRef, err := recipientRefFor(invite.AudiencePipeID, in.Name)
	if err != nil {
		return ConnectExactResult{}, fmt.Errorf("invalid_input: %w", err)
	}
	recipientKeyFile := filepath.Join(u.Secrets, "pipes", "connect-"+invite.AudiencePipeID+".key")
	recipient, err := localkey.Provision(recipientKeyFile)
	if err != nil {
		return ConnectExactResult{}, persistenceError("recipient identity key", err)
	}
	recipientPublic := accesslease.PublicString(recipient.Public().(ed25519.PublicKey))
	if u.Reach == nil {
		return ConnectExactResult{}, errors.New("reachability_unavailable: reachability capability is not wired")
	}
	address, err := redemptionAddress(invite)
	if err != nil {
		return ConnectExactResult{}, err
	}
	carrier, err := u.Reach.Acquire(ctx, address)
	if err != nil {
		return ConnectExactResult{}, fmt.Errorf("pipe_unreachable: %w", err)
	}
	defer carrier.Close()
	if u.Redemption == nil {
		return ConnectExactResult{}, errors.New("redemption_unavailable: redemption capability is not wired")
	}
	lease, err := u.Redemption.Redeem(ctx, carrier, invite, recipientPublic)
	if err != nil {
		return ConnectExactResult{}, authorizationDenied(fmt.Errorf("redemption failed: %w", err))
	}
	issuer, err := accesslease.PublicFromString(invite.Issuer)
	if err != nil {
		return ConnectExactResult{}, authorizationDenied(err)
	}
	// The lease must bind this recipient to the exact invite Pipe under the
	// invite's bound issuer; any mismatch fails closed before any local
	// definition is created. Verify against the current wall clock so the
	// just-minted lease's not-before cannot race ahead of a stale capture.
	now = u.now()
	if err := lease.Verify(issuer, recipientPublic, invite.AudiencePipeID, now); err != nil {
		return ConnectExactResult{}, authorizationDenied(err)
	}
	now = u.now()
	spec := domain.PipeSpec{
		Ref:     recipientRef,
		PipeID:  accesslease.PipeIDFromOwner(recipient.Public().(ed25519.PublicKey)),
		Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: in.LocalListen},
		Target: domain.Ref{
			Kind: domain.KindPipe,
			Name: invite.AudiencePipeID,
		},
		Visibility: domain.VisibilityPrivate,
		Access:     domain.AccessInviteOnly,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	// Preserve the durable identity of an existing local definition, but only
	// when it targets the same remote Pipe. Reusing a friendly name across
	// different remote Pipes would silently rebind the identity, which is
	// never what an operator intends.
	if existing, existingKey, existingErr := u.Pipes.Get(ctx, recipientRef.String()); existingErr == nil {
		if existing.Target.Kind == domain.KindPipe && existing.Target.Name != invite.AudiencePipeID {
			return ConnectExactResult{}, fmt.Errorf("conflict: %s already targets a different remote pipe; remove it first or choose another --name", recipientRef)
		}
		spec.PipeID = existing.PipeID
		spec.CreatedAt = existing.CreatedAt
		spec.UpdatedAt = existing.UpdatedAt
		recipientKeyFile = existingKey
	} else if !errors.Is(existingErr, domain.ErrNotFound) {
		return ConnectExactResult{}, persistenceError("pipe", existingErr)
	}
	if _, err := u.Pipes.Upsert(ctx, spec, recipientKeyFile); err != nil {
		return ConnectExactResult{}, persistenceError("local pipe", err)
	}
	// Persist the exact connection state for detached-start consumption.
	// Expired state fails closed on read; this is not a generic credential
	// repository and not a PipeSpec field.
	leaseStr, encodeErr := accesslease.Encode(lease)
	if encodeErr != nil {
		return ConnectExactResult{}, fmt.Errorf("persistence_failed: encode lease: %w", encodeErr)
	}
	exactState := domain.ExactConnectionState{
		RemotePipeID: invite.AudiencePipeID,
		Locator:      address,
		AccessLease:  leaseStr,
		ExpiresAt:    lease.ExpiresAt.UTC(),
		LocalPipeRef: spec.Ref.String(),
	}
	if u.ExactConnections != nil {
		if err := u.ExactConnections.Put(ctx, exactState); err != nil {
			return ConnectExactResult{}, persistenceError("exact connection state", err)
		}
	}
	return ConnectExactResult{PipeRef: spec.Ref, PipeID: spec.PipeID, TargetPipeID: invite.AudiencePipeID}, nil
}
