package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	bridgeapp "github.com/origama/tubo/internal/app/bridge"
	cfgpkg "github.com/origama/tubo/internal/config"
	processes "github.com/origama/tubo/internal/processes"
)

type detachedSpec = processes.DetachedSpec

type detachedProcessState = processes.State

type processView = processes.View

type processSystemAdapter struct{}

func (processSystemAdapter) PIDRunning(pid int) bool              { return pidRunning(pid) }
func (processSystemAdapter) TerminatePID(pid int) error           { return terminatePID(pid) }
func (processSystemAdapter) KillPID(pid int) error                { return killPID(pid) }
func (processSystemAdapter) CommandLine(pid int) ([]string, bool) { return processCommandLine(pid) }

func buildDetachedSpec(commandName string, cfg cfgpkg.Config, args []string) (detachedSpec, error) {
	return processes.BuildSpec(commandName, cfg, args, defaultTuboDataDir())
}

func startDetachedProcess(spec detachedSpec) (detachedProcessState, error) {
	return startDetachedProcessWithTimeout(spec, 5*time.Second)
}

func startDetachedProcessWithTimeout(spec detachedSpec, timeout time.Duration) (detachedProcessState, error) {
	exe, err := os.Executable()
	if err != nil {
		return detachedProcessState{}, err
	}
	return processes.StartDetached(spec, exe, append(os.Environ(), "TUBO_DETACHED_CHILD=1"), configureDetachedCommand, timeout)
}

func registerCurrentProcess(state detachedProcessState) (detachedProcessState, func() error, error) {
	if os.Getenv("TUBO_DETACHED_CHILD") == "1" {
		return state, nil, nil
	}
	state.CommandLine = append([]string(nil), os.Args...)
	state.Source = runtimeProcessSource()
	registered, cleanup, err := processes.RegisterCurrentProcess(defaultTuboDataDir(), state, processSystemAdapter{})
	return registered, cleanup, err
}

func runtimeProcessSource() string {
	if source := strings.TrimSpace(os.Getenv("TUBO_PROCESS_SOURCE")); source != "" {
		return source
	}
	return "foreground"
}

func processStateDir() string { return processes.StateDir(defaultTuboDataDir()) }
func processLogDir() string   { return processes.LogDir(defaultTuboDataDir()) }
func processRunDir() string   { return processes.RunDir(defaultTuboDataDir()) }

func listProcessViews(includeAll bool) ([]processView, error) {
	return processes.ListViews(defaultTuboDataDir(), includeAll, processSystemAdapter{})
}

func loadProcessState(ref string) (detachedProcessState, string, error) {
	return processes.LoadState(defaultTuboDataDir(), ref, processSystemAdapter{})
}

func printLogTail(path string, lines int) error {
	items, err := processes.ReadLogTail(path, lines)
	if err != nil {
		return err
	}
	for _, line := range items {
		fmt.Println(line)
	}
	return nil
}

func followLogFile(ctx context.Context, path string) error {
	return processes.FollowLog(ctx, path, os.Stdout)
}

func stopProcess(ref string, force bool) (detachedProcessState, error) {
	return processes.Stop(defaultTuboDataDir(), ref, processSystemAdapter{}, force)
}

func removeStaleProcesses() (int, error) {
	return processes.RemoveStale(defaultTuboDataDir(), processSystemAdapter{})
}

func updateProcessRuntimeState(stateFile string, runtime bridgeapp.RuntimeStatus) error {
	if strings.TrimSpace(stateFile) == "" {
		return nil
	}
	return processes.UpdateState(stateFile, func(state *detachedProcessState) {
		state.RuntimeStatus = runtime.Status
		state.DegradedReason = runtime.Reason
		state.Path = runtime.Path
		if runtime.ConnectAccessExpiresAt != nil {
			state.ConnectAccessExpiresAt = runtime.ConnectAccessExpiresAt.UTC().Format(time.RFC3339)
		} else {
			state.ConnectAccessExpiresAt = ""
		}
		if runtime.ConnectRefreshExpiresAt != nil {
			state.ConnectRefreshExpiresAt = runtime.ConnectRefreshExpiresAt.UTC().Format(time.RFC3339)
		} else {
			state.ConnectRefreshExpiresAt = ""
		}
		state.LastTunnelError = runtime.LastTunnelError
		state.LastRefreshError = runtime.LastRefreshError
		if runtime.NextRefreshRetryAt != nil {
			state.NextRefreshRetryAt = runtime.NextRefreshRetryAt.UTC().Format(time.RFC3339)
		} else {
			state.NextRefreshRetryAt = ""
		}
		if runtime.LastTunnelErrorAt != nil {
			state.LastTunnelErrorAt = runtime.LastTunnelErrorAt.UTC().Format(time.RFC3339)
		} else {
			state.LastTunnelErrorAt = ""
		}
		if runtime.LastTunnelHealthyAt != nil {
			state.LastTunnelHealthyAt = runtime.LastTunnelHealthyAt.UTC().Format(time.RFC3339)
		} else {
			state.LastTunnelHealthyAt = ""
		}
	})
}

func updateProcessConnectState(stateFile string, result connectResult) error {
	if strings.TrimSpace(stateFile) == "" {
		return nil
	}
	return processes.UpdateState(stateFile, func(state *detachedProcessState) {
		state.ResourceKind = "pipe"
		state.ServiceKind = result.ServiceKind
		state.ServiceID = result.ServiceID
		state.PeerID = result.SelectedPeerID
		state.Path = result.Path
		state.SelectedAddr = result.Selected
		state.SelectedPath = result.Path
	})
}

func sanitizeProcessName(s string) string {
	if s == "" {
		return "default"
	}
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return '-'
		default:
			return '-'
		}
	}, s)
	mapped = strings.Trim(mapped, "-")
	for strings.Contains(mapped, "--") {
		mapped = strings.ReplaceAll(mapped, "--", "-")
	}
	if mapped == "" {
		return "default"
	}
	return mapped
}
