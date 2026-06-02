package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	workspace "github.com/origama/tubo/internal/workspace"
)

func localCreateCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo create <cluster/name|namespace/name|service/name> [flags]")
	}
	resource := args[0]
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	kind, name, err := parseLocalResourceRef(resource)
	if err != nil {
		return err
	}
	switch kind {
	case "cluster":
		return createLocalCluster(*configPath, name)
	case "namespace":
		return createLocalNamespace(*configPath, name)
	case "service":
		return createLocalService(*configPath, name)
	default:
		return fmt.Errorf("unsupported create resource %q", resource)
	}
}

func createLocalCluster(configPath, name string) error {
	result, err := localWorkspace().CreateCluster(configPath, name)
	if err != nil {
		return err
	}
	fmt.Printf("created cluster %q\n", result.Name)
	fmt.Printf("cluster id: %s\n", result.ClusterID)
	fmt.Printf("authority public key: %s\n", result.AuthorityPublicKey)
	fmt.Printf("authority key file: %s\n", workspace.DerivePaths(configPath).ClusterAuthorityKey(name))
	fmt.Printf("membership capability file: %s\n", workspace.DerivePaths(configPath).ClusterMembershipCapability(name))
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	defaultNS := cfg.Clusters[result.Name].Namespaces["default"]
	if defaultNS.DiscoverySecretCurrent != nil {
		fingerprint, err := cfgpkg.NamespaceDiscoverySecretFingerprint(defaultNS.DiscoverySecretCurrent)
		if err != nil {
			return err
		}
		fmt.Printf("default namespace discovery secret file: %s\n", defaultNS.DiscoverySecretCurrent.File)
		fmt.Printf("default namespace discovery key id: %s\n", defaultNS.DiscoverySecretCurrent.KeyID)
		fmt.Printf("default namespace discovery fingerprint: %s\n", fingerprint)
	}
	return nil
}

func createLocalNamespace(configPath, name string) error {
	result, err := localWorkspace().CreateNamespace(configPath, name)
	if err != nil {
		return err
	}
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	ns := cfg.Clusters[result.Cluster].Namespaces[result.Name]
	fmt.Printf("created namespace %q in cluster %q\n", result.Name, result.Cluster)
	fmt.Printf("membership capability file: %s\n", ns.MembershipCapabilityFile)
	if ns.DiscoverySecretCurrent != nil {
		fingerprint, err := cfgpkg.NamespaceDiscoverySecretFingerprint(ns.DiscoverySecretCurrent)
		if err != nil {
			return err
		}
		fmt.Printf("discovery secret file: %s\n", ns.DiscoverySecretCurrent.File)
		fmt.Printf("discovery key id: %s\n", ns.DiscoverySecretCurrent.KeyID)
		fmt.Printf("discovery fingerprint: %s\n", fingerprint)
	}
	return nil
}

func writeCapabilityFile(path string, cap capability.MembershipCapability) error {
	if err := os.MkdirAll(filepathDir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cap, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0600)
}

func filepathDir(path string) string {
	return filepath.Dir(path)
}
