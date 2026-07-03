package discovery

import (
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// OpaqueAnnouncementV3Record wraps a raw AnnouncementV3 with cache metadata,
// mirroring query.OpaqueAnnouncementV3Record but living in the discovery
// package for storage implementation.
type OpaqueAnnouncementV3Record struct {
	PeerID       peer.ID
	Announcement AnnouncementV3
	ReceivedAt   time.Time
	TTL          time.Duration
	SizeBytes    int
}

// Expired reports whether the record's stored TTL has elapsed.
func (r OpaqueAnnouncementV3Record) Expired() bool {
	if r.TTL <= 0 {
		return true
	}
	return time.Since(r.ReceivedAt) > r.TTL
}

// OpaqueAnnouncementCache is a bounded in-memory store of AnnouncementV3
// records that a relay accepts on behalf of clusters it cannot itself
// validate. It caps record count and per-record byte size to limit DoS/spam
// surface; the trust boundary for correctness is enforced by consumers.
//
// Records are keyed by (peer_id, announcement key_id) so a given publisher can
// refresh its own announcement without duplicating entries. Different
// publishers announcing the same service produce distinct entries so consumers
// can pick whichever one they can verify.
type OpaqueAnnouncementCache struct {
	mu         sync.Mutex
	entries    map[string]OpaqueAnnouncementV3Record
	maxRecords int
	maxBytes   int
	maxTTL     time.Duration
	now        func() time.Time
}

// NewOpaqueAnnouncementCache creates a new opaque relay cache with the given
// limits. Values <= 0 fall back to safe defaults.
func NewOpaqueAnnouncementCache(maxRecords, maxBytes int, maxTTL time.Duration) *OpaqueAnnouncementCache {
	if maxRecords <= 0 {
		maxRecords = 1024
	}
	if maxBytes <= 0 {
		maxBytes = 32 << 10
	}
	if maxTTL <= 0 {
		maxTTL = 15 * time.Minute
	}
	return &OpaqueAnnouncementCache{
		entries:    make(map[string]OpaqueAnnouncementV3Record),
		maxRecords: maxRecords,
		maxBytes:   maxBytes,
		maxTTL:     maxTTL,
		now:        time.Now,
	}
}

// Put stores an opaque record after applying per-record limits. Returns an
// error when the record exceeds size or when the cache is full and cannot
// evict an expired peer entry to make room.
func (c *OpaqueAnnouncementCache) Put(peerID peer.ID, ann AnnouncementV3, ttl time.Duration, size int) error {
	if peerID == "" {
		return fmt.Errorf("peer id missing")
	}
	if size > c.maxBytes {
		return fmt.Errorf("announcement size %d exceeds relay cap %d", size, c.maxBytes)
	}
	if ttl > c.maxTTL {
		ttl = c.maxTTL
	}
	key := opaqueRecordKey(peerID, ann)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked()
	if _, exists := c.entries[key]; !exists && len(c.entries) >= c.maxRecords {
		return fmt.Errorf("opaque relay cache full (max %d records)", c.maxRecords)
	}
	c.entries[key] = OpaqueAnnouncementV3Record{
		PeerID:       peerID,
		Announcement: ann,
		ReceivedAt:   c.now().UTC(),
		TTL:          ttl,
		SizeBytes:    size,
	}
	return nil
}

// List returns a snapshot of non-expired opaque records.
func (c *OpaqueAnnouncementCache) List() []OpaqueAnnouncementV3Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked()
	out := make([]OpaqueAnnouncementV3Record, 0, len(c.entries))
	for _, entry := range c.entries {
		out = append(out, entry)
	}
	return out
}

// Count returns the current number of stored records.
func (c *OpaqueAnnouncementCache) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked()
	return len(c.entries)
}

func (c *OpaqueAnnouncementCache) evictExpiredLocked() {
	for k, e := range c.entries {
		if e.Expired() {
			delete(c.entries, k)
		}
	}
}

func opaqueRecordKey(peerID peer.ID, ann AnnouncementV3) string {
	return peerID.String() + "|" + ann.KeyID
}
