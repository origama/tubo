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
