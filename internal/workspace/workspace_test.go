package workspace

import (
	"os"
	"strings"
	"testing"
	"time"

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
					"default":       {Discovery: cfgpkg.NamespaceDiscoveryEnabled, ConnectPolicy: cfgpkg.ConnectPolicyNamespaceMember},
					"observability": {Discovery: cfgpkg.NamespaceDiscoveryDisabled, ConnectPolicy: cfgpkg.ConnectPolicyPublic},
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
	if !namespaceDesc.CurrentCluster || !namespaceDesc.CurrentNamespace || namespaceDesc.CurrentOverlay != "public" || namespaceDesc.Discovery != cfgpkg.NamespaceDiscoveryEnabled || namespaceDesc.ConnectPolicy != cfgpkg.ConnectPolicyNamespaceMember || namespaceDesc.PublicDefault {
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
	if ns := storedCluster.Namespaces["default"]; ns.Discovery != cfgpkg.NamespaceDiscoveryEnabled || ns.ConnectPolicy != cfgpkg.ConnectPolicyNamespaceMember {
		t.Fatalf("default namespace policy=%#v", ns)
	} else {
		if ns.DiscoverySecretCurrent == nil || ns.DiscoverySecretCurrent.KeyID == "" || ns.DiscoverySecretCurrent.File == "" {
			t.Fatalf("default namespace discovery secret missing: %#v", ns)
		}
		if info, err := os.Stat(ns.DiscoverySecretCurrent.File); err != nil {
			t.Fatalf("default namespace discovery secret file missing: %v", err)
		} else if info.Mode().Perm() != 0o600 {
			t.Fatalf("default namespace discovery secret permissions = %04o", info.Mode().Perm())
		}
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
	storedNamespace := reloaded.Clusters["home"].Namespaces["observability"]
	if storedNamespace.MembershipCapabilityFile == "" {
		t.Fatalf("cluster namespaces=%#v", reloaded.Clusters["home"].Namespaces)
	}
	if storedNamespace.Discovery != cfgpkg.NamespaceDiscoveryEnabled || storedNamespace.ConnectPolicy != cfgpkg.ConnectPolicyNamespaceMember {
		t.Fatalf("stored namespace policy=%#v", storedNamespace)
	}
	if storedNamespace.DiscoverySecretCurrent == nil || storedNamespace.DiscoverySecretCurrent.KeyID == "" || storedNamespace.DiscoverySecretCurrent.File == "" {
		t.Fatalf("stored namespace discovery secret missing: %#v", storedNamespace)
	}
	if info, err := os.Stat(storedNamespace.DiscoverySecretCurrent.File); err != nil {
		t.Fatalf("namespace discovery secret file missing: %v", err)
	} else if info.Mode().Perm() != 0o600 {
		t.Fatalf("namespace discovery secret permissions = %04o", info.Mode().Perm())
	}
}

func TestEnsureAndCreateService(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	ws := Open(FSStore{})
	if _, err := ws.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	ensure, err := ws.EnsureService(path, "myapi")
	if err != nil {
		t.Fatal(err)
	}
	if ensure.Context.Service.ServiceID == "" || ensure.Context.Service.ServiceSeed == "" {
		t.Fatalf("ensure=%#v", ensure)
	}
	if ensure.Context.Service.ServiceOwnerKeyFile == "" || ensure.Context.Service.ServiceClaimFile == "" || ensure.Context.Service.ServicePublishLeaseFile == "" {
		t.Fatalf("ensure paths=%#v", ensure.Context.Service)
	}
	created, err := ws.CreateService(path, "myapi")
	if err != nil {
		t.Fatal(err)
	}
	if created.Context.Service.ServiceClaimFile == "" || created.Context.Service.ServicePublishLeaseFile == "" {
		t.Fatalf("created=%#v", created)
	}
	ctx, err := ws.ResolveServiceContext(path, created.Context.Service.ServiceID, "home", "default")
	if err != nil {
		t.Fatal(err)
	}
	if ctx.Name != "myapi" {
		t.Fatalf("ctx=%#v", ctx)
	}
	again, err := ws.CreateService(path, "myapi")
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyExists {
		t.Fatalf("again=%#v", again)
	}
}

func TestResolveMembershipCapabilityFileRequiresRuntimeEvidence(t *testing.T) {
	cfg := cfgpkg.Config{Clusters: map[string]cfgpkg.Cluster{"home": {
		ClusterID:          "cluster-123",
		AuthorityPublicKey: "ssh-ed25519 AAAATEST home",
		MembershipGrant: &cfgpkg.ClusterMembershipGrant{
			ClusterName: "home",
			ClusterID:   "cluster-123",
			Namespace:   "default",
			Role:        "member",
			ExpiresAt:   time.Now().Add(time.Hour),
		},
		Namespaces: map[string]cfgpkg.Namespace{"default": {}},
	}}}
	path := writeTestConfig(t, cfg)
	ws := Open(FSStore{})
	if _, err := ws.ResolveMembershipCapabilityFile(path, cfg.Clusters["home"], "home", "default", "seed"); err == nil || !strings.Contains(err.Error(), "no membership capability file configured") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadConfigOrErrorMissing(t *testing.T) {
	ws := Open(FSStore{})
	_, err := ws.LoadConfigOrError(t.TempDir() + "/missing.yaml")
	if err == nil || !strings.Contains(err.Error(), "run `tubo join` first") {
		t.Fatalf("err=%v", err)
	}
}
