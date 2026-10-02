package store

import (
	"context"
	"testing"
	"time"
)

// TestReopenDoesNotPromoteScopedRedemptionToExactRelationship: a scoped
// (legacy share.invite) redemption is anti-replay history only. Store.Open
// must never synthesize an accepted exact relationship from it, regardless of
// how many times the store is reopened. The schema-13 backfill is a one-time
// legacy upgrade step, not an invariant to re-assert on every open.
func TestReopenDoesNotPromoteScopedRedemptionToExactRelationship(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()

	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RedeemInvite(ctx, "jti_scoped_promote", "pip_promote", "ses_1", "b_client_promote"); err != nil {
		t.Fatal(err)
	}
	adapter := NewExactAuthorityRevocationState(state, "pip_promote")
	if _, _, ok, err := adapter.AcceptedRelationship(ctx, "pip_promote", "b_client_promote"); err != nil || ok {
		t.Fatalf("scoped redemption must not create exact relationship before reopen: ok=%v %v", ok, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	adapter = NewExactAuthorityRevocationState(reopened, "pip_promote")
	if _, _, ok, err := adapter.AcceptedRelationship(ctx, "pip_promote", "b_client_promote"); err != nil || ok {
		t.Fatalf("Store.Open synthesized exact accepted relationship from scoped redemption: ok=%v %v", ok, err)
	}
}

// TestExactRelationshipSurvivesReopen proves the same reopen path preserves
// genuinely accepted exact relationships (the durable binding contract).
func TestExactRelationshipSurvivesReopen(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()

	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_exact_keep", "pip_keep", "ses_1", "b_client_keep", 0, now); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	adapter := NewExactAuthorityRevocationState(reopened, "pip_keep")
	jti, epoch, ok, err := adapter.AcceptedRelationship(ctx, "pip_keep", "b_client_keep")
	if err != nil || !ok || jti != "jti_exact_keep" || epoch != 0 {
		t.Fatalf("accepted exact relationship lost on reopen: jti=%q epoch=%d ok=%v %v", jti, epoch, ok, err)
	}
}
