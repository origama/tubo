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

// AcceptExactInvite is the one-time bootstrap of the durable exact remote-Pipe
// relationship:
//
//	ExactInvite -> accept/redemption -> RemotePipeBinding (durable)
//
// It validates the invite, redeems it exactly once with recipient possession
// proof, verifies the resulting short-lived AccessLease, persists the durable
// binding plus the ephemeral lease cache, and returns the accepted remote Pipe
// identity. It creates no local Pipe, no ingress, and starts no runtime: the
// accepted relationship is ingress-independent and survives invite expiry and
// the initial lease's expiry (access is later reacquired through
// AcquireAcceptedPipeAccess).
//
// Failure semantics:
//   - Invalid/expired/tampered invite -> authorization_denied, no state change.
//   - Recipient identity key failure -> persistence_failed (key may remain on
//     disk deterministically and is reused next attempt).
//   - Reachability not wired -> reachability_unavailable, no state change.
//   - Carrier acquisition failure -> pipe_unreachable, no state change.
//   - Redemption failure or lease mismatch -> authorization_denied, no binding.
//   - Binding or cache persistence failure -> persistence_failed; the accepted
//     relationship may already exist authority-side, so retry stays safe.
func (u UseCases) AcceptExactInvite(ctx context.Context, in AcceptExactInviteInput) (AcceptExactInviteResult, error) {
	if strings.TrimSpace(in.Invite) == "" {
		return AcceptExactInviteResult{}, errors.New("invalid_input: accept requires an invite")
	}
	invite, err := accesslease.DecodeInvite(in.Invite)
	if err != nil {
		return AcceptExactInviteResult{}, authorizationDenied(fmt.Errorf("invalid exact invite: %w", err))
	}
	if err := invite.Verify(u.now(), invite.AudiencePipeID); err != nil {
		return AcceptExactInviteResult{}, authorizationDenied(err)
	}
	accepted, err := u.acceptExactInvite(ctx, invite)
	if err != nil {
		return AcceptExactInviteResult{}, err
	}
	return AcceptExactInviteResult{
		RemotePipeID:    accepted.binding.RemotePipeID,
		RecipientPublic: accepted.recipientPublic,
		LeaseExpiresAt:  accepted.lease.ExpiresAt.UTC(),
	}, nil
}

// acceptedInviteResult is the internal contract shared by the public Accept
// use case and the compatibility ConnectExact path: the durable binding, the
// initial verified lease, and the accepted recipient public key.
type acceptedInviteResult struct {
	binding         domain.RemotePipeBinding
	lease           accesslease.AccessLease
	recipientPublic string
}

