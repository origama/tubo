package main

import (
	"path/filepath"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
)

// TestJoinClusterInvitePreservesExistingClusters verifica il Bug 1:
// il join tramite invite a un cluster (clusterB) NON deve rimuovere
// cluster già presenti nel config (clusterA) né cambiare il contesto corrente.
func TestJoinClusterInvitePreservesExistingClusters(t *testing.T) {
	// Setup: crea clusterA ed è il cluster corrente
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterA", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}

	// Verifica stato iniziale
	cfgBefore, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfgBefore.CurrentCluster != "clusterA" {
		t.Fatalf("expected current cluster clusterA, got %q", cfgBefore.CurrentCluster)
	}

	// Crea clusterB con un config separato (fa da authority di clusterB)
	configPathB := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterB", "--config", configPathB})
	}); err != nil {
		t.Fatal(err)
	}

	// Genera un invite token per clusterB
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterB", "--config", configPathB, "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)

	// Join a clusterB usando il config che ha già clusterA
	if _, err := capture(func() error {
		return run([]string{"join", "cluster/clusterB", "--token", token, "--config-dir", filepath.Dir(configPath)})
	}); err != nil {
		t.Fatalf("join clusterB failed: %v", err)
	}

	cfgAfter, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Bug 1: clusterA deve ancora esistere
	if _, ok := cfgAfter.Clusters["clusterA"]; !ok {
		t.Fatalf("clusterA was lost after joining clusterB: clusters=%v", cfgAfter.Clusters)
	}

	// Bug 1: clusterB deve essere aggiunto
	if _, ok := cfgAfter.Clusters["clusterB"]; !ok {
		t.Fatalf("clusterB not found after join: clusters=%v", cfgAfter.Clusters)
	}

	// Bug 3 (fix): il contesto corrente NON deve cambiare a clusterB
	// perché clusterA era già selezionato
	if cfgAfter.CurrentCluster != "clusterA" {
		t.Fatalf("current cluster changed from clusterA to %q after join (should not change when already set)", cfgAfter.CurrentCluster)
	}
}

// TestJoinClusterInvitePreservesExistingNamespaceServices verifica il Bug 2:
// il join tramite invite NON deve azzerare le entry di servizio già presenti
// nel namespace dello stesso cluster (es. dopo un re-join o un refresh del grant).
func TestJoinClusterInvitePreservesExistingNamespaceServices(t *testing.T) {
	// Setup: crea clusterA con namespace "staging"
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterA", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error {
		return run([]string{"create", "namespace/staging", "--config", configPath})
	}); err != nil {
		t.Fatal(err)
	}
	// Simula un servizio già presente nel namespace (crea la sua identità)
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

	// Genera invite per clusterA/staging (simula re-join o nuovo membro)
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterA", "--config", configPath, "--namespace", "staging", "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)

	// Re-join clusterA/staging sullo stesso config (es. refresh del grant)
	if _, err := capture(func() error {
		return run([]string{"join", "cluster/clusterA", "--token", token, "--config-dir", filepath.Dir(configPath)})
	}); err != nil {
		t.Fatalf("re-join clusterA/staging failed: %v", err)
	}

	cfgAfter, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// Bug 2: il servizio myapi deve essere ancora presente
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

// TestJoinClusterInviteSetsContextWhenEmpty verifica che il contesto venga
// impostato al nuovo cluster SOLO quando non c'è ancora un contesto corrente.
func TestJoinClusterInviteSetsContextWhenEmpty(t *testing.T) {
	// Setup: crea authority di clusterB in un config separato
	configPathAuth := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterB", "--config", configPathAuth})
	}); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterB", "--config", configPathAuth, "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)

	// Join su config VUOTO (no current cluster)
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
	// Su config vuoto, il contesto DEVE essere impostato al cluster joinato
	if cfgAfter.CurrentCluster != "clusterB" {
		t.Fatalf("expected CurrentCluster=clusterB on fresh config, got %q", cfgAfter.CurrentCluster)
	}
}
