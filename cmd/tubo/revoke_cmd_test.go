package main

import (
	"path/filepath"
	"strings"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

func TestRevokeCmdRecordsInviteSessionAndEpochs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revocations.json")
	if err := revokeCmd([]string{"invite", "si_test", "--revocations", path, "--reason", "no"}); err != nil {
		t.Fatal(err)
	}
	store := grantspkg.NewRevocationStore(path)
	if ok, _, err := store.IsInviteRevoked("si_test"); err != nil || !ok {
		t.Fatalf("invite should be revoked ok=%t err=%v", ok, err)
	}
	if err := revokeCmd([]string{"session", "cs_test", "--revocations", path}); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := store.IsSessionRevoked("cs_test"); err != nil || !ok {
		t.Fatalf("session should be revoked ok=%t err=%v", ok, err)
	}
	if err := revokeCmd([]string{"service-access", "svc_test", "--revocations", path}); err != nil {
		t.Fatal(err)
	}
	if epoch, err := store.ServiceAccessEpoch("svc_test"); err != nil || epoch != 1 {
		t.Fatalf("access epoch=%d err=%v", epoch, err)
	}
	if err := revokeCmd([]string{"publish", "svc_test", "--revocations", path}); err != nil {
		t.Fatal(err)
	}
	if ok, _, err := store.IsPublishRevoked("svc_test"); err != nil || !ok {
		t.Fatalf("publish should be revoked ok=%t err=%v", ok, err)
	}
}

func TestRevokeCmdResolvesServiceNameFromConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	cfg := cfgpkg.Config{CurrentCluster: "home", CurrentNamespace: "default", Clusters: map[string]cfgpkg.Cluster{"home": {Namespaces: map[string]cfgpkg.Namespace{"default": {Services: map[string]cfgpkg.NamespaceService{"myapi": {ServiceID: "svc_resolved"}}}}}}}
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "revocations.json")
	if err := revokeCmd([]string{"service-access", "service/myapi", "--config", configPath, "--revocations", path}); err != nil {
		t.Fatal(err)
	}
	store := grantspkg.NewRevocationStore(path)
	if epoch, err := store.ServiceAccessEpoch("svc_resolved"); err != nil || epoch != 1 {
		t.Fatalf("resolved access epoch=%d err=%v", epoch, err)
	}
}

func TestRevokeCmdRejectsBadUsage(t *testing.T) {
	if err := revokeCmd([]string{"nope", "x", "--revocations", filepath.Join(t.TempDir(), "r.json")}); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported target, got %v", err)
	}
	if err := revokeCmd([]string{"invite"}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("expected usage error, got %v", err)
	}
}
