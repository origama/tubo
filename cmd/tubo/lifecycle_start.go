package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
)

func startCmd(args []string) error {
	configPath := defaultTuboConfigPath()
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--config":
			i++
			if i >= len(args) {
				return errors.New("usage: tubo start [--config <path>] service/<name>")
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
		return errors.New("usage: tubo start [--config <path>] service/<name>")
	}
	kind, name, err := parseLocalResourceRef(positionals[0])
	if err != nil {
		return err
	}
	switch kind {
	case "service":
		state, err := startServiceLifecycle(name, configPath)
		if err != nil {
			return err
		}
		printDetachedSummary("start", state)
		return nil
	default:
		return fmt.Errorf("start currently supports only service/<name> in this slice")
	}
}

func startServiceLifecycle(serviceName, configPath string) (detachedProcessState, error) {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return detachedProcessState{}, err
	}
	ctx, err := localWorkspace().ResolveServiceContext(configPath, serviceName, cfg.CurrentCluster, cfg.CurrentNamespace)
	if err != nil {
		return detachedProcessState{}, fmt.Errorf("service/%s not found: %w", serviceName, err)
	}
	if storedName := strings.TrimSpace(cfg.Service.Name); storedName != "" && storedName != serviceName {
		return detachedProcessState{}, fmt.Errorf("service/%s does not match saved service.name %q", serviceName, storedName)
	}
	if strings.TrimSpace(cfg.Service.Target) == "" {
		return detachedProcessState{}, fmt.Errorf("service/%s is missing service.target; set the target in the saved config before starting", serviceName)
	}
	if strings.TrimSpace(ctx.Service.ServiceID) == "" {
		return detachedProcessState{}, fmt.Errorf("service/%s is missing service_id", serviceName)
	}
	if strings.TrimSpace(ctx.Service.ServiceSeed) == "" {
		return detachedProcessState{}, fmt.Errorf("service/%s is missing service_seed", serviceName)
	}
	if strings.TrimSpace(ctx.Service.ServiceOwnerKeyFile) == "" {
		return detachedProcessState{}, fmt.Errorf("service/%s is missing service_owner_key_file", serviceName)
	}
	if strings.TrimSpace(ctx.Service.ServiceClaimFile) == "" {
		return detachedProcessState{}, fmt.Errorf("service/%s is missing service_claim_file", serviceName)
	}
	if strings.TrimSpace(ctx.Service.ServicePublishLeaseFile) == "" {
		return detachedProcessState{}, fmt.Errorf("service/%s is missing service_publish_lease_file", serviceName)
	}
	for _, path := range []string{ctx.Service.ServiceOwnerKeyFile, ctx.Service.ServiceClaimFile, ctx.Service.ServicePublishLeaseFile} {
		if _, err := os.Stat(path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return detachedProcessState{}, fmt.Errorf("service/%s is incomplete: missing %s", serviceName, path)
			}
			return detachedProcessState{}, err
		}
	}
	if err := cfgpkg.Validate(cfgpkg.Config{Role: "service", Service: cfgpkg.Service{Name: serviceName, Target: cfg.Service.Target, Kind: cfg.Service.Kind}}); err != nil {
		return detachedProcessState{}, fmt.Errorf("service/%s is not startable: %w", serviceName, err)
	}
	attachCfg := cfg
	attachCfg.Service.Name = serviceName
	attachCfg.Service.Target = cfg.Service.Target
	attachCfg.Service.Kind = cfg.Service.Kind
	authz, err := resolveAttachAuthorization(configPath, attachCfg)
	if err != nil {
		return detachedProcessState{}, err
	}
	spec, err := buildDetachedSpec("attach", authz.Config, []string{"--config", configPath, "--target", authz.Config.Service.Target, "--name", authz.Config.Service.Name})
	if err != nil {
		return detachedProcessState{}, err
	}
	spec.State.ServiceID = authz.Service.ServiceID
	spec.State.ResourceKind = "service"
	spec.State.ServiceKind = string(cfgpkg.NormalizeServiceKind(authz.Config.Service.Kind, authz.Config.Service.Target))
	if authz.ServicePeerID != "" {
		spec.State.PeerID = authz.ServicePeerID
	}
	updateAttachProcessState(&spec.State, authz.Config)
	state, err := startDetachedProcess(spec)
	if err != nil {
		return detachedProcessState{}, err
	}
	return state, nil
}
