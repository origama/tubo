package workspace

import (
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type staleLoadStore struct {
	FSStore
	stale cfgpkg.Config
}

func (s staleLoadStore) Load(string) (cfgpkg.Config, error) { return s.stale, nil }

func TestWorkspaceLifecycleMutationsIgnoreStaleLoadSnapshots(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	baseWorkspace := Open(FSStore{})
	if _, err := baseWorkspace.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	stale, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfgpkg.NewConfigRepository(path).Update(t.Context(), func(cfg *cfgpkg.Config) error {
		cfg.Clusters["concurrent"] = cfgpkg.Cluster{ClusterID: "cluster-concurrent", DiscoveryQueryPeers: []string{"/ip4/203.0.113.20/tcp/4001"}, Namespaces: map[string]cfgpkg.Namespace{"default": {}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	workspace := Open(staleLoadStore{stale: stale})
	if _, err := workspace.Use(path, Ref{Kind: "cluster", Name: "home"}); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.CreateNamespace(path, "observability"); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.EnsureService(path, "myapi"); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.RemoveService(path, "myapi"); err != nil {
		t.Fatal(err)
	}

	stored, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	concurrent, ok := stored.Clusters["concurrent"]
	if !ok || concurrent.ClusterID != "cluster-concurrent" || len(concurrent.DiscoveryQueryPeers) != 1 {
		t.Fatalf("disjoint concurrent cluster update was lost: %#v", stored.Clusters)
	}
	if _, ok := stored.Clusters["home"].Namespaces["observability"]; !ok {
		t.Fatalf("namespace transaction missing: %#v", stored.Clusters["home"].Namespaces)
	}
	if _, ok := stored.Clusters["home"].Namespaces["observability"].Services["myapi"]; ok {
		t.Fatal("removed service still present")
	}
}

func TestConcurrentCreateClusterHasOneWinnerAndConsistentAuthority(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	workspace := Open(FSStore{})
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := workspace.CreateCluster(path, "home")
			results <- err
		}()
	}
	close(start)
	first, second := <-results, <-results
	successes := 0
	for _, err := range []error{first, second} {
		if err == nil {
			successes++
		} else if !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("unexpected create error: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creates = %d, want 1", successes)
	}
	stored, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cluster := stored.Clusters["home"]
	privateKey, err := loadPrivateKey(FSStore{}, cluster.AuthorityPrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, err := ssh.NewPublicKey(privateKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	encodedPublic := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(publicKey)))
	if encodedPublic != cluster.AuthorityPublicKey {
		t.Fatal("persisted authority public key does not match private artifact")
	}
}

type recreateOnCleanupStore struct {
	FSStore
	mu       sync.Mutex
	updates  int
	onSecond func() error
}

func (s *recreateOnCleanupStore) Update(path string, mutate cfgpkg.ConfigMutation) (cfgpkg.Config, error) {
	s.mu.Lock()
	s.updates++
	updateNumber := s.updates
	s.mu.Unlock()
	if updateNumber == 2 && s.onSecond != nil {
		if err := s.onSecond(); err != nil {
			return cfgpkg.Config{}, err
		}
	}
	return s.FSStore.Update(path, mutate)
}

func TestRemoveServiceDoesNotDeleteConcurrentlyRecreatedArtifacts(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	workspace := Open(FSStore{})
	if _, err := workspace.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	created, err := workspace.EnsureService(path, "myapi")
	if err != nil {
		t.Fatal(err)
	}
	service := created.Context.Service
	store := &recreateOnCleanupStore{}
	store.onSecond = func() error {
		if err := os.WriteFile(service.ServiceOwnerKeyFile, []byte("new-owner-artifact"), 0o600); err != nil {
			return err
		}
		_, err := store.FSStore.Update(path, func(cfg *cfgpkg.Config) error {
			cluster := cfg.Clusters["home"]
			namespace := cluster.Namespaces["default"]
			if namespace.Services == nil {
				namespace.Services = make(map[string]cfgpkg.NamespaceService)
			}
			namespace.Services["myapi"] = service
			cluster.Namespaces["default"] = namespace
			cfg.Clusters["home"] = cluster
			return nil
		})
		return err
	}
	result, err := Open(store).RemoveService(path, "myapi")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.RemovedPaths) != 0 {
		t.Fatalf("recreated artifacts reported removed: %v", result.RemovedPaths)
	}
	ownerBytes, err := os.ReadFile(service.ServiceOwnerKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(ownerBytes) != "new-owner-artifact" {
		t.Fatalf("recreated artifact changed: %q", ownerBytes)
	}
	stored, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.Clusters["home"].Namespaces["default"].Services["myapi"]; !ok {
		t.Fatal("concurrently recreated service definition was lost")
	}
}

type commitFailAfterMutationStore struct {
	FSStore
	err error
}

func (s commitFailAfterMutationStore) Update(path string, mutate cfgpkg.ConfigMutation) (cfgpkg.Config, error) {
	cfg, err := s.FSStore.Load(path)
	if err != nil {
		return cfgpkg.Config{}, err
	}
	if err := mutate(&cfg); err != nil {
		return cfgpkg.Config{}, err
	}
	return cfgpkg.Config{}, s.err
}

func TestRotateSecretRollsBackArtifactsWhenConfigCommitFails(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	workspace := Open(FSStore{})
	if _, err := workspace.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	before, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	currentRef := before.Clusters["home"].Namespaces["default"].DiscoverySecretCurrent
	currentBytes, err := os.ReadFile(currentRef.File)
	if err != nil {
		t.Fatal(err)
	}
	previousPath := DerivePaths(path).NamespaceDiscoveryPreviousSecret("home", "default")
	failing := Open(commitFailAfterMutationStore{err: errors.New("commit failed")})
	if _, err := failing.RotateNamespaceDiscoverySecret(path, "secret/namespace-discovery/home/default", time.Hour); err == nil || !strings.Contains(err.Error(), "commit failed") {
		t.Fatalf("rotation error = %v", err)
	}
	afterBytes, err := os.ReadFile(currentRef.File)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBytes) != string(currentBytes) {
		t.Fatal("current secret was not restored after config commit failure")
	}
	if _, err := os.Stat(previousPath); !os.IsNotExist(err) {
		t.Fatalf("new previous secret remained after rollback: %v", err)
	}
	after, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Clusters["home"].Namespaces["default"].DiscoverySecretPrevious != nil {
		t.Fatal("failed rotation changed persisted config")
	}
}

func TestExpiredSecretCleanupMergesLatestConfigIntoCaller(t *testing.T) {
	path := writeTestConfig(t, cfgpkg.Config{})
	workspace := Open(FSStore{})
	if _, err := workspace.CreateCluster(path, "home"); err != nil {
		t.Fatal(err)
	}
	stale, err := cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cluster := stale.Clusters["home"]
	namespace := cluster.Namespaces["default"]
	previousPath := DerivePaths(path).NamespaceDiscoveryPreviousSecret("home", "default")
	if err := os.WriteFile(previousPath, []byte("expired"), 0o600); err != nil {
		t.Fatal(err)
	}
	namespace.DiscoverySecretPrevious = &cfgpkg.ManagedSecretRef{Type: cfgpkg.SecretTypeNamespaceDiscovery, KeyID: "expired", File: previousPath, ExpiresAt: time.Now().Add(-time.Minute)}
	cluster.Namespaces["default"] = namespace
	stale.Clusters["home"] = cluster
	if err := workspace.SaveConfig(path, stale); err != nil {
		t.Fatal(err)
	}
	stale, err = cfgpkg.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = cfgpkg.NewConfigRepository(path).Update(t.Context(), func(cfg *cfgpkg.Config) error {
		cfg.Clusters["concurrent"] = cfgpkg.Cluster{ClusterID: "cluster-concurrent"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := workspace.cleanupExpiredNamespaceDiscoverySecrets(path, &stale); err != nil {
		t.Fatal(err)
	}
	if _, ok := stale.Clusters["concurrent"]; !ok {
		t.Fatal("cleanup did not return latest disjoint config state")
	}
	if stale.Clusters["home"].Namespaces["default"].DiscoverySecretPrevious != nil {
		t.Fatal("expired previous secret metadata remains")
	}
}
