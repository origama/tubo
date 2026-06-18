package main

import (
	"errors"
	"fmt"
	"strings"
)

func restartCmd(args []string) error {
	configPath := defaultTuboConfigPath()
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--config":
			i++
			if i >= len(args) {
				return errors.New("usage: tubo restart [--config <path>] service/<name>")
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
	if len(positionals) != 1 {
		return errors.New("usage: tubo restart [--config <path>] <service/name|pipe/name>")
	}
	kind, name, err := parseLocalResourceRef(positionals[0])
	if err != nil {
		return err
	}
	switch kind {
	case "service":
		state, err := restartServiceLifecycle(name, configPath)
		if err != nil {
			return err
		}
		printDetachedSummary("start", state)
		return nil
	case "pipe":
		state, err := restartPipeLifecycle(name, configPath)
		if err != nil {
			return err
		}
		printDetachedSummary("start", state)
		return nil
	default:
		return fmt.Errorf("restart currently supports only service/<name> and pipe/<name>")
	}
}

func restartServiceLifecycle(serviceName, configPath string) (detachedProcessState, error) {
	running, err := serviceLifecycleHasLiveRuntime(serviceName, configPath)
	if err != nil {
		return detachedProcessState{}, err
	}
	if running {
		stopped, err := stopServiceLifecycle(serviceName, configPath, false)
		if err != nil {
			var noRuntime noLiveServiceRuntimeError
			if !errors.As(err, &noRuntime) {
				return detachedProcessState{}, err
			}
		} else {
			fmt.Printf("stopped %s\n", stopped.ID)
		}
	}
	return startServiceLifecycle(serviceName, configPath)
}

func serviceLifecycleHasLiveRuntime(serviceName, configPath string) (bool, error) {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return false, err
	}
	ctx, err := localWorkspace().ResolveServiceContext(configPath, serviceName, cfg.CurrentCluster, cfg.CurrentNamespace)
	if err != nil {
		return false, fmt.Errorf("service/%s not found: %w", serviceName, err)
	}
	views, err := listProcessViews(true)
	if err != nil {
		return false, err
	}
	for _, view := range views {
		if _, ok := serviceStopMatch(view, ctx); !ok {
			continue
		}
		if view.Status == "running" || view.Status == "degraded" {
			return true, nil
		}
	}
	return false, nil
}
