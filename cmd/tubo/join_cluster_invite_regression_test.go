package main

import (
	"path/filepath"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
)

// TestJoinClusterInvitePreservesExistingClusters verifies Bug 1:
// joining a cluster (clusterB) via invite must NOT remove
// clusters already present in the config (clusterA), nor change the current context.
func TestJoinClusterInvitePreservesExistingClusters(t *testing.T) {
	// Setup: create clusterA and make it the current cluster
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterA", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["clusterA"]
	cluster.DiscoveryQueryPeers = []string{"/dns4/authority.example/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}
	cfg.Clusters["clusterA"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}

	// Verify the initial state
	cfgBefore, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgBefore.CurrentCluster != "clusterA" {
		t.Fatalf("expected current cluster clusterA, got %q", cfgBefore.CurrentCluster)
	}

	// Create clusterB in a separate config (it acts as clusterB's authority)
	configPathB := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterB", "--config", configPathB})
	}); err != nil {
		t.Fatal(err)
	}
	cfgB, err := cfgpkg.LoadFile(configPathB)
	if err != nil {
		t.Fatal(err)
	}
	clusterB := cfgB.Clusters["clusterB"]
	clusterB.DiscoveryQueryPeers = []string{"/dns4/authority.example/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}
	cfgB.Clusters["clusterB"] = clusterB
	if err := cfgpkg.WriteFile(configPathB, cfgB, true); err != nil {
		t.Fatal(err)
	}

	// Generate an invite token for clusterB
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterB", "--config", configPathB, "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)

	// Join clusterB using the config that already contains clusterA
	if _, err := capture(func() error {
		return run([]string{"join", "cluster/clusterB", "--token", token, "--config-dir", filepath.Dir(configPath)})
	}); err != nil {
		t.Fatalf("join clusterB failed: %v", err)
	}

	cfgAfter, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Bug 1: clusterA must still exist
	if _, ok := cfgAfter.Clusters["clusterA"]; !ok {
		t.Fatalf("clusterA was lost after joining clusterB: clusters=%v", cfgAfter.Clusters)
	}

	// Bug 1: clusterB must be added
	if _, ok := cfgAfter.Clusters["clusterB"]; !ok {
		t.Fatalf("clusterB not found after join: clusters=%v", cfgAfter.Clusters)
	}

	// Bug 3 (fix): the current context must NOT change to clusterB
	// because clusterA was already selected
	if cfgAfter.CurrentCluster != "clusterA" {
		t.Fatalf("current cluster changed from clusterA to %q after join (should not change when already set)", cfgAfter.CurrentCluster)
	}
}

// TestJoinClusterInvitePreservesExistingNamespaceServices verifies Bug 2:
// joining via invite must NOT wipe existing service entries
// in the namespace of the same cluster (for example after a re-join or grant refresh).
func TestJoinClusterInvitePreservesExistingNamespaceServices(t *testing.T) {
	// Setup: create clusterA with the "staging" namespace
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterA", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}
	cfgA, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	clusterA := cfgA.Clusters["clusterA"]
	clusterA.DiscoveryQueryPeers = []string{"/dns4/authority.example/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}
	cfgA.Clusters["clusterA"] = clusterA
	if err := cfgpkg.WriteFile(configPath, cfgA, true); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error {
		return run([]string{"create", "namespace/staging", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate a service that already exists in the namespace (create its identity)
	if _, err := capture(func() error {
		return run([]string{"create", "service/myapi", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}

	cfgBefore, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	stagingBefore, ok := cfgBefore.Clusters["clusterA"].Namespaces["staging"]
	if !ok {
		t.Fatal("namespace staging missing before join")
	}
	svcBefore, ok := stagingBefore.Services["myapi"]
	if !ok || svcBefore.ServiceID == "" {
		t.Fatalf("service myapi missing or incomplete before join: %#v", stagingBefore.Services)
	}

	// Generate an invite for clusterA/staging (simulates a re-join or a new member)
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterA", "--config", configPath, "--namespace", "staging", "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)

	// Re-join clusterA/staging on the same config (for example after a grant refresh)
	if _, err := capture(func() error {
		return run([]string{"join", "cluster/clusterA", "--token", token, "--config-dir", filepath.Dir(configPath)})
	}); err != nil {
		t.Fatalf("re-join clusterA/staging failed: %v", err)
	}

	cfgAfter, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Bug 2: the myapi service must still be present
	stagingAfter, ok := cfgAfter.Clusters["clusterA"].Namespaces["staging"]
	if !ok {
		t.Fatalf("namespace staging disappeared after re-join: %#v", cfgAfter.Clusters["clusterA"].Namespaces)
	}
	svcAfter, ok := stagingAfter.Services["myapi"]
	if !ok || svcAfter.ServiceID == "" {
		t.Fatalf("service myapi was lost after re-join clusterA/staging: %#v", stagingAfter.Services)
	}
	if svcAfter.ServiceID != svcBefore.ServiceID {
		t.Fatalf("service id changed after re-join: before=%q after=%q", svcBefore.ServiceID, svcAfter.ServiceID)
	}
}

// TestJoinClusterInviteSetsContextWhenEmpty verifies that the context is
// set to the new cluster ONLY when there is no current context yet.
func TestJoinClusterInviteSetsContextWhenEmpty(t *testing.T) {
	// Setup: create clusterB authority in a separate config
	configPathAuth := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterB", "--config", configPathAuth})
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPathAuth)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["clusterB"]
	cluster.DiscoveryQueryPeers = []string{"/dns4/authority.example/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}
	cfg.Clusters["clusterB"] = cluster
	if err := cfgpkg.WriteFile(configPathAuth, cfg, true); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterB", "--config", configPathAuth, "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)

	// Join on an EMPTY config (no current cluster)
	joinDir := t.TempDir()
	if _, err := capture(func() error {
		return run([]string{"join", "cluster/clusterB", "--token", token, "--config-dir", joinDir})
	}); err != nil {
		t.Fatalf("join clusterB on empty config failed: %v", err)
	}

	cfgAfter, err := cfgpkg.LoadFile(filepath.Join(joinDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	// On an empty config, the context MUST be set to the joined cluster
	if cfgAfter.CurrentCluster != "clusterB" {
		t.Fatalf("expected CurrentCluster=clusterB on fresh config, got %q", cfgAfter.CurrentCluster)
	}
}

// TestJoinClusterInviteSwitchesNamespaceWhenClusterAlreadySelected verifies
// that re-joining the same cluster follows the invited namespace.
func TestJoinClusterInviteSwitchesNamespaceWhenClusterAlreadySelected(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterA", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["clusterA"]
	cluster.DiscoveryQueryPeers = []string{"/dns4/authority.example/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}
	cfg.Clusters["clusterA"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error {
		return run([]string{"create", "namespace/collab", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterA", "--config", configPath, "--namespace", "collab", "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)
	if _, err := capture(func() error {
		return run([]string{"join", "cluster/clusterA", "--token", token, "--config-dir", filepath.Dir(configPath), "--force"})
	}); err != nil {
		t.Fatalf("re-join clusterA/collab failed: %v", err)
	}
	cfgAfter, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgAfter.CurrentCluster != "clusterA" || cfgAfter.CurrentNamespace != "collab" {
		t.Fatalf("expected current scope clusterA/collab, got %q/%q", cfgAfter.CurrentCluster, cfgAfter.CurrentNamespace)
	}
}
