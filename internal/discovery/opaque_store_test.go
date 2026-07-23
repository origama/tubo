package discovery

import (
	"crypto/rand"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestOpaqueAnnouncementCacheEnforcesPerPeerQuotaWithoutStarvingOtherPeer(t *testing.T) {
	peerA := newOpaqueTestPeer(t)
	peerB := newOpaqueTestPeer(t)
	cache := NewOpaqueAnnouncementCacheWithLimits(OpaqueAnnouncementCacheLimits{
		MaxRecords:       4,
		MaxRecordBytes:   100,
		MaxTotalBytes:    400,
		MaxPeerRecords:   2,
		MaxPeerBytes:     200,
		MinRefreshPeriod: 0,
		MaxTTL:           time.Minute,
	})

	mustPutOpaque(t, cache, peerA, "key-a1", 50, time.Minute)
	mustPutOpaque(t, cache, peerA, "key-a2", 50, time.Minute)
	if err := cache.Put(peerA, opaqueTestAnnouncement(peerA, "key-a3"), time.Minute, 50); err == nil || !strings.Contains(err.Error(), "peer record quota") {
		t.Fatalf("third peer-A record error = %v, want peer quota", err)
	}
	mustPutOpaque(t, cache, peerB, "key-b1", 50, time.Minute)

	stats := cache.Stats()
	if stats.Records != 3 || stats.Bytes != 150 || stats.RejectedPeerRecords != 1 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestOpaqueAnnouncementCacheConcurrentSaturationPreservesQuota(t *testing.T) {
	peerA := newOpaqueTestPeer(t)
	peerB := newOpaqueTestPeer(t)
	cache := NewOpaqueAnnouncementCacheWithLimits(OpaqueAnnouncementCacheLimits{
		MaxRecords:       32,
		MaxRecordBytes:   100,
		MaxTotalBytes:    3200,
		MaxPeerRecords:   16,
		MaxPeerBytes:     1600,
		MinRefreshPeriod: time.Second,
		MaxTTL:           time.Minute,
	})
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			ann := opaqueTestAnnouncement(peerA, fmt.Sprintf("key-%02d", index))
			if err := cache.Put(peerA, ann, time.Minute, 10); err == nil {
				successes.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if got := successes.Load(); got != 16 {
		t.Fatalf("concurrent accepted records = %d, want 16", got)
	}
	mustPutOpaque(t, cache, peerB, "key-b", 10, time.Minute)
	if stats := cache.Stats(); stats.Records != 17 || stats.RejectedPeerRecords != 48 {
		t.Fatalf("concurrent saturation stats: %#v", stats)
	}
}

func TestOpaqueAnnouncementCacheReplacementAccountingAndRefreshRate(t *testing.T) {
	pid := newOpaqueTestPeer(t)
	now := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	cache := NewOpaqueAnnouncementCacheWithLimits(OpaqueAnnouncementCacheLimits{
		MaxRecords:       4,
		MaxRecordBytes:   100,
		MaxTotalBytes:    100,
		MaxPeerRecords:   4,
		MaxPeerBytes:     70,
		MinRefreshPeriod: time.Second,
		MaxTTL:           time.Minute,
	})
	cache.now = func() time.Time { return now }
	mustPutOpaque(t, cache, pid, "key-1", 40, time.Minute)

	if err := cache.Put(pid, opaqueTestAnnouncement(pid, "key-1"), time.Minute, 60); err == nil || !strings.Contains(err.Error(), "minimum interval") {
		t.Fatalf("immediate refresh error = %v, want rate rejection", err)
	}
	now = now.Add(time.Second)
	mustPutOpaque(t, cache, pid, "key-1", 60, time.Minute)
	if stats := cache.Stats(); stats.Records != 1 || stats.Bytes != 60 || stats.RejectedRefreshRate != 1 {
		t.Fatalf("replacement accounting stats: %#v", stats)
	}
	if err := cache.Put(pid, opaqueTestAnnouncement(pid, "key-2"), time.Minute, 20); err == nil || !strings.Contains(err.Error(), "peer byte quota") {
		t.Fatalf("peer byte quota error = %v", err)
	}
}

func TestOpaqueAnnouncementCacheGlobalByteQuotaAndExpiry(t *testing.T) {
	peerA := newOpaqueTestPeer(t)
	peerB := newOpaqueTestPeer(t)
	now := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	cache := NewOpaqueAnnouncementCacheWithLimits(OpaqueAnnouncementCacheLimits{
		MaxRecords:       4,
		MaxRecordBytes:   100,
		MaxTotalBytes:    100,
		MaxPeerRecords:   4,
		MaxPeerBytes:     100,
		MinRefreshPeriod: 0,
		MaxTTL:           time.Minute,
	})
	cache.now = func() time.Time { return now }
	mustPutOpaque(t, cache, peerA, "key-a", 70, 2*time.Second)
	if err := cache.Put(peerB, opaqueTestAnnouncement(peerB, "key-b"), time.Minute, 40); err == nil || !strings.Contains(err.Error(), "cache byte quota") {
		t.Fatalf("global byte quota error = %v", err)
	}

	now = now.Add(2 * time.Second)
	mustPutOpaque(t, cache, peerB, "key-b", 40, time.Minute)
	stats := cache.Stats()
	if stats.Records != 1 || stats.Bytes != 40 || stats.ExpiredEvictions != 1 || stats.RejectedGlobalBytes != 1 {
		t.Fatalf("expiry stats: %#v", stats)
	}
}

func TestOpaqueAnnouncementCacheRejectsMalformedAndOversizedRecords(t *testing.T) {
	pid := newOpaqueTestPeer(t)
	cache := NewOpaqueAnnouncementCacheWithLimits(OpaqueAnnouncementCacheLimits{
		MaxRecords:       2,
		MaxRecordBytes:   16,
		MaxTotalBytes:    32,
		MaxPeerRecords:   2,
		MaxPeerBytes:     32,
		MinRefreshPeriod: 0,
		MaxTTL:           time.Minute,
	})
	if err := cache.Put(pid, opaqueTestAnnouncement(pid, ""), time.Minute, 8); err == nil {
		t.Fatal("expected empty key ID rejection")
	}
	if err := cache.Put(pid, opaqueTestAnnouncement(pid, "key"), time.Minute, 17); err == nil || !strings.Contains(err.Error(), "record cap") {
		t.Fatalf("oversized error = %v", err)
	}
	stats := cache.Stats()
	if stats.RejectedMalformed != 1 || stats.RejectedRecordBytes != 1 || stats.Records != 0 {
		t.Fatalf("unexpected rejection stats: %#v", stats)
	}
}

func TestOpaqueAnnouncementCacheListOrderingIsDeterministic(t *testing.T) {
	peerA := newOpaqueTestPeer(t)
	peerB := newOpaqueTestPeer(t)
	cache := NewOpaqueAnnouncementCacheWithLimits(OpaqueAnnouncementCacheLimits{
		MaxRecords:       8,
		MaxRecordBytes:   100,
		MaxTotalBytes:    800,
		MaxPeerRecords:   4,
		MaxPeerBytes:     400,
		MinRefreshPeriod: 0,
		MaxTTL:           time.Minute,
	})
	mustPutOpaque(t, cache, peerB, "z", 10, time.Minute)
	mustPutOpaque(t, cache, peerA, "b", 10, time.Minute)
	mustPutOpaque(t, cache, peerA, "a", 10, time.Minute)

	first := opaqueRecordKeys(cache.List())
	second := opaqueRecordKeys(cache.List())
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("list order changed: %v then %v", first, second)
	}
	for i := 1; i < len(first); i++ {
		if first[i-1] > first[i] {
			t.Fatalf("list is not sorted: %v", first)
		}
	}
}

func newOpaqueTestPeer(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := libcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func opaqueTestAnnouncement(pid peer.ID, keyID string) AnnouncementV3 {
	return AnnouncementV3{Version: AnnouncementVersionV3, PeerID: pid, KeyID: keyID}
}

func mustPutOpaque(t *testing.T, cache *OpaqueAnnouncementCache, pid peer.ID, keyID string, size int, ttl time.Duration) {
	t.Helper()
	if err := cache.Put(pid, opaqueTestAnnouncement(pid, keyID), ttl, size); err != nil {
		t.Fatalf("put %s/%s: %v", pid, keyID, err)
	}
}

func opaqueRecordKeys(records []OpaqueAnnouncementV3Record) []string {
	out := make([]string, 0, len(records))
	for _, record := range records {
		out = append(out, opaqueRecordKey(record.PeerID, record.Announcement))
	}
	return out
}
