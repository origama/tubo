package discovery

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	DefaultOpaqueAnnouncementMaxRecords       = 256
	DefaultOpaqueAnnouncementMaxRecordBytes   = 32 << 10
	DefaultOpaqueAnnouncementMaxTotalBytes    = 768 << 10
	DefaultOpaqueAnnouncementMaxPeerRecords   = 16
	DefaultOpaqueAnnouncementMaxPeerBytes     = 128 << 10
	DefaultOpaqueAnnouncementMinRefreshPeriod = time.Second
	DefaultOpaqueAnnouncementMaxTTL           = 15 * time.Minute
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
	return r.expiredAt(time.Now())
}

func (r OpaqueAnnouncementV3Record) expiredAt(now time.Time) bool {
	return r.TTL <= 0 || !now.Before(r.ReceivedAt.Add(r.TTL))
}

// OpaqueAnnouncementCacheLimits defines relay admission and ownership budgets.
type OpaqueAnnouncementCacheLimits struct {
	MaxRecords       int
	MaxRecordBytes   int
	MaxTotalBytes    int
	MaxPeerRecords   int
	MaxPeerBytes     int
	MinRefreshPeriod time.Duration
	MaxTTL           time.Duration
}

// OpaqueAnnouncementCacheStats exposes aggregate capacity and rejection
// counters without identifying publishers.
type OpaqueAnnouncementCacheStats struct {
	Records               int    `json:"records"`
	Bytes                 int    `json:"bytes"`
	RejectedTotal         uint64 `json:"rejected_total"`
	RejectedMalformed     uint64 `json:"rejected_malformed"`
	RejectedRecordBytes   uint64 `json:"rejected_record_bytes"`
	RejectedPeerRecords   uint64 `json:"rejected_peer_records"`
	RejectedPeerBytes     uint64 `json:"rejected_peer_bytes"`
	RejectedGlobalRecords uint64 `json:"rejected_global_records"`
	RejectedGlobalBytes   uint64 `json:"rejected_global_bytes"`
	RejectedRefreshRate   uint64 `json:"rejected_refresh_rate"`
	ExpiredEvictions      uint64 `json:"expired_evictions"`
	TruncatedResponses    uint64 `json:"truncated_responses"`
}

type opaquePeerUsage struct {
	records int
	bytes   int
}

// OpaqueAnnouncementCache is a bounded in-memory store of AnnouncementV3
// records that a relay accepts on behalf of clusters it cannot itself
// validate. Admission is bounded globally and by observed publisher PeerID.
type OpaqueAnnouncementCache struct {
	mu         sync.Mutex
	entries    map[string]OpaqueAnnouncementV3Record
	peerUsage  map[peer.ID]opaquePeerUsage
	totalBytes int
	limits     OpaqueAnnouncementCacheLimits
	stats      OpaqueAnnouncementCacheStats
	now        func() time.Time
}

// NewOpaqueAnnouncementCache preserves the original constructor while applying
// safe global and per-peer defaults in addition to caller-supplied record caps.
func NewOpaqueAnnouncementCache(maxRecords, maxRecordBytes int, maxTTL time.Duration) *OpaqueAnnouncementCache {
	limits := defaultOpaqueAnnouncementCacheLimits()
	if maxRecords > 0 {
		limits.MaxRecords = maxRecords
	}
	if maxRecordBytes > 0 {
		limits.MaxRecordBytes = maxRecordBytes
	}
	if maxTTL > 0 {
		limits.MaxTTL = maxTTL
	}
	if limits.MaxPeerRecords > limits.MaxRecords {
		limits.MaxPeerRecords = limits.MaxRecords
	}
	if product, ok := boundedProduct(limits.MaxRecords, limits.MaxRecordBytes); ok && product < limits.MaxTotalBytes {
		limits.MaxTotalBytes = product
	}
	if limits.MaxPeerBytes > limits.MaxTotalBytes {
		limits.MaxPeerBytes = limits.MaxTotalBytes
	}
	return NewOpaqueAnnouncementCacheWithLimits(limits)
}

