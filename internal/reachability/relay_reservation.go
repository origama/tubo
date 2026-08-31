package reachability

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo-rebuilt/internal/node"
)

// ErrReservationTooShort is returned when a relay-provided reservation
// expires too soon for a renewal to be scheduled strictly before expiration.
// The caller must fail closed: scheduling renewal after the actual expiration
// would reintroduce NO_RESERVATION, so "renew later" is never substituted for
// "renew before expiry" when the relay lifetime is too short.
var ErrReservationTooShort = errors.New("reachability: reservation expires before a renewal can be scheduled")

const (
	// DefaultReservationMargin is how long before the relay-provided reservation
	// expiration a renewal is scheduled. It is a conservative safety buffer: Tubo
	// renews before the actual reservation expires, never merely before an assumed
	// relay TTL. A short relay TTL simply makes the next renewal earlier (bounded
	// below by minimumRenewDelay), so the margin never trades safety for timing.
	DefaultReservationMargin = 1 * time.Minute

	// minimumRenewDelay is the earliest a renewal may follow the previous
	// successful maintenance. It is a guarded floor that bounds a pathological
	// (very short or near-past) relay-provided expiration from driving a tight
	// maintenance loop. A reservation whose remaining lifetime is not strictly
	// longer than this floor cannot be renewed before it expires and is refused
	// (ErrReservationTooShort) rather than scheduled after expiration.
	minimumRenewDelay = 30 * time.Second
)

// DefaultMaintenanceTick is how often the maintainer evaluates whether a
// renewal or reconnect is due.
const DefaultMaintenanceTick = 5 * time.Second

// RelayReservation acquires and maintains a usable Circuit Relay v2
// reservation for the lifetime of a host. It is the L1 Reachability primitive
// that owns all relay-reservation policy:
//
//  1. initial relay connection;
//  2. initial reservation acquisition;
//  3. periodic reservation renewal/reacquisition before expiration;
//  4. reconnect when relay connectivity is lost;
//  5. reservation reacquisition after reconnect;
//  6. surfacing maintenance failure to its owner;
//  7. successful recovery after a later maintenance attempt.
//
// The invariant it encodes: relay connection alive != relay reservation valid.
// A connected relay whose reservation has passed its pre-expiration renewal
// point is renewed; a disconnected relay is reconnected and re-acquired. The
// scheduling fact is the relay-provided reservation expiration returned by
// ReserveRelay, not a fixed assumed TTL: each successful maintenance records
// the new expiration and drives the next refresh deadline from it.
//
// An owner provides only lifecycle/cancellation context and reacts to
// success/failure; it never reimplements reservation timing or validity
// assumptions. Reachability is the only place with reservation TTL, expiry,
// refresh-margin, and renewal-interval knowledge.
type RelayReservation struct {
	h        host.Host
	info     peer.AddrInfo
	margin   time.Duration
	minDelay time.Duration
	tick     time.Duration

	// Injectable seams for deterministic tests. Production defaults bind to the
	// node host adapter; tests replace them with fakes and a controlled clock
	// so correctness never depends on multi-minute sleeps. maintain returns the
	// relay-provided expiration of the acquired reservation, or an error.
	connected func() bool
	maintain  func(ctx context.Context) (time.Time, error)
	now       func() time.Time
	newTicks  func() (<-chan time.Time, func())

	mu             sync.Mutex
	expires        time.Time
	lastMaintained time.Time
	ready          bool
}

// NewRelayReservation constructs a maintainer over a host and relay contact.
// A zero or negative margin selects DefaultReservationMargin.
func NewRelayReservation(h host.Host, info peer.AddrInfo, margin time.Duration) *RelayReservation {
	if margin <= 0 {
		margin = DefaultReservationMargin
	}
	return &RelayReservation{
		h:        h,
		info:     info,
		margin:   margin,
		minDelay: minimumRenewDelay,
		tick:     DefaultMaintenanceTick,
		connected: func() bool {
			return node.PeerConnected(h, info.ID)
		},
		maintain: func(ctx context.Context) (time.Time, error) {
			// Connect if needed, then acquire/reserve the circuit. The returned
			// reservation carries the relay-provided expiration.
			reservation, err := node.ReserveRelay(ctx, h, info)
			if err != nil {
				return time.Time{}, err
			}
			return reservation.Expiration, nil
		},
		now: time.Now,
		newTicks: func() (<-chan time.Time, func()) {
			t := time.NewTicker(DefaultMaintenanceTick)
			return t.C, t.Stop
		},
	}
}

