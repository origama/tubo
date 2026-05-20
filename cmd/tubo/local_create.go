package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	workspace "github.com/origama/tubo/internal/workspace"

	capability "github.com/origama/tubo/internal/capability"
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
	fmt.Printf("created namespace %q in cluster %q\n", result.Name, result.Cluster)
	fmt.Printf("membership capability file: %s\n", cfg.Clusters[result.Cluster].Namespaces[result.Name].MembershipCapabilityFile)
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
