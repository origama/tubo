package networkbundle

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
)

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
	if cfg.Network.PrivateKeyFile != filepath.Join(dir, "swarm.key") || cfg.Network.PrivateKeyB64 != "" {
		t.Fatalf("unexpected network key config: %#v", cfg.Network)
	}
	if _, err := os.Stat(res.SwarmKeyPath); err != nil {
		t.Fatalf("swarm key not written: %v", err)
	}
}
