package runtime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/origama/tubo-rebuilt/internal/authority"
	"github.com/origama/tubo-rebuilt/internal/openauth"
	"github.com/origama/tubo-rebuilt/internal/store"
)

// scopeTokenOpenAuthorizer is QUARANTINED as of step 6B2 (issue #49/#49).
// The prototype Scope-token is UNREACHABLE as remote-open authorization.
// This file remains only for test-reference. Any production call panics.
//
// It was a TRANSITIONAL concrete L3 adapter that moved the prototype
// Scope-token remote-open validation out of the receiving-boundary files
// (issue #48) so the receiving L2 path consumed only the neutral
// openauth.OpenAuthorizer contract.
//
// Step 6B2 replaces this transitional remote-open path with the canonical
// Scope-independent exact-Pipe Access Lease + Exact Pipe Authority.
type scopeTokenOpenAuthorizer struct {
	state           *store.Store
	scope           string
	authorityPublic ed25519.PublicKey
}

// newScopeTokenOpenAuthorizer is QUARANTINED. Production code must use
// ExactOpenAuthorizer. Calling this function panics.
func newScopeTokenOpenAuthorizer(state *store.Store, scope string, public ed25519.PublicKey) *scopeTokenOpenAuthorizer {
	panic("scopeTokenOpenAuthorizer is quarantined as of A0 step 6B2")
}

func (a *scopeTokenOpenAuthorizer) AuthorizeOpen(ctx context.Context, req openauth.OpenRequest) (openauth.Authorization, error) {
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	lease, err := authority.Decode(req.AccessLease)
	if err != nil {
		return openauth.Authorization{}, errors.New("invalid access lease")
	}
	if err := lease.Verify(a.authorityPublic, now.UTC()); err != nil {
		return openauth.Authorization{}, errors.New("invalid access lease")
	}
	if lease.Type != "connect.access" || lease.Audience != req.PipeID || lease.Subject != req.ClientPublic || lease.Scope.String() != a.scope {
		return openauth.Authorization{}, errors.New("access lease binding mismatch")
	}
	revoked, revokeErr := a.state.IsRevoked(ctx, a.scope, "connect.access", lease.JTI)
	if revokeErr != nil {
		return openauth.Authorization{}, errors.New("revocation state unavailable")
	}
	if revoked {
		return openauth.Authorization{}, errors.New("access revoked")
	}
	accessEpoch, revokeErr := a.state.RevocationEpoch(ctx, a.scope, "pipe-access", req.PipeID)
	if revokeErr != nil {
		return openauth.Authorization{}, errors.New("revocation state unavailable")
	}
	if lease.AccessEpoch < accessEpoch {
		return openauth.Authorization{}, errors.New("access epoch revoked")
	}
	clientPublic, err := authority.PublicFromString(req.ClientPublic)
	if err != nil {
		return openauth.Authorization{}, errors.New("invalid access lease")
	}
	if !authority.VerifyProof(clientPublic, req.ChallengeProof, "tubo-open-v2", req.Challenge, req.PipeID) {
		return openauth.Authorization{}, errors.New("invalid challenge proof")
	}
	_ = ctx
	return openauth.Authorization{
		PipeID:      req.PipeID,
		Subject:     req.ClientPublic,
		Issuer:      authority.PublicString(a.authorityPublic),
		LeaseID:     lease.JTI,
		ExpiresAt:   lease.ExpiresAt,
		AccessEpoch: lease.AccessEpoch,
	}, nil
}
