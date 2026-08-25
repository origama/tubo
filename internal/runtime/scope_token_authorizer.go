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

// scopeTokenOpenAuthorizer is a TRANSITIONAL concrete L3 adapter that moves the
// prototype Scope-token remote-open validation out of the receiving-boundary
// files (issue #48) so the receiving L2 path consumes only the neutral
// openauth.OpenAuthorizer contract.
//
// It re-uses the existing prototype authority.Token decode/verify, Scope
// binding, revocation/epoch, and challenge-proof checks to preserve the
// current scoped production behavior unchanged and fail-closed. This adapter
// is NOT a target Access Lease contract and MUST NOT be renamed, wrapped, or
// promoted as one: issue #49 replaces this transitional remote-open path with
// the canonical Scope-independent exact-Pipe Access Lease + Exact Pipe
// Authority. No compatibility promise is made for the prototype
// authority.Token.
type scopeTokenOpenAuthorizer struct {
	state           *store.Store
	scope           string
	authorityPublic ed25519.PublicKey
}

func newScopeTokenOpenAuthorizer(state *store.Store, scope string, public ed25519.PublicKey) *scopeTokenOpenAuthorizer {
	return &scopeTokenOpenAuthorizer{state: state, scope: scope, authorityPublic: public}
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
