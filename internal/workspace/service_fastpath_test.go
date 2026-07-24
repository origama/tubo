package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
)

// materializeAttachService writes a writable config with a fully materialized
// service identity (ServiceID, ServiceSeed, owner key file, claim/lease paths)
// and returns the config path plus the persisted service.
func materializeAttachService(t *testing.T) (string, cfgpkg.NamespaceService) {
	t.Helper()
	path := writeTestConfig(t, cfgpkg.Config{})
	ws := Open(FSStore{})
	if _, err := ws.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.EnsureService(path, "myapi"); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := cfg.Clusters["home"].Namespaces["default"].Services["myapi"]
	if svc.ServiceID == "" || svc.ServiceSeed == "" || svc.ServiceOwnerKeyFile == "" ||
		svc.ServiceClaimFile == "" || svc.ServicePublishLeaseFile == "" {
		t.Fatalf("service not fully materialized: %#v", svc)
	}
	return path, svc
}

func TestExistingAttachServiceIdentityCompleteDetectsComplete(t *testing.T) {
	path, _ := materializeAttachService(t)
	ws := Open(FSStore{})
	loaded, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg := loaded
	runtimeCfg.Service.Name = "myapi"
	svc, ok, err := ws.existingAttachServiceIdentityComplete(loaded, runtimeCfg, "myapi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatal("expected complete service to be detected")
	}
	if svc.ServiceID == "" {
		t.Fatalf("returned svc missing identity: %#v", svc)
	}
}

func TestExistingAttachServiceIdentityCompleteFalseWhenArtifactMissing(t *testing.T) {
	path, _ := materializeAttachService(t)
	ws := Open(FSStore{})
	// Remove the owner key file so the identity is no longer complete.
	loaded, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := loaded.Clusters["home"].Namespaces["default"].Services["myapi"]
	if err := os.Remove(svc.ServiceOwnerKeyFile); err != nil {
		t.Fatal(err)
	}
	runtimeCfg := loaded
	runtimeCfg.Service.Name = "myapi"
	_, ok, err := ws.existingAttachServiceIdentityComplete(loaded, runtimeCfg, "myapi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("fast path must not trigger when an artifact is missing")
	}
}

func TestExistingAttachServiceIdentityCompleteFalseWhenFieldUnset(t *testing.T) {
	path, _ := materializeAttachService(t)
	ws := Open(FSStore{})
	loaded, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	// Clear ServiceID on the persisted service: ensureServiceState would have to
	// regenerate the identity (a write), so the fast path must not apply.
	cluster := loaded.Clusters["home"]
	namespace := cluster.Namespaces["default"]
	svc := namespace.Services["myapi"]
	svc.ServiceID = ""
	namespace.Services["myapi"] = svc
	cluster.Namespaces["default"] = namespace
	loaded.Clusters["home"] = cluster
	runtimeCfg := loaded
	runtimeCfg.Service.Name = "myapi"
	_, ok, err := ws.existingAttachServiceIdentityComplete(loaded, runtimeCfg, "myapi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("fast path must not trigger when ServiceID is unset")
	}
}

func TestExistingAttachServiceIdentityCompleteFalseOnTargetMismatch(t *testing.T) {
	path, _ := materializeAttachService(t)
	ws := Open(FSStore{})
	loaded, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg := loaded
	runtimeCfg.Service.Name = "myapi"
	runtimeCfg.Service.Target = "http://127.0.0.1:9999" // differs from persisted placeholder
	_, ok, err := ws.existingAttachServiceIdentityComplete(loaded, runtimeCfg, "myapi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("fast path must not trigger on target mismatch")
	}
}

