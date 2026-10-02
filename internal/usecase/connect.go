package usecase

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/origama/tubo/internal/accesslease"
	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/localkey"
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

// ConnectExact is the Journey A compatibility connect path. It composes the new
// accepted-binding lifecycle with the legacy local-ingress Pipe creation:
//
//	connect --token <invite>   -> AcceptExactInvite -> durable binding -> legacy pipe
//	connect --remote <pipe-id> -> durable binding -> AcquireAcceptedPipeAccess -> legacy pipe
//
// There is no second exact authorization implementation: the token path
// redeems exactly once through acceptExactInvite (persisting the durable
// binding and caching the initial lease) and the remote path reacquires access
// through AcquireAcceptedPipeAccess (fresh short-lived AccessLease from the
// binding). The old local-ingress Pipe behavior remains for CLI compatibility;
// once created, its runtime reacquires access through the durable binding after
// lease expiry or restart (runLocalPipeExact), never by resending the invite.
//
// Failure semantics:
//   - Invalid/expired/tampered invite -> authorization_denied, no state change.
//   - Missing local listen -> invalid_input, no state change.
//   - No token and no accepted binding for --remote -> authorization_denied.
//   - Reachability not wired -> reachability_unavailable, no state change.
//   - Carrier acquisition failure -> pipe_unreachable, no durable definition.
//   - Redemption or renewal failure -> authorization_denied, no durable
//     definition and no misleading ready state.
//   - Local Pipe upsert failure -> persistence_failed; the accepted binding may
//     remain (harmless: the relationship is durable and reconnect reuses it).
func (u UseCases) ConnectExact(ctx context.Context, in ConnectExactInput) (ConnectExactResult, error) {
	if strings.TrimSpace(in.LocalListen) == "" {
		return ConnectExactResult{}, errors.New("invalid_input: connect requires a local listen address")
	}
	if strings.TrimSpace(u.Secrets) == "" {
		return ConnectExactResult{}, errors.New("invalid_input: secrets directory is not configured")
	}
	if in.Invite != "" && in.RemotePipeID != "" {
		return ConnectExactResult{}, errors.New("invalid_input: connect accepts exactly one of --token or --remote")
	}
	if in.Invite == "" && in.RemotePipeID == "" {
		return ConnectExactResult{}, errors.New("invalid_input: connect requires --token <invite> or --remote <pipe-id>; see 'tubo connect --help'")
	}
	if u.Bindings == nil {
		return ConnectExactResult{}, errors.New("persistence_unavailable: durable binding repository is not wired")
	}

	// Resolve the target Pipe and the recipient identity. The recipient
	// identity is deterministic per remote Pipe (connect-<pipe>.key), so both
	// branches resolve it LOCALLY: no network dial, no lease acquisition, and
	// — critically — no consumption of the one-time invite. Verified access
	// material is established only after every local conflict check has
	// passed, so a failing connect never burns the bootstrap credential.
	var (
		targetPipeID     string
		recipientPublic  string
		recipientRef     domain.Ref
		recipientKeyFile string
		invite           accesslease.ExactInvite
	)
	if in.Invite != "" {
		decoded, err := accesslease.DecodeInvite(in.Invite)
		if err != nil {
			return ConnectExactResult{}, authorizationDenied(fmt.Errorf("invalid exact invite: %w", err))
		}
		if err := decoded.Verify(u.now(), decoded.AudiencePipeID); err != nil {
			return ConnectExactResult{}, authorizationDenied(err)
		}
		invite = decoded
		targetPipeID = decoded.AudiencePipeID
		recipientKeyFile = filepath.Join(u.Secrets, "pipes", "connect-"+targetPipeID+".key")
		recipient, keyErr := localkey.Provision(recipientKeyFile)
		if keyErr != nil {
			return ConnectExactResult{}, persistenceError("recipient identity key", keyErr)
		}
		recipientPublic = accesslease.PublicString(recipient.Public().(ed25519.PublicKey))
	} else {
		// Remote path: the accepted relationship already exists on this
		// consumer. The recipient key must already be the accepted identity;
		// provisioning a new key here would silently change identity and is
		// never done.
		targetPipeID = in.RemotePipeID
		recipientKeyFile = filepath.Join(u.Secrets, "pipes", "connect-"+targetPipeID+".key")
		recipient, err := localkey.Load(recipientKeyFile)
		if err != nil {
			return ConnectExactResult{}, authorizationDenied(fmt.Errorf("no accepted recipient identity for %s", targetPipeID))
		}
		recipientPublic = accesslease.PublicString(recipient.Public().(ed25519.PublicKey))
	}
	ref, refErr := recipientRefFor(targetPipeID, in.Name)
	if refErr != nil {
		return ConnectExactResult{}, fmt.Errorf("invalid_input: %w", refErr)
	}
	recipientRef = ref

	now := u.now()
	spec := domain.PipeSpec{
		Ref:     recipientRef,
		PipeID:  accesslease.PipeIDFromOwner(recipientPublicBytes(recipientPublic)),
		Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: in.LocalListen},
		Target: domain.Ref{
			Kind: domain.KindPipe,
			Name: targetPipeID,
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
	// exists byte-compatibly and must not be rewritten.
	noUpsert := false
	canonicalPipeID := spec.PipeID
	existingByRef, existingKeyByRef, errRef := u.Pipes.Get(ctx, recipientRef.String())
	switch {
	case errRef == nil:
		// The requested ref already owns a definition. Reuse it only when it
		// is a fully compatible local connection to this exact remote Pipe;
		// anything else must fail rather than silently rebind.
		if err := exactConnectConflict(existingByRef, canonicalPipeID, targetPipeID, in.LocalListen); err != nil {
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
			if err := exactConnectConflict(existingByID, canonicalPipeID, targetPipeID, in.LocalListen); err != nil {
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
	// Verified access material comes AFTER all local validation. For the
	// invite path this is where the one-time redemption finally happens; the
	// deterministic recipient key guarantees the identity used above is the
	// one redeemed here. For the remote path the durable binding and its
	// accepted relationship are verified and a fresh lease acquired.
	if in.Invite != "" {
		if _, err := u.acceptExactInvite(ctx, invite); err != nil {
			return ConnectExactResult{}, err
		}
	} else {
		binding, bindingErr := u.Bindings.Get(ctx, targetPipeID)
		if bindingErr != nil {
			if errors.Is(bindingErr, domain.ErrNotFound) {
				return ConnectExactResult{}, authorizationDenied(fmt.Errorf("no accepted binding for %s; redeem the invite first with 'tubo accept --token <invite>'", targetPipeID))
			}
			return ConnectExactResult{}, persistenceError("accepted remote pipe binding", bindingErr)
		}
		if binding.RecipientPublic != recipientPublic {
			return ConnectExactResult{}, authorizationDenied(fmt.Errorf("recipient identity does not match accepted binding for %s", targetPipeID))
		}
		access, accessErr := u.AcquireAcceptedPipeAccess(ctx, targetPipeID)
		if accessErr != nil {
			return ConnectExactResult{}, accessErr
		}
		_ = access // verified and cached; the runtime opener reacquires per open
	}
	if !noUpsert {
		if _, err := u.Pipes.Upsert(ctx, spec, recipientKeyFile); err != nil {
			return ConnectExactResult{}, persistenceError("local pipe", err)
		}
	}
	return ConnectExactResult{PipeRef: spec.Ref, PipeID: spec.PipeID, TargetPipeID: targetPipeID, LocalListen: spec.Ingress.Listen}, nil
}

// recipientPublicBytes decodes a canonical recipient public key string into
// the bytes needed for self-deriving PipeID. Callers pass values already
// validated at acceptance/acquisition.
func recipientPublicBytes(public string) ed25519.PublicKey {
	key, _ := accesslease.PublicFromString(public)
	return key
}
