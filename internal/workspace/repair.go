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
	updated, err := w.UpdateConfig(configPath, func(latest *cfgpkg.Config) error {
		now := time.Now().UTC()
		for clusterName, cluster := range latest.Clusters {
			if len(cluster.Namespaces) == 0 {
				continue
			}
			for namespaceName, namespace := range cluster.Namespaces {
				previous := namespace.DiscoverySecretPrevious
				if previous == nil || previous.ExpiresAt.IsZero() || !now.After(previous.ExpiresAt.UTC()) {
					continue
				}
				if path := strings.TrimSpace(previous.File); path != "" {
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
			}
			latest.Clusters[clusterName] = cluster
		}
		return nil
	})
	if err != nil {
		return err
	}
	*cfg = updated
	return nil
}
