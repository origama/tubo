package routing

import (
	"fmt"
	"testing"
	"time"
)

// TestAddRoute verifies that adding a single route makes it appear in List().
func TestAddRoute(t *testing.T) {
	tt := NewRouteTable()

	route := Route{
		Hostname:    "example.com",
		PathPrefix:  "/api",
		ServiceName: "users-svc",
		PeerID:      "peer-123",
		TenantID:    "tenant-a",
	}

	if err := tt.Add(route); err != nil {
		t.Fatalf("Add() returned unexpected error: %v", err)
	}

	list := tt.List()
	if len(list) != 1 {
		t.Fatalf("List() returned %d routes, want 1", len(list))
	}

	got := list[0]
	if got.Hostname != route.Hostname || got.PathPrefix != route.PathPrefix ||
		got.ServiceName != route.ServiceName || got.PeerID != route.PeerID ||
		got.TenantID != route.TenantID {
		t.Errorf("List()[0] = %+v, want %+v", got, route)
	}

	if tt.Count() != 1 {
		t.Errorf("Count() = %d, want 1", tt.Count())
	}
}

// TestAddDuplicateRoute verifies that adding a route with the same Hostname+PathPrefix
// replaces the existing entry (actual behavior: Add returns nil on duplicate).
func TestAddDuplicateRoute(t *testing.T) {
	tt := NewRouteTable()

	route1 := Route{
		Hostname:    "example.com",
		PathPrefix:  "/api",
		ServiceName: "users-svc-v1",
		PeerID:      "peer-old",
		TenantID:    "tenant-a",
	}
	if err := tt.Add(route1); err != nil {
		t.Fatalf("Add(first) returned unexpected error: %v", err)
	}

	route2 := Route{
		Hostname:    "example.com",
		PathPrefix:  "/api",
		ServiceName: "users-svc-v2",
		PeerID:      "peer-new",
		TenantID:    "tenant-a",
	}
	if err := tt.Add(route2); err != nil {
		t.Fatalf("Add(duplicate) returned unexpected error: %v", err)
	}

	// Duplicate should be replaced, not cause an error. Count stays at 1.
	if tt.Count() != 1 {
		t.Errorf("Count() = %d after duplicate add, want 1 (should replace)", tt.Count())
	}

	list := tt.List()
	got := list[0]
	if got.ServiceName != "users-svc-v2" || got.PeerID != "peer-new" {
		t.Errorf("After duplicate Add(), route was not updated: %+v", got)
	}
}

// TestAddInvalidRoute verifies that routes with empty Hostname or ServiceName are rejected.
func TestAddInvalidRoute(t *testing.T) {
	tests := []struct {
		name  string
		route Route
	}{
		{
			name: "empty hostname",
			route: Route{
				Hostname:    "",
				PathPrefix:  "/api",
				ServiceName: "users-svc",
				PeerID:      "peer-123",
			},
		},
		{
			name: "empty service name",
			route: Route{
				Hostname:    "example.com",
				PathPrefix:  "/api",
				ServiceName: "",
				PeerID:      "peer-123",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewRouteTable()
			if err := tt.Add(tc.route); err == nil {
				t.Error("Add() should return error for invalid route")
			}
		})
	}
}

// TestRemoveExisting verifies that removing an existing route returns true and the
// route is no longer in List().
func TestRemoveExisting(t *testing.T) {
	tt := NewRouteTable()

	route1 := Route{Hostname: "example.com", PathPrefix: "/api", ServiceName: "svc-1"}
	route2 := Route{Hostname: "example.com", PathPrefix: "/health", ServiceName: "svc-2"}
	if err := tt.Add(route1); err != nil {
		t.Fatalf("Add(route1) error: %v", err)
	}
	if err := tt.Add(route2); err != nil {
		t.Fatalf("Add(route2) error: %v", err)
	}

	if !tt.Remove("example.com", "/api") {
		t.Error("Remove() returned false for existing route")
	}

	if tt.Count() != 1 {
		t.Errorf("Count() = %d after removal, want 1", tt.Count())
	}

	list := tt.List()
	for _, r := range list {
		if r.PathPrefix == "/api" {
			t.Error("/api route still present in List() after Remove()")
		}
	}
}

