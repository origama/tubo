package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
)

func writePipePersistenceConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := cfgpkg.Config{
		CurrentCluster:   "solar",
		CurrentNamespace: "default",
		Clusters: map[string]cfgpkg.Cluster{
			"solar": {ClusterID: "cluster-solar", Namespaces: map[string]cfgpkg.Namespace{"default": {Services: map[string]cfgpkg.NamespaceService{"api": {Target: "http://127.0.0.1:8080"}}}}},
		},
	}
	if err := cfgpkg.NewConfigRepository(path).Write(context.Background(), cfg, false); err != nil {
		t.Fatal(err)
	}
	return path
}

func testPipeState(name, service, local string) detachedProcessState {
	return detachedProcessState{Name: name, Service: service, ServiceID: "service-" + service, ServiceKind: "tcp", Cluster: "solar", Namespace: "default", Local: local, Path: "relayed", SelectedAddr: "/dns4/relay.example/tcp/4001", SelectedPath: "relayed"}
}

func TestPipeCreatePreservesConcurrentDisjointMutation(t *testing.T) {
	path := writePipePersistenceConfig(t)
	stale, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := cfgpkg.NewConfigRepository(path)
	if _, err := repo.Update(context.Background(), func(cfg *cfgpkg.Config) error {
		cluster := cfg.Clusters["solar"]
		cluster.DiscoveryQueryPeers = []string{"/dns4/authority.example/tcp/4001"}
		ns := cluster.Namespaces["default"]
		svc := ns.Services["api"]
		svc.GrantServicePeer = "12D3KooWAuthority"
		ns.Services["api"] = svc
		cluster.Namespaces["default"] = ns
		cfg.Clusters["solar"] = cluster
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: stale.CurrentCluster, Namespace: stale.CurrentNamespace}, testPipeState("connect-api-4101", "api", "127.0.0.1:4101")); err != nil {
		t.Fatal(err)
	}
	stored, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cluster := stored.Clusters["solar"]
	if _, ok := cluster.Namespaces["default"].Pipes["connect-api-4101"]; !ok {
		t.Fatal("pipe missing")
	}
	if cluster.Namespaces["default"].Services["api"].GrantServicePeer != "12D3KooWAuthority" || len(cluster.DiscoveryQueryPeers) != 1 {
		t.Fatalf("concurrent mutation lost: %#v", cluster)
	}
}

func TestConcurrentDifferentPipeCreatesPreserveBoth(t *testing.T) {
	path := writePipePersistenceConfig(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	create := func(state detachedProcessState) {
		ready.Done()
		<-start
		_, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: state.Service, Cluster: "solar", Namespace: "default"}, state)
		errs <- err
	}
	go create(testPipeState("connect-api-4101", "api", "127.0.0.1:4101"))
	go create(testPipeState("connect-db-4102", "db", "127.0.0.1:4102"))
	ready.Wait()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	stored, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	pipes := stored.Clusters["solar"].Namespaces["default"].Pipes
	if len(pipes) != 2 || pipes["connect-api-4101"].ServiceRef != "api" || pipes["connect-db-4102"].ServiceRef != "db" {
		t.Fatalf("concurrent pipe definitions lost: %#v", pipes)
	}
}

func TestPipeRollbackPreservesConcurrentReplacement(t *testing.T) {
	path := writePipePersistenceConfig(t)
	_, _, mutation, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: "solar", Namespace: "default"}, testPipeState("connect-api-4101", "api", "127.0.0.1:4101"))
	if err != nil {
		t.Fatal(err)
	}
	replacement := mutation.Definition
	replacement.SelectedAddr = "/dns4/new-relay.example/tcp/4001"
	replacement.UpdatedAt = replacement.UpdatedAt.Add(time.Second)
	if _, err := updatePipeConfig(path, func(cfg *cfgpkg.Config) error {
		cluster := cfg.Clusters["solar"]
		ns := cluster.Namespaces["default"]
		ns.Pipes[mutation.Name] = replacement
		cluster.Namespaces["default"] = ns
		cfg.Clusters["solar"] = cluster
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	changed, err := rollbackPipeDefinition(path, mutation)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("rollback changed concurrent replacement")
	}
	stored, err := loadPipeDefinition(path, "solar", "default", mutation.Name)
	if err != nil {
		t.Fatal(err)
	}
	if stored.SelectedAddr != replacement.SelectedAddr {
		t.Fatalf("replacement lost: %#v", stored)
	}
}

