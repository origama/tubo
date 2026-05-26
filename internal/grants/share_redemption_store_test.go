package grants

import (
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestShareRedemptionStoreTryConsumeSucceedsOnce(t *testing.T) {
	store := NewShareRedemptionStore(filepath.Join(t.TempDir(), "share-redemptions.json"))
	rec := ShareRedemptionRecord{JTI: "si_1", ClusterID: "cluster-1", NamespaceID: "default", ServiceID: "svc-1", TokenExpiresAt: time.Now().Add(time.Hour)}
	if err := store.TryConsume(rec); err != nil {
		t.Fatal(err)
	}
	if err := store.TryConsume(rec); !errors.Is(err, ErrShareInviteAlreadyRedeemed) {
		t.Fatalf("expected already redeemed, got %v", err)
	}
}

func TestShareRedemptionStorePrunesExpiredRecords(t *testing.T) {
	store := NewShareRedemptionStore(filepath.Join(t.TempDir(), "share-redemptions.json"))
	now := time.Now().UTC()
	store.now = func() time.Time { return now }
	if err := store.TryConsume(ShareRedemptionRecord{JTI: "si_old", ClusterID: "cluster-1", NamespaceID: "default", ServiceID: "svc-1", TokenExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if err := store.TryConsume(ShareRedemptionRecord{JTI: "si_new", ClusterID: "cluster-1", NamespaceID: "default", ServiceID: "svc-1", TokenExpiresAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	items, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].JTI != "si_new" {
		t.Fatalf("unexpected records: %#v", items)
	}
}

func TestShareRedemptionStoreConcurrentConsumeOnlyOneSucceeds(t *testing.T) {
	store := NewShareRedemptionStore(filepath.Join(t.TempDir(), "share-redemptions.json"))
	rec := ShareRedemptionRecord{JTI: "si_race", ClusterID: "cluster-1", NamespaceID: "default", ServiceID: "svc-1", TokenExpiresAt: time.Now().Add(time.Hour)}
	var success atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := store.TryConsume(rec); err == nil {
				success.Add(1)
			} else if !errors.Is(err, ErrShareInviteAlreadyRedeemed) {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	if got := success.Load(); got != 1 {
		t.Fatalf("success count = %d, want 1", got)
	}
}

func TestShareRedemptionStorePersistsAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "share-redemptions.json")
	store := NewShareRedemptionStore(path)
	rec := ShareRedemptionRecord{JTI: "si_persist", ClusterID: "cluster-1", NamespaceID: "default", ServiceID: "svc-1", TokenExpiresAt: time.Now().Add(time.Hour)}
	if err := store.TryConsume(rec); err != nil {
		t.Fatal(err)
	}
	reloaded := NewShareRedemptionStore(path)
	if err := reloaded.TryConsume(rec); !errors.Is(err, ErrShareInviteAlreadyRedeemed) {
		t.Fatalf("expected persisted already redeemed error, got %v", err)
	}
}