// TestRemoveNonExistent verifies that removing a non-existent key returns false.
func TestRemoveNonExistent(t *testing.T) {
	tt := NewRouteTable()

	route := Route{Hostname: "example.com", PathPrefix: "/api", ServiceName: "svc-1"}
	if err := tt.Add(route); err != nil {
		t.Fatalf("Add() error: %v", err)
	}

	tests := []struct {
		name       string
		hostname   string
		pathPrefix string
	}{
		{"wrong hostname", "other.com", "/api"},
		{"wrong path", "example.com", "/health"},
		{"both wrong", "other.com", "/health"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tt.Remove(tc.hostname, tc.pathPrefix) {
				t.Error("Remove() returned true for non-existent route")
			}
		})
	}

	// Original route should still be there.
	if tt.Count() != 1 {
		t.Errorf("Count() = %d after failed removals, want 1", tt.Count())
	}
}

// TestMatchExact verifies that a path starting with the registered prefix matches correctly.
func TestMatchExact(t *testing.T) {
	tests := []struct {
		name       string
		route      Route
		queryPath  string
		wantMatch  bool
		wantFields Route
	}{
		{
			name:       "root prefix matches subpath",
			route:      Route{Hostname: "example.com", PathPrefix: "/", ServiceName: "catchall"},
			queryPath:  "/api/users",
			wantMatch:  true,
			wantFields: Route{Hostname: "example.com", PathPrefix: "/", ServiceName: "catchall"},
		},
		{
			name:       "exact prefix match",
			route:      Route{Hostname: "example.com", PathPrefix: "/api", ServiceName: "users-svc"},
			queryPath:  "/api/users/123",
			wantMatch:  true,
			wantFields: Route{Hostname: "example.com", PathPrefix: "/api", ServiceName: "users-svc"},
		},
		{
			name:       "prefix equals path exactly",
			route:      Route{Hostname: "example.com", PathPrefix: "/health", ServiceName: "healthcheck"},
			queryPath:  "/health",
			wantMatch:  true,
			wantFields: Route{Hostname: "example.com", PathPrefix: "/health", ServiceName: "healthcheck"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewRouteTable()
			if err := tt.Add(tc.route); err != nil {
				t.Fatalf("Add() error: %v", err)
			}

			got, ok := tt.Match(tc.route.Hostname, tc.queryPath)
			if ok != tc.wantMatch {
				t.Errorf("Match() ok = %v, want %v", ok, tc.wantMatch)
			}
			if ok && got.ServiceName != tc.wantFields.ServiceName {
				t.Errorf("Match() route = %+v, want %+v", got, tc.wantFields)
			}
		})
	}
}

