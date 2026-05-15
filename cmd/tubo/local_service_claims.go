package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
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

func mintLocalServiceClaim(cluster cfgpkg.Cluster, clusterName, namespaceName string, svc cfgpkg.NamespaceService) error {
	if svc.ServiceClaimFile == "" {
		return errors.New("service claim file is required")
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

type attachAuthorization struct {
	Config                   cfgpkg.Config
	Service                  cfgpkg.NamespaceService
	ServicePeerID            string
	ServiceClaimFile         string
	MembershipCapabilityFile string
	MintedServiceClaim       bool
}

func resolveAttachAuthorization(configPath string, cfg cfgpkg.Config) (attachAuthorization, error) {
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		return attachAuthorization{}, err
	}
	cluster := cfg.Clusters[cfg.CurrentCluster]
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return attachAuthorization{}, err
	}
	pub, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey)
	if err != nil {
		return attachAuthorization{}, fmt.Errorf("parse authority public key for cluster %q: %w", cfg.CurrentCluster, err)
	}

	if err := verifyServiceClaimFile(svc.ServiceClaimFile, pub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID.String()); err == nil {
		membershipFile, err := resolveAttachMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
		if err != nil {
			return attachAuthorization{}, err
		}
		return attachAuthorization{Config: cfg, Service: svc, ServicePeerID: servicePeerID.String(), ServiceClaimFile: svc.ServiceClaimFile, MembershipCapabilityFile: membershipFile}, nil
	} else if !errors.Is(err, os.ErrNotExist) && cluster.AuthorityPrivateKeyFile == "" {
		return attachAuthorization{}, fmt.Errorf("service publish grant for cluster %q namespace %q service %q rejected: %w", cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, err)
	}

	if cluster.AuthorityPrivateKeyFile != "" {
		if err := mintLocalServiceClaim(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc); err != nil {
			return attachAuthorization{}, err
		}
		membershipFile, err := resolveAttachMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
		if err != nil {
			return attachAuthorization{}, err
		}
		return attachAuthorization{Config: cfg, Service: svc, ServicePeerID: servicePeerID.String(), ServiceClaimFile: svc.ServiceClaimFile, MembershipCapabilityFile: membershipFile, MintedServiceClaim: true}, nil
	}

	grantPeer := svc.GrantServicePeer
	if grantPeer == "" {
		grantPeer = clusterGrantServicePeer(cluster)
	}
	if grantPeer != "" {
		svc.GrantServicePeer = grantPeer
		updatedCfg, updatedSvc, err := requestPublishGrantForAttach(configPath, cfg, svc, servicePeerID.String())
		if err != nil {
			return attachAuthorization{}, err
		}
		cfg = updatedCfg
		svc = updatedSvc
		cluster = cfg.Clusters[cfg.CurrentCluster]
		if err := verifyServiceClaimFile(svc.ServiceClaimFile, pub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID.String()); err == nil {
			membershipFile, err := resolveAttachMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
			if err != nil {
				return attachAuthorization{}, err
			}
			return attachAuthorization{Config: cfg, Service: svc, ServicePeerID: servicePeerID.String(), ServiceClaimFile: svc.ServiceClaimFile, MembershipCapabilityFile: membershipFile}, nil
		}
		return attachAuthorization{}, fmt.Errorf("publish grant request %q is pending; publication requires an approved ServiceClaim", svc.GrantRequestID)
	}

	return attachAuthorization{}, noServicePublishGrantError(cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name)
}

func requestPublishGrantForAttach(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, error) {
	cluster := cfg.Clusters[cfg.CurrentCluster]
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return cfg, svc, err
	}
	h, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", grantsFirstNonEmpty(cfg.Node.Seed, "grant-client-"+svc.ServiceSeed), psk)
	if err != nil {
		return cfg, svc, err
	}
	defer h.Close()
	info, err := p2p.AddrInfoFromString(svc.GrantServicePeer)
	if err != nil {
		return cfg, svc, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var resp grantspkg.Message
	if svc.GrantRequestID != "" {
		resp, err = grantspkg.Poll(ctx, h, info, svc.GrantRequestID)
	} else {
		resp, err = grantspkg.Submit(ctx, h, info, grantspkg.Message{
			Type:                 grantspkg.TypeSubmit,
			Version:              grantspkg.VersionV1,
			ClusterID:            cluster.ClusterID,
			NamespaceID:          cfg.CurrentNamespace,
			ServiceName:          cfg.Service.Name,
			ServiceID:            svc.ServiceID,
			ServicePeerID:        servicePeerID,
			RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce},
			RequestedTTLSeconds:  int64((30 * 24 * time.Hour).Seconds()),
		})
	}
	if err != nil {
		return cfg, svc, err
	}
	if err := handleGrantClientResponse(configPath, cfg, svc, svc.GrantServicePeer, resp, servicePeerID); err != nil {
		return cfg, svc, err
	}
	updated, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		return cfg, svc, err
	}
	updatedSvc := updated.Clusters[updated.CurrentCluster].Namespaces[updated.CurrentNamespace].Services[updated.Service.Name]
	return updated, updatedSvc, nil
}

func verifyServiceClaimFile(path string, pub ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error {
	if strings.TrimSpace(path) == "" {
		return os.ErrNotExist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var claim capability.ServiceClaim
	if err := json.Unmarshal(b, &claim); err != nil {
		return err
	}
	return capability.VerifyServiceClaim(claim, pub, clusterID, namespaceID, serviceID, servicePeerID)
}

func resolveAttachMembershipCapabilityFile(configPath string, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceSeed string) (string, error) {
	capPath := serviceMembershipCapabilityPath(configPath, clusterName, namespaceName)
	if _, err := os.Stat(capPath); err == nil {
		return capPath, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if cluster.AuthorityPrivateKeyFile != "" {
		return ensureServiceMembershipCapabilityFile(configPath, cluster, clusterName, namespaceName, serviceSeed)
	}
	return namespaceMembershipCapabilityFile(cluster, namespaceName)
}

func noServicePublishGrantError(clusterName, namespaceName, serviceName string) error {
	return fmt.Errorf("no service publish grant for cluster %q namespace %q service %q; request a grant from a cluster authority or run attach from an authority node", clusterName, namespaceName, serviceName)
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
