package discovery

import (
	"sort"
	"sync"
	"time"
)

const defaultReplayCacheSize = 1024

type announcementReplayCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
	max  int
}

func newAnnouncementReplayCache(max int) *announcementReplayCache {
	if max <= 0 {
		max = defaultReplayCacheSize
	}
	return &announcementReplayCache{seen: make(map[string]time.Time), max: max}
}

// Seen records key until expiresAt and returns true if the key was already
// seen and is still within its replay window.
func (c *announcementReplayCache) Seen(key string, expiresAt time.Time) bool {
	now := time.Now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purgeLocked(now)
	if existing, ok := c.seen[key]; ok && now.Before(existing) {
		return true
	}
	if len(c.seen) >= c.max {
		c.evictOldestLocked()
	}
	c.seen[key] = expiresAt.UTC()
	return false
}

func (c *announcementReplayCache) purgeLocked(now time.Time) {
	for key, expiry := range c.seen {
		if !now.Before(expiry) {
			delete(c.seen, key)
		}
	}
}

func (c *announcementReplayCache) evictOldestLocked() {
	if len(c.seen) == 0 {
		return
	}
	keys := make([]string, 0, len(c.seen))
	for key := range c.seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return c.seen[keys[i]].Before(c.seen[keys[j]]) })
	delete(c.seen, keys[0])
}
