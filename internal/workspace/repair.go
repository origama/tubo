package workspace

import (
	"fmt"
	"os"
	"strings"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
)

func (w *Workspace) cleanupExpiredNamespaceDiscoverySecrets(configPath string, cfg *cfgpkg.Config) error {
	if cfg == nil {
		return nil
	}
	now := time.Now().UTC()
	changed := false
	for clusterName, cluster := range cfg.Clusters {
		if len(cluster.Namespaces) == 0 {
			continue
		}
		for namespaceName, namespace := range cluster.Namespaces {
			prev := namespace.DiscoverySecretPrevious
			if prev == nil || prev.ExpiresAt.IsZero() || !now.After(prev.ExpiresAt.UTC()) {
				continue
			}
			if path := strings.TrimSpace(prev.File); path != "" {
				currentPath := ""
				if namespace.DiscoverySecretCurrent != nil {
					currentPath = strings.TrimSpace(namespace.DiscoverySecretCurrent.File)
				}
				if path != currentPath {
					info, err := w.store.Stat(path)
					if err == nil {
						if !info.IsDir() {
							if err := w.store.Remove(path); err != nil && !os.IsNotExist(err) {
								return fmt.Errorf("remove expired previous discovery secret for %s/%s: %w", clusterName, namespaceName, err)
							}
						}
					} else if !os.IsNotExist(err) {
						return fmt.Errorf("stat expired previous discovery secret for %s/%s: %w", clusterName, namespaceName, err)
					}
				}
			}
			namespace.DiscoverySecretPrevious = nil
			cluster.Namespaces[namespaceName] = namespace
			changed = true
		}
		cfg.Clusters[clusterName] = cluster
	}
	if !changed {
		return nil
	}
	return w.SaveConfig(configPath, *cfg)
}
