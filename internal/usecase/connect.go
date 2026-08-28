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

// exactConnectConflict decides whether an existing local Pipe definition can
// be reused as the exact-connect Pipe for an invite. Reuse requires every
// relevant durable semantic to match: the target must be the invite audience,
// the local identity must be the canonical identity derived from the
// canonical recipient key, the ingress must be local, and the local listen
// must equal the requested one. Any mismatch is a stable conflict; the
// existing Pipe is never mutated or rebound.
func exactConnectConflict(existing domain.PipeSpec, canonicalPipeID, audiencePipeID, requestedListen string) error {
	refName := existing.Ref.String()
	if existing.Target.Kind != domain.KindPipe || existing.Target.Name != audiencePipeID {
		return fmt.Errorf("conflict: %s already exists with a different target; remove it first or choose another --name", refName)
	}
	if existing.PipeID != canonicalPipeID {
		return fmt.Errorf("conflict: %s already exists with a different identity; remove it first", refName)
	}
	if existing.Ingress.Kind != domain.IngressLocal {
		return fmt.Errorf("conflict: %s is not a local-ingress connection; remove it first", refName)
	}
	if existing.Ingress.Listen != requestedListen {
		return fmt.Errorf("conflict: %s already connects this remote pipe on %s; reuse that listener or remove the pipe before reconnecting on %s", refName, existing.Ingress.Listen, requestedListen)
	}
	return nil
}

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
	if strings.TrimSpace(u.Secrets) == "" {
		return ConnectExactResult{}, errors.New("invalid_input: secrets directory is not configured")
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
	// The recipient identity key is canonical per remote Pipe ID, so the
	// derived local Pipe ID is canonical too, and the Store enforces Pipe ID
	// uniqueness. Resolve both the requested ref and the canonical Pipe ID
	// BEFORE any reachability or redemption work: a connection may already
	// exist under a different ref (a previous friendly --name), and an
	// existing exact-connect Pipe is reusable only when every relevant durable
	// semantic matches the request. Compatibility conflicts fail closed here,
	// without a durable write, without a network dial, and without consuming
	// the one-time invite. noUpsert means the durable local Pipe already
	// exists byte-compatibly and must not be rewritten; only the exact
	// connection state (fresh lease) is persisted below.
	noUpsert := false
	canonicalPipeID := spec.PipeID
	existingByRef, existingKeyByRef, errRef := u.Pipes.Get(ctx, recipientRef.String())
	switch {
	case errRef == nil:
		// The requested ref already owns a definition. Reuse it only when it
		// is a fully compatible local connection to this exact remote Pipe;
		// anything else must fail rather than silently rebind.
		if err := exactConnectConflict(existingByRef, canonicalPipeID, invite.AudiencePipeID, in.LocalListen); err != nil {
			return ConnectExactResult{}, err
		}
		recipientRef = existingByRef.Ref
		spec = existingByRef
		recipientKeyFile = existingKeyByRef
		noUpsert = true
	case errors.Is(errRef, domain.ErrNotFound):
		// The requested ref is free; check whether this canonical identity is
		// already represented by another local Pipe.
		existingByID, existingKeyByID, errID := u.Pipes.Get(ctx, canonicalPipeID)
		switch {
		case errID == nil:
			// This canonical identity is already a durable local Pipe under
			// another ref. Fail closed on any shape/target/identity mismatch;
			// then an explicit different name is a conflict, and a nameless
			// reconnect with a different listener is a conflict. No rename and
			// no durable mutation.
			if err := exactConnectConflict(existingByID, canonicalPipeID, invite.AudiencePipeID, in.LocalListen); err != nil {
				return ConnectExactResult{}, err
			}
			if in.Name != "" {
				return ConnectExactResult{}, fmt.Errorf("conflict: %s already connects this remote pipe; reuse it ('tubo start %s') or remove it before choosing a different name", existingByID.Ref, existingByID.Ref)
			}
			recipientRef = existingByID.Ref
			spec = existingByID
			recipientKeyFile = existingKeyByID
			noUpsert = true
		case errors.Is(errID, domain.ErrNotFound):
			// Fresh connection; no local Pipe owns this identity yet.
		default:
			return ConnectExactResult{}, persistenceError("pipe", errID)
		}
	default:
		return ConnectExactResult{}, persistenceError("pipe", errRef)
	}
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
	if !noUpsert {
		if _, err := u.Pipes.Upsert(ctx, spec, recipientKeyFile); err != nil {
			return ConnectExactResult{}, persistenceError("local pipe", err)
		}
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
	return ConnectExactResult{PipeRef: spec.Ref, PipeID: spec.PipeID, TargetPipeID: invite.AudiencePipeID, LocalListen: spec.Ingress.Listen}, nil
}
