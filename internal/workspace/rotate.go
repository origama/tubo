package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
)

func (w *Workspace) RotateNamespaceDiscoverySecret(configPath, resource string, grace time.Duration) (SecretScopeDescription, error) {
	if grace <= 0 {
		return SecretScopeDescription{}, fmt.Errorf("--grace must be > 0")
	}
	secretType, clusterName, namespaceName, err := ParseSecretRef(resource)
	if err != nil {
		return SecretScopeDescription{}, err
	}
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return SecretScopeDescription{}, err
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return SecretScopeDescription{}, fmt.Errorf("cluster %q not found", clusterName)
	}
	if cluster.AuthorityPrivateKeyFile == "" {
		return SecretScopeDescription{}, fmt.Errorf("cluster %q is missing authority private key file; rotation requires local cluster authority material", clusterName)
	}
	if _, err := loadPrivateKey(w.store, cluster.AuthorityPrivateKeyFile); err != nil {
		return SecretScopeDescription{}, fmt.Errorf("load cluster authority key: %w", err)
	}
	namespace, ok := cluster.Namespaces[namespaceName]
	if !ok {
		return SecretScopeDescription{}, fmt.Errorf("namespace %q not found in cluster %q", namespaceName, clusterName)
	}
	if namespace.Discovery != cfgpkg.NamespaceDiscoveryEnabled {
		return SecretScopeDescription{}, fmt.Errorf("namespace %q discovery is not enabled", namespaceName)
	}
	if namespace.DiscoverySecretCurrent == nil {
		return SecretScopeDescription{}, fmt.Errorf("namespace %q is missing discovery_secret_current", namespaceName)
	}
	paths := DerivePaths(configPath)
	currentPath := namespace.DiscoverySecretCurrent.File
	if currentPath == "" {
		currentPath = paths.NamespaceDiscoveryCurrentSecret(clusterName, namespaceName)
	}
	currentBytes, err := cfgpkg.ReadNamespaceDiscoverySecretFile(currentPath)
	if err != nil {
		return SecretScopeDescription{}, fmt.Errorf("read current discovery secret: %w", err)
	}
	previousPath := paths.NamespaceDiscoveryPreviousSecret(clusterName, namespaceName)
	now := time.Now().UTC()
	previousBytes, previousRef, err := cfgpkg.BuildNamespaceDiscoverySecretRefFromBytes(previousPath, currentBytes, namespace.DiscoverySecretCurrent.KeyID, namespace.DiscoverySecretCurrent.CreatedAt, now.Add(grace))
	if err != nil {
		return SecretScopeDescription{}, err
	}
	newCurrentBytes, newCurrentRef, err := cfgpkg.BuildNamespaceDiscoverySecretRef(currentPath, now)
	if err != nil {
		return SecretScopeDescription{}, err
	}

	backupCurrent := append([]byte(nil), currentBytes...)
	var backupPrevious []byte
	hadPreviousFile := false
	if info, err := w.store.Stat(previousPath); err == nil && !info.IsDir() {
		if b, err := w.store.ReadFile(previousPath); err == nil {
			backupPrevious = append([]byte(nil), b...)
			hadPreviousFile = true
		}
	}

	if err := w.store.MkdirAll(filepath.Dir(previousPath), 0700); err != nil {
		return SecretScopeDescription{}, err
	}
	if err := w.store.WriteFile(previousPath, previousBytes, 0600); err != nil {
		return SecretScopeDescription{}, err
	}
	if err := w.store.MkdirAll(filepath.Dir(currentPath), 0700); err != nil {
		return SecretScopeDescription{}, err
	}
	if err := w.store.WriteFile(currentPath, newCurrentBytes, 0600); err != nil {
		_ = rollbackSecretFiles(w.store, currentPath, backupCurrent, previousPath, backupPrevious, hadPreviousFile)
		return SecretScopeDescription{}, err
	}

	namespace.DiscoverySecretCurrent = newCurrentRef
	namespace.DiscoverySecretPrevious = previousRef
	cluster.Namespaces[namespaceName] = namespace
	cfg.Clusters[clusterName] = cluster
	if err := w.SaveConfig(configPath, cfg); err != nil {
		_ = rollbackSecretFiles(w.store, currentPath, backupCurrent, previousPath, backupPrevious, hadPreviousFile)
		return SecretScopeDescription{}, err
	}
	return SecretScopeDescription{
		Type:      secretType,
		Cluster:   clusterName,
		Namespace: namespaceName,
		Current:   describeManagedSecret(clusterName, namespaceName, "current", newCurrentRef),
		Previous:  describeManagedSecret(clusterName, namespaceName, "previous", previousRef),
	}, nil
}

func rollbackSecretFiles(store Store, currentPath string, currentBytes []byte, previousPath string, previousBytes []byte, hadPreviousFile bool) error {
	if err := store.WriteFile(currentPath, currentBytes, 0600); err != nil {
		return err
	}
	if hadPreviousFile {
		return store.WriteFile(previousPath, previousBytes, 0600)
	}
	if err := store.Remove(previousPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
