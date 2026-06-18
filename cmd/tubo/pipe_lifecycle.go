package main

import (
	"fmt"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
)

func pipeLifecycleDefinition(configPath, pipeName string) (serviceScope, cfgpkg.NamespacePipe, error) {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return serviceScope{}, cfgpkg.NamespacePipe{}, err
	}
	scope, err := resolveServiceScope(cfg, "", "", false)
	if err != nil {
		return serviceScope{}, cfgpkg.NamespacePipe{}, err
	}
	def, err := loadPipeDefinition(configPath, scope.Cluster, scope.Namespace, pipeName)
	if err != nil {
		return serviceScope{}, cfgpkg.NamespacePipe{}, err
	}
	return scope, def, nil
}

func pipeLifecycleIncompleteError(pipeName string, missing []string) error {
	return fmt.Errorf("pipe/%s is incomplete: missing %s; inspect pipe/%s to review the saved definition", pipeName, strings.Join(missing, ", "), pipeName)
}

func pipeLifecycleRequest(configPath string, scope serviceScope, def cfgpkg.NamespacePipe) connectCLIRequest {
	serviceRef := firstNonEmpty(strings.TrimSpace(def.ServiceRef), strings.TrimSpace(def.ServiceID))
	return connectCLIRequest{
		ServiceRef: serviceRef,
		Local:      normalizeConnectProcessLocal(def.Local),
		ConfigPath: configPath,
		Timeout:    defaultDiscoveryTimeout,
		Cluster:    strings.TrimSpace(scope.Cluster),
		Namespace:  strings.TrimSpace(scope.Namespace),
	}
}

func pipeLifecycleChildArgs(configPath string, scope serviceScope, def cfgpkg.NamespacePipe) []string {
	serviceRef := firstNonEmpty(strings.TrimSpace(def.ServiceRef), strings.TrimSpace(def.ServiceID))
	args := make([]string, 0, 8)
	if serviceRef != "" {
		args = append(args, serviceRef)
	}
	args = append(args, "--config", configPath)
	if local := normalizeConnectProcessLocal(def.Local); local != "" {
		args = append(args, "--local", local)
	}
	if scope.Cluster != "" {
		args = append(args, "--cluster", scope.Cluster)
	}
	if scope.Namespace != "" {
		args = append(args, "--namespace", scope.Namespace)
	}
	return args
}

var startPipeDetachedProcessFn = startDetachedProcess

func startPipeLifecycle(pipeName, configPath string) (detachedProcessState, error) {
	scope, def, err := pipeLifecycleDefinition(configPath, pipeName)
	if err != nil {
		return detachedProcessState{}, err
	}
	if missing := pipeDefinitionMissingFields(def); len(missing) > 0 {
		return detachedProcessState{}, pipeLifecycleIncompleteError(pipeName, missing)
	}
	req := pipeLifecycleRequest(configPath, scope, def)
	spec, err := buildDetachedConnectSpec(req, pipeLifecycleChildArgs(configPath, scope, def))
	if err != nil {
		return detachedProcessState{}, err
	}
	return startPipeDetachedProcessFn(spec)
}

func pipeLifecycleLiveViews(pipeName string) ([]processView, error) {
	views, err := listProcessViews(true)
	if err != nil {
		return nil, err
	}
	var live []processView
	for _, view := range views {
		if !pipeLifecycleMatches(view, pipeName) {
			continue
		}
		if view.Status == "running" || view.Status == "degraded" {
			live = append(live, view)
		}
	}
	return live, nil
}

func pipeLifecycleMatches(view processView, pipeName string) bool {
	if view.Command != "connect" || view.ResourceKind != "pipe" {
		return false
	}
	trimmed := strings.TrimSpace(view.Name)
	if trimmed == pipeName {
		return true
	}
	if strings.TrimPrefix(trimmed, "connect-") == pipeName {
		return true
	}
	return false
}

func stopPipeRuntime(pipeName string, force bool) (detachedProcessState, error) {
	live, err := pipeLifecycleLiveViews(pipeName)
	if err != nil {
		return detachedProcessState{}, err
	}
	switch len(live) {
	case 0:
		return detachedProcessState{}, fmt.Errorf("no matching runtime process exists for pipe/%s", pipeName)
	case 1:
		state, err := stopView(live[0], force)
		if err != nil {
			return detachedProcessState{}, err
		}
		return state, nil
	default:
		return detachedProcessState{}, fmt.Errorf("pipe/%s matches multiple live runtimes; stop a specific process instead", pipeName)
	}
}

func restartPipeLifecycle(pipeName, configPath string) (detachedProcessState, error) {
	_, def, err := pipeLifecycleDefinition(configPath, pipeName)
	if err != nil {
		return detachedProcessState{}, err
	}
	if missing := pipeDefinitionMissingFields(def); len(missing) > 0 {
		return detachedProcessState{}, pipeLifecycleIncompleteError(pipeName, missing)
	}
	live, err := pipeLifecycleLiveViews(pipeName)
	if err != nil {
		return detachedProcessState{}, err
	}
	if len(live) > 1 {
		return detachedProcessState{}, fmt.Errorf("pipe/%s matches multiple live runtimes; stop a specific process instead", pipeName)
	}
	if len(live) == 1 {
		stopped, err := stopPipeRuntime(pipeName, false)
		if err != nil {
			return detachedProcessState{}, err
		}
		fmt.Printf("stopped %s\n", stopped.ID)
	}
	return startPipeLifecycle(pipeName, configPath)
}

func rmPipeLifecycle(pipeName, configPath string, force bool) (detachedProcessState, error) {
	scope, _, err := pipeLifecycleDefinition(configPath, pipeName)
	if err != nil {
		return detachedProcessState{}, err
	}
	live, err := pipeLifecycleLiveViews(pipeName)
	if err != nil {
		return detachedProcessState{}, err
	}
	switch len(live) {
	case 0:
		// no live runtime
	case 1:
		if !force {
			return detachedProcessState{}, fmt.Errorf("pipe/%s is running or degraded; use --force to stop and remove it", pipeName)
		}
		stopped, err := stopPipeRuntime(pipeName, true)
		if err != nil {
			return detachedProcessState{}, err
		}
		if err := deletePipeDefinition(configPath, scope.Cluster, scope.Namespace, pipeName); err != nil {
			return detachedProcessState{}, err
		}
		return stopped, nil
	default:
		return detachedProcessState{}, fmt.Errorf("pipe/%s matches multiple live runtimes; stop a specific process instead", pipeName)
	}
	if err := deletePipeDefinition(configPath, scope.Cluster, scope.Namespace, pipeName); err != nil {
		return detachedProcessState{}, err
	}
	return detachedProcessState{}, nil
}

func deletePipeDefinition(configPath, clusterName, namespaceName, name string) error {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return fmt.Errorf("cluster %q not found in config", clusterName)
	}
	namespace, ok := cluster.Namespaces[namespaceName]
	if !ok {
		return fmt.Errorf("namespace %q not found in cluster %q", namespaceName, clusterName)
	}
	if _, ok := namespace.Pipes[name]; !ok {
		return fmt.Errorf("pipe %q not found in cluster %q namespace %q", name, clusterName, namespaceName)
	}
	delete(namespace.Pipes, name)
	cluster.Namespaces[namespaceName] = namespace
	cfg.Clusters[clusterName] = cluster
	return saveLocalConfig(configPath, cfg)
}
