package exactauthority

import "context"

// RevocationState is the minimal revocation/epoch query surface the
// ExactOpenAuthorizer and RedeemExactInvite need. The scope prefix is
// embedded by the caller (e.g. via a store adapter) so the interface
// stays free of runtime/scope coupling.
type RevocationState interface {
	IsRevoked(ctx context.Context, jti, subjectType string) (bool, error)
	RevocationEpoch(ctx context.Context, pipeID, subjectType string) (uint64, error)
}

// RedemptionStore is the narrow persistence port for atomic invite redemption.
type RedemptionStore interface {
	RedeemInvite(ctx context.Context, jti, pipeID, sessionID, clientPublic string) (bool, error)
}

// AcceptedBindingStore is the narrow persistence port for the authority-side
// accepted exact relationship: authoritative knowledge that recipient X was
// validly accepted for Pipe P under the current revocation state. Renewal
// requires an existing accepted relationship; a missing authoritative row must
// never mint a lease.
type AcceptedBindingStore interface {
	// AcceptedRedemption returns the originating invite JTI when an accepted
	// redemption exists for this recipient and Pipe; ok=false when the
	// relationship never existed. Persistence errors fail closed (err != nil).
	AcceptedRedemption(ctx context.Context, pipeID, clientPublic string) (jti string, ok bool, err error)
}
