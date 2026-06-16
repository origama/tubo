package main

import (
	"fmt"
	"os"
	"strings"

	processes "github.com/origama/tubo/internal/processes"
	workspace "github.com/origama/tubo/internal/workspace"
)

func stopLifecycleResource(kind, name, configPath string, force bool) (detachedProcessState, error) {
	switch kind {
	case "service":
		return stopServiceLifecycle(name, configPath, force)
	case "pipe":
		return stopPipeLifecycle(name, force)
	default:
		return detachedProcessState{}, fmt.Errorf("unsupported stop resource %q", kind)
	}
}

func stopServiceLifecycle(serviceName, configPath string, force bool) (detachedProcessState, error) {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return detachedProcessState{}, err
	}
	ctx, err := localWorkspace().ResolveServiceContext(configPath, serviceName, cfg.CurrentCluster, cfg.CurrentNamespace)
	if err != nil {
		return detachedProcessState{}, fmt.Errorf("service/%s not found: %w", serviceName, err)
	}
	views, err := listProcessViews(true)
	if err != nil {
		return detachedProcessState{}, err
	}
	var live []processView
	var stale bool
	for _, view := range views {
		if !serviceStopMatches(view, ctx) {
			continue
		}
		if view.Status == "running" || view.Status == "degraded" {
			live = append(live, view)
			continue
		}
		stale = true
	}
	switch {
	case len(live) > 1:
		return detachedProcessState{}, fmt.Errorf("service %s matches multiple live runtimes; stop a specific process instead", serviceName)
	case len(live) == 1:
		state, err := stopView(live[0], force)
		if err != nil {
			return detachedProcessState{}, err
		}
		return state, nil
	case stale:
		return detachedProcessState{}, fmt.Errorf("service %s is stale; no matching live runtime process exists", serviceName)
	default:
		return detachedProcessState{}, fmt.Errorf("no matching runtime process exists for service/%s", serviceName)
	}
}

func serviceStopMatches(view processView, ctx workspace.ServiceContext) bool {
	if view.Command != "attach" {
		return false
	}
	if strings.TrimSpace(view.Service) != "" && strings.TrimSpace(view.Service) != ctx.Name {
		return false
	}
	if strings.TrimSpace(view.ServiceID) != "" && strings.TrimSpace(ctx.Service.ServiceID) != "" && strings.TrimSpace(view.ServiceID) != strings.TrimSpace(ctx.Service.ServiceID) {
		return false
	}
	if strings.TrimSpace(view.Cluster) != "" && strings.TrimSpace(view.Cluster) != ctx.ClusterName {
		return false
	}
	if strings.TrimSpace(view.Namespace) != "" && strings.TrimSpace(view.Namespace) != ctx.Namespace {
		return false
	}
	return true
}

func stopPipeLifecycle(name string, force bool) (detachedProcessState, error) {
	views, err := listProcessViews(true)
	if err != nil {
		return detachedProcessState{}, err
	}
	var live []processView
	var stale bool
	for _, view := range views {
		if !pipeStopMatches(view, name) {
			continue
		}
		if view.Status == "running" || view.Status == "degraded" {
			live = append(live, view)
			continue
		}
		stale = true
	}
	switch {
	case len(live) > 1:
		return detachedProcessState{}, fmt.Errorf("pipe/%s matches multiple live runtimes; stop a specific process instead", name)
	case len(live) == 1:
		state, err := stopView(live[0], force)
		if err != nil {
			return detachedProcessState{}, err
		}
		return state, nil
	case stale:
		return detachedProcessState{}, fmt.Errorf("pipe/%s is stale; no matching live runtime process exists", name)
	default:
		return detachedProcessState{}, fmt.Errorf("no matching runtime process exists for pipe/%s", name)
	}
}

func pipeStopMatches(view processView, name string) bool {
	if view.Command != "connect" || view.ResourceKind != "pipe" {
		return false
	}
	if strings.TrimSpace(view.Name) == name {
		return true
	}
	if strings.TrimPrefix(strings.TrimSpace(view.Name), "connect-") == name {
		return true
	}
	if strings.TrimSpace(view.Service) == name {
		return true
	}
	if strings.TrimSpace(view.Target) == name {
		return true
	}
	return false
}

func stopView(view processView, force bool) (detachedProcessState, error) {
	state, _, err := loadProcessState(view.ID)
	if err != nil {
		state = detachedProcessState{
			ID:           view.ID,
			Kind:         "process",
			ResourceKind: view.ResourceKind,
			Command:      view.Command,
			Name:         view.Name,
			Service:      view.Service,
			ServiceKind:  view.ServiceKind,
			ServiceID:    view.ServiceID,
			PeerID:       view.PeerID,
			Cluster:      view.Cluster,
			Namespace:    view.Namespace,
			Local:        view.Local,
			Target:       view.Target,
			Path:         view.Path,
			SelectedAddr: view.SelectedAddr,
			SelectedPath: view.SelectedPath,
			PID:          view.PID,
			LogFile:      view.LogFile,
			StateFile:    view.StateFile,
			PIDFile:      view.PIDFile,
			StatusURL:    view.StatusURL,
			StatsURL:     view.StatsURL,
			Source:       view.Source,
		}
	}
	if state.PID == 0 {
		state.PID = view.PID
	}
	if err := processes.StopState(state, processSystemAdapter{}, force); err != nil {
		return detachedProcessState{}, err
	}
	return state, nil
}

func livePIDForState(state detachedProcessState) (int, bool, error) {
	if state.PID > 0 && pidRunning(state.PID) {
		return state.PID, true, nil
	}
	if strings.TrimSpace(state.PIDFile) == "" {
		return 0, false, nil
	}
	raw, err := os.ReadFile(state.PIDFile)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	pid := strings.TrimSpace(string(raw))
	if pid == "" {
		return 0, false, nil
	}
	if !pidRunning(mustAtoi(pid)) {
		return 0, false, nil
	}
	return mustAtoi(pid), true, nil
}

func mustAtoi(s string) int {
	var n int
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