func TestExistingAttachServiceIdentityCompleteFalseOnStaleLease(t *testing.T) {
	path, _ := materializeAttachService(t)
	ws := Open(FSStore{})
	loaded, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	svc := loaded.Clusters["home"].Namespaces["default"].Services["myapi"]
	// Write a lease file whose publisher peer id does not match the service seed.
	staleLease := `{"publisher_peer_id":"12D3KooWAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	if err := os.WriteFile(svc.ServicePublishLeaseFile, []byte(staleLease), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeCfg := loaded
	runtimeCfg.Service.Name = "myapi"
	_, ok, err := ws.existingAttachServiceIdentityComplete(loaded, runtimeCfg, "myapi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("fast path must not trigger when a stale lease needs cleanup")
	}
}

// TestEnsureAttachServiceIdentityFastPathHasNoSideEffects verifies that when the
// service is already fully materialized, EnsureAttachServiceIdentity takes the
// read-only fast path: it does not create missing files, remove lease/claim
// artifacts, or rewrite the config. This is enforced even when the config file
// is read-only (simulating a containerized `:ro` smoke mount).
func TestEnsureAttachServiceIdentityFastPathHasNoSideEffects(t *testing.T) {
	path, svc := materializeAttachService(t)
	ws := Open(FSStore{})

	// Snapshot the set of files under the config dir before the call.
	configDir := filepath.Dir(path)
	before := snapshotFiles(t, configDir)
	beforeConfig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	// Make the config dir tree read-only to prove the fast path does not need to
	// write. Use file mode 0o444 on the config and 0o500 on dirs; the owner key
	// and lease files live under the config dir and must remain readable.
	chmodReadonly(t, configDir)

	runtimeCfg, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg.Service.Name = "myapi"
	updated, returnedSvc, err := ws.EnsureAttachServiceIdentity(path, runtimeCfg)
	if err != nil {
		t.Fatalf("fast path must succeed on a read-only, complete config: %v", err)
	}
	if returnedSvc.ServiceID != svc.ServiceID || returnedSvc.ServiceSeed != svc.ServiceSeed {
		t.Fatalf("returned service identity drifted: got=%#v want id=%q seed=%q", returnedSvc, svc.ServiceID, svc.ServiceSeed)
	}
	if updated.Clusters["home"].Namespaces["default"].Services["myapi"].ServiceID != svc.ServiceID {
		t.Fatal("updated runtime config lost the persisted service identity")
	}

	// Restore permissions before inspecting the filesystem.
	chmodWritable(t, configDir)

	after := snapshotFiles(t, configDir)
	if !sameFileSet(before, after) {
		t.Fatalf("fast path created or removed files\nbefore:\n%s\nafter:\n%s", before, after)
	}
	afterConfig, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterConfig) != string(beforeConfig) {
		t.Fatalf("fast path rewrote on-disk config\nbefore:\n%s\nafter:\n%s", beforeConfig, afterConfig)
	}
	afterInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("config file mtime changed: before=%s after=%s", beforeInfo.ModTime(), afterInfo.ModTime())
	}
}

// TestEnsureAttachServiceIdentityFallsToTransactionalWhenIdentityMissing
// verifies the fast path does not silently skip a missing artifact: when the
// owner key file is absent, EnsureAttachServiceIdentity must enter the
// transactional path (which recreates it under lock) rather than returning the
// incomplete persisted state.
func TestEnsureAttachServiceIdentityFallsToTransactionalWhenIdentityMissing(t *testing.T) {
	path, svc := materializeAttachService(t)
	ws := Open(FSStore{})
	loaded, err := ws.LoadConfigOrError(path)
	if err != nil {
		t.Fatal(err)
	}
	// Remove the owner key and clear ServiceID so ensureServiceState regenerates.
	if err := os.Remove(svc.ServiceOwnerKeyFile); err != nil {
		t.Fatal(err)
	}
	cluster := loaded.Clusters["home"]
	namespace := cluster.Namespaces["default"]
	existing := namespace.Services["myapi"]
	existing.ServiceID = ""
	existing.ServiceOwnerKeyFile = ""
	namespace.Services["myapi"] = existing
	cluster.Namespaces["default"] = namespace
	loaded.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(path, loaded, true); err != nil {
		t.Fatal(err)
	}

	runtimeCfg := loaded
	runtimeCfg.Service.Name = "myapi"
	_, returnedSvc, err := ws.EnsureAttachServiceIdentity(path, runtimeCfg)
	if err != nil {
		t.Fatalf("transactional path must recreate identity: %v", err)
	}
	if returnedSvc.ServiceID == "" || returnedSvc.ServiceOwnerKeyFile == "" {
		t.Fatalf("transactional path did not materialize identity: %#v", returnedSvc)
	}
	if _, err := os.Stat(returnedSvc.ServiceOwnerKeyFile); err != nil {
		t.Fatalf("owner key file was not recreated: %v", err)
	}
}

func snapshotFiles(t *testing.T, dir string) string {
	t.Helper()
	entries := []string{}
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		entries = append(entries, rel)
		return nil
	})
	return strings.Join(entries, "\n")
}

func sameFileSet(a, b string) bool {
	return a == b
}

func chmodReadonly(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			_ = os.Chmod(p, 0o500)
		} else {
			_ = os.Chmod(p, 0o444)
		}
		return nil
	})
}

func chmodWritable(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			_ = os.Chmod(p, 0o700)
		} else {
			_ = os.Chmod(p, 0o600)
		}
		return nil
	})
}
