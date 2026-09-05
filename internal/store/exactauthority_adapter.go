package store

import (
	"context"
	"crypto/ed25519"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

// ExactAuthorityRevocationState adapts a *Store to the exactauthority.RevocationState
// and exactauthority.RedemptionStore interfaces. It lives in the store package
// so internal/exactauthority never imports *store.Store.
type ExactAuthorityRevocationState struct {
	state *Store
	scope string
}

// NewExactAuthorityRevocationState creates an adapter for the given store and
// scope (typically the PipeID). The scope is used for revocation lookups.
func NewExactAuthorityRevocationState(state *Store, scope string) *ExactAuthorityRevocationState {
	return &ExactAuthorityRevocationState{state: state, scope: scope}
}

// IsRevoked implements exactauthority.RevocationState.
func (a *ExactAuthorityRevocationState) IsRevoked(ctx context.Context, jti, subjectType string) (bool, error) {
	return a.state.IsRevoked(ctx, a.scope, subjectType, jti)
}

// RevocationEpoch implements exactauthority.RevocationState.
func (a *ExactAuthorityRevocationState) RevocationEpoch(ctx context.Context, pipeID, subjectType string) (uint64, error) {
	return a.state.RevocationEpoch(ctx, a.scope, subjectType, pipeID)
}

// RedeemInvite implements exactauthority.RedemptionStore.
func (a *ExactAuthorityRevocationState) RedeemInvite(ctx context.Context, jti, pipeID, sessionID, clientPublic string) (bool, error) {
	return a.state.RedeemInvite(ctx, jti, pipeID, sessionID, clientPublic)
}

// AcceptedRedemption implements exactauthority.AcceptedBindingStore: it
// returns the originating invite JTI when recipient was validly accepted for
// the pipe, or ok=false when the relationship never existed.
func (a *ExactAuthorityRevocationState) AcceptedRedemption(ctx context.Context, pipeID, clientPublic string) (string, bool, error) {
	return a.state.AcceptedRedemption(ctx, pipeID, clientPublic)
}

// NewExactAuthorityRevocationStateFromOwner creates an adapter from a store
// and the owner private key (to derive the PipeID scope).
func NewExactAuthorityRevocationStateFromOwner(
	state *Store,
	ownerPrivate ed25519.PrivateKey,
) *ExactAuthorityRevocationState {
	pipeID := accesslease.PipeIDFromOwner(ownerPrivate.Public().(ed25519.PublicKey))
	return NewExactAuthorityRevocationState(state, pipeID)
}

// Ensure compile-time interface compliance.
var _ RevocationStateInterface = (*ExactAuthorityRevocationState)(nil)
var _ RedemptionStoreInterface = (*ExactAuthorityRevocationState)(nil)
var _ AcceptedBindingStoreInterface = (*ExactAuthorityRevocationState)(nil)

// RevocationStateInterface duplicates the exactauthority.RevocationState contract
// so internal/store does not import exactauthority. Consumers satisfy both.
type RevocationStateInterface interface {
	IsRevoked(ctx context.Context, jti, subjectType string) (bool, error)
	RevocationEpoch(ctx context.Context, pipeID, subjectType string) (uint64, error)
}

// RedemptionStoreInterface duplicates the exactauthority.RedemptionStore contract
// so internal/store does not import exactauthority.
type RedemptionStoreInterface interface {
	RedeemInvite(ctx context.Context, jti, pipeID, sessionID, clientPublic string) (bool, error)
}

// AcceptedBindingStoreInterface duplicates the exactauthority.AcceptedBindingStore
// contract so internal/store does not import exactauthority.
type AcceptedBindingStoreInterface interface {
	AcceptedRedemption(ctx context.Context, pipeID, clientPublic string) (jti string, ok bool, err error)
}