// TestMatchLongestPrefix verifies that when multiple prefixes match a path, the longest
// prefix wins. NOTE: Current implementation returns first match found (not longest).
// This test documents actual behavior and will fail until Match is fixed to select longest.
func TestMatchLongestPrefix(t *testing.T) {
	tt := NewRouteTable()

	// Add routes in order: shorter prefix first, then longer prefix.
	if err := tt.Add(Route{Hostname: "example.com", PathPrefix: "/", ServiceName: "catchall"}); err != nil {
		t.Fatalf("Add(catchall) error: %v", err)
	}
	if err := tt.Add(Route{Hostname: "example.com", PathPrefix: "/api", ServiceName: "users-svc"}); err != nil {
		t.Fatalf("Add(users-svc) error: %v", err)
	}

	got, ok := tt.Match("example.com", "/api/users")
	if !ok {
		t.Fatal("Match() returned no match for /api/users")
	}

	// The longest prefix (/api) should win over the shorter one (/).
	wantServiceName := "users-svc"
	if got.ServiceName != wantServiceName {
		t.Errorf("MatchLongestPrefix: got ServiceName=%q, want %q (longest prefix /api should win over /)",
			got.ServiceName, wantServiceName)
	}

	// Also test reverse insertion order: longer prefix first.
	tt2 := NewRouteTable()
	if err := tt2.Add(Route{Hostname: "example.com", PathPrefix: "/api", ServiceName: "users-svc"}); err != nil {
		t.Fatalf("Add(users-svc) error: %v", err)
	}
	if err := tt2.Add(Route{Hostname: "example.com", PathPrefix: "/", ServiceName: "catchall"}); err != nil {
		t.Fatalf("Add(catchall) error: %v", err)
	}

	got2, ok2 := tt2.Match("example.com", "/api/users")
	if !ok2 {
		t.Fatal("Match() returned no match for /api/users (reverse order)")
	}

	if got2.ServiceName != wantServiceName {
		t.Errorf("MatchLongestPrefix (reverse insert): got ServiceName=%q, want %q",
			got2.ServiceName, wantServiceName)
	}
}

// TestMatchNoMatch verifies that when no route matches the hostname+path combination,
// Match returns an empty Route and false.
func TestMatchNoMatch(t *testing.T) {
	tests := []struct {
		name      string
		route     Route
		queryHost string
		queryPath string
	}{
		{
			name:      "wrong hostname",
			route:     Route{Hostname: "example.com", PathPrefix: "/api"},
			queryHost: "other.com", queryPath: "/api/users",
		},
		{
			name:      "path doesn't start with prefix",
			route:     Route{Hostname: "example.com", PathPrefix: "/api"},
			queryHost: "example.com", queryPath: "/health",
		},
		{
			name:      "empty table",
			route:     Route{},
			queryHost: "example.com", queryPath: "/anything",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tt := NewRouteTable()
			if tc.route.Hostname != "" {
				tc.route.ServiceName = "svc"
				if err := tt.Add(tc.route); err != nil {
					t.Fatalf("Add() error: %v", err)
				}
			}

			got, ok := tt.Match(tc.queryHost, tc.queryPath)
			if ok {
				t.Errorf("Match() returned true for non-matching query (host=%q path=%q)", tc.queryHost, tc.queryPath)
			}
			if got.Hostname != "" || got.PathPrefix != "" || got.ServiceName != "" {
				t.Errorf("Match() returned %+v, want empty Route", got)
			}
		})
	}
}

// TestMatchHostnameFilter verifies that routes registered for one hostname do not
// match queries for a different hostname.
func TestMatchHostnameFilter(t *testing.T) {
	tt := NewRouteTable()

	if err := tt.Add(Route{Hostname: "app.example.com", PathPrefix: "/api", ServiceName: "app-svc"}); err != nil {
		t.Fatalf("Add(app) error: %v", err)
	}
	if err := tt.Add(Route{Hostname: "admin.example.com", PathPrefix: "/api", ServiceName: "admin-svc"}); err != nil {
		t.Fatalf("Add(admin) error: %v", err)
	}

	// Query for app hostname should match app route only.
	got, ok := tt.Match("app.example.com", "/api/users")
	if !ok {
		t.Fatal("Match(app.example.com, /api/users) returned no match")
	}
	if got.ServiceName != "app-svc" {
		t.Errorf("Expected app-svc, got %s", got.ServiceName)
	}

	// Query for admin hostname should match admin route only.
	got2, ok2 := tt.Match("admin.example.com", "/api/users")
	if !ok2 {
		t.Fatal("Match(admin.example.com, /api/users) returned no match")
	}
	if got2.ServiceName != "admin-svc" {
		t.Errorf("Expected admin-svc, got %s", got2.ServiceName)
	}

	// Query for unknown hostname should not match anything.
	_, ok3 := tt.Match("unknown.example.com", "/api/users")
	if ok3 {
		t.Error("Match(unknown.example.com, /api/users) unexpectedly matched a route")
	}
}

