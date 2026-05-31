package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

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
	state.CommandLine = append([]string(nil), os.Args...)
	state.Source = runtimeProcessSource()
	registered, cleanup, err := processes.RegisterCurrentProcess(defaultTuboDataDir(), state, processSystemAdapter{})
	return registered, cleanup, err
}

func runtimeProcessSource() string {
	for _, key := range []string{"INVOCATION_ID", "JOURNAL_STREAM", "NOTIFY_SOCKET"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return "systemd"
		}
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

func processStateStatus(state detachedProcessState) string {
	return processes.Status(state, processSystemAdapter{})
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
