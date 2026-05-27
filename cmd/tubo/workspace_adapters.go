package main

import (
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
	return localWorkspace().SaveConfig(path, cfg)
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