// NewOpaqueAnnouncementCacheWithLimits creates a cache with explicit limits.
// Non-positive values use secure defaults; peer limits are clamped to global
// limits.
func NewOpaqueAnnouncementCacheWithLimits(limits OpaqueAnnouncementCacheLimits) *OpaqueAnnouncementCache {
	defaults := defaultOpaqueAnnouncementCacheLimits()
	if limits.MaxRecords <= 0 {
		limits.MaxRecords = defaults.MaxRecords
	}
	if limits.MaxRecordBytes <= 0 {
		limits.MaxRecordBytes = defaults.MaxRecordBytes
	}
	if limits.MaxTotalBytes <= 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if limits.MaxPeerRecords <= 0 {
		limits.MaxPeerRecords = defaults.MaxPeerRecords
	}
	if limits.MaxPeerBytes <= 0 {
		limits.MaxPeerBytes = defaults.MaxPeerBytes
	}
	if limits.MinRefreshPeriod <= 0 {
		limits.MinRefreshPeriod = defaults.MinRefreshPeriod
	}
	if limits.MaxTTL <= 0 {
		limits.MaxTTL = defaults.MaxTTL
	}
	if limits.MaxPeerRecords > limits.MaxRecords {
		limits.MaxPeerRecords = limits.MaxRecords
	}
	if limits.MaxPeerBytes > limits.MaxTotalBytes {
		limits.MaxPeerBytes = limits.MaxTotalBytes
	}
	return &OpaqueAnnouncementCache{
		entries:   make(map[string]OpaqueAnnouncementV3Record),
		peerUsage: make(map[peer.ID]opaquePeerUsage),
		limits:    limits,
		now:       time.Now,
	}
}

func defaultOpaqueAnnouncementCacheLimits() OpaqueAnnouncementCacheLimits {
	return OpaqueAnnouncementCacheLimits{
		MaxRecords:       DefaultOpaqueAnnouncementMaxRecords,
		MaxRecordBytes:   DefaultOpaqueAnnouncementMaxRecordBytes,
		MaxTotalBytes:    DefaultOpaqueAnnouncementMaxTotalBytes,
		MaxPeerRecords:   DefaultOpaqueAnnouncementMaxPeerRecords,
		MaxPeerBytes:     DefaultOpaqueAnnouncementMaxPeerBytes,
		MinRefreshPeriod: DefaultOpaqueAnnouncementMinRefreshPeriod,
		MaxTTL:           DefaultOpaqueAnnouncementMaxTTL,
	}
}

func boundedProduct(a, b int) (int, bool) {
	if a <= 0 || b <= 0 || a > int(^uint(0)>>1)/b {
		return 0, false
	}
	return a * b, true
}

