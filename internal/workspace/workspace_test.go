package workspace

import (
	"errors"
	"os"
	"path/filepath"
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
	secretType, clusterName, namespaceName, err := ParseSecretRef("secret/namespace-discovery/home/default")
	if err != nil {
		t.Fatal(err)
	}
	if secretType != cfgpkg.SecretTypeNamespaceDiscovery || clusterName != "home" || namespaceName != "default" {
		t.Fatalf("secret ref=%q %q %q", secretType, clusterName, namespaceName)
	}
	for _, bad := range []string{"bad", "secret/foo/home/default", "secret/namespace-discovery", "secret/namespace-discovery/home", "secret/namespace-discovery/home/default/extra"} {
		if _, _, _, err := ParseSecretRef(bad); err == nil {
			t.Fatalf("expected error for invalid secret ref %q", bad)
		}
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
	secretPath := filepath.Join(t.TempDir(), "default.secret")
	secret, err := cfgpkg.GenerateSecretBytes(cfgpkg.NamespaceDiscoverySecretLength)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secretPath, secret, 0600); err != nil {
		t.Fatal(err)
	}
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
					"default":       {Discovery: cfgpkg.NamespaceDiscoveryEnabled, DiscoverySecretCurrent: &cfgpkg.ManagedSecretRef{Type: cfgpkg.SecretTypeNamespaceDiscovery, KeyID: "nsdk_test", File: secretPath, CreatedAt: time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC)}, ConnectPolicy: cfgpkg.ConnectPolicyNamespaceMember},
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
	if namespaceDesc.DiscoverySecretCurrent == nil || namespaceDesc.DiscoverySecretCurrent.KeyID == "" || namespaceDesc.DiscoverySecretCurrent.File == "" || namespaceDesc.DiscoverySecretCurrent.Fingerprint == "" {
		t.Fatalf("namespaceDesc discovery secret=%#v", namespaceDesc.DiscoverySecretCurrent)
	}
	secrets, err := ws.ListSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(secrets) != 1 || secrets[0].Status != "current" || secrets[0].FileStatus != "ok" {
		t.Fatalf("secrets=%#v", secrets)
	}
	secretDesc, err := ws.DescribeSecret(path, "secret/namespace-discovery/home/default")
	if err != nil {
		t.Fatal(err)
	}
	if secretDesc.Type != cfgpkg.SecretTypeNamespaceDiscovery || secretDesc.Cluster != "home" || secretDesc.Namespace != "default" || secretDesc.Current == nil || secretDesc.Current.Fingerprint == "" {
		t.Fatalf("secretDesc=%#v", secretDesc)
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

type saveFailStore struct {
	FSStore
	saveErr error
	removed []string
}

func (s *saveFailStore) Save(string, cfgpkg.Config) error { return s.saveErr }

func (s *saveFailStore) Remove(path string) error {
	s.removed = append(s.removed, path)
	return s.FSStore.Remove(path)
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

func TestRemoveServiceDoesNotRemoveArtifactsWhenSaveFails(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	ws := Open(FSStore{})
	if _, err := ws.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.CreateService(path, "myapi"); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := cfg.Clusters["home"].Namespaces["default"].Services["myapi"]
	store := &saveFailStore{saveErr: errors.New("save failed")}
	wsFail := Open(store)
	if _, err := wsFail.RemoveService(path, "myapi"); err == nil || !strings.Contains(err.Error(), "save failed") {
		t.Fatalf("expected save failure, got %v", err)
	}
	reloaded, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reloaded.Clusters["home"].Namespaces["default"].Services["myapi"]; !ok {
		t.Fatal("service definition should remain when config save fails")
	}
	if len(store.removed) != 0 {
		t.Fatalf("expected no artifact cleanup on save failure, got %v", store.removed)
	}
	for _, artifact := range []string{svc.ServiceOwnerKeyFile, svc.ServiceClaimFile, svc.ServicePublishLeaseFile} {
		if _, err := os.Stat(artifact); err != nil {
			t.Fatalf("expected artifact to remain after save failure: %s err=%v", artifact, err)
		}
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
	if _, err := ws.ResolveMembershipCapabilityFile(path, cfg.Clusters["home"], "home", "default", "testservice", "seed"); err == nil || !strings.Contains(err.Error(), "service membership capability file missing") {
		t.Fatalf("err=%v", err)
	}
}

func TestResolveMembershipCapabilityFileDoesNotFallBackToNamespaceOrClusterMembership(t *testing.T) {
	namespaceCap := filepath.Join(t.TempDir(), "namespace-membership.cap.json")
	if err := os.WriteFile(namespaceCap, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	clusterCap := filepath.Join(t.TempDir(), "cluster-membership.cap.json")
	if err := os.WriteFile(clusterCap, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{Clusters: map[string]cfgpkg.Cluster{"home": {
		ClusterID:                "cluster-123",
		AuthorityPublicKey:       "ssh-ed25519 AAAATEST home",
		MembershipCapabilityFile: clusterCap,
		Namespaces: map[string]cfgpkg.Namespace{"default": {
			MembershipCapabilityFile: namespaceCap,
		}},
	}}}
	path := writeTestConfig(t, cfg)
	ws := Open(FSStore{})
	got, err := ws.ResolveMembershipCapabilityFile(path, cfg.Clusters["home"], "home", "default", "testservice", "seed")
	if err == nil || !strings.Contains(err.Error(), "service membership capability file missing") {
		t.Fatalf("err=%v", err)
	}
	if got != "" {
		t.Fatalf("path=%q want empty", got)
	}
}

func TestRotateNamespaceDiscoverySecret(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	ws := Open(FSStore{})
	if _, err := ws.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	before, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	oldCurrent := before.Clusters["home"].Namespaces["default"].DiscoverySecretCurrent
	if oldCurrent == nil {
		t.Fatal("missing current secret before rotation")
	}
	oldCurrentBytes, err := os.ReadFile(oldCurrent.File)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := ws.RotateNamespaceDiscoverySecret(path, "secret/namespace-discovery/home/default", 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Current == nil || rotated.Previous == nil {
		t.Fatalf("rotation result missing refs: %#v", rotated)
	}
	after, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	ns := after.Clusters["home"].Namespaces["default"]
	if ns.DiscoverySecretCurrent == nil || ns.DiscoverySecretPrevious == nil {
		t.Fatalf("rotation state missing refs: %#v", ns)
	}
	if ns.DiscoverySecretCurrent.KeyID == oldCurrent.KeyID {
		t.Fatal("expected new current key id after rotation")
	}
	if ns.DiscoverySecretPrevious.KeyID != oldCurrent.KeyID {
		t.Fatalf("previous key id = %q want %q", ns.DiscoverySecretPrevious.KeyID, oldCurrent.KeyID)
	}
	if ns.DiscoverySecretPrevious.ExpiresAt.IsZero() || time.Until(ns.DiscoverySecretPrevious.ExpiresAt) <= time.Hour {
		t.Fatalf("unexpected previous expiry: %v", ns.DiscoverySecretPrevious.ExpiresAt)
	}
	rotatedPreviousBytes, err := os.ReadFile(ns.DiscoverySecretPrevious.File)
	if err != nil {
		t.Fatal(err)
	}
	if string(rotatedPreviousBytes) != string(oldCurrentBytes) {
		t.Fatal("previous secret bytes do not match old current bytes")
	}
	rotatedCurrentBytes, err := os.ReadFile(ns.DiscoverySecretCurrent.File)
	if err != nil {
		t.Fatal(err)
	}
	if string(rotatedCurrentBytes) == string(oldCurrentBytes) {
		t.Fatal("new current secret bytes should differ from old current bytes")
	}
	runtime := after.DiscoveryRuntime()
	if runtime.Context == nil || runtime.PreviousContext == nil {
		t.Fatalf("expected current and previous runtime contexts after rotation: %#v", runtime)
	}
	if runtime.Context.KeyID != ns.DiscoverySecretCurrent.KeyID || runtime.PreviousContext.KeyID != ns.DiscoverySecretPrevious.KeyID {
		t.Fatalf("unexpected runtime key ids: %#v", runtime)
	}
}

func TestListSecretsCleansUpExpiredPreviousDiscoverySecret(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	ws := Open(FSStore{})
	if _, err := ws.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	cfg, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	ns := cluster.Namespaces["default"]
	previousPath := filepath.Join(t.TempDir(), "expired-previous.secret")
	previousSecret, err := cfgpkg.GenerateSecretBytes(cfgpkg.NamespaceDiscoverySecretLength)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previousPath, previousSecret, 0600); err != nil {
		t.Fatal(err)
	}
	ns.DiscoverySecretPrevious = &cfgpkg.ManagedSecretRef{Type: cfgpkg.SecretTypeNamespaceDiscovery, KeyID: "nsdk_previous", File: previousPath, CreatedAt: time.Now().Add(-2 * time.Hour).UTC(), ExpiresAt: time.Now().Add(-time.Minute).UTC()}
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := ws.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	items, err := ws.ListSecrets(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != "current" {
		t.Fatalf("expected only current secret after cleanup, got %#v", items)
	}
	cfg, err = ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Clusters["home"].Namespaces["default"].DiscoverySecretPrevious != nil {
		t.Fatalf("expected expired previous secret metadata to be cleared, got %#v", cfg.Clusters["home"].Namespaces["default"].DiscoverySecretPrevious)
	}
	if _, err := os.Stat(previousPath); !os.IsNotExist(err) {
		t.Fatalf("expected expired previous secret file to be removed, got err=%v", err)
	}
}

func TestRotateNamespaceDiscoverySecretRequiresCurrentAndAuthority(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	ws := Open(FSStore{})
	if _, err := ws.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	cfg, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	ns := cluster.Namespaces["default"]
	ns.DiscoverySecretCurrent = nil
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := ws.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RotateNamespaceDiscoverySecret(path, "secret/namespace-discovery/home/default", time.Hour); err == nil || !strings.Contains(err.Error(), "missing discovery_secret_current") {
		t.Fatalf("expected missing current secret error, got %v", err)
	}
	cfg, err = ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	cluster = cfg.Clusters["home"]
	cluster.AuthorityPrivateKeyFile = ""
	ns = cluster.Namespaces["default"]
	ns.DiscoverySecretCurrent = mustSecretRef(t, path, "home", "default")
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := ws.SaveConfig(path, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RotateNamespaceDiscoverySecret(path, "secret/namespace-discovery/home/default", time.Hour); err == nil || !strings.Contains(err.Error(), "rotation requires local cluster authority material") {
		t.Fatalf("expected missing authority material error, got %v", err)
	}
}

func mustSecretRef(t *testing.T, configPath, cluster, namespace string) *cfgpkg.ManagedSecretRef {
	t.Helper()
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Clusters[cluster].Namespaces[namespace].DiscoverySecretCurrent
}

func TestLoadConfigOrErrorMissing(t *testing.T) {
	ws := Open(FSStore{})
	_, err := ws.LoadConfigOrError(t.TempDir() + "/missing.yaml")
	if err == nil || !strings.Contains(err.Error(), "run `tubo join` first") {
		t.Fatalf("err=%v", err)
	}
}
