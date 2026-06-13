package main

import (
	"os"

	cfgpkg "github.com/origama/tubo/internal/config"
	workspace "github.com/origama/tubo/internal/workspace"
)

func localWorkspace() *workspace.Workspace {
	return workspace.Open(workspace.FSStore{})
}

func loadLocalConfigOrError(path string) (cfgpkg.Config, error) {
	if path == "" {
		path = defaultTuboConfigPath()
	}
	return localWorkspace().LoadConfigOrError(path)
}

func saveLocalConfig(path string, cfg cfgpkg.Config) error {
	if path == "" {
		path = defaultTuboConfigPath()
	}
	if _, err := os.Stat(path); err == nil {
		if current, err := cfgpkg.LoadFile(path); err == nil {
			cfg = preserveLocalMembershipGrant(cfg, current)
		}
	}
	return localWorkspace().SaveConfig(path, cfg)
}

func preserveLocalMembershipGrant(next, current cfgpkg.Config) cfgpkg.Config {
	if next.Clusters == nil || len(current.Clusters) == 0 {
		return next
	}
	for name, currentCluster := range current.Clusters {
		nextCluster, ok := next.Clusters[name]
		if !ok || nextCluster.MembershipGrant != nil || currentCluster.MembershipGrant == nil {
			continue
		}
		if currentCluster.ClusterID != "" && nextCluster.ClusterID != "" && currentCluster.ClusterID != nextCluster.ClusterID {
			continue
		}
		grant := *currentCluster.MembershipGrant
		grant.Permissions = append([]string(nil), currentCluster.MembershipGrant.Permissions...)
		grant.GrantServicePeers = append([]string(nil), currentCluster.MembershipGrant.GrantServicePeers...)
		nextCluster.MembershipGrant = &grant
		next.Clusters[name] = nextCluster
	}
	return next
}

func parseLocalResourceRef(resource string) (string, string, error) {
	ref, err := workspace.ParseRef(resource)
	if err != nil {
		return "", "", err
	}
	return ref.Kind, ref.Name, nil
}

func resolveServiceScope(cfg cfgpkg.Config, clusterFlag, namespaceFlag string, allNamespaces bool) (serviceScope, error) {
	scope, err := workspace.ResolveScope(cfg, clusterFlag, namespaceFlag, allNamespaces)
	if err != nil {
		return serviceScope{}, err
	}
	return serviceScope{Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}, nil
}