// Put stores an opaque record after applying per-record, per-peer, global, TTL,
// and refresh-rate limits. Saturated caches reject deterministically; only
// expired records are evicted automatically.
func (c *OpaqueAnnouncementCache) Put(peerID peer.ID, ann AnnouncementV3, ttl time.Duration, size int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	c.evictExpiredLocked(now)

	if peerID == "" || ann.PeerID == "" || peerID != ann.PeerID || strings.TrimSpace(ann.KeyID) == "" || ttl <= 0 || size <= 0 {
		c.rejectLocked(&c.stats.RejectedMalformed)
		return fmt.Errorf("opaque announcement metadata invalid")
	}
	if err := peerID.Validate(); err != nil {
		c.rejectLocked(&c.stats.RejectedMalformed)
		return fmt.Errorf("peer id invalid: %w", err)
	}
	if size > c.limits.MaxRecordBytes {
		c.rejectLocked(&c.stats.RejectedRecordBytes)
		return fmt.Errorf("announcement size %d exceeds relay record cap %d", size, c.limits.MaxRecordBytes)
	}
	if ttl > c.limits.MaxTTL {
		ttl = c.limits.MaxTTL
	}

	key := opaqueRecordKey(peerID, ann)
	existing, replacing := c.entries[key]
	usage := c.peerUsage[peerID]
	if replacing && c.limits.MinRefreshPeriod > 0 && now.Sub(existing.ReceivedAt) < c.limits.MinRefreshPeriod {
		c.rejectLocked(&c.stats.RejectedRefreshRate)
		return fmt.Errorf("opaque announcement refresh exceeds minimum interval %s", c.limits.MinRefreshPeriod)
	}

	nextPeerRecords := usage.records
	nextPeerBytes := usage.bytes
	nextTotalRecords := len(c.entries)
	nextTotalBytes := c.totalBytes
	if replacing {
		nextPeerBytes -= existing.SizeBytes
		nextTotalBytes -= existing.SizeBytes
	} else {
		nextPeerRecords++
		nextTotalRecords++
	}
	nextPeerBytes += size
	nextTotalBytes += size

	switch {
	case nextPeerRecords > c.limits.MaxPeerRecords:
		c.rejectLocked(&c.stats.RejectedPeerRecords)
		return fmt.Errorf("opaque peer record quota exceeded (max %d)", c.limits.MaxPeerRecords)
	case nextPeerBytes > c.limits.MaxPeerBytes:
		c.rejectLocked(&c.stats.RejectedPeerBytes)
		return fmt.Errorf("opaque peer byte quota exceeded (max %d)", c.limits.MaxPeerBytes)
	case nextTotalRecords > c.limits.MaxRecords:
		c.rejectLocked(&c.stats.RejectedGlobalRecords)
		return fmt.Errorf("opaque relay cache record quota exceeded (max %d)", c.limits.MaxRecords)
	case nextTotalBytes > c.limits.MaxTotalBytes:
		c.rejectLocked(&c.stats.RejectedGlobalBytes)
		return fmt.Errorf("opaque relay cache byte quota exceeded (max %d)", c.limits.MaxTotalBytes)
	}

	c.entries[key] = OpaqueAnnouncementV3Record{
		PeerID:       peerID,
		Announcement: ann,
		ReceivedAt:   now,
		TTL:          ttl,
		SizeBytes:    size,
	}
	c.peerUsage[peerID] = opaquePeerUsage{records: nextPeerRecords, bytes: nextPeerBytes}
	c.totalBytes = nextTotalBytes
	return nil
}

func (c *OpaqueAnnouncementCache) rejectLocked(counter *uint64) {
	c.stats.RejectedTotal++
	*counter++
}

// List returns a deterministic snapshot of non-expired opaque records.
func (c *OpaqueAnnouncementCache) List() []OpaqueAnnouncementV3Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked(c.now().UTC())
	keys := make([]string, 0, len(c.entries))
	for key := range c.entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]OpaqueAnnouncementV3Record, 0, len(keys))
	for _, key := range keys {
		out = append(out, c.entries[key])
	}
	return out
}

// Count returns the current number of stored records.
func (c *OpaqueAnnouncementCache) Count() int {
	return c.Stats().Records
}

// RecordTruncation increments the observable response-truncation counter.
func (c *OpaqueAnnouncementCache) RecordTruncation() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stats.TruncatedResponses++
}

// Stats returns aggregate capacity and rejection metrics.
func (c *OpaqueAnnouncementCache) Stats() OpaqueAnnouncementCacheStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.evictExpiredLocked(c.now().UTC())
	stats := c.stats
	stats.Records = len(c.entries)
	stats.Bytes = c.totalBytes
	return stats
}

func (c *OpaqueAnnouncementCache) evictExpiredLocked(now time.Time) {
	for key, entry := range c.entries {
		if !entry.expiredAt(now) {
			continue
		}
		delete(c.entries, key)
		c.totalBytes -= entry.SizeBytes
		usage := c.peerUsage[entry.PeerID]
		usage.records--
		usage.bytes -= entry.SizeBytes
		if usage.records <= 0 {
			delete(c.peerUsage, entry.PeerID)
		} else {
			c.peerUsage[entry.PeerID] = usage
		}
		c.stats.ExpiredEvictions++
	}
}

func opaqueRecordKey(peerID peer.ID, ann AnnouncementV3) string {
	return peerID.String() + "|" + ann.KeyID
}
