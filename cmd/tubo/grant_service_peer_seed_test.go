package main

import (
	"bytes"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

// baseGrantServicePeerConfig builds a non-authority cluster config where the
// service has no GrantServicePeer set, so seeding is applicable.
func baseGrantServicePeerConfig(t *testing.T) (string, cfgpkg.Config) {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := cfgpkg.Config{
		CurrentCluster:   "solar",
		CurrentNamespace: "default",
		Service:          cfgpkg.Service{Name: "myapi", Target: "http://127.0.0.1:8080"},
		Clusters: map[string]cfgpkg.Cluster{"solar": {
			ClusterID:           "cluster-solar",
			AuthorityPublicKey:  "authority-public-key",
			DiscoveryQueryPeers: []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWdemo"},
			MembershipGrant:     &cfgpkg.ClusterMembershipGrant{ClusterID: "cluster-solar"},
			Namespaces: map[string]cfgpkg.Namespace{"default": {
				Services: map[string]cfgpkg.NamespaceService{"myapi": {
					ServiceID: "service-aaaaaaaaaaaaaaaa",
					Kind:      cfgpkg.ServiceKindHTTP,
					Target:    "http://127.0.0.1:8080",
				}},
			}},
		}},
	}
	if err := cfgpkg.WriteFile(configPath, cfg, false); err != nil {
		t.Fatal(err)
	}
	return configPath, cfg
}

func withInjectedPeer(peer string) func() {
	prev := discoverGrantServicePeerFn
	discoverGrantServicePeerFn = func(string, cfgpkg.Config) string { return peer }
	return func() { discoverGrantServicePeerFn = prev }
}

func TestApplyDiscoveredGrantServicePeerSetsFieldsWhenMissing(t *testing.T) {
	_, cfg := baseGrantServicePeerConfig(t)
	peer := "/dnsaddr/grant.example/tcp/4001/p2p/12D3KooWpeer"
	if err := applyDiscoveredGrantServicePeer(&cfg, "solar", "default", "myapi", peer); err != nil {
		t.Fatal(err)
	}
	svc := cfg.Clusters["solar"].Namespaces["default"].Services["myapi"]
	if svc.GrantServicePeer != peer {
		t.Fatalf("GrantServicePeer = %q want %q", svc.GrantServicePeer, peer)
	}
}

func TestApplyDiscoveredGrantServicePeerDoesNotOverwriteExistingPeer(t *testing.T) {
	_, cfg := baseGrantServicePeerConfig(t)
	existing := "/dnsaddr/other.example/tcp/4001/p2p/12D3KooWexisting"
	cluster := cfg.Clusters["solar"]
	ns := cluster.Namespaces["default"]
	svc := ns.Services["myapi"]
	svc.GrantServicePeer = existing
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["solar"] = cluster

	if err := applyDiscoveredGrantServicePeer(&cfg, "solar", "default", "myapi", "/dnsaddr/new/tcp/4001/p2p/12D3KooWnew"); err != nil {
		t.Fatal(err)
	}
	got := cfg.Clusters["solar"].Namespaces["default"].Services["myapi"].GrantServicePeer
	if got != existing {
		t.Fatalf("GrantServicePeer overwritten: got %q want %q", got, existing)
	}
}

func TestApplyDiscoveredGrantServicePeerFillsMembershipGrant(t *testing.T) {
	_, cfg := baseGrantServicePeerConfig(t)
	cluster := cfg.Clusters["solar"]
	cluster.MembershipGrant = &cfgpkg.ClusterMembershipGrant{ClusterID: "cluster-solar"}
	cfg.Clusters["solar"] = cluster
	peer := "/dnsaddr/grant.example/tcp/4001/p2p/12D3KooWpeer"
	if err := applyDiscoveredGrantServicePeer(&cfg, "solar", "default", "myapi", peer); err != nil {
		t.Fatal(err)
	}
	grant := cfg.Clusters["solar"].MembershipGrant
	if grant.GrantServiceProtocol != grantspkg.ProtocolID {
		t.Fatalf("GrantServiceProtocol = %q want %q", grant.GrantServiceProtocol, grantspkg.ProtocolID)
	}
	if len(grant.GrantServicePeers) != 1 || grant.GrantServicePeers[0] != peer {
		t.Fatalf("GrantServicePeers = %#v want [%q]", grant.GrantServicePeers, peer)
	}
}

func TestApplyDiscoveredGrantServicePeerNoGrantStructIsFine(t *testing.T) {
	_, cfg := baseGrantServicePeerConfig(t)
	// Drop the membership grant; function must not create one and must not error.
	cluster := cfg.Clusters["solar"]
	cluster.MembershipGrant = nil
	cfg.Clusters["solar"] = cluster
	if err := applyDiscoveredGrantServicePeer(&cfg, "solar", "default", "myapi", "/dnsaddr/g/tcp/4001/p2p/12D3KooWp"); err != nil {
		t.Fatal(err)
	}
	if cfg.Clusters["solar"].MembershipGrant != nil {
		t.Fatalf("MembershipGrant unexpectedly created: %#v", cfg.Clusters["solar"].MembershipGrant)
	}
	if cfg.Clusters["solar"].Namespaces["default"].Services["myapi"].GrantServicePeer == "" {
		t.Fatal("GrantServicePeer not set on service")
	}
}

func TestApplyDiscoveredGrantServicePeerMissingCluster(t *testing.T) {
	_, cfg := baseGrantServicePeerConfig(t)
	if err := applyDiscoveredGrantServicePeer(&cfg, "ghost", "default", "myapi", "peer"); err == nil ||
		!strings.Contains(err.Error(), "cluster") {
		t.Fatalf("expected cluster error, got %v", err)
	}
}

func TestApplyDiscoveredGrantServicePeerMissingNamespace(t *testing.T) {
	_, cfg := baseGrantServicePeerConfig(t)
	if err := applyDiscoveredGrantServicePeer(&cfg, "solar", "ghost", "myapi", "peer"); err == nil ||
		!strings.Contains(err.Error(), "namespace") {
		t.Fatalf("expected namespace error, got %v", err)
	}
}

func TestApplyDiscoveredGrantServicePeerMissingService(t *testing.T) {
	_, cfg := baseGrantServicePeerConfig(t)
	if err := applyDiscoveredGrantServicePeer(&cfg, "solar", "default", "ghost", "peer"); err == nil ||
		!strings.Contains(err.Error(), "service") {
		t.Fatalf("expected service error, got %v", err)
	}
}

func TestSeedDiscoveredGrantServicePeerPersistsNormally(t *testing.T) {
	configPath, cfg := baseGrantServicePeerConfig(t)
	defer withInjectedPeer("/dnsaddr/g/tcp/4001/p2p/12D3KooWpersisted")()

	updated, err := seedDiscoveredGrantServicePeer(configPath, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Clusters["solar"].Namespaces["default"].Services["myapi"].GrantServicePeer != "/dnsaddr/g/tcp/4001/p2p/12D3KooWpersisted" {
		t.Fatalf("runtime cfg not updated: %#v", updated.Clusters["solar"].Namespaces["default"].Services["myapi"])
	}
	persisted, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	got := persisted.Clusters["solar"].Namespaces["default"].Services["myapi"].GrantServicePeer
	if got != "/dnsaddr/g/tcp/4001/p2p/12D3KooWpersisted" {
		t.Fatalf("peer not persisted on disk: %q", got)
	}
	grant := persisted.Clusters["solar"].MembershipGrant
	if grant == nil || grant.GrantServiceProtocol != grantspkg.ProtocolID || len(grant.GrantServicePeers) != 1 {
		t.Fatalf("membership grant not persisted: %#v", grant)
	}
}

func TestSeedDiscoveredGrantServicePeerSkipsWhenPeerEmpty(t *testing.T) {
	configPath, cfg := baseGrantServicePeerConfig(t)
	defer withInjectedPeer("")()
	before, _ := os.ReadFile(configPath)
	updated, err := seedDiscoveredGrantServicePeer(configPath, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if updated.Clusters["solar"].Namespaces["default"].Services["myapi"].GrantServicePeer != "" {
		t.Fatal("empty discovery must not set a peer")
	}
	after, _ := os.ReadFile(configPath)
	if string(after) != string(before) {
		t.Fatal("empty discovery must not modify config")
	}
}

func TestSeedDiscoveredGrantServicePeerEROFSFallbackInjected(t *testing.T) {
	configPath, cfg := baseGrantServicePeerConfig(t)
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	peer := "/dnsaddr/g/tcp/4001/p2p/12D3KooWerofs"
	defer withInjectedPeer(peer)()

	// Force the persistent path to fail with a genuine EROFS by making the
	// config directory read-only at the OS level (chmod 0o500): the repository
	// cannot create the .lock sidecar, producing a real syscall error. On Linux
	// this is EROFS only on a read-only *filesystem*; on a plain non-writable
	// directory it is EACCES. To keep this test deterministic across platforms
	// we wrap updateLocalConfig to return a wrapped syscall.EROFS, which is the
	// exact condition the fallback must handle.
	prevUpdate := updateLocalConfigFn
	updateLocalConfigFn = func(path string, mutate cfgpkg.ConfigMutation) (cfgpkg.Config, error) {
		_ = path
		_ = mutate
		return cfgpkg.Config{}, &os.PathError{Op: "open", Path: configPath + ".lock", Err: syscall.EROFS}
	}
	defer func() { updateLocalConfigFn = prevUpdate }()

	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(io.MultiWriter(prevOut, &buf))
	defer log.SetOutput(prevOut)

	updated, err := seedDiscoveredGrantServicePeer(configPath, cfg)
	if err != nil {
		t.Fatalf("EROFS must be tolerated: %v", err)
	}
	// Runtime config must actually carry the discovered peer.
	if updated.Clusters["solar"].Namespaces["default"].Services["myapi"].GrantServicePeer != peer {
		t.Fatalf("EROFS fallback did not apply peer to runtime config: %#v", updated.Clusters["solar"].Namespaces["default"].Services["myapi"])
	}
	// Membership grant must also be filled in memory.
	grant := updated.Clusters["solar"].MembershipGrant
	if grant == nil || grant.GrantServiceProtocol != grantspkg.ProtocolID || len(grant.GrantServicePeers) != 1 || grant.GrantServicePeers[0] != peer {
		t.Fatalf("EROFS fallback did not fill membership grant: %#v", grant)
	}
	// On-disk file must be unchanged.
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("EROFS fallback modified on-disk config\nbefore:\n%s\nafter:\n%s", before, after)
	}
	warning := buf.String()
	if !strings.Contains(warning, "warning: config volume is read-only") || !strings.Contains(warning, "myapi") {
		t.Fatalf("expected observable read-only warning mentioning myapi, got: %q", warning)
	}
}

func TestSeedDiscoveredGrantServicePeerEACCEPropagated(t *testing.T) {
	configPath, cfg := baseGrantServicePeerConfig(t)
	peer := "/dnsaddr/g/tcp/4001/p2p/12D3KooWeacces"
	defer withInjectedPeer(peer)()

	// Inject a non-EROFS permission error (EACCES). It must propagate, NOT be
	// hidden behind an in-memory-only success.
	prevUpdate := updateLocalConfigFn
	updateLocalConfigFn = func(path string, mutate cfgpkg.ConfigMutation) (cfgpkg.Config, error) {
		_ = path
		_ = mutate
		return cfgpkg.Config{}, &os.PathError{Op: "open", Path: configPath + ".lock", Err: syscall.EACCES}
	}
	defer func() { updateLocalConfigFn = prevUpdate }()

	_, err := seedDiscoveredGrantServicePeer(configPath, cfg)
	if err == nil {
		t.Fatal("EACCES must be propagated, not hidden")
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("expected EACCES error, got %v", err)
	}
}

func TestSeedDiscoveredGrantServicePeerAlreadySetIsNoop(t *testing.T) {
	configPath, cfg := baseGrantServicePeerConfig(t)
	existing := "/dnsaddr/g/tcp/4001/p2p/12D3KooWexisting"
	cluster := cfg.Clusters["solar"]
	ns := cluster.Namespaces["default"]
	svc := ns.Services["myapi"]
	svc.GrantServicePeer = existing
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["solar"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}

	called := false
	defer withInjectedPeer("/dnsaddr/g/tcp/4001/p2p/12D3KooWnew")()
	prevUpdate := updateLocalConfigFn
	updateLocalConfigFn = func(path string, mutate cfgpkg.ConfigMutation) (cfgpkg.Config, error) {
		called = true
		return cfgpkg.NewConfigRepository(path).Update(nil, mutate)
	}
	defer func() { updateLocalConfigFn = prevUpdate }()

	updated, err := seedDiscoveredGrantServicePeer(configPath, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if called {
		t.Fatal("persistence must not run when peer is already set")
	}
	if updated.Clusters["solar"].Namespaces["default"].Services["myapi"].GrantServicePeer != existing {
		t.Fatal("existing peer must not be overwritten")
	}
}