func TestDetachedConnectFailureRollsBackOwnPipeAndPreservesOtherState(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	path := writePipePersistenceConfig(t)
	args := []string{"api", "--config", path, "--cluster", "solar", "--namespace", "default", "--local", "127.0.0.1:4101", "--cached-only"}
	oldStart := startDetachedProcessWithTimeoutFn
	startDetachedProcessWithTimeoutFn = func(detachedSpec, time.Duration) (detachedProcessState, error) {
		return detachedProcessState{}, errors.New("readiness failed")
	}
	t.Cleanup(func() { startDetachedProcessWithTimeoutFn = oldStart })
	if err := detachConnectCommand(args, globalCLIOptions{}); err == nil || !strings.Contains(err.Error(), "readiness failed") {
		t.Fatalf("expected readiness failure, got %v", err)
	}
	stored, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Clusters["solar"].Namespaces["default"].Pipes) != 0 {
		t.Fatalf("own pipe not rolled back: %#v", stored.Clusters["solar"].Namespaces["default"].Pipes)
	}
	if stored.Clusters["solar"].Namespaces["default"].Services["api"].Target == "" {
		t.Fatal("unrelated config state lost")
	}
	if entries, err := os.ReadDir(processRunDir()); err == nil && len(entries) != 0 {
		t.Fatalf("unexpected pid artifacts: %#v", entries)
	}
}

func TestDetachedConnectFailureDoesNotRollbackConcurrentReplacement(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	path := writePipePersistenceConfig(t)
	args := []string{"api", "--config", path, "--cluster", "solar", "--namespace", "default", "--local", "127.0.0.1:4101", "--cached-only"}
	oldStart := startDetachedProcessWithTimeoutFn
	startDetachedProcessWithTimeoutFn = func(spec detachedSpec, _ time.Duration) (detachedProcessState, error) {
		_, err := updatePipeConfig(path, func(cfg *cfgpkg.Config) error {
			cluster := cfg.Clusters["solar"]
			ns := cluster.Namespaces["default"]
			replacement := ns.Pipes[spec.State.Name]
			replacement.SelectedAddr = "/dns4/replacement.example/tcp/4001"
			replacement.UpdatedAt = replacement.UpdatedAt.Add(time.Second)
			ns.Pipes[spec.State.Name] = replacement
			cluster.Namespaces["default"] = ns
			cfg.Clusters["solar"] = cluster
			return nil
		})
		if err != nil {
			return detachedProcessState{}, err
		}
		return detachedProcessState{}, errors.New("readiness failed")
	}
	t.Cleanup(func() { startDetachedProcessWithTimeoutFn = oldStart })
	if err := detachConnectCommand(args, globalCLIOptions{}); err == nil || !strings.Contains(err.Error(), "readiness failed") {
		t.Fatalf("expected readiness failure, got %v", err)
	}
	stored, err := loadPipeDefinition(path, "solar", "default", "connect-api-4101")
	if err != nil {
		t.Fatal(err)
	}
	if stored.SelectedAddr != "/dns4/replacement.example/tcp/4001" {
		t.Fatalf("concurrent replacement was rolled back: %#v", stored)
	}
}

func TestPipeRuntimePersistenceUpdatesOnlySelectedPipe(t *testing.T) {
	path := writePipePersistenceConfig(t)
	first := testPipeState("connect-api-4101", "api", "127.0.0.1:4101")
	second := testPipeState("connect-db-4102", "db", "127.0.0.1:4102")
	if _, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: "solar", Namespace: "default"}, first); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "db", Cluster: "solar", Namespace: "default"}, second); err != nil {
		t.Fatal(err)
	}
	expected, err := loadPipeDefinition(path, "solar", "default", first.Name)
	if err != nil {
		t.Fatal(err)
	}
	otherBefore, err := loadPipeDefinition(path, "solar", "default", second.Name)
	if err != nil {
		t.Fatal(err)
	}
	runtime := first
	runtime.SelectedAddr = "/dns4/new-relay.example/tcp/4001"
	if err := persistPipeRuntimeState(path, serviceScope{Cluster: "solar", Namespace: "default"}, first.Name, expected, runtime); err != nil {
		t.Fatal(err)
	}
	firstAfter, err := loadPipeDefinition(path, "solar", "default", first.Name)
	if err != nil {
		t.Fatal(err)
	}
	otherAfter, err := loadPipeDefinition(path, "solar", "default", second.Name)
	if err != nil {
		t.Fatal(err)
	}
	if firstAfter.SelectedAddr != runtime.SelectedAddr {
		t.Fatalf("selected pipe not updated: %#v", firstAfter)
	}
	if !pipeDefinitionsEqual(otherAfter, otherBefore) {
		t.Fatalf("other pipe changed: before=%#v after=%#v", otherBefore, otherAfter)
	}
}

