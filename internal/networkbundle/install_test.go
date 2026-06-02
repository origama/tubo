package networkbundle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
)

// TestInstallBundlePreservesExistingNamespaceServices verifies Bug 3:
// networkbundle.Install with Force=true must NOT delete namespaces or
// services that are already configured in the cluster being updated by the bundle.
func TestInstallBundlePreservesExistingNamespaceServices(t *testing.T) {
	now := time.Now().UTC()
	payload := samplePayload(now.Add(-time.Hour), now.Add(time.Hour))
	dir := t.TempDir()

	// First install: clean config
	if _, err := Install(&payload, InstallOptions{ConfigDir: dir, Force: false}); err != nil {
		t.Fatal(err)
	}

	// Simulate services that are already configured in the "default" namespace of the "home" cluster
	configPath := filepath.Join(dir, "config.yaml")
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	homecluster := cfg.Clusters["home"]
	if homecluster.Namespaces == nil {
		homecluster.Namespaces = make(map[string]cfgpkg.Namespace)
	}
	homecluster.Namespaces["default"] = cfgpkg.Namespace{
		Discovery:     cfgpkg.NamespaceDiscoveryDisabled,
		ConnectPolicy: cfgpkg.ConnectPolicyInviteOnly,
		Services: map[string]cfgpkg.NamespaceService{
			"myapi": {ServiceID: "service-abc123", ServiceSeed: "seed-xyz"},
		},
	}
	// Also add a second private namespace with services
	homecluster.Namespaces["staging"] = cfgpkg.Namespace{
		Discovery:     cfgpkg.NamespaceDiscoveryEnabled,
		ConnectPolicy: cfgpkg.ConnectPolicyNamespaceMember,
		Services: map[string]cfgpkg.NamespaceService{
			"backendapi": {ServiceID: "service-staging-456"},
		},
	}
	cfg.Clusters["home"] = homecluster
	// Also add a separate private cluster that must NOT be touched
	cfg.Clusters["oricluster"] = cfgpkg.Cluster{
		ClusterID:          "cluster-ori-xyz",
		AuthorityPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAorioriori",
		Namespaces: map[string]cfgpkg.Namespace{
			"orins": {Services: map[string]cfgpkg.NamespaceService{
				"tuboweb": {ServiceID: "service-tuboweb-789"},
			}},
		},
	}
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}

	// Re-install the bundle (for example during a relay update) with Force=true
	if _, err := Install(&payload, InstallOptions{ConfigDir: dir, Force: true}); err != nil {
		t.Fatal(err)
	}

	cfgAfter, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	// The "oricluster" cluster (private, not touched by the bundle) must survive
	if _, ok := cfgAfter.Clusters["oricluster"]; !ok {
		t.Fatalf("oricluster was lost after bundle re-install: %v", cfgAfter.Clusters)
	}
	if cfgAfter.Clusters["oricluster"].Namespaces["orins"].Services["tuboweb"].ServiceID != "service-tuboweb-789" {
		t.Fatalf("oricluster/orins/tuboweb service lost after bundle re-install")
	}

	// The "default" namespace must preserve the myapi service
	defaultNs := cfgAfter.Clusters["home"].Namespaces["default"]
	if svc, ok := defaultNs.Services["myapi"]; !ok || svc.ServiceID != "service-abc123" {
		t.Fatalf("home/default/myapi service lost after bundle re-install: %#v", defaultNs.Services)
	}

	// The "staging" namespace (private, not in the bundle) must survive
	stagingNs, ok := cfgAfter.Clusters["home"].Namespaces["staging"]
	if !ok {
		t.Fatalf("home/staging namespace lost after bundle re-install: %v", cfgAfter.Clusters["home"].Namespaces)
	}
	if svc, ok := stagingNs.Services["backendapi"]; !ok || svc.ServiceID != "service-staging-456" {
		t.Fatalf("home/staging/backendapi service lost after bundle re-install: %#v", stagingNs.Services)
	}
	if stagingNs.DiscoverySecretCurrent == nil || stagingNs.DiscoverySecretCurrent.KeyID == "" || stagingNs.DiscoverySecretCurrent.File == "" {
		t.Fatalf("home/staging discovery secret missing after bundle re-install: %#v", stagingNs)
	}
	if info, err := os.Stat(stagingNs.DiscoverySecretCurrent.File); err != nil {
		t.Fatalf("home/staging discovery secret file missing: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("home/staging discovery secret permissions = %04o", info.Mode().Perm())
	}
	cfgAfter.Role = "relay"
	if err := cfgpkg.Validate(cfgAfter); err != nil {
		t.Fatalf("Validate(cfgAfter) error = %v", err)
	}
}