// TestListReturnsCopy verifies that modifying the slice returned by List() does not
// affect the internal state of the RouteTable.
func TestListReturnsCopy(t *testing.T) {
	tt := NewRouteTable()

	route1 := Route{Hostname: "example.com", PathPrefix: "/api", ServiceName: "svc-1"}
	route2 := Route{Hostname: "example.com", PathPrefix: "/health", ServiceName: "svc-2"}
	if err := tt.Add(route1); err != nil {
		t.Fatalf("Add() error: %v", err)
	}
	if err := tt.Add(route2); err != nil {
		t.Fatalf("Add() error: %v", err)
	}

	list1 := tt.List()
	if len(list1) != 2 {
		t.Fatalf("List() returned %d routes, want 2", len(list1))
	}

	// Mutate the returned list.
	list1[0].ServiceName = "MUTATED"
	list1[1] = Route{Hostname: "GARBAGE"}

	// Get a fresh copy and verify internal state is unchanged.
	list2 := tt.List()
	if len(list2) != 2 {
		t.Fatalf("After mutation, List() returned %d routes, want 2", len(list2))
	}
	if list2[0].ServiceName != "svc-1" {
		t.Errorf("Internal state was affected by external mutation: got ServiceName=%q, want %q",
			list2[0].ServiceName, "svc-1")
	}
	if list2[1].Hostname != "example.com" || list2[1].ServiceName != "svc-2" {
		t.Errorf("Internal state was affected by external mutation: %+v", list2[1])
	}
}

// TestMatchPackageLevel tests the standalone Match function directly.
func TestMatchPackageLevel(t *testing.T) {
	tests := []struct {
		name       string
		routes     []Route
		hostname   string
		path       string
		wantOk     bool
		wantPrefix string
	}{
		{
			name:     "single route exact match",
			routes:   []Route{{Hostname: "x.com", PathPrefix: "/api"}},
			hostname: "x.com", path: "/api/v1", wantOk: true, wantPrefix: "/api",
		},
		{
			name:     "root prefix matches everything",
			routes:   []Route{{Hostname: "x.com", PathPrefix: "/"}},
			hostname: "x.com", path: "/anything/deep/here", wantOk: true, wantPrefix: "/",
		},
		{
			name:     "no matching hostname",
			routes:   []Route{{Hostname: "a.com", PathPrefix: "/api"}},
			hostname: "b.com", path: "/api", wantOk: false,
		},
		{
			name:     "path shorter than prefix",
			routes:   []Route{{Hostname: "x.com", PathPrefix: "/api/v1/users"}},
			hostname: "x.com", path: "/api", wantOk: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Match(tc.hostname, tc.path, tc.routes)
			if ok != tc.wantOk {
				t.Errorf("Match() ok = %v, want %v", ok, tc.wantOk)
			}
			if ok && got.PathPrefix != tc.wantPrefix {
				t.Errorf("Match() PathPrefix = %q, want %q", got.PathPrefix, tc.wantPrefix)
			}
		})
	}
}

// TestMultipleAddsAndRemoves exercises the table with a sequence of adds and removes.
func TestMultipleAddsAndRemoves(t *testing.T) {
	tt := NewRouteTable()

	routes := []Route{
		{Hostname: "a.com", PathPrefix: "/v1", ServiceName: "svc-a"},
		{Hostname: "b.com", PathPrefix: "/v2", ServiceName: "svc-b"},
		{Hostname: "a.com", PathPrefix: "/v3", ServiceName: "svc-c"},
	}

	for _, r := range routes {
		if err := tt.Add(r); err != nil {
			t.Fatalf("Add(%+v) error: %v", r, err)
		}
	}

	if tt.Count() != 3 {
		t.Errorf("Count() = %d after 3 adds, want 3", tt.Count())
	}

	// Remove middle route.
	if !tt.Remove("b.com", "/v2") {
		t.Error("Remove(b.com, /v2) returned false")
	}
	if tt.Count() != 2 {
		t.Errorf("Count() = %d after removal, want 2", tt.Count())
	}

	// Remaining routes should be a.com/v1 and a.com/v3.
	list := tt.List()
	for _, r := range list {
		if r.Hostname == "b.com" {
			t.Error("b.com route still present after removal")
		}
	}

	// Remove non-existent should not change count.
	if tt.Remove("c.com", "/v4") {
		t.Error("Remove(c.com, /v4) returned true for non-existent route")
	}
	if tt.Count() != 2 {
		t.Errorf("Count() = %d after failed removal, want 2", tt.Count())
	}
}