// Acquire connects to the relay and acquires the initial reservation. It
// returns an error only when no usable reservation exists; the caller must
// fail closed — a circuit address without an active reservation is not
// reachable and must not be exposed or marked ready. On success the relay-
// provided expiration becomes the scheduling fact for the first renewal.
func (r *RelayReservation) Acquire(ctx context.Context) error {
	expiresAt, err := r.maintain(ctx)
	if err != nil {
		r.setReady(false)
		return err
	}
	if err := r.record(expiresAt); err != nil {
		r.setReady(false)
		return err
	}
	r.setReady(true)
	return nil
}

// Ready reports whether a usable reservation is currently held. It is the L1
// statement an owner may use to avoid advertising reachability otherwise.
func (r *RelayReservation) Ready() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ready
}

// Serve maintains the reservation until ctx is cancelled. onReady is called
// only after a successful maintenance operation — a reserve, renewal, or
// reconnect+reacquire — that produced a usable reservation whose next renewal
// is scheduled strictly before its expiration. onFailure is called after each
// failed maintenance attempt, including when the relay returns a reservation
// too short to renew before expiry (ErrReservationTooShort, fail closed; a
// renewal is never scheduled at or after the actual expiration). A no-op
// maintenance tick (reservation healthy and not due for renewal) calls
// neither. Serve returns ctx.Err() on cancellation.
//
// Degraded/recovery semantics:
//
//	maintenance succeeds -> onReady (owner marks Ready)
//	renewal/reacquisition fails      -> onFailure (owner degrades)
//	later maintenance succeeds       -> onReady (owner restores Ready)
func (r *RelayReservation) Serve(ctx context.Context, onReady func(), onFailure func(error)) error {
	ticks, stop := r.newTicks()
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticks:
			if !r.due() {
				continue
			}
			expiresAt, err := r.maintain(ctx)
			if err != nil {
				r.setReady(false)
				if onFailure != nil {
					onFailure(err)
				}
				continue
			}
			if err := r.record(expiresAt); err != nil {
				r.setReady(false)
				if onFailure != nil {
					onFailure(err)
				}
				continue
			}
			r.setReady(true)
			if onReady != nil {
				onReady()
			}
		}
	}
}

// record stores the relay-provided reservation expiration and the time it was
// acquired, replacing any previous scheduling fact (renewal and reacquisition
// both update the next refresh deadline). It returns ErrReservationTooShort
// when the next renewal deadline computed from this reservation is not strictly
// before the reservation's expiration — i.e. the reservation lifetime is too
// short to renew safely without a tight loop. The schedule is never allowed to
// place a renewal at or after Reservation.Expiration (that would reintroduce
// NO_RESERVATION); the owner must treat this as a maintenance failure.
func (r *RelayReservation) record(expiresAt time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expires = expiresAt
	r.lastMaintained = r.now()
	next := r.nextRenewLocked()
	if !next.Before(expiresAt) {
		return fmt.Errorf("%w: expiration=%s next renewal=%s", ErrReservationTooShort, expiresAt.Format(time.RFC3339), next.Format(time.RFC3339))
	}
	return nil
}

// due reports whether a maintenance pass is required now: the relay is
// disconnected (reconnect + reacquire) or a connected relay's reservation has
// reached its pre-expiration renewal deadline.
func (r *RelayReservation) due() bool {
	if !r.connected() {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.now().Before(r.nextRenewLocked())
}

// nextRenewLocked is the next renewal deadline, derived from the relay-provided
// expiration: expiring reservation expiry minus the safety margin, but never
// earlier than minimumRenewDelay after the last successful maintenance. The
// floor bounds a pathological (near-past or very short) expiration from driving
// a tight loop; when the floor would land at or after the reservation's own
// expiration the maintenance is refused as ErrReservationTooShort, so a
// renewal is never scheduled after expiration. Caller holds mu.
func (r *RelayReservation) nextRenewLocked() time.Time {
	base := r.expires.Add(-r.margin)
	floor := r.lastMaintained.Add(r.minDelay)
	if base.After(floor) {
		return base
	}
	return floor
}

func (r *RelayReservation) setReady(ready bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ready = ready
}
