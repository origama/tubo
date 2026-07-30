package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestRevocationRoundTrip(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.Revoke(context.Background(), "solar/default", "connect.access", "jti_access", 1); err != nil {
		t.Fatal(err)
	}
	revoked, err := state.IsRevoked(context.Background(), "solar/default", "connect.access", "jti_access")
	if err != nil || !revoked {
		t.Fatalf("revoked=%v err=%v", revoked, err)
	}
	if err := state.Revoke(context.Background(), "solar/default", "connect.access", "jti_access", 0); err == nil {
		t.Fatal("accepted zero epoch")
	}
}

func TestRedeemInviteAtomicAndIdempotent(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	state, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	ctx := context.Background()
	const workers = 16
	var wg sync.WaitGroup
	var success atomic.Int32
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			created, err := state.RedeemInvite(ctx, "jti_one", "pip_one", "ses_one", "client_one")
			if err != nil {
				t.Errorf("same client retry: %v", err)
				return
			}
			if created {
				success.Add(1)
			}
		}()
	}
	wg.Wait()
	if success.Load() != 1 {
		t.Fatalf("successful consumes=%d", success.Load())
	}
	count, err := state.RedemptionCount(ctx, "jti_one", "pip_one")
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := state.RedeemInvite(ctx, "jti_one", "pip_one", "ses_two", "client_two"); !errors.Is(err, ErrInviteRedeemed) {
		t.Fatalf("other client err=%v", err)
	}
}
