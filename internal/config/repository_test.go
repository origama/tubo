package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestConfigRepositoryUpdateCreatesMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.yaml")
	if _, err := NewConfigRepository(path).Update(context.Background(), func(*Config) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err != nil {
		t.Fatal(err)
	}
	assertPrivateConfigModes(t, path)
}

func TestConfigRepositoryConcurrentUpdatesPreserveDisjointChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.yaml")
	repo := NewConfigRepository(path)
	if err := repo.Write(context.Background(), Config{}, false); err != nil {
		t.Fatal(err)
	}

	const writers = 64
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := NewConfigRepository(path).Update(context.Background(), func(cfg *Config) error {
				if cfg.Overlays == nil {
					cfg.Overlays = make(map[string]Overlay)
				}
				name := fmt.Sprintf("overlay-%02d", index)
				cfg.Overlays[name] = Overlay{Kind: name}
				return nil
			})
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Overlays) != writers {
		t.Fatalf("overlays = %d, want %d", len(cfg.Overlays), writers)
	}
	assertPrivateConfigModes(t, path)
}

func TestConfigRepositorySubprocessUpdatesPreserveDisjointChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := NewConfigRepository(path).Write(context.Background(), Config{}, false); err != nil {
		t.Fatal(err)
	}
	const writers = 12
	commands := make([]*exec.Cmd, 0, writers)
	for i := 0; i < writers; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestConfigRepositoryHelperProcess$")
		cmd.Env = append(os.Environ(),
			"TUBO_CONFIG_REPOSITORY_HELPER=update",
			"TUBO_CONFIG_REPOSITORY_PATH="+path,
			fmt.Sprintf("TUBO_CONFIG_REPOSITORY_KEY=process-%02d", i),
		)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v", err)
		}
	}
	cfg, err := LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Overlays) != writers {
		t.Fatalf("subprocess overlays = %d, want %d", len(cfg.Overlays), writers)
	}
}

