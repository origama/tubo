package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
		if _, err := ensureServiceMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, existing.ServiceSeed); err != nil {
			return err
		}
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
	if _, err := ensureServiceMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, serviceSeed); err != nil {
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

func serviceIDFor(clusterID, namespaceID, serviceName string) string {
	serviceID, _ := serviceIdentityFor(clusterID, namespaceID, serviceName)
	return serviceID
}

func generateServiceSeed() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "service-" + hex.EncodeToString(buf), nil
}

func serviceClaimPath(configPath, clusterName, namespaceName, serviceName string) string {
	return filepath.Join(filepath.Dir(configPath), "clusters", sanitizeProcessName(clusterName), "namespaces", sanitizeProcessName(namespaceName), "services", sanitizeProcessName(serviceName)+".claim.json")
}

func ensureAttachServiceIdentity(configPath string, cfg cfgpkg.Config) (cfgpkg.Config, cfgpkg.NamespaceService, error) {
	if cfg.CurrentCluster == "" {
		return cfg, cfgpkg.NamespaceService{}, errors.New("no current cluster selected; run `tubo use cluster/<name>` first")
	}
	if cfg.CurrentNamespace == "" {
		return cfg, cfgpkg.NamespaceService{}, errors.New("no current namespace selected; run `tubo use namespace/<name>` first")
	}
	if cfg.Service.Name == "" {
		return cfg, cfgpkg.NamespaceService{}, errors.New("service.name is required (set --name or SERVICE_NAME)")
	}
	cluster, ok := cfg.Clusters[cfg.CurrentCluster]
	if !ok {
		return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("current cluster %q not found in config", cfg.CurrentCluster)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" {
		return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("cluster %q is missing identity metadata", cfg.CurrentCluster)
	}
	if cluster.Namespaces == nil {
		return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("cluster %q has no namespaces", cfg.CurrentCluster)
	}
	namespace, ok := cluster.Namespaces[cfg.CurrentNamespace]
	if !ok {
		return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("current namespace %q not found in cluster %q", cfg.CurrentNamespace, cfg.CurrentCluster)
	}
	if namespace.Services == nil {
		namespace.Services = make(map[string]cfgpkg.NamespaceService)
	}

	expectedServiceID := serviceIDFor(cluster.ClusterID, cfg.CurrentNamespace, cfg.Service.Name)
	svc := namespace.Services[cfg.Service.Name]
	changed := false
	if svc.ServiceID == "" {
		svc.ServiceID = expectedServiceID
		changed = true
	} else if svc.ServiceID != expectedServiceID {
		return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("service %q identity mismatch in cluster %q namespace %q: service_id=%q want %q", cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceID, expectedServiceID)
	}
	if svc.ServiceSeed == "" {
		seed, err := generateServiceSeed()
		if err != nil {
			return cfg, cfgpkg.NamespaceService{}, err
		}
		svc.ServiceSeed = seed
		changed = true
	}
	if _, err := p2p.PeerIDFromSeed(svc.ServiceSeed); err != nil {
		return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("service %q has invalid service_seed: %w", cfg.Service.Name, err)
	}
	if svc.ServiceClaimFile == "" {
		svc.ServiceClaimFile = serviceClaimPath(configPath, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name)
		changed = true
	}

	if cluster.AuthorityPrivateKeyFile != "" {
		if err := ensureLocalServiceClaim(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc); err != nil {
			return cfg, cfgpkg.NamespaceService{}, err
		}
	}

	if changed {
		namespace.Services[cfg.Service.Name] = svc
		cluster.Namespaces[cfg.CurrentNamespace] = namespace
		cfg.Clusters[cfg.CurrentCluster] = cluster
		if err := saveLocalConfig(configPath, cfg); err != nil {
			return cfg, cfgpkg.NamespaceService{}, err
		}
	}
	return cfg, svc, nil
}

func ensureLocalServiceClaim(cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) error {
	if _, err := os.Stat(svc.ServiceClaimFile); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
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
		return fmt.Errorf("cluster %q authority public key mismatch", clusterName)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return err
	}
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{
		ClusterID:     cluster.ClusterID,
		NamespaceID:   namespaceName,
		ServiceID:     svc.ServiceID,
		SubjectPeerID: servicePeerID.String(),
		Permissions:   []string{capability.PermissionAttach, capability.PermissionAnnounce},
		ExpiresAt:     time.Now().Add(365 * 24 * time.Hour),
	}, privKey)
	if err != nil {
		return err
	}
	return writeServiceClaimFile(svc.ServiceClaimFile, claim)
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

func serviceMembershipCapabilityPath(configPath, clusterName, namespaceName string) string {
	return filepath.Join(filepath.Dir(configPath), "clusters", sanitizeProcessName(clusterName), "namespaces", sanitizeProcessName(namespaceName), "cluster.membership.cap.json")
}

func ensureServiceMembershipCapabilityFile(configPath string, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceSeed string) (string, error) {
	capPath := serviceMembershipCapabilityPath(configPath, clusterName, namespaceName)
	if _, err := os.Stat(capPath); err == nil {
		return capPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" || cluster.AuthorityPrivateKeyFile == "" {
		return "", fmt.Errorf("cluster %q is missing identity metadata", clusterName)
	}
	privKey, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return "", fmt.Errorf("load cluster authority key: %w", err)
	}
	pubAuthorized, err := clusterAuthorityPublicKeyString(privKey)
	if err != nil {
		return "", err
	}
	if cluster.AuthorityPublicKey != pubAuthorized {
		return "", fmt.Errorf("cluster %q authority public key mismatch", clusterName)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		return "", err
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     cluster.ClusterID,
		NamespaceID:   namespaceName,
		SubjectPeerID: servicePeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(365 * 24 * time.Hour),
	}, privKey)
	if err != nil {
		return "", err
	}
	if err := writeCapabilityFile(capPath, membership); err != nil {
		return "", err
	}
	return capPath, nil
}
