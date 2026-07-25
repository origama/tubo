package main

import (
	"fmt"
	"strings"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
	processes "github.com/origama/tubo/internal/processes"
)

func pipeLifecycleDefinition(configPath, pipeName string) (serviceScope, cfgpkg.NamespacePipe, error) {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return serviceScope{}, cfgpkg.NamespacePipe{}, err
	}
	location, def, err := locatePipeDefinition(cfg, pipeName)
	if err == nil {
		return serviceScope{Cluster: location.Cluster, Namespace: location.Namespace}, def, nil
	}
	// Keep legacy current-scope error wording when no cross-scope definition exists.
	scope, scopeErr := resolveServiceScope(cfg, "", "", false)
	if scopeErr != nil {
		return serviceScope{}, cfgpkg.NamespacePipe{}, scopeErr
	}
	def, scopeErr = pipeDefinitionInConfig(cfg, scope.Cluster, scope.Namespace, pipeName)
	if scopeErr != nil {
		return serviceScope{}, cfgpkg.NamespacePipe{}, scopeErr
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
	spec.State.PrimaryKind = "pipe"
	spec.State.PrimaryName = pipeName
	spec.State.PrimaryRef = "pipe/" + pipeName
	spec.State.PrimaryID = ""
	spec.State.Purpose = "pipe-runtime"
	state, err := startPipeDetachedProcessFn(spec)
	if err != nil {
		return detachedProcessState{}, err
	}
	if err := persistPipeRuntimeState(configPath, scope, pipeName, def, state); err != nil {
		if stopErr := stopStartedPipeProcess(state); stopErr != nil {
			return detachedProcessState{}, fmt.Errorf("persist started pipe definition: %w (also failed to stop started process: %v)", err, stopErr)
		}
		return detachedProcessState{}, fmt.Errorf("persist started pipe definition: %w", err)
	}
	return state, nil
}

func persistPipeRuntimeState(configPath string, scope serviceScope, pipeName string, expected cfgpkg.NamespacePipe, state detachedProcessState) error {
	_, err := updatePipeConfig(configPath, func(cfg *cfgpkg.Config) error {
		cluster, ok := cfg.Clusters[scope.Cluster]
		if !ok {
			return fmt.Errorf("cluster %q not found in config", scope.Cluster)
		}
		namespace, ok := cluster.Namespaces[scope.Namespace]
		if !ok {
			return fmt.Errorf("namespace %q not found in cluster %q", scope.Namespace, scope.Cluster)
		}
		current, ok := namespace.Pipes[pipeName]
		if !ok {
			return fmt.Errorf("pipe %q not found in cluster %q namespace %q", pipeName, scope.Cluster, scope.Namespace)
		}
		if !pipeDefinitionMatchesLoaded(current, expected) {
			return fmt.Errorf("pipe %q changed while runtime was starting; started process was stopped and replacement definition preserved", pipeName)
		}
		updated := current
		updated.ServiceRef = firstNonEmpty(strings.TrimSpace(state.Service), updated.ServiceRef)
		updated.ServiceID = firstNonEmpty(strings.TrimSpace(state.ServiceID), updated.ServiceID)
		if state.ServiceKind != "" {
			updated.ServiceKind = cfgpkg.ServiceKind(strings.TrimSpace(state.ServiceKind))
		}
		updated.Local = firstNonEmpty(normalizeConnectProcessLocal(state.Local), updated.Local)
		updated.Path = firstNonEmpty(strings.TrimSpace(state.Path), updated.Path)
		updated.SelectedAddr = firstNonEmpty(strings.TrimSpace(state.SelectedAddr), updated.SelectedAddr)
		updated.SelectedPath = firstNonEmpty(strings.TrimSpace(state.SelectedPath), updated.SelectedPath)
		updated.UpdatedAt = nowUTC()
		namespace.Pipes[pipeName] = updated
		cluster.Namespaces[scope.Namespace] = namespace
		cfg.Clusters[scope.Cluster] = cluster
		return nil
	})
	return err
}

var nowUTC = func() time.Time { return time.Now().UTC() }

func stopStartedPipeProcess(state detachedProcessState) error {
	if strings.TrimSpace(state.StateFile) != "" || state.PID > 0 {
		return processes.StopState(state, processSystemAdapter{}, true)
	}
	return nil
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
	return strings.TrimPrefix(trimmed, "connect-") == pipeName
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
	scope, def, err := pipeLifecycleDefinition(configPath, pipeName)
	if err != nil {
		return detachedProcessState{}, err
	}
	live, err := pipeLifecycleLiveViews(pipeName)
	if err != nil {
		return detachedProcessState{}, err
	}
	var stopped detachedProcessState
	switch len(live) {
	case 0:
	case 1:
		if !force {
			return detachedProcessState{}, fmt.Errorf("pipe/%s is running or degraded; use --force to stop and remove it", pipeName)
		}
		stopped, err = stopPipeRuntime(pipeName, true)
		if err != nil {
			return detachedProcessState{}, err
		}
	default:
		return detachedProcessState{}, fmt.Errorf("pipe/%s matches multiple live runtimes; stop a specific process instead", pipeName)
	}
	if err := deletePipeDefinition(configPath, scope.Cluster, scope.Namespace, pipeName, &def); err != nil {
		return detachedProcessState{}, err
	}
	return stopped, nil
}

func deletePipeDefinition(configPath, clusterName, namespaceName, name string, expected ...*cfgpkg.NamespacePipe) error {
	_, err := updatePipeConfig(configPath, func(cfg *cfgpkg.Config) error {
		cluster, ok := cfg.Clusters[clusterName]
		if !ok {
			return fmt.Errorf("cluster %q not found in config", clusterName)
		}
		namespace, ok := cluster.Namespaces[namespaceName]
		if !ok {
			return fmt.Errorf("namespace %q not found in cluster %q", namespaceName, clusterName)
		}
		current, ok := namespace.Pipes[name]
		if !ok {
			return nil
		}
		if len(expected) > 0 && expected[0] != nil && !pipeDefinitionMatchesLoaded(current, *expected[0]) {
			return fmt.Errorf("pipe %q changed before removal; replacement definition preserved", name)
		}
		delete(namespace.Pipes, name)
		cluster.Namespaces[namespaceName] = namespace
		cfg.Clusters[clusterName] = cluster
		return nil
	})
	return err
}
