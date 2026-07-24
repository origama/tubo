package main

import (
	"bytes"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
)

// TestPersistResolvedAttachServiceDefinitionReadOnlyFallback verifies that a
// genuine read-only filesystem (EROFS) lets the attach caller continue with the
// in-memory resolved config while emitting an observable warning and leaving
// the on-disk file untouched. EACCES/permission problems must NOT trigger the
// fallback and must surface as errors.
func TestPersistResolvedAttachServiceDefinitionReadOnlyFallback(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("read-only tmpfs mount is Linux-specific")
	}
	if os.Getuid() != 0 {
		t.Skip("mounting a read-only tmpfs requires root")
	}

	mnt := t.TempDir()
	// A read-only tmpfs mount is the only portable way to produce a genuine
	// EROFS errno for os.CreateTemp / os.Rename inside a test.
	if out, err := exec.Command("mount", "-t", "tmpfs", "-o", "ro,mode=0700", "tmpfs", mnt).CombinedOutput(); err != nil {
		t.Skipf("cannot mount read-only tmpfs (CAP_SYS_ADMIN unavailable): %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = syscall.Unmount(mnt, 0) })

	configPath := filepath.Join(mnt, "config.yaml")
	seed := cfgpkg.Config{
		CurrentCluster:   "solar",
		CurrentNamespace: "default",
		Service:          cfgpkg.Service{Name: "testapi"},
		Clusters: map[string]cfgpkg.Cluster{"solar": {
			ClusterID: "cluster-solar",
			Namespaces: map[string]cfgpkg.Namespace{"default": {
				Services: map[string]cfgpkg.NamespaceService{"testapi": {}},
			}},
		}},
	}
	if err := cfgpkg.WriteFile(configPath, seed, false); err != nil {
		t.Fatalf("seed write on read-only mount unexpectedly failed: %v", err)
	}
	before, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}

	svc := cfgpkg.NamespaceService{ServiceID: "service-1", ServiceSeed: "service-seed-1"}

	// Capture log output so the warning is observable in tests.
	var buf bytes.Buffer
	prevOut := log.Writer()
	log.SetOutput(io.MultiWriter(prevOut, &buf))
	t.Cleanup(func() { log.SetOutput(prevOut) })

	runtimeCfg := seed
	runtimeCfg.Service.Name = "testapi"
	result, err := persistResolvedAttachServiceDefinition(configPath, runtimeCfg, svc)
	if err != nil {
		t.Fatalf("EROFS must be tolerated by the attach caller: %v", err)
	}
	if result.Service.Name != "testapi" {
		t.Fatalf("runtime config lost service name: %#v", result.Service)
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("EROFS fallback must not modify on-disk config\nbefore:\n%s\nafter:\n%s", before, after)
	}
	warning := buf.String()
	if !strings.Contains(warning, "warning: config volume is read-only") || !strings.Contains(warning, "testapi") {
		t.Fatalf("expected observable read-only warning mentioning testapi, got: %q", warning)
	}
}

// TestPersistResolvedAttachServiceDefinitionPermissionDeniedIsNotTolerated
// ensures that a permission problem (EACCES) is NOT treated as a read-only
// filesystem: the caller must surface the error instead of silently proceeding.
func TestPersistResolvedAttachServiceDefinitionPermissionDeniedIsNotTolerated(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions required")
	}
	if os.Getuid() == 0 {
		t.Skip("permission enforcement requires non-root")
	}
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	seed := cfgpkg.Config{
		CurrentCluster:   "solar",
		CurrentNamespace: "default",
		Service:          cfgpkg.Service{Name: "testapi"},
		Clusters: map[string]cfgpkg.Cluster{"solar": {
			ClusterID: "cluster-solar",
			Namespaces: map[string]cfgpkg.Namespace{"default": {
				Services: map[string]cfgpkg.NamespaceService{"testapi": {}},
			}},
		}},
	}
	if err := cfgpkg.WriteFile(configPath, seed, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()

	svc := cfgpkg.NamespaceService{ServiceID: "service-1", ServiceSeed: "service-seed-1"}
	_, err := persistResolvedAttachServiceDefinition(configPath, seed, svc)
	if err == nil {
		t.Fatal("permission-denied must not produce a fake success")
	}
}
