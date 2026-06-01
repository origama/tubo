package discovery

import (
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	grantspkg "github.com/origama/tubo/internal/grants"
)

// ServiceEntry represents a cached service registration.
type ServiceEntry struct {
	ServiceID        string
	ServiceName      string
	ServiceKind      string
	ServicePublicKey string
	ConnectPolicy    string
	GrantService     *grantspkg.GrantServiceEndpoint
	Capabilities     []string
	PeerID           peer.ID
	Addresses        []string
	TTL              time.Duration
	Registered       time.Time // when the entry was last registered/renewed
}

// Expired returns true if the entry's TTL has elapsed since registration.
func (e *ServiceEntry) Expired() bool {
	return time.Since(e.Registered) > e.TTL
}

// Cache maintains service registrations keyed primarily by service_id.
// A secondary display-name index preserves legacy Resolve(name) behavior.
// It runs a background goroutine that periodically removes expired entries.
type Cache struct {
	mu          sync.Mutex
	entries     map[string]*ServiceEntry // keyed by serviceID when available, otherwise serviceName
	nameIndex   map[string][]string      // display name -> entry keys
	defaultTTL  time.Duration
	cleanupTick time.Duration
	stopCh      chan struct{}
	onExpired   func(serviceName string, peerID peer.ID)
}

// NewCache creates a new discovery cache with the given default TTL and cleanup interval.
func NewCache(defaultTTL, cleanupTick time.Duration) *Cache {
	c := &Cache{
		entries:     make(map[string]*ServiceEntry),
		nameIndex:   make(map[string][]string),
		defaultTTL:  defaultTTL,
		cleanupTick: cleanupTick,
		stopCh:      make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// SetExpiredCallback registers a callback invoked when an entry expires.
func (c *Cache) SetExpiredCallback(fn func(serviceName string, peerID peer.ID)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onExpired = fn
}

// Add registers or updates a legacy name-keyed service entry.
func (c *Cache) Add(pID peer.ID, serviceName string, addresses []string, ttl time.Duration) error {
	return c.AddV2(pID, "", serviceName, "http", "", "", nil, addresses, nil, ttl)
}

// AddV2 registers or updates a service_id-keyed entry. Display name is metadata
// and is not unique; multiple entries may share the same ServiceName.
func (c *Cache) AddV2(pID peer.ID, serviceID, serviceName, serviceKind, servicePublicKey, connectPolicy string, grantService *grantspkg.GrantServiceEndpoint, addresses []string, capabilities []string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if ttl <= 0 {
		ttl = c.defaultTTL
	}
	key := serviceID
	if key == "" {
		key = serviceName
	}

	entry := &ServiceEntry{
		ServiceID:        serviceID,
		ServiceName:      serviceName,
		ServiceKind:      serviceKind,
		ServicePublicKey: servicePublicKey,
		ConnectPolicy:    connectPolicy,
		GrantService:     grantspkg.CloneGrantServiceEndpoint(grantService),
		Capabilities:     append([]string(nil), capabilities...),
		PeerID:           pID,
		Addresses:        append([]string(nil), addresses...), // copy to prevent mutation
		TTL:              ttl,
		Registered:       time.Now(),
	}

	if old, ok := c.entries[key]; ok && old.ServiceName != serviceName {
		c.removeNameIndexLocked(old.ServiceName, key)
	}
	c.entries[key] = entry
	c.addNameIndexLocked(serviceName, key)
	return nil
}

// Resolve looks up a service by name. Returns the entry and true if found & not expired,
// or nil and false otherwise. Expired entries are lazily removed on lookup.
func (c *Cache) Resolve(serviceName string) (*ServiceEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, key, ok := c.resolveLocked(serviceName)
	if !ok {
		return nil, false
	}
	if entry.Expired() {
		c.removeLocked(key, entry)
		if c.onExpired != nil {
			go c.onExpired(entry.ServiceName, entry.PeerID)
		}
		return nil, false
	}
	// Return a copy to prevent external mutation
	e := *entry
	e.Addresses = append([]string(nil), entry.Addresses...)
	e.GrantService = grantspkg.CloneGrantServiceEndpoint(entry.GrantService)
	return &e, true
}

// Remove explicitly removes a service from the cache.
func (c *Cache) Remove(serviceName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, key, ok := c.resolveLocked(serviceName)
	if !ok {
		return
	}
	c.removeLocked(key, entry)
}

// Count returns the number of active (non-expired) entries in the cache.
func (c *Cache) Count() int {
	return len(c.List())
}

// List returns a snapshot of all active (non-expired) entries.
func (c *Cache) List() []*ServiceEntry {
	c.mu.Lock()
	entries := make([]*ServiceEntry, 0, len(c.entries))
	var expired []struct {
		name string
		pid  peer.ID
	}
	for key, entry := range c.entries {
		if entry.Expired() {
			c.removeLocked(key, entry)
			expired = append(expired, struct {
				name string
				pid  peer.ID
			}{entry.ServiceName, entry.PeerID})
			continue
		}
		copyEntry := *entry
		copyEntry.Addresses = append([]string(nil), entry.Addresses...)
		copyEntry.GrantService = grantspkg.CloneGrantServiceEndpoint(entry.GrantService)
		entries = append(entries, &copyEntry)
	}
	onExpired := c.onExpired
	c.mu.Unlock()

	if onExpired != nil {
		for _, e := range expired {
			go onExpired(e.name, e.pid)
		}
	}
	return entries
}

// Stop shuts down the background cleanup goroutine.
func (c *Cache) Stop() {
	close(c.stopCh)
}

func (c *Cache) resolveLocked(value string) (*ServiceEntry, string, bool) {
	if entry, ok := c.entries[value]; ok {
		return entry, value, true
	}
	for _, key := range c.nameIndex[value] {
		if entry, ok := c.entries[key]; ok {
			return entry, key, true
		}
	}
	return nil, "", false
}

func (c *Cache) addNameIndexLocked(serviceName, key string) {
	if serviceName == "" || key == "" {
		return
	}
	for _, existing := range c.nameIndex[serviceName] {
		if existing == key {
			return
		}
	}
	c.nameIndex[serviceName] = append(c.nameIndex[serviceName], key)
}

func (c *Cache) removeNameIndexLocked(serviceName, key string) {
	keys := c.nameIndex[serviceName]
	for i, existing := range keys {
		if existing == key {
			keys = append(keys[:i], keys[i+1:]...)
			break
		}
	}
	if len(keys) == 0 {
		delete(c.nameIndex, serviceName)
		return
	}
	c.nameIndex[serviceName] = keys
}

func (c *Cache) removeLocked(key string, entry *ServiceEntry) {
	delete(c.entries, key)
	c.removeNameIndexLocked(entry.ServiceName, key)
}

// cleanupLoop periodically removes expired entries from the cache.
func (c *Cache) cleanupLoop() {
	ticker := time.NewTicker(c.cleanupTick)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			var expired []struct {
				name string
				pid  peer.ID
			}
			c.mu.Lock()
			for key, entry := range c.entries {
				if entry.Expired() {
					c.removeLocked(key, entry)
					expired = append(expired, struct {
						name string
						pid  peer.ID
					}{entry.ServiceName, entry.PeerID})
				}
			}
			c.mu.Unlock()

			for _, e := range expired {
				if c.onExpired != nil {
					go c.onExpired(e.name, e.pid)
				}
			}
		case <-c.stopCh:
			return
		}
	}
}
