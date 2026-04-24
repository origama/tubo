package discovery

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// ServiceEntry represents a cached service registration.
type ServiceEntry struct {
	ServiceName string
	PeerID      peer.ID
	Addresses   []string
	TTL         time.Duration
	Registered  time.Time // when the entry was last registered/renewed
}

// Expired returns true if the entry's TTL has elapsed since registration.
func (e *ServiceEntry) Expired() bool {
	return time.Since(e.Registered) > e.TTL
}

// Cache maintains a map of service names to their current registrations.
// It runs a background goroutine that periodically removes expired entries.
type Cache struct {
	mu          sync.Mutex
	entries     map[string]*ServiceEntry // keyed by serviceName
	defaultTTL  time.Duration
	cleanupTick time.Duration
	stopCh      chan struct{}
}

// NewCache creates a new discovery cache with the given default TTL and cleanup interval.
func NewCache(defaultTTL, cleanupTick time.Duration) *Cache {
	c := &Cache{
		entries:     make(map[string]*ServiceEntry),
		defaultTTL:  defaultTTL,
		cleanupTick: cleanupTick,
		stopCh:      make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Add registers or updates a service entry. If the service already exists, it's renewed
// with fresh TTL and updated addresses. Returns nil on success.
func (c *Cache) Add(pID peer.ID, serviceName string, addresses []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry := &ServiceEntry{
		ServiceName: serviceName,
		PeerID:      pID,
		Addresses:   append([]string(nil), addresses...), // copy to prevent mutation
		TTL:         c.defaultTTL,
		Registered:  time.Now(),
	}

	c.entries[serviceName] = entry
	return nil
}

// Resolve looks up a service by name. Returns the entry and true if found & not expired,
// or nil and false otherwise. Expired entries are lazily removed on lookup.
func (c *Cache) Resolve(serviceName string) (*ServiceEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[serviceName]
	if !ok {
		return nil, false
	}
	if entry.Expired() {
		delete(c.entries, serviceName) // lazy cleanup
		return nil, false
	}
	// Return a copy to prevent external mutation
	e := *entry
	return &e, true
}

// Remove explicitly removes a service from the cache.
func (c *Cache) Remove(serviceName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, serviceName)
}

// Count returns the number of active (non-expired) entries in the cache.
func (c *Cache) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for name, entry := range c.entries {
		if entry.Expired() {
			delete(c.entries, name) // lazy cleanup during count too
		} else {
			count++
		}
	}
	return count
}

// Stop shuts down the background cleanup goroutine.
func (c *Cache) Stop() {
	close(c.stopCh)
}

// cleanupLoop periodically removes expired entries from the cache.
func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(c.cleanupTick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.mu.Lock()
			for name, entry := range c.entries {
				if entry.Expired() {
					delete(c.entries, name)
				}
			}
			c.mu.Unlock()
		case <-c.stopCh:
			return
		}
	}
}
