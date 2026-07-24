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

	var description SecretScopeDescription
	var currentPath, previousPath string
	var backupCurrent, backupPrevious []byte
	var hadPreviousFile, filesTouched, rolledBack bool
	_, err = w.UpdateConfig(configPath, func(cfg *cfgpkg.Config) error {
		cluster, ok := cfg.Clusters[clusterName]
		if !ok {
			return fmt.Errorf("cluster %q not found", clusterName)
		}
		if cluster.AuthorityPrivateKeyFile == "" {
			return fmt.Errorf("cluster %q is missing authority private key file; rotation requires local cluster authority material", clusterName)
		}
		if _, err := loadPrivateKey(w.store, cluster.AuthorityPrivateKeyFile); err != nil {
			return fmt.Errorf("load cluster authority key: %w", err)
		}
		namespace, ok := cluster.Namespaces[namespaceName]
		if !ok {
			return fmt.Errorf("namespace %q not found in cluster %q", namespaceName, clusterName)
		}
		if namespace.Discovery != cfgpkg.NamespaceDiscoveryEnabled {
			return fmt.Errorf("namespace %q discovery is not enabled", namespaceName)
		}
		if namespace.DiscoverySecretCurrent == nil {
			return fmt.Errorf("namespace %q is missing discovery_secret_current", namespaceName)
		}
		paths := DerivePaths(configPath)
		currentPath = namespace.DiscoverySecretCurrent.File
		if currentPath == "" {
			currentPath = paths.NamespaceDiscoveryCurrentSecret(clusterName, namespaceName)
		}
		currentBytes, err := cfgpkg.ReadNamespaceDiscoverySecretFile(currentPath)
		if err != nil {
			return fmt.Errorf("read current discovery secret: %w", err)
		}
		previousPath = paths.NamespaceDiscoveryPreviousSecret(clusterName, namespaceName)
		now := time.Now().UTC()
		previousBytes, previousRef, err := cfgpkg.BuildNamespaceDiscoverySecretRefFromBytes(previousPath, currentBytes, namespace.DiscoverySecretCurrent.KeyID, namespace.DiscoverySecretCurrent.CreatedAt, now.Add(grace))
		if err != nil {
			return err
		}
		newCurrentBytes, newCurrentRef, err := cfgpkg.BuildNamespaceDiscoverySecretRef(currentPath, now)
		if err != nil {
			return err
		}

		backupCurrent = append([]byte(nil), currentBytes...)
		backupPrevious = nil
		hadPreviousFile = false
		if info, err := w.store.Stat(previousPath); err == nil {
			if !info.IsDir() {
				b, err := w.store.ReadFile(previousPath)
				if err != nil {
					return fmt.Errorf("backup previous discovery secret: %w", err)
				}
				backupPrevious = append([]byte(nil), b...)
				hadPreviousFile = true
			}
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat previous discovery secret: %w", err)
		}
		if err := w.store.MkdirAll(filepath.Dir(previousPath), 0o700); err != nil {
			return err
		}
		if err := w.store.WriteFile(previousPath, previousBytes, 0o600); err != nil {
			return err
		}
		filesTouched = true
		if err := w.store.MkdirAll(filepath.Dir(currentPath), 0o700); err != nil {
			rolledBack = true
			_ = rollbackSecretFiles(w.store, currentPath, backupCurrent, previousPath, backupPrevious, hadPreviousFile)
			return err
		}
		if err := w.store.WriteFile(currentPath, newCurrentBytes, 0o600); err != nil {
			rolledBack = true
			_ = rollbackSecretFiles(w.store, currentPath, backupCurrent, previousPath, backupPrevious, hadPreviousFile)
			return err
		}

		namespace.DiscoverySecretCurrent = newCurrentRef
		namespace.DiscoverySecretPrevious = previousRef
		cluster.Namespaces[namespaceName] = namespace
		cfg.Clusters[clusterName] = cluster
		description = SecretScopeDescription{
			Type:      secretType,
			Cluster:   clusterName,
			Namespace: namespaceName,
			Current:   describeManagedSecret(clusterName, namespaceName, "current", newCurrentRef),
			Previous:  describeManagedSecret(clusterName, namespaceName, "previous", previousRef),
		}
		return nil
	})
	if err != nil {
		if filesTouched && !rolledBack {
			_ = rollbackSecretFiles(w.store, currentPath, backupCurrent, previousPath, backupPrevious, hadPreviousFile)
		}
		return SecretScopeDescription{}, err
	}
	return description, nil
}

func rollbackSecretFiles(store Store, currentPath string, currentBytes []byte, previousPath string, previousBytes []byte, hadPreviousFile bool) error {
	if err := store.WriteFile(currentPath, currentBytes, 0o600); err != nil {
		return err
	}
	if hadPreviousFile {
		return store.WriteFile(previousPath, previousBytes, 0o600)
	}
	if err := store.Remove(previousPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
