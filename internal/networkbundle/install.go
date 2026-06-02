package networkbundle

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	workspace "github.com/origama/tubo/internal/workspace"
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
			namespaceName: {
				Discovery:     cfgpkg.NamespaceDiscoveryEnabled,
				ConnectPolicy: cfgpkg.ConnectPolicyNamespaceMember,
			},
		},
	}
	if payload.PublicCluster != nil {
		clusterName = payload.PublicCluster.Name
		namespaceName = payload.PublicCluster.DefaultNamespace
		cluster = cfgpkg.Cluster{
			ClusterID:          payload.PublicCluster.ClusterID,
			AuthorityPublicKey: payload.PublicCluster.AuthorityPublicKey,
			Namespaces: map[string]cfgpkg.Namespace{
				namespaceName: {
					Discovery:     cfgpkg.NamespaceDiscoveryDisabled,
					ConnectPolicy: cfgpkg.ConnectPolicyInviteOnly,
				},
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
	// Bug fix: Merge would replace the whole cluster entry (including all
	// namespaces and their services) with the new struct from the bundle.
	// Instead we merge at cluster/namespace granularity:
	//  - For namespaces that exist in both: keep the bundle's discovery/policy
	//    settings (authoritative) but preserve the existing services and
	//    capability file references.
	//  - For namespaces that only exist locally (e.g. private namespaces created
	//    by the user): carry them forward unchanged.
	//  - For clusters that are not touched by the bundle (e.g. oricluster):
	//    Merge already handles them by not overwriting keys absent from the
	//    override config; they are preserved via the base.
	if existing.Clusters != nil {
		if existingCluster, ok := existing.Clusters[clusterName]; ok {
			if cluster.Namespaces == nil {
				cluster.Namespaces = make(map[string]cfgpkg.Namespace)
			}
			for nsName, existingNs := range existingCluster.Namespaces {
				if bundleNs, inBundle := cluster.Namespaces[nsName]; inBundle {
					// Namespace exists in both: merge services and capability
					// files from existing into the bundle namespace entry,
					// keeping bundle's policy settings authoritative.
					if len(existingNs.Services) > 0 {
						if bundleNs.Services == nil {
							bundleNs.Services = make(map[string]cfgpkg.NamespaceService, len(existingNs.Services))
						}
						for svcName, svc := range existingNs.Services {
							if _, alreadySet := bundleNs.Services[svcName]; !alreadySet {
								bundleNs.Services[svcName] = svc
							}
						}
					}
					if bundleNs.MembershipCapabilityFile == "" {
						bundleNs.MembershipCapabilityFile = existingNs.MembershipCapabilityFile
					}
					if bundleNs.DiscoverySecretCurrent == nil {
						bundleNs.DiscoverySecretCurrent = existingNs.DiscoverySecretCurrent
					}
					if bundleNs.DiscoverySecretPrevious == nil {
						bundleNs.DiscoverySecretPrevious = existingNs.DiscoverySecretPrevious
					}
					cluster.Namespaces[nsName] = bundleNs
				} else {
					// Namespace only exists locally: carry it forward.
					cluster.Namespaces[nsName] = existingNs
				}
			}
			// Preserve authority key files if we are the authority.
			if cluster.AuthorityPrivateKeyFile == "" {
				cluster.AuthorityPrivateKeyFile = existingCluster.AuthorityPrivateKeyFile
			}
			if cluster.MembershipCapabilityFile == "" {
				cluster.MembershipCapabilityFile = existingCluster.MembershipCapabilityFile
			}
		}
	}
	if err := ensureNamespaceDiscoverySecrets(configPath, clusterName, cluster.Namespaces); err != nil {
		return nil, err
	}
	joined := cfgpkg.Merge(existing, cfgpkg.Config{
		CurrentOverlay:   payload.Name,
		CurrentCluster:   clusterName,
		CurrentNamespace: namespaceName,
		Overlays: map[string]cfgpkg.Overlay{
			payload.Name: {
				Kind:                   cfgpkg.OverlayKindPublicBundle,
				PublicDefaultCluster:   clusterName,
				PublicDefaultNamespace: namespaceName,
				Relays:                 append([]string(nil), payload.Relays...),
				BootstrapPeers:         append([]string(nil), payload.Relays...),
				SwarmKeyFile:           swarmKeyPath,
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

func ensureNamespaceDiscoverySecrets(configPath, clusterName string, namespaces map[string]cfgpkg.Namespace) error {
	paths := workspace.DerivePaths(configPath)
	for namespaceName, namespace := range namespaces {
		if namespace.Discovery != cfgpkg.NamespaceDiscoveryEnabled || namespace.DiscoverySecretCurrent != nil {
			continue
		}
		secretPath := paths.NamespaceDiscoveryCurrentSecret(clusterName, namespaceName)
		secretBytes, ref, err := cfgpkg.BuildNamespaceDiscoverySecretRef(secretPath, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(secretPath), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(secretPath, secretBytes, 0600); err != nil {
			return err
		}
		namespace.DiscoverySecretCurrent = ref
		namespaces[namespaceName] = namespace
	}
	return nil
}

func mustParseTime(value string) time.Time {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}
