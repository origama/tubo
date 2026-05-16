package networkbundle

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	capability "github.com/origama/tubo/internal/capability"
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
	clusterName := "home"
	namespaceName := "default"
	cluster := cfgpkg.Cluster{
		Namespaces: map[string]cfgpkg.Namespace{
			namespaceName: {},
		},
	}
	if payload.PublicCluster != nil {
		clusterName = payload.PublicCluster.Name
		namespaceName = payload.PublicCluster.DefaultNamespace
		cluster = cfgpkg.Cluster{
			ClusterID:          payload.PublicCluster.ClusterID,
			AuthorityPublicKey: payload.PublicCluster.AuthorityPublicKey,
			Namespaces: map[string]cfgpkg.Namespace{
				namespaceName: {},
			},
			MembershipGrant: &cfgpkg.ClusterMembershipGrant{
				ClusterName:        payload.PublicCluster.Name,
				ClusterID:          payload.PublicCluster.ClusterID,
				AuthorityPublicKey: payload.PublicCluster.AuthorityPublicKey,
				Namespace:          namespaceName,
				Role:               "member",
				Permissions: []string{
					capability.PermissionSubscribe,
					capability.PermissionList,
					capability.PermissionPublish,
				},
				GrantServiceProtocol: payload.PublicCluster.GrantServiceProtocol,
				GrantServicePeers:    append([]string(nil), payload.PublicCluster.GrantServicePeers...),
				IssuedAt:             mustParseTime(payload.Validity.NotBefore),
				ExpiresAt:            mustParseTime(payload.Validity.NotAfter),
			},
		}
	}
	joined := cfgpkg.Merge(existing, cfgpkg.Config{
		CurrentOverlay:   payload.Name,
		CurrentCluster:   clusterName,
		CurrentNamespace: namespaceName,
		Overlays: map[string]cfgpkg.Overlay{
			payload.Name: {
				Relays:         append([]string(nil), payload.Relays...),
				BootstrapPeers: append([]string(nil), payload.Relays...),
				SwarmKeyFile:   swarmKeyPath,
			},
		},
		Clusters: map[string]cfgpkg.Cluster{
			clusterName: cluster,
		},
		Network: cfgpkg.Network{
			PrivateKeyFile:    swarmKeyPath,
			BootstrapPeers:    append([]string(nil), payload.Relays...),
			RelayPeers:        append([]string(nil), payload.Relays...),
			Autorelay:         payload.Network.Autorelay,
			HolePunching:      payload.Network.HolePunching,
			ForceReachability: payload.Network.ForceReachability,
		},
	})
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

func mustParseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}
