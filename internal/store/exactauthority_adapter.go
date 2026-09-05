package store

import (
	"context"
	"crypto/ed25519"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

// ExactAuthorityRevocationState adapts a *Store to the exactauthority.RevocationState,
// exactauthority.RedemptionStore, and exactauthority.AcceptedBindingStore
// interfaces. It lives in the store package so internal/exactauthority never
// imports *store.Store.
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

// RedeemInviteAndAccept implements exactauthority.RedemptionStore: consume
// the one-time invite atomically with the authoritative accepted
// relationship.
func (a *ExactAuthorityRevocationState) RedeemInviteAndAccept(ctx context.Context, jti, pipeID, sessionID, clientPublic string, accessEpoch uint64, now time.Time) (bool, error) {
	return a.state.RedeemInviteAndAccept(ctx, jti, pipeID, sessionID, clientPublic, accessEpoch, now)
}

// AcceptedRelationship implements exactauthority.AcceptedBindingStore: it
// returns the single current authoritative relationship (backing invite JTI +
// accepted epoch) for the recipient and pipe, or ok=false when none exists.
func (a *ExactAuthorityRevocationState) AcceptedRelationship(ctx context.Context, pipeID, clientPublic string) (string, uint64, bool, error) {
	return a.state.AcceptedRelationship(ctx, pipeID, clientPublic)
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

// ExactEpochAdapter is the composition-root adapter that reads the current
// pipe-access revocation epoch for an exact Pipe (L4 ShareExact mints invites
// at this epoch). Scope = PipeID, subject JTI = PipeID, matching the
// revocation rows the exact authorizer/renewal read.
type ExactEpochAdapter struct {
	state *Store
}

// NewExactEpochAdapter creates the epoch reader over a store.
func NewExactEpochAdapter(state *Store) *ExactEpochAdapter {
	return &ExactEpochAdapter{state: state}
}

// PipeAccessEpoch returns the current pipe-access epoch for pipeID (0 when
// never revoked). Persistence errors fail closed.
func (a *ExactEpochAdapter) PipeAccessEpoch(ctx context.Context, pipeID string) (uint64, error) {
	return a.state.RevocationEpoch(ctx, pipeID, "pipe-access", pipeID)
}

// Ensure compile-time interface compliance.
var _ RevocationStateInterface = (*ExactAuthorityRevocationState)(nil)
var _ RedemptionStoreInterface = (*ExactAuthorityRevocationState)(nil)
var _ AcceptedBindingStoreInterface = (*ExactAuthorityRevocationState)(nil)
var _ ExactEpochReaderInterface = (*ExactEpochAdapter)(nil)

// RevocationStateInterface duplicates the exactauthority.RevocationState contract
// so internal/store does not import exactauthority. Consumers satisfy both.
type RevocationStateInterface interface {
	IsRevoked(ctx context.Context, jti, subjectType string) (bool, error)
	RevocationEpoch(ctx context.Context, pipeID, subjectType string) (uint64, error)
}

// RedemptionStoreInterface duplicates the exactauthority.RedemptionStore contract
// so internal/store does not import exactauthority.
type RedemptionStoreInterface interface {
	RedeemInviteAndAccept(ctx context.Context, jti, pipeID, sessionID, clientPublic string, accessEpoch uint64, now time.Time) (ok bool, err error)
}

// AcceptedBindingStoreInterface duplicates the exactauthority.AcceptedBindingStore
// contract so internal/store does not import exactauthority.
type AcceptedBindingStoreInterface interface {
	AcceptedRelationship(ctx context.Context, pipeID, clientPublic string) (jti string, accessEpoch uint64, ok bool, err error)
}

// ExactEpochReaderInterface duplicates the usecase.ExactAccessEpochReader
// contract so internal/store does not import usecase.
type ExactEpochReaderInterface interface {
	PipeAccessEpoch(ctx context.Context, pipeID string) (uint64, error)
}
