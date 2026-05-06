package networkbundle

import (
	"errors"
	"os"
	"path/filepath"

	cfgpkg "github.com/origama/tubo/internal/config"
	"gopkg.in/yaml.v3"
)

type InstallOptions struct {
	ConfigDir string
	Force     bool
}

type InstallResult struct {
	NetworkName    string
	NetworkID      string
	ConfigPath     string
	SwarmKeyPath   string
	RelayPeers     []string
	BootstrapPeers []string
}

func Install(payload *NetworkPayload, opts InstallOptions) (*InstallResult, error) {
	if err := ValidatePayload(payload); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.ConfigDir, 0700); err != nil {
		return nil, err
	}
	configPath := filepath.Join(opts.ConfigDir, "config.yaml")
	swarmKeyPath := filepath.Join(opts.ConfigDir, "swarm.key")
	if !opts.Force {
		for _, path := range []string{configPath, swarmKeyPath} {
			if _, err := os.Stat(path); err == nil {
				return nil, errors.New(path + " exists (use --force)")
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
		}
	}
	existing, err := cfgpkg.LoadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	joined := cfgpkg.Merge(existing, cfgpkg.Config{Network: cfgpkg.Network{
		PrivateKeyFile:    swarmKeyPath,
		BootstrapPeers:    append([]string(nil), payload.Relays...),
		RelayPeers:        append([]string(nil), payload.Relays...),
		Autorelay:         payload.Network.Autorelay,
		HolePunching:      payload.Network.HolePunching,
		ForceReachability: payload.Network.ForceReachability,
	}})
	joined.Network.PrivateKeyB64 = ""
	b, err := yaml.Marshal(joined)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(swarmKeyPath, []byte(payload.SwarmKey.Value), 0600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(configPath, b, 0600); err != nil {
		return nil, err
	}
	return &InstallResult{
		NetworkName:    payload.Name,
		NetworkID:      payload.ID,
		ConfigPath:     configPath,
		SwarmKeyPath:   swarmKeyPath,
		RelayPeers:     append([]string(nil), payload.Relays...),
		BootstrapPeers: append([]string(nil), payload.Relays...),
	}, nil
}
