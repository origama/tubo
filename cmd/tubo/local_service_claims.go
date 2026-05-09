package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/p2p"
)

func createLocalService(configPath, name string) error {
	cfg, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return err
	}
	if cfg.CurrentCluster == "" {
		return errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	if cfg.CurrentNamespace == "" {
		return errors.New("no current namespace selected; run `tubo use namespace/<name>` first")
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" || cluster.AuthorityPrivateKeyFile == "" {
		return fmt.Errorf("cluster %q is missing identity metadata", cfg.CurrentCluster)
	}
	if cluster.Namespaces == nil {
		cluster.Namespaces = make(map[string]cfgpkg.Namespace)
	}
	namespace, ok := cluster.Namespaces[cfg.CurrentNamespace]
	if !ok {
		return fmt.Errorf("current namespace %q not found in cluster %q", cfg.CurrentNamespace, cfg.CurrentCluster)
	}
	if namespace.Services == nil {
		namespace.Services = make(map[string]cfgpkg.NamespaceService)
	}
	if existing, ok := namespace.Services[name]; ok && existing.ServiceID != "" && existing.ServiceSeed != "" && existing.ServiceClaimFile != "" {
		fmt.Printf("service %q already exists in cluster %q namespace %q\n", name, cfg.CurrentCluster, cfg.CurrentNamespace)
		fmt.Printf("service id: %s\n", existing.ServiceID)
		fmt.Printf("service seed: %s\n", existing.ServiceSeed)
		fmt.Printf("service claim file: %s\n", existing.ServiceClaimFile)
		return nil
	}
	privKey, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return fmt.Errorf("load cluster authority key: %w", err)
	}
	pubAuthorized, err := clusterAuthorityPublicKeyString(privKey)
	if err != nil {
		return err
	}
	if cluster.AuthorityPublicKey != pubAuthorized {
		return fmt.Errorf("cluster %q authority public key mismatch", cfg.CurrentCluster)
	}
	serviceID, serviceSeed := serviceIdentityFor(cfg.Clusters[cfg.CurrentCluster].ClusterID, cfg.CurrentNamespace, name)
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		return err
	}
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{
		ClusterID:     cluster.ClusterID,
		NamespaceID:   cfg.CurrentNamespace,
		ServiceID:     serviceID,
		SubjectPeerID: servicePeerID.String(),
		Permissions:   []string{capability.PermissionAttach, capability.PermissionAnnounce},
		ExpiresAt:     time.Now().Add(365 * 24 * time.Hour),
	}, privKey)
	if err != nil {
		return err
	}
	serviceDir := filepath.Join(filepath.Dir(configPath), "clusters", sanitizeProcessName(cfg.CurrentCluster), "namespaces", sanitizeProcessName(cfg.CurrentNamespace), "services")
	if err := os.MkdirAll(serviceDir, 0700); err != nil {
		return err
	}
	claimPath := filepath.Join(serviceDir, sanitizeProcessName(name)+".claim.json")
	if err := writeServiceClaimFile(claimPath, claim); err != nil {
		return err
	}
	namespace.Services[name] = cfgpkg.NamespaceService{ServiceID: serviceID, ServiceSeed: serviceSeed, ServiceClaimFile: claimPath}
	cluster.Namespaces[cfg.CurrentNamespace] = namespace
	cfg.Clusters[cfg.CurrentCluster] = cluster
	if err := saveLocalConfig(configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("created service %q in cluster %q namespace %q\n", name, cfg.CurrentCluster, cfg.CurrentNamespace)
	fmt.Printf("service id: %s\n", serviceID)
	fmt.Printf("service seed: %s\n", serviceSeed)
	fmt.Printf("service claim file: %s\n", claimPath)
	return nil
}

func serviceIdentityFor(clusterID, namespaceID, serviceName string) (string, string) {
	sum := sha256.Sum256([]byte(clusterID + "\x00" + namespaceID + "\x00" + serviceName))
	serviceID := "service-" + fmt.Sprintf("%x", sum[:8])
	serviceSeed := "service-" + fmt.Sprintf("%x", sum[8:24])
	return serviceID, serviceSeed
}

func writeServiceClaimFile(path string, claim capability.ServiceClaim) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(claim, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0600)
}