// acceptExactInvite redeems an already-verified invite and persists the
// durable binding + ephemeral lease cache. Callers must have verified the
// invite already (AcceptExactInvite and ConnectExact both do).
func (u UseCases) acceptExactInvite(ctx context.Context, invite accesslease.ExactInvite) (acceptedInviteResult, error) {
	var zero acceptedInviteResult
	if strings.TrimSpace(u.Secrets) == "" {
		return zero, errors.New("invalid_input: secrets directory is not configured")
	}
	now := u.now()
	// Recipient identity is durable and deterministic per invite audience: the
	// same accepted recipient key is reused across restarts and retries.
	recipientKeyFile := filepath.Join(u.Secrets, "pipes", "connect-"+invite.AudiencePipeID+".key")
	recipient, err := localkey.Provision(recipientKeyFile)
	if err != nil {
		return zero, persistenceError("recipient identity key", err)
	}
	recipientPublic := accesslease.PublicString(recipient.Public().(ed25519.PublicKey))
	if u.Reach == nil {
		return zero, errors.New("reachability_unavailable: reachability capability is not wired")
	}
	address, err := redemptionAddress(invite)
	if err != nil {
		return zero, err
	}
	carrier, err := u.Reach.Acquire(ctx, address)
	if err != nil {
		return zero, fmt.Errorf("pipe_unreachable: %w", err)
	}
	defer carrier.Close()
	if u.Redemption == nil {
		return zero, errors.New("redemption_unavailable: redemption capability is not wired")
	}
	lease, err := u.Redemption.Redeem(ctx, carrier, invite, recipientPublic)
	if err != nil {
		return zero, authorizationDenied(fmt.Errorf("redemption failed: %w", err))
	}
	issuer, err := accesslease.PublicFromString(invite.Issuer)
	if err != nil {
		return zero, authorizationDenied(err)
	}
	// The lease must bind this recipient to the exact invite Pipe under the
	// invite's bound issuer; any mismatch fails closed before any durable
	// write. Verify against the current wall clock so the just-minted lease's
	// not-before cannot race ahead of a stale capture.
	now = u.now()
	if err := lease.Verify(issuer, recipientPublic, invite.AudiencePipeID, now); err != nil {
		return zero, authorizationDenied(err)
	}
	binding := domain.RemotePipeBinding{
		RemotePipeID:    invite.AudiencePipeID,
		AuthorityPublic: invite.Issuer,
		OwnerBinding:    invite.Binding,
		Locator:         address,
		RecipientPublic: recipientPublic,
		Source:          "invite",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := binding.Validate(); err != nil {
		return zero, authorizationDenied(err)
	}
	if u.Bindings == nil {
		return zero, errors.New("persistence_unavailable: durable binding repository is not wired")
	}
	if err := u.Bindings.Put(ctx, binding); err != nil {
		return zero, persistenceError("accepted remote pipe binding", err)
	}
	if u.LeaseCache != nil {
		leaseStr, encodeErr := accesslease.Encode(lease)
		if encodeErr == nil {
			_ = u.LeaseCache.Put(ctx, domain.AccessLeaseCache{
				RemotePipeID: invite.AudiencePipeID,
				AccessLease:  leaseStr,
				ExpiresAt:    lease.ExpiresAt.UTC(),
			})
		}
	}
	return acceptedInviteResult{binding: binding, lease: lease, recipientPublic: recipientPublic}, nil
}

// AcquireAcceptedPipeAccess loads the durable binding and returns verified
// current/fresh short-lived access material for an already accepted remote
// Pipe:
//
//	RemotePipeBinding -> (cached unexpired lease | renewal) -> fresh short-lived
//	AccessLease
//
// Steps: (1) load the durable binding; (2) use an unexpired, locally verified
// cached AccessLease when safe; (3) otherwise acquire reachability to the bound
// authority endpoint; (4) execute renewal with recipient possession proof;
// (5) verify the returned AccessLease locally; (6) persist/update only the
// ephemeral lease cache; (7) return usable access material. It never creates
// an ingress and never mutates Pipe topology.
//
// The durable binding is not itself permission to open at L2: the actual Pipe
// open continues to require a fresh short-lived AccessLease on the receiving
// boundary.
func (u UseCases) AcquireAcceptedPipeAccess(ctx context.Context, remotePipeID string) (AcceptedPipeAccess, error) {
	var zero AcceptedPipeAccess
	if strings.TrimSpace(remotePipeID) == "" {
		return zero, errors.New("invalid_input: acquire requires a remote pipe id")
	}
	// 1. Load the durable binding (relation/reachability knowledge).
	if u.Bindings == nil {
		return zero, errors.New("persistence_unavailable: durable binding repository is not wired")
	}
	binding, err := u.Bindings.Get(ctx, remotePipeID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return zero, authorizationDenied(fmt.Errorf("no accepted binding for %s", remotePipeID))
		}
		return zero, persistenceError("accepted remote pipe binding", err)
	}
	if err := binding.Validate(); err != nil {
		return zero, authorizationDenied(fmt.Errorf("corrupt accepted binding for %s: %w", remotePipeID, err))
	}
	authorityPub, err := accesslease.PublicFromString(binding.AuthorityPublic)
	if err != nil {
		return zero, authorizationDenied(fmt.Errorf("invalid bound authority identity: %w", err))
	}
	// 2. Reuse an unexpired, locally verified cached lease when safe.
	now := u.now()
	if u.LeaseCache != nil {
		if cached, cacheErr := u.LeaseCache.Get(ctx, remotePipeID); cacheErr == nil {
			if lease, decodeErr := accesslease.Decode(cached.AccessLease); decodeErr == nil {
				if verifyErr := lease.Verify(authorityPub, binding.RecipientPublic, remotePipeID, now); verifyErr == nil {
					return AcceptedPipeAccess{
						RemotePipeID:    remotePipeID,
						Locator:         binding.Locator,
						AuthorityPublic: binding.AuthorityPublic,
						AccessLease:     lease,
					}, nil
				}
			}
		}
	}
	// 3. Reachability to the bound authority endpoint (never authorizes).
	if u.Reach == nil {
		return zero, errors.New("reachability_unavailable: reachability capability is not wired")
	}
	carrier, err := u.Reach.Acquire(ctx, binding.Locator)
	if err != nil {
		return zero, fmt.Errorf("pipe_unreachable: %w", err)
	}
	defer carrier.Close()
	// 4. Renewal with recipient possession proof (invite never needed again).
	if u.Renewal == nil {
		return zero, errors.New("renewal_unavailable: renewal capability is not wired")
	}
	lease, err := u.Renewal.Renew(ctx, carrier, RenewalRequest{
		PipeID:          remotePipeID,
		RecipientPublic: binding.RecipientPublic,
		OwnerBinding:    binding.OwnerBinding,
	})
	if err != nil {
		return zero, authorizationDenied(fmt.Errorf("renewal failed: %w", err))
	}
	// 5. Verify the returned lease locally: subject, exact Pipe audience, and
	// the owner-bound Exact Authority issuer (which derives from the binding).
	now = u.now()
	if err := lease.Verify(authorityPub, binding.RecipientPublic, remotePipeID, now); err != nil {
		return zero, authorizationDenied(fmt.Errorf("renewed lease mismatch: %w", err))
	}
	// 6. Persist/update only the ephemeral lease cache (never the binding).
	if u.LeaseCache != nil {
		leaseStr, encodeErr := accesslease.Encode(lease)
		if encodeErr != nil {
			return zero, persistenceError("encode renewed lease", encodeErr)
		}
		if err := u.LeaseCache.Put(ctx, domain.AccessLeaseCache{
			RemotePipeID: remotePipeID,
			AccessLease:  leaseStr,
			ExpiresAt:    lease.ExpiresAt.UTC(),
		}); err != nil {
			return zero, persistenceError("access lease cache", err)
		}
	}
	return AcceptedPipeAccess{
		RemotePipeID:    remotePipeID,
		Locator:         binding.Locator,
		AuthorityPublic: binding.AuthorityPublic,
		AccessLease:     lease,
	}, nil
}