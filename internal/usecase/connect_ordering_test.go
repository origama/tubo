package usecase

import (
	"context"
	"strings"
	"testing"
)

// TestConflictingConnectDoesNotConsumeInvite: a connect that will be rejected
// by a local Pipe conflict must fail BEFORE redeeming the one-time invite. The
// invite is the exact bootstrap credential; consuming it in a failing connect
// burns the credential and forces a fresh share for a request that never
// succeeded. Conflicts must fail closed with zero redemption calls.
func TestConflictingConnectDoesNotConsumeInvite(t *testing.T) {
	ctx := context.Background()
	u, pipes, firstInvite := newCollisionHarness(t, "remote-order")

	if _, err := u.ConnectExact(ctx, ConnectExactInput{Invite: firstInvite, LocalListen: "127.0.0.1:9001", Name: "west"}); err != nil {
		t.Fatal(err)
	}
	redemption := u.Redemption.(*fakeRedemptionService)
	redemption.mu.Lock()
	callsAfterFirst := redemption.calls
	redemption.mu.Unlock()
	if callsAfterFirst != 1 {
		t.Fatalf("baseline redemption calls = %d, want 1", callsAfterFirst)
	}

	// A second invite for the same remote Pipe, then a conflicting connect:
	// different friendly name for an identity that already has a local Pipe.
	secondInvite, err := attachAndShare(t, u, "remote-order")
	if err != nil {
		t.Fatal(err)
	}
	_, err = u.ConnectExact(ctx, ConnectExactInput{Invite: secondInvite, LocalListen: "127.0.0.1:9002", Name: "east"})
	if err == nil {
		t.Fatal("conflicting connect must fail")
	}
	if !strings.Contains(err.Error(), "already connects") {
		t.Fatalf("unexpected conflict error: %v", err)
	}
	redemption.mu.Lock()
	callsAfterConflict := redemption.calls
	redemption.mu.Unlock()
	if callsAfterConflict != callsAfterFirst {
		t.Fatalf("conflicting connect consumed the invite: redemption calls %d -> %d", callsAfterFirst, callsAfterConflict)
	}
	if count := pipeCount(pipes); count != 2 {
		t.Fatalf("conflicting connect mutated durable pipes: count=%d", count)
	}
}
