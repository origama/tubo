package main

import (
	"fmt"
	"strings"

	logging "github.com/origama/tubo/internal/logging"
	processes "github.com/origama/tubo/internal/processes"
	workspace "github.com/origama/tubo/internal/workspace"
)

type stopMatchStrength int

type noLiveServiceRuntimeError struct {
	message string
}

func (e noLiveServiceRuntimeError) Error() string {
	return e.message
}

func (e noLiveServiceRuntimeError) Is(target error) bool {
	_, ok := target.(noLiveServiceRuntimeError)
	return ok
}

const (
	stopMatchNone stopMatchStrength = iota
	stopMatchWeak
	stopMatchExact
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
	var exactLive []processView
	var exactStale []processView
	var weakLive []processView
	var weakStale []processView
	for _, view := range views {
		strength, ok := serviceStopMatch(view, ctx)
		if !ok {
			continue
		}
		switch strength {
		case stopMatchExact:
			if view.Status == "running" || view.Status == "degraded" {
				exactLive = append(exactLive, view)
			} else {
				exactStale = append(exactStale, view)
			}
		case stopMatchWeak:
			if view.Status == "running" || view.Status == "degraded" {
				weakLive = append(weakLive, view)
			} else {
				weakStale = append(weakStale, view)
			}
		}
	}
	switch {
	case len(exactLive) > 1:
		return detachedProcessState{}, fmt.Errorf("service %s matches multiple live runtimes by service_id; stop a specific process instead", serviceName)
	case len(exactLive) == 1:
		state, err := stopView(exactLive[0], force)
		if err != nil {
			return detachedProcessState{}, err
		}
		return state, nil
	case len(exactStale) > 0:
		return detachedProcessState{}, noLiveServiceRuntimeError{message: fmt.Sprintf("service %s has stale process state with matching service_id; no live runtime process exists", serviceName)}
	case len(weakLive) > 1:
		return detachedProcessState{}, fmt.Errorf("service %s matches multiple legacy name-based runtimes; stop a specific process instead", serviceName)
	case len(weakLive) == 1 && len(weakStale) == 0:
		logging.Warnf("stopping service/%s via legacy name-based process state; add service_id to avoid cross-scope ambiguity\n", serviceName)
		state, err := stopView(weakLive[0], force)
		if err != nil {
			return detachedProcessState{}, err
		}
		return state, nil
	case len(weakLive) == 0 && len(weakStale) > 0:
		return detachedProcessState{}, noLiveServiceRuntimeError{message: fmt.Sprintf("service %s is stale; no matching live runtime process exists", serviceName)}
	case len(weakLive) == 1 && len(weakStale) > 0:
		return detachedProcessState{}, fmt.Errorf("service %s has multiple legacy name-based matches; stop a specific process instead", serviceName)
	default:
		return detachedProcessState{}, noLiveServiceRuntimeError{message: fmt.Sprintf("no matching runtime process exists for service/%s", serviceName)}
	}
}

func serviceStopMatch(view processView, ctx workspace.ServiceContext) (stopMatchStrength, bool) {
	if view.Command != "attach" {
		return stopMatchNone, false
	}
	serviceID := strings.TrimSpace(ctx.Service.ServiceID)
	viewServiceID := strings.TrimSpace(view.ServiceID)
	if serviceID != "" {
		if viewServiceID != "" {
			if viewServiceID == serviceID {
				return stopMatchExact, true
			}
			return stopMatchNone, false
		}
		if !serviceNameMatches(view, ctx) {
			return stopMatchNone, false
		}
		return stopMatchWeak, true
	}
	if viewServiceID != "" {
		return stopMatchNone, false
	}
	if !serviceNameMatches(view, ctx) {
		return stopMatchNone, false
	}
	return stopMatchWeak, true
}

func serviceNameMatches(view processView, ctx workspace.ServiceContext) bool {
	name := strings.TrimSpace(view.Service)
	if name == "" {
		name = strings.TrimPrefix(strings.TrimSpace(view.Name), "attach-")
	}
	if name != ctx.Name {
		return false
	}
	return serviceScopeMatches(view, ctx)
}

func serviceScopeMatches(view processView, ctx workspace.ServiceContext) bool {
	if strings.TrimSpace(view.Cluster) != "" && strings.TrimSpace(view.Cluster) != ctx.ClusterName {
		return false
	}
	if strings.TrimSpace(view.Namespace) != "" && strings.TrimSpace(view.Namespace) != ctx.Namespace {
		return false
	}
	return true
}

func stopPipeLifecycle(name string, force bool) (detachedProcessState, error) {
	// Pipe stop stays process-backed and does not delete the persistent
	// pipe definition.
	views, err := listProcessViews(true)
	if err != nil {
		return detachedProcessState{}, err
	}
	var live []processView
	var stale []processView
	for _, view := range views {
		if !pipeStopMatches(view, name) {
			continue
		}
		if view.Status == "running" || view.Status == "degraded" {
			live = append(live, view)
		} else {
			stale = append(stale, view)
		}
	}
	switch {
	case len(live) > 1:
		return detachedProcessState{}, fmt.Errorf("pipe/%s matches multiple live runtimes; stop a specific process instead", name)
	case len(live) == 1 && len(stale) == 0:
		state, err := stopView(live[0], force)
		if err != nil {
			return detachedProcessState{}, err
		}
		return state, nil
	case len(live) == 1 && len(stale) > 0:
		return detachedProcessState{}, fmt.Errorf("pipe/%s has multiple matches; stop a specific process instead", name)
	case len(live) == 0 && len(stale) > 0:
		return detachedProcessState{}, fmt.Errorf("pipe/%s is stale; no matching live runtime process exists", name)
	default:
		return detachedProcessState{}, fmt.Errorf("no matching runtime process exists for pipe/%s", name)
	}
}

func pipeStopMatches(view processView, name string) bool {
	if view.Command != "connect" || view.ResourceKind != "pipe" {
		return false
	}
	trimmed := strings.TrimSpace(view.Name)
	if trimmed == name {
		return true
	}
	if strings.TrimPrefix(trimmed, "connect-") == name {
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
