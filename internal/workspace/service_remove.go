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
	cfg, err := w.LoadConfigOrError(configPath)
	if err != nil {
		return RemoveServiceResult{}, err
	}
	ctx, err := w.resolveServiceContext(cfg, "", "", serviceName)
	if err != nil {
		return RemoveServiceResult{}, err
	}
	updated := ctx.Config
	cluster := updated.Clusters[ctx.ClusterName]
	namespace := cluster.Namespaces[ctx.Namespace]
	svc := namespace.Services[ctx.Name]
	delete(namespace.Services, ctx.Name)
	cluster.Namespaces[ctx.Namespace] = namespace
	updated.Clusters[ctx.ClusterName] = cluster
	if strings.TrimSpace(updated.Service.Name) == ctx.Name {
		updated.Service = cfgpkg.Service{}
	}
	if err := w.SaveConfig(configPath, updated); err != nil {
		return RemoveServiceResult{}, err
	}
	removedPaths := make([]string, 0, 3)
	seen := map[string]struct{}{}
	for _, path := range []string{svc.ServiceOwnerKeyFile, svc.ServiceClaimFile, svc.ServicePublishLeaseFile} {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		if err := removePathIfExists(w.store, path); err != nil {
			return RemoveServiceResult{Config: updated, Context: ctx, RemovedPaths: removedPaths}, fmt.Errorf("saved updated service config, but partial cleanup failed removing %s: %w", path, err)
		}
		removedPaths = append(removedPaths, path)
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