// TestMatchPathPrefixBoundary tests edge cases around path prefix matching.
func TestMatchPathPrefixBoundary(t *testing.T) {
	tests := []struct {
		name      string
		route     Route
		queryPath string
		wantOk    bool
	}{
		{
			name:      "prefix with trailing slash matches subpath",
			route:     Route{Hostname: "x.com", PathPrefix: "/api/", ServiceName: "svc"},
			queryPath: "/api/users", wantOk: true,
		},
		{
			name:      "prefix without trailing slash matches prefix+subpath",
			route:     Route{Hostname: "x.com", PathPrefix: "/api", ServiceName: "svc"},
			queryPath: "/api/users", wantOk: true,
		},
		{
			name:      "prefix without trailing slash matches exact path",
			route:     Route{Hostname: "x.com", PathPrefix: "/api", ServiceName: "svc"},
			queryPath: "/api", wantOk: true,
		},
		{
			name:      "path is substring of prefix (too short)",
			route:     Route{Hostname: "x.com", PathPrefix: "/api/v1/users", ServiceName: "svc"},
			queryPath: "/api", wantOk: false,
		},
		{
			name:      "prefix shares start but diverges",
			route:     Route{Hostname: "x.com", PathPrefix: "/api", ServiceName: "svc"},
			queryPath: "/application", wantOk: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Match(tc.route.Hostname, tc.queryPath, []Route{tc.route})
			if ok != tc.wantOk {
				t.Errorf("Match(%q, %q) ok = %v, want %v", tc.route.PathPrefix, tc.queryPath, ok, tc.wantOk)
			}
			if ok && got.ServiceName != tc.route.ServiceName {
				t.Errorf("Match() returned wrong route: %+v", got)
			}
		})
	}
}

// TestConcurrentAccess exercises the table under concurrent read/write to verify mutex safety.
func TestConcurrentAccess(t *testing.T) {
	tt := NewRouteTable()
	const goroutines = 10
	const opsPerGoroutine = 50

	done := make(chan bool, goroutines*2)

	// Writers: add routes concurrently.
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()
			for j := 0; j < opsPerGoroutine; j++ {
				route := Route{
					Hostname:    fmt.Sprintf("host-%d.com", id),
					PathPrefix:  fmt.Sprintf("/path-%d-%d", id, j),
					ServiceName: fmt.Sprintf("svc-%d-%d", id, j),
				}
				if err := tt.Add(route); err != nil {
					t.Errorf("Add(%+v) err = %v", route, err)
					return
				}
			}
		}(i)
	}

	// Readers: list and match concurrently.
	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer func() { done <- true }()
			for j := 0; j < opsPerGoroutine; j++ {
				tt.List()
				tt.Count()
				tt.Match(fmt.Sprintf("host-%d.com", id), "/anything")
				tt.Remove("nonexistent.com", "/nope")
			}
		}(i)
	}

	// Wait for all goroutines to finish.
	for i := 0; i < goroutines*2; i++ {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("Concurrent access test timed out (possible deadlock)")
		}
	}

	// Final count should be reasonable.
	count := tt.Count()
	if count < 1 || count > goroutines*opsPerGoroutine {
		t.Errorf("Final Count() = %d, expected between 1 and %d", count, goroutines*opsPerGoroutine)
	}
}