func TestPipeLifecycleUsesPersistedScopeAfterCurrentScopeChanges(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	path := writePipePersistenceConfig(t)
	state := testPipeState("connect-api-4101", "api", "127.0.0.1:4101")
	if _, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: "solar", Namespace: "default"}, state); err != nil {
		t.Fatal(err)
	}
	if _, err := updatePipeConfig(path, func(cfg *cfgpkg.Config) error {
		cfg.CurrentCluster = "lunar"
		cfg.CurrentNamespace = "other"
		cfg.Clusters["lunar"] = cfgpkg.Cluster{ClusterID: "cluster-lunar", Namespaces: map[string]cfgpkg.Namespace{"other": {}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldStart := startPipeDetachedProcessFn
	var captured detachedSpec
	startPipeDetachedProcessFn = func(spec detachedSpec) (detachedProcessState, error) {
		captured = spec
		return spec.State, nil
	}
	t.Cleanup(func() { startPipeDetachedProcessFn = oldStart })
	if _, err := startPipeLifecycle(state.Name, path); err != nil {
		t.Fatal(err)
	}
	if captured.State.Cluster != "solar" || captured.State.Namespace != "default" {
		t.Fatalf("current scope leaked into lifecycle: %#v", captured.State)
	}
	args := strings.Join(captured.ChildArgs, " ")
	if !strings.Contains(args, "--cluster solar") || !strings.Contains(args, "--namespace default") {
		t.Fatalf("persisted scope absent from child args: %v", captured.ChildArgs)
	}
}

func TestDeletePipePreservesConcurrentDisjointMutationAndIsIdempotent(t *testing.T) {
	path := writePipePersistenceConfig(t)
	state := testPipeState("connect-api-4101", "api", "127.0.0.1:4101")
	if _, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: "solar", Namespace: "default"}, state); err != nil {
		t.Fatal(err)
	}
	if _, err := updatePipeConfig(path, func(cfg *cfgpkg.Config) error {
		cluster := cfg.Clusters["solar"]
		cluster.AuthorityPublicKey = "new-authority"
		cfg.Clusters["solar"] = cluster
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := deletePipeDefinition(path, "solar", "default", state.Name); err != nil {
		t.Fatal(err)
	}
	if err := deletePipeDefinition(path, "solar", "default", state.Name); err != nil {
		t.Fatalf("idempotent delete failed: %v", err)
	}
	stored, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Clusters["solar"].AuthorityPublicKey != "new-authority" {
		t.Fatal("disjoint update lost during remove")
	}
}

func TestPipePersistenceErrorsFailClosed(t *testing.T) {
	t.Run("malformed config", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("clusters: [\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: "solar", Namespace: "default"}, testPipeState("connect-api-4101", "api", "127.0.0.1:4101"))
		if err == nil {
			t.Fatal("malformed config reported success")
		}
	})

	t.Run("write failure", func(t *testing.T) {
		path := writePipePersistenceConfig(t)
		oldUpdate := updatePipeConfig
		updatePipeConfig = func(string, cfgpkg.ConfigMutation) (cfgpkg.Config, error) {
			return cfgpkg.Config{}, errors.New("injected atomic write failure")
		}
		t.Cleanup(func() { updatePipeConfig = oldUpdate })
		_, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: "solar", Namespace: "default"}, testPipeState("connect-api-4101", "api", "127.0.0.1:4101"))
		if err == nil || !strings.Contains(err.Error(), "atomic write failure") {
			t.Fatalf("write failure reported success: %v", err)
		}
		stored, loadErr := cfgpkg.LoadFile(path)
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(stored.Clusters["solar"].Namespaces["default"].Pipes) != 0 {
			t.Fatal("failed write persisted pipe")
		}
	})

	t.Run("permission failure", func(t *testing.T) {
		path := writePipePersistenceConfig(t)
		oldUpdate := updatePipeConfig
		updatePipeConfig = func(string, cfgpkg.ConfigMutation) (cfgpkg.Config, error) {
			return cfgpkg.Config{}, os.ErrPermission
		}
		t.Cleanup(func() { updatePipeConfig = oldUpdate })
		_, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: "solar", Namespace: "default"}, testPipeState("connect-api-4101", "api", "127.0.0.1:4101"))
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("permission failure did not propagate: %v", err)
		}
	})

	for _, tc := range []struct {
		name string
		hook func(*cfgpkg.ConfigRepository)
		want string
	}{
		{name: "lock timeout", hook: func(repo *cfgpkg.ConfigRepository) { repo.LockTimeout = time.Nanosecond }, want: "lock config"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePipePersistenceConfig(t)
			lockRelease := func() {}
			if tc.name == "lock timeout" {
				lockRepo := cfgpkg.NewConfigRepository(path)
				entered := make(chan struct{})
				release := make(chan struct{})
				done := make(chan struct{})
				go func() {
					_, _ = lockRepo.Update(context.Background(), func(*cfgpkg.Config) error { close(entered); <-release; return nil })
					close(done)
				}()
				<-entered
				lockRelease = func() { close(release); <-done }
			}
			t.Cleanup(lockRelease)
			oldFactory := newPipeConfigRepository
			newPipeConfigRepository = func(p string) *cfgpkg.ConfigRepository {
				repo := cfgpkg.NewConfigRepository(p)
				tc.hook(repo)
				return repo
			}
			t.Cleanup(func() { newPipeConfigRepository = oldFactory })
			_, _, _, err := persistPipeDefinitionFromConnect(path, connectCLIRequest{ServiceRef: "api", Cluster: "solar", Namespace: "default"}, testPipeState("connect-api-4101", "api", "127.0.0.1:4101"))
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Fatalf("expected %q, got %v", tc.want, err)
			}
		})
	}
}
