package routing

import (
	"fmt"
	"sync"
)

// RouteTable maintains a thread-safe collection of routes.
type RouteTable struct {
	mu     sync.Mutex
	routes []Route
}

// NewRouteTable creates an empty route table.
func NewRouteTable() *RouteTable {
	return &RouteTable{
		routes: make([]Route, 0),
	}
}

// Add inserts or updates a route in the table. If a route with the same
// Hostname+PathPrefix already exists, it's replaced. Returns an error if
// the combination is invalid.
func (t *RouteTable) Add(r Route) error {
	if r.Hostname == "" || r.ServiceName == "" {
		return fmt.Errorf("hostname and service name are required")
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Check for duplicate hostname+pathPrefix
	for i, existing := range t.routes {
		if existing.Hostname == r.Hostname && existing.PathPrefix == r.PathPrefix {
			t.routes[i] = r
			return nil
		}
	}

	t.routes = append(t.routes, r)
	return nil
}

// Remove deletes a route matching the given hostname and path prefix.
// Returns true if a route was found and removed.
func (t *RouteTable) Remove(hostname, pathPrefix string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	for i, r := range t.routes {
		if r.Hostname == hostname && r.PathPrefix == pathPrefix {
			t.routes[i] = t.routes[len(t.routes)-1]
			t.routes = t.routes[:len(t.routes)-1]
			return true
		}
	}
	return false
}

// Match finds the best matching route for a given hostname and path.
// Returns the matched route and true if found, or an empty Route and false otherwise.
func (t *RouteTable) Match(hostname, path string) (Route, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := Match(hostname, path, t.routes)
	return r, ok
}

// List returns a copy of all routes in the table.
func (t *RouteTable) List() []Route {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Route, len(t.routes))
	copy(out, t.routes)
	return out
}

// Count returns the number of routes in the table.
func (t *RouteTable) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.routes)
}
