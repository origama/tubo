package workspace

import (
	"strings"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
)

func writeTestConfig(t *testing.T, cfg cfgpkg.Config) string {
	t.Helper()
	path := t.TempDir() + "/config.yaml"
	if err := cfgpkg.WriteFile(path, cfg, true); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseRef(t *testing.T) {
	ref, err := ParseRef("cluster/home")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Kind != "cluster" || ref.Name != "home" {
		t.Fatalf("ref=%#v", ref)
	}
	if _, err := ParseRef("bad"); err == nil {
		t.Fatal("expected error for invalid ref")
	}
}

func TestResolveScope(t *testing.T) {
	cfg := cfgpkg.Config{CurrentCluster: "home", CurrentNamespace: "default"}
	scope, err := ResolveScope(cfg, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Cluster != "home" || scope.Namespace != "default" || scope.AllNamespaces {
		t.Fatalf("scope=%#v", scope)
	}
	if _, err := ResolveScope(cfgpkg.Config{}, "", "metrics", false); err == nil {
		t.Fatal("expected missing cluster error")
	}
	if _, err := ResolveScope(cfg, "", "metrics", true); err == nil {
		t.Fatal("expected all-namespaces conflict")
	}
}

func TestListDescribeAndUseLocalResources(t *testing.T) {
	cfg := cfgpkg.Config{
		CurrentOverlay:   "public",
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Overlays: map[string]cfgpkg.Overlay{
			"public":  {Relays: []string{"relay-a"}, BootstrapPeers: []string{"boot-a"}, SwarmKeyFile: "/tmp/public.key"},
			"staging": {Relays: []string{"relay-b"}},
		},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				ClusterID:          "cluster-123",
				AuthorityPublicKey: "ssh-ed25519 AAAATEST home",
				Capabilities:       []string{"list", "publish"},
				Namespaces: map[string]cfgpkg.Namespace{
					"default":       {},
					"observability": {},
				},
			},
		},
	}
	path := writeTestConfig(t, cfg)
	ws := Open(FSStore{})

	overlays, err := ws.ListOverlays(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(overlays) != 2 || overlays[0].Name != "public" {
		t.Fatalf("overlays=%#v", overlays)
	}
	clusters, err := ws.ListClusters(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(clusters) != 1 || clusters[0].Name != "home" {
		t.Fatalf("clusters=%#v", clusters)
	}
	namespaces, err := ws.ListNamespaces(path)
	if err != nil {
		t.Fatal(err)
	}
	if namespaces.Cluster != "home" || len(namespaces.Items) != 2 {
		t.Fatalf("namespaces=%#v", namespaces)
	}
	overlayDesc, err := ws.DescribeOverlay(path, "public")
	if err != nil {
		t.Fatal(err)
	}
	if !overlayDesc.Current || overlayDesc.SwarmKeyFile != "/tmp/public.key" {
		t.Fatalf("overlayDesc=%#v", overlayDesc)
	}
	clusterDesc, err := ws.DescribeCluster(path, "home")
	if err != nil {
		t.Fatal(err)
	}
	if clusterDesc.ClusterID != "cluster-123" || len(clusterDesc.Namespaces) != 2 {
		t.Fatalf("clusterDesc=%#v", clusterDesc)
	}
	namespaceDesc, err := ws.DescribeNamespace(path, "default")
	if err != nil {
		t.Fatal(err)
	}
	if !namespaceDesc.CurrentCluster || !namespaceDesc.CurrentNamespace || namespaceDesc.CurrentOverlay != "public" {
		t.Fatalf("namespaceDesc=%#v", namespaceDesc)
	}

	updated, err := ws.Use(path, Ref{Kind: "overlay", Name: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentOverlay != "staging" || updated.Network.RelayPeers[0] != "relay-b" {
		t.Fatalf("updated overlay cfg=%#v", updated)
	}
	updated, err = ws.Use(path, Ref{Kind: "namespace", Name: "observability"})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentNamespace != "observability" {
		t.Fatalf("updated namespace cfg=%#v", updated)
	}
}

func TestCreateClusterAndNamespace(t *testing.T) {
	cfg := cfgpkg.Config{}
	path := writeTestConfig(t, cfg)
	ws := Open(FSStore{})
	cluster, err := ws.CreateCluster(path, "home")
	if err != nil {
		t.Fatal(err)
	}
	if cluster.Name != "home" || cluster.ClusterID == "" || !cluster.Current {
		t.Fatalf("cluster=%#v", cluster)
	}
	reloaded, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	storedCluster := reloaded.Clusters["home"]
	if storedCluster.AuthorityPrivateKeyFile == "" || storedCluster.MembershipCapabilityFile == "" {
		t.Fatalf("storedCluster=%#v", storedCluster)
	}
	ns, err := ws.CreateNamespace(path, "observability")
	if err != nil {
		t.Fatal(err)
	}
	if ns.Name != "observability" || ns.Cluster != "home" || !ns.Current {
		t.Fatalf("namespace=%#v", ns)
	}
	reloaded, err = ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.CurrentNamespace != "observability" {
		t.Fatalf("currentNamespace=%q", reloaded.CurrentNamespace)
	}
	if reloaded.Clusters["home"].Namespaces["observability"].MembershipCapabilityFile == "" {
		t.Fatalf("cluster namespaces=%#v", reloaded.Clusters["home"].Namespaces)
	}
}

func TestLoadConfigOrErrorMissing(t *testing.T) {
	ws := Open(FSStore{})
	_, err := ws.LoadConfigOrError(t.TempDir() + "/missing.yaml")
	if err == nil || !strings.Contains(err.Error(), "run `tubo join` first") {
		t.Fatalf("err=%v", err)
	}
}