func TestConfigRepositoryKilledWriterPreservesActiveConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	initial := Config{CurrentCluster: "before"}
	if err := NewConfigRepository(path).Write(context.Background(), initial, false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestConfigRepositoryHelperProcess$")
	cmd.Env = append(os.Environ(),
		"TUBO_CONFIG_REPOSITORY_HELPER=crash-before-rename",
		"TUBO_CONFIG_REPOSITORY_PATH="+path,
	)
	if err := cmd.Run(); err == nil {
		t.Fatal("expected killed writer helper failure")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("active config changed after killed writer\nbefore:\n%s\nafter:\n%s", before, after)
	}
	cfg, err := LoadFile(path)
	if err != nil || cfg.CurrentCluster != "before" {
		t.Fatalf("active config after crash = %#v, %v", cfg, err)
	}
	if _, err := NewConfigRepository(path).Update(context.Background(), func(cfg *Config) error {
		cfg.CurrentNamespace = "recovered"
		return nil
	}); err != nil {
		t.Fatalf("update after stale temp/lock: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config.yaml.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("stale temp files remain after recovery: %v", matches)
	}
}

func TestConfigRepositoryHelperProcess(t *testing.T) {
	mode := os.Getenv("TUBO_CONFIG_REPOSITORY_HELPER")
	if mode == "" {
		return
	}
	path := os.Getenv("TUBO_CONFIG_REPOSITORY_PATH")
	repo := NewConfigRepository(path)
	switch mode {
	case "update":
		key := os.Getenv("TUBO_CONFIG_REPOSITORY_KEY")
		if _, err := repo.Update(context.Background(), func(cfg *Config) error {
			if cfg.Overlays == nil {
				cfg.Overlays = make(map[string]Overlay)
			}
			cfg.Overlays[key] = Overlay{Kind: key}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	case "crash-before-rename":
		repo.hooks.beforeRename = func(string, string) error {
			os.Exit(23)
			return nil
		}
		_, _ = repo.Update(context.Background(), func(cfg *Config) error {
			cfg.CurrentCluster = "corrupt"
			return nil
		})
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func TestConfigRepositoryMutationErrorAndCancellationDoNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := NewConfigRepository(path).Write(context.Background(), Config{CurrentCluster: "before"}, false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("mutation failed")
	if _, err := NewConfigRepository(path).Update(context.Background(), func(cfg *Config) error {
		cfg.CurrentCluster = "after"
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("mutation error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewConfigRepository(path).Update(ctx, func(cfg *Config) error {
		cfg.CurrentCluster = "canceled"
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled update error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("failed mutation changed config\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestConfigRepositoryFailureInjectionPreservesPreviousConfig(t *testing.T) {
	failures := []struct {
		name  string
		hooks atomicWriteHooks
	}{
		{name: "disk full", hooks: atomicWriteHooks{write: func(*os.File, []byte) (int, error) { return 0, syscall.ENOSPC }}},
		{name: "sync", hooks: atomicWriteHooks{beforeSync: func(string) error { return errors.New("injected sync failure") }}},
		{name: "rename", hooks: atomicWriteHooks{beforeRename: func(string, string) error { return errors.New("injected rename failure") }}},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := NewConfigRepository(path).Write(context.Background(), Config{CurrentCluster: "before"}, false); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			repo := NewConfigRepository(path)
			repo.hooks = tc.hooks
			if _, err := repo.Update(context.Background(), func(cfg *Config) error {
				cfg.CurrentCluster = "after"
				return nil
			}); err == nil {
				t.Fatal("expected injected write failure")
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("active config changed after failure\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestConfigRepositoryReadOnlyDirectoryReturnsInMemoryMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions required")
	}
	// Run as non-root so the read-only directory mode is enforced.
	if os.Getuid() == 0 {
		t.Skip("read-only mode enforcement requires non-root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := NewConfigRepository(path).Write(context.Background(), Config{CurrentCluster: "before", Clusters: map[string]Cluster{"before": {}}}, false); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	updated, updateErr := NewConfigRepository(path).Update(context.Background(), func(cfg *Config) error {
		cfg.CurrentCluster = "after"
		return nil
	})
	if updateErr != nil {
		t.Fatalf("read-only volume should be tolerated in-memory, got %v", updateErr)
	}
	if updated.CurrentCluster != "after" {
		t.Fatalf("in-memory mutation lost: %#v", updated.CurrentCluster)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("read-only volume should not change on-disk config\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestConfigRepositoryRejectsLockSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("O_NOFOLLOW lock test is Unix-specific")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := NewConfigRepository(path).Write(context.Background(), Config{CurrentCluster: "before"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".lock"); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewConfigRepository(path).Update(context.Background(), func(cfg *Config) error {
		cfg.CurrentCluster = "after"
		return nil
	}); err == nil {
		t.Fatal("expected lock symlink rejection")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("lock symlink target changed: %q", data)
	}
	info, err := os.Stat(victim)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("lock symlink target mode changed to %o", info.Mode().Perm())
	}
}

func TestConfigRepositoryLockTimeoutAndStaleFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := NewConfigRepository(path).Write(context.Background(), Config{}, false); err != nil {
		t.Fatal(err)
	}
	lock, err := acquireConfigFileLock(context.Background(), path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	repo := NewConfigRepository(path)
	repo.LockTimeout = 50 * time.Millisecond
	_, err = repo.Update(context.Background(), func(cfg *Config) error {
		cfg.CurrentCluster = "blocked"
		return nil
	})
	lock.release()
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock timeout error = %v", err)
	}

	// Advisory ownership, not lock-file existence, controls exclusion.
	if err := os.WriteFile(path+".lock", []byte("stale metadata\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Update(context.Background(), func(cfg *Config) error {
		cfg.CurrentCluster = "after-stale-lock"
		return nil
	}); err != nil {
		t.Fatalf("stale lock file blocked update: %v", err)
	}
}

func TestConfigRepositoryPreservesUnknownYAMLMappingFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `role: edge
future_top:
  enabled: true
clusters:
  home:
    cluster_id: cluster-home
    future_cluster: keep
    namespaces:
      default:
        discovery: enabled
        future_namespace:
          mode: keep
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewConfigRepository(path).Update(context.Background(), func(cfg *Config) error {
		cfg.CurrentCluster = "home"
		cfg.CurrentNamespace = "default"
		cluster := cfg.Clusters["home"]
		namespace := cluster.Namespaces["default"]
		namespace.ConnectPolicy = ConnectPolicyInviteOnly
		cluster.Namespaces["default"] = namespace
		cfg.Clusters["home"] = cluster
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"future_top", "future_cluster", "future_namespace", "current_cluster", "current_namespace"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("updated YAML lost %q:\n%s", want, encoded)
		}
	}
	cfg, err := LoadFile(path)
	if err != nil || cfg.CurrentCluster != "home" || cfg.CurrentNamespace != "default" || cfg.Clusters["home"].Namespaces["default"].ConnectPolicy != ConnectPolicyInviteOnly {
		t.Fatalf("updated config = %#v, %v", cfg, err)
	}
}

func TestConfigWriteFileIsAtomicAndCreateOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "config.yaml")
	if err := WriteFile(path, Config{CurrentCluster: "first"}, false); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, Config{CurrentCluster: "second"}, false); err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("create-only error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := WriteFile(path, Config{CurrentCluster: "second"}, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadFile(path)
	if err != nil || cfg.CurrentCluster != "second" {
		t.Fatalf("forced config = %#v, %v", cfg, err)
	}
	assertPrivateConfigModes(t, path)
}

func assertPrivateConfigModes(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("config directory mode = %o, want 700", got)
	}
}
