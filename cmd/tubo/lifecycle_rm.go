package main

import (
	"errors"
	"fmt"
	"strings"
)

func rmCmd(args []string) error {
	configPath := defaultTuboConfigPath()
	force := false
	stale := false
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--force":
			force = true
		case "--stale":
			stale = true
		case "--config":
			i++
			if i >= len(args) {
				return errors.New("usage: tubo rm --stale")
			}
			configPath = args[i]
		default:
			if strings.HasPrefix(arg, "--config=") {
				configPath = strings.TrimPrefix(arg, "--config=")
				continue
			}
			if strings.HasPrefix(arg, "-") {
				return fmt.Errorf("unknown flag %s", arg)
			}
			positionals = append(positionals, arg)
		}
	}
	if stale {
		if len(positionals) != 0 {
			return errors.New("usage: tubo rm --stale")
		}
		removed, err := removeStaleProcesses()
		if err != nil {
			return err
		}
		fmt.Printf("removed %d stale process artifacts\n", removed)
		return nil
	}
	if len(positionals) != 1 {
		return errors.New("usage: tubo rm [--config <path>] [--force] service/<name>")
	}
	kind, name, err := parseLocalResourceRef(positionals[0])
	if err != nil {
		return err
	}
	switch kind {
	case "service":
		state, err := rmServiceLifecycle(name, configPath, force)
		if err != nil {
			return err
		}
		if state.ID != "" {
			fmt.Printf("stopped %s\n", state.ID)
		}
		fmt.Printf("removed service/%s\n", name)
		return nil
	default:
		return fmt.Errorf("rm currently supports only service/<name> in this slice")
	}
}

func rmServiceLifecycle(serviceName, configPath string, force bool) (detachedProcessState, error) {
	running, err := serviceLifecycleHasLiveRuntime(serviceName, configPath)
	if err != nil {
		return detachedProcessState{}, err
	}
	if running && !force {
		return detachedProcessState{}, fmt.Errorf("service/%s is running or degraded; use --force to stop and remove it", serviceName)
	}
	var stopped detachedProcessState
	if running && force {
		stopped, err = stopServiceLifecycle(serviceName, configPath, true)
		if err != nil {
			var noRuntime noLiveServiceRuntimeError
			if !errors.As(err, &noRuntime) {
				return detachedProcessState{}, err
			}
		}
	}
	if _, err := localWorkspace().RemoveService(configPath, serviceName); err != nil {
		return detachedProcessState{}, err
	}
	return stopped, nil
}
