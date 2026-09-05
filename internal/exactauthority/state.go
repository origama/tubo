package exactauthority

import (
	"context"
	"time"
)

// RevocationState is the minimal revocation/epoch query surface the
// ExactOpenAuthorizer and RedeemExactInvite need. The scope prefix is
// embedded by the caller (e.g. via a store adapter) so the interface
// stays free of runtime/scope coupling.
type RevocationState interface {
	IsRevoked(ctx context.Context, jti, subjectType string) (bool, error)
	RevocationEpoch(ctx context.Context, pipeID, subjectType string) (uint64, error)
}

// RedemptionStore is the narrow persistence port for atomic invite
// redemption. Every successful redemption is simultaneously the (re-)
// acceptance of the authoritative relationship for (pipe_id,
// recipient_public): the one-time redemption insert (anti-replay) and the
// deterministic current-relationship upsert commit in one transaction.
type RedemptionStore interface {
	// RedeemInviteAndAccept consumes the one-time invite atomically with the
	// authoritative accepted-relationship write. Returns ok=true when this
	// call established or safely re-confirmed the acceptance (first
	// redemption, or an idempotent same-recipient retry); a different
	// recipient for an already-redeemed invite fails closed.
	RedeemInviteAndAccept(ctx context.Context, jti, pipeID, sessionID, clientPublic string, accessEpoch uint64, now time.Time) (ok bool, err error)
}

// AcceptedBindingStore is the narrow persistence port for the authoritative
// accepted exact relationship: exactly one current row per (pipe_id,
// recipient_public), carrying the originating/current invite JTI and the
// accepted pipe-access epoch. Renewal resolves it deterministically; a
// missing authoritative row never mints a lease.
type AcceptedBindingStore interface {
	// AcceptedRelationship returns the current authoritative relationship for
	// this recipient and Pipe: the backing invite JTI and the accepted epoch.
	// ok=false when the relationship never existed. Persistence errors fail
	// closed (err != nil).
	AcceptedRelationship(ctx context.Context, pipeID, clientPublic string) (jti string, accessEpoch uint64, ok bool, err error)
}