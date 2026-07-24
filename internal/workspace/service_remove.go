package workspace

import (
	"fmt"
	"os"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
)

type RemoveServiceResult struct {
	Config       cfgpkg.Config
	Context      ServiceContext
	RemovedPaths []string
}

func (w *Workspace) RemoveService(configPath, serviceName string) (RemoveServiceResult, error) {
	var ctx ServiceContext
	var svc cfgpkg.NamespaceService
	updated, err := w.UpdateConfig(configPath, func(cfg *cfgpkg.Config) error {
		resolved, err := w.resolveServiceContext(*cfg, "", "", serviceName)
		if err != nil {
			return err
		}
		ctx = resolved
		cluster := cfg.Clusters[ctx.ClusterName]
		namespace := cluster.Namespaces[ctx.Namespace]
		svc = namespace.Services[ctx.Name]
		delete(namespace.Services, ctx.Name)
		cluster.Namespaces[ctx.Namespace] = namespace
		cfg.Clusters[ctx.ClusterName] = cluster
		if strings.TrimSpace(cfg.Service.Name) == ctx.Name {
			cfg.Service = cfgpkg.Service{}
		}
		return nil
	})
	if err != nil {
		return RemoveServiceResult{}, err
	}

	removedPaths := make([]string, 0, 3)
	_, err = w.UpdateConfig(configPath, func(latest *cfgpkg.Config) error {
		cluster, ok := latest.Clusters[ctx.ClusterName]
		if !ok {
			return nil
		}
		namespace, ok := cluster.Namespaces[ctx.Namespace]
		if !ok {
			return nil
		}
		// Another transaction may have recreated this service after deletion.
		// Never remove artifacts while any current definition owns the name.
		if _, recreated := namespace.Services[ctx.Name]; recreated {
			return nil
		}
		seen := map[string]struct{}{}
		for _, path := range []string{svc.ServiceOwnerKeyFile, svc.ServiceClaimFile, svc.ServicePublishLeaseFile} {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			if _, exists := seen[path]; exists {
				continue
			}
			seen[path] = struct{}{}
			if err := removePathIfExists(w.store, path); err != nil {
				return fmt.Errorf("saved updated service config, but partial cleanup failed removing %s: %w", path, err)
			}
			removedPaths = append(removedPaths, path)
		}
		return nil
	})
	if err != nil {
		return RemoveServiceResult{Config: updated, Context: ctx, RemovedPaths: removedPaths}, err
	}
	return RemoveServiceResult{Config: updated, Context: ctx, RemovedPaths: removedPaths}, nil
}

func removePathIfExists(store Store, path string) error {
	if path == "" {
		return nil
	}
	if err := store.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