func TestInstallPrivateBundleGeneratesNamespaceDiscoverySecret(t *testing.T) {
	now := time.Now().UTC()
	payload := samplePayload(now.Add(-time.Hour), now.Add(time.Hour))
	payload.PublicCluster = nil
	dir := t.TempDir()
	res, err := Install(&payload, InstallOptions{ConfigDir: dir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	ns := cfg.Clusters["home"].Namespaces["default"]
	if ns.Discovery != cfgpkg.NamespaceDiscoveryEnabled {
		t.Fatalf("default namespace discovery = %q", ns.Discovery)
	}
	if ns.DiscoverySecretCurrent == nil || ns.DiscoverySecretCurrent.KeyID == "" || ns.DiscoverySecretCurrent.File == "" {
		t.Fatalf("default namespace discovery secret missing: %#v", ns)
	}
	if info, err := os.Stat(ns.DiscoverySecretCurrent.File); err != nil {
		t.Fatalf("default namespace discovery secret file missing: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("default namespace discovery secret permissions = %04o", info.Mode().Perm())
	}
}

func TestInstallPublicBundleWritesPublicClusterMetadata(t *testing.T) {
	now := time.Now().UTC()
	payload := samplePayload(now.Add(-time.Hour), now.Add(time.Hour))
	dir := t.TempDir()
	res, err := Install(&payload, InstallOptions{ConfigDir: dir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.NetworkName != payload.Name || res.ConfigPath != filepath.Join(dir, "config.yaml") {
		t.Fatalf("unexpected install result: %#v", res)
	}
	cfg, err := cfgpkg.LoadFile(res.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentCluster != payload.PublicCluster.Name || cfg.CurrentNamespace != payload.PublicCluster.DefaultNamespace {
		t.Fatalf("unexpected current scope: %#v", cfg)
	}
	overlay, ok := cfg.Overlays[payload.Name]
	if !ok {
		t.Fatalf("overlay %q not installed", payload.Name)
	}
	if overlay.Kind != cfgpkg.OverlayKindPublicBundle || overlay.PublicDefaultCluster != payload.PublicCluster.Name || overlay.PublicDefaultNamespace != payload.PublicCluster.DefaultNamespace {
		t.Fatalf("unexpected overlay metadata: %#v", overlay)
	}
	cluster, ok := cfg.Clusters[payload.PublicCluster.Name]
	if !ok {
		t.Fatalf("cluster %q not installed", payload.PublicCluster.Name)
	}
	if cluster.ClusterID != payload.PublicCluster.ClusterID {
		t.Fatalf("cluster id = %q, want %q", cluster.ClusterID, payload.PublicCluster.ClusterID)
	}
	if cluster.AuthorityPublicKey != payload.PublicCluster.AuthorityPublicKey {
		t.Fatalf("authority public key = %q, want %q", cluster.AuthorityPublicKey, payload.PublicCluster.AuthorityPublicKey)
	}
	if cluster.AuthorityPrivateKeyFile != "" {
		t.Fatalf("unexpected authority private key file: %q", cluster.AuthorityPrivateKeyFile)
	}
	if cluster.MembershipGrant == nil || len(cluster.MembershipGrant.GrantServicePeers) != len(payload.PublicCluster.GrantServicePeers) {
		t.Fatalf("missing membership grant metadata: %#v", cluster.MembershipGrant)
	}
	if ns := cluster.Namespaces[payload.PublicCluster.DefaultNamespace]; ns.Discovery != cfgpkg.NamespaceDiscoveryDisabled || ns.ConnectPolicy != cfgpkg.ConnectPolicyInviteOnly {
		t.Fatalf("unexpected public default namespace policy: %#v", ns)
	}
	if cfg.Network.PrivateKeyFile != filepath.Join(dir, "swarm.key") || cfg.Network.PrivateKeyB64 != "" {
		t.Fatalf("unexpected network key config: %#v", cfg.Network)
	}
	if _, err := os.Stat(res.SwarmKeyPath); err != nil {
		t.Fatalf("swarm key not written: %v", err)
	}
}
