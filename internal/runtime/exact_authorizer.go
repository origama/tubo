// Package runtime is the composition root for the Tubo runtime. It
// concretely wires the receiving-boundary open-authorizer behind the neutral
// openauth seam (step 6B2). The ExactOpenAuthorizer lives here as a
// composition-root adapter, not in internal/exactauthority, so that the pure
// L3 exactauthority package imports only accesslease + localkey + stdlib.
package runtime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"sync"

	"github.com/origama/tubo/internal/accesslease"
	"github.com/origama/tubo/internal/exactauthority"
	"github.com/origama/tubo/internal/localkey"
	"github.com/origama/tubo/internal/openauth"
	"github.com/origama/tubo/internal/store"
)

// ExactOpenAuthorizer is the concrete L3 open-authorizer behind the neutral
// openauth.OpenAuthorizer contract (step 6B2). It validates a canonical
// AccessLease + receiver-challenge possession proof. Failures produce only
// generic "authorization denied" errors; internal revocation/trust details
// never leak to the caller.
//
// Issuer trust is derived from the local owner-binding -> exact authority
// public key -> lease issuer match. There is no generic issuer registry: the
// authorizer trusts exactly the local owner-bound Exact Authority key.
type ExactOpenAuthorizer struct {
	leasePublic ed25519.PublicKey
	pipeID      string
	state       exactauthority.RevocationState
}

// NewExactOpenAuthorizer creates an ExactOpenAuthorizer that trusts the
// given exact authority public key for the given pipe audience. Revocation
// state is a MANDATORY security dependency: without it a minted lease could
// never be revoked or fenced by the authorization epoch. Missing dependency
// fails closed at construction; there is no revocation-free configuration.
func NewExactOpenAuthorizer(leasePublic ed25519.PublicKey, pipeID string, state exactauthority.RevocationState) (*ExactOpenAuthorizer, error) {
	if state == nil {
		return nil, errors.New("exact open authorizer requires revocation state")
	}
	return &ExactOpenAuthorizer{leasePublic: leasePublic, pipeID: pipeID, state: state}, nil
}

// LazyExactAuthorizer is a lazy wrapper that loads the exact authority key
// on the first AuthorizeOpen call. This allows the authorizer to be created
// before the key is provisioned (e.g., pipe daemon starts before ShareExact
// provisions the key).
type LazyExactAuthorizer struct {
	state  *store.Store
	pipeID string
	mu     sync.Mutex
	loaded bool
	auth   *ExactOpenAuthorizer
}

// NewLazyExactAuthorizer creates a lazy authorizer that loads the key from
// the store's secrets directory on first use.
func NewLazyExactAuthorizer(state *store.Store, pipeID string) *LazyExactAuthorizer {
	return &LazyExactAuthorizer{state: state, pipeID: pipeID}
}

func (a *LazyExactAuthorizer) AuthorizeOpen(ctx context.Context, req openauth.OpenRequest) (openauth.Authorization, error) {
	a.mu.Lock()
	if !a.loaded {
		// Try to load the exact authority key.
		secretDir := a.state.SecretPath()
		keyPath, keyErr := exactauthority.KeyPath(secretDir, a.pipeID)
		if keyErr == nil {
			private, loadErr := localkey.Load(keyPath)
			if loadErr == nil {
				adapter := store.NewExactAuthorityRevocationState(a.state, a.pipeID)
				pub := private.Public().(ed25519.PublicKey)
				if auth, authErr := NewExactOpenAuthorizer(pub, a.pipeID, adapter); authErr == nil {
					a.auth = auth
					a.loaded = true
				}
			}
		}
	}
	a.mu.Unlock()

	if a.auth == nil {
		return openauth.Authorization{}, errors.New("authorization denied: no open authorizer")
	}
	return a.auth.AuthorizeOpen(ctx, req)
}

func (a *ExactOpenAuthorizer) AuthorizeOpen(ctx context.Context, req openauth.OpenRequest) (openauth.Authorization, error) {
	var zero openauth.Authorization

	// Decode and verify the AccessLease.
	lease, err := accesslease.Decode(req.AccessLease)
	if err != nil {
		return zero, errors.New("authorization denied")
	}

	// Verify lease against the exact authority key and pipe audience.
	if err := lease.Verify(a.leasePublic, req.ClientPublic, req.PipeID, req.Now); err != nil {
		return zero, errors.New("authorization denied")
	}

	// Verify the issuer is exactly the expected exact authority.
	wantIssuer := accesslease.PublicString(a.leasePublic)
	if lease.Issuer != wantIssuer {
		return zero, errors.New("authorization denied")
	}

	// Verify challenge proof.
	clientPub, err := accesslease.PublicFromString(req.ClientPublic)
	if err != nil {
		return zero, errors.New("authorization denied")
	}
	if !accesslease.VerifyProof(clientPub, req.ChallengeProof, accesslease.OpenProofPurpose, req.Challenge, req.PipeID) {
		return zero, errors.New("authorization denied")
	}

	// Revocation checks: the revocation state is mandatory (the constructor
	// fails closed without it); an unavailable state denies.
	{
		revoked, revErr := a.state.IsRevoked(ctx, lease.JTI, "access-lease")
		if revErr != nil {
			return zero, errors.New("authorization denied")
		}
		if revoked {
			return zero, errors.New("authorization denied")
		}
		currentEpoch, epochErr := a.state.RevocationEpoch(ctx, req.PipeID, "pipe-access")
		if epochErr != nil {
			return zero, errors.New("authorization denied")
		}
		if currentEpoch > lease.AccessEpoch {
			return zero, errors.New("authorization denied")
		}
	}
	return openauth.Authorization{
		PipeID:      req.PipeID,
		Subject:     req.ClientPublic,
		Issuer:      lease.Issuer,
		LeaseID:     lease.JTI,
		ExpiresAt:   lease.ExpiresAt,
		AccessEpoch: lease.AccessEpoch,
	}, nil
}
