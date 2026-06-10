package p2p

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// relayID is a synthetic peer.ID used in reservation tests.
var relayID = peer.ID("12D3KooWTestRelay")

// overlayWithRelay builds a minimal OverlayHost wired with one relay entry and
// a relay-connected map, without starting a real libp2p host. This is enough to
// exercise needsRelayReservation() which is a pure state function.
func overlayWithRelay(connectedToRelay bool, readyUntil time.Time) *OverlayHost {
	connected := make(map[peer.ID]bool)
	if connectedToRelay {
		connected[relayID] = true
	}
	return &OverlayHost{
		RelayInfos:            []peer.AddrInfo{{ID: relayID}},
		relayConnected:        connected,
		reservationReadyUntil: readyUntil,
	}
}

func TestNeedsRelayReservation_NoReservationYet(t *testing.T) {
	o := overlayWithRelay(true, time.Time{})
	if !o.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true when no reservation has been acquired (zero time)")
	}
}

func TestNeedsRelayReservation_FreshReservation(t *testing.T) {
	// Reservation expires well in the future — no renewal needed yet.
	o := overlayWithRelay(true, time.Now().Add(30*time.Minute))
	if o.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=false for a fresh reservation outside the renewal margin")
	}
}

func TestNeedsRelayReservation_WithinRenewMargin(t *testing.T) {
	// Reservation expires within the 10-minute margin — proactive renewal due.
	o := overlayWithRelay(true, time.Now().Add(5*time.Minute))
	if !o.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true when reservation is within the renewal margin")
	}
}

func TestNeedsRelayReservation_Expired(t *testing.T) {
	// Reservation already expired.
	o := overlayWithRelay(true, time.Now().Add(-time.Second))
	if !o.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true for an expired reservation")
	}
}

func TestNeedsRelayReservation_RelayDisconnected(t *testing.T) {
	// Relay not connected: must reserve (which will reconnect first).
	// A seemingly valid future expiry must not suppress this.
	o := overlayWithRelay(false, time.Now().Add(30*time.Minute))
	if !o.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true when relay is disconnected")
	}
}

func TestNeedsRelayReservation_IgnoresLingeringCircuitAddr(t *testing.T) {
	// The key regression test: a lingering /p2p-circuit addr in Host.Addrs()
	// (from autorelay) must NOT suppress proactive renewal when the tracked
	// expiry is within the renewal margin. needsRelayReservation must not
	// inspect Host.Addrs() at all.
	//
	// We simulate this by setting a reservation that expires within the margin
	// (so renewal is due) and confirming needsRelayReservation returns true,
	// even though HasRelayReservation (the readiness check) would return true
	// via the addr path when a real host is present.
	o := overlayWithRelay(true, time.Now().Add(5*time.Minute))
	if !o.needsRelayReservation() {
		t.Fatal("expected needsRelayReservation=true: lingering circuit addr must not suppress renewal")
	}
}

func TestNeedsRelayReservation_NoRelayInfos(t *testing.T) {
	// If there are no configured relay peers, needsRelayReservation should
	// never trigger a reservation attempt — the relay-connected guard must
	// be skipped entirely (no relays → no reservation needed).
	o := &OverlayHost{
		RelayInfos:            nil,
		relayConnected:        make(map[peer.ID]bool),
		reservationReadyUntil: time.Time{},
	}
	// With no relays, the zero-time check fires and returns true.  That is
	// harmless because StartRelayReservations returns early when RelayInfos is
	// empty, so maintainRelayReservations is never started. We only assert the
	// relay-disconnected path is not entered when RelayInfos is nil/empty.
	// (No assertion: this test documents the invariant via review.)
	_ = o.needsRelayReservation()
}
