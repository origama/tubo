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
	"github.com/origama/tubo/internal/serviceidentity"
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
	svc := namespace.Services[name]
	if svc.ServiceID != "" && svc.ServiceSeed != "" && svc.ServiceClaimFile != "" {
		if _, err := ensureServiceMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed); err != nil {
			return err
		}
		fmt.Printf("service %q already exists in cluster %q namespace %q\n", name, cfg.CurrentCluster, cfg.CurrentNamespace)
		fmt.Printf("service id: %s\n", svc.ServiceID)
		fmt.Printf("service seed: %s\n", svc.ServiceSeed)
		fmt.Printf("service claim file: %s\n", svc.ServiceClaimFile)
		if svc.ServicePublishLeaseFile != "" {
			fmt.Printf("service publish lease file: %s\n", svc.ServicePublishLeaseFile)
		}
		if svc.ServiceOwnerKeyFile != "" {
			fmt.Printf("service owner key file: %s\n", svc.ServiceOwnerKeyFile)
		}
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
	serviceSeed, err := generateServiceSeed()
	if err != nil {
		return err
	}
	serviceDir := filepath.Join(filepath.Dir(configPath), "clusters", sanitizeProcessName(cfg.CurrentCluster), "namespaces", sanitizeProcessName(cfg.CurrentNamespace), "services")
	if err := os.MkdirAll(serviceDir, 0700); err != nil {
		return err
	}
	claimPath := filepath.Join(serviceDir, sanitizeProcessName(name)+".claim.json")
	ownerKeyPath := serviceOwnerKeyPath(configPath, cfg.CurrentCluster, cfg.CurrentNamespace, name)
	serviceID := svc.ServiceID
	if serviceID == "" {
		identity, created, err := serviceidentity.Ensure(ownerKeyPath)
		if err != nil {
			return err
		}
		serviceID = identity.ServiceID
		if svc.ServiceOwnerKeyFile == "" || svc.ServiceOwnerKeyFile != ownerKeyPath || created {
			svc.ServiceOwnerKeyFile = ownerKeyPath
		}
	} else if svc.ServiceOwnerKeyFile != "" {
		identity, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
		if err != nil {
			return err
		}
		if identity.ServiceID != serviceID {
			return fmt.Errorf("service %q identity mismatch in cluster %q namespace %q: service_id=%q want %q", cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, serviceID, identity.ServiceID)
		}
	}
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
	if err := writeServiceClaimFile(claimPath, claim); err != nil {
		return err
	}
	if _, err := ensureServiceMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, serviceSeed); err != nil {
		return err
	}
	svc = cfgpkg.NamespaceService{ServiceID: serviceID, ServiceSeed: serviceSeed, ServiceOwnerKeyFile: svc.ServiceOwnerKeyFile, ServiceClaimFile: claimPath, ServicePublishLeaseFile: servicePublishLeasePath(configPath, cfg.CurrentCluster, cfg.CurrentNamespace, name)}
	if err := mintLocalServicePublishLease(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, name, svc); err != nil {
		return err
	}
	namespace.Services[name] = svc
	cluster.Namespaces[cfg.CurrentNamespace] = namespace
	cfg.Clusters[cfg.CurrentCluster] = cluster
	if err := saveLocalConfig(configPath, cfg); err != nil {
		return err
	}
	fmt.Printf("created service %q in cluster %q namespace %q\n", name, cfg.CurrentCluster, cfg.CurrentNamespace)
	fmt.Printf("service id: %s\n", serviceID)
	fmt.Printf("service seed: %s\n", serviceSeed)
	if svc.ServiceOwnerKeyFile != "" {
		fmt.Printf("service owner key file: %s\n", svc.ServiceOwnerKeyFile)
	}
	fmt.Printf("service claim file: %s\n", claimPath)
	fmt.Printf("service publish lease file: %s\n", servicePublishLeasePath(configPath, cfg.CurrentCluster, cfg.CurrentNamespace, name))
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

func servicePublishLeasePath(configPath, clusterName, namespaceName, serviceName string) string {
	return filepath.Join(filepath.Dir(configPath), "clusters", sanitizeProcessName(clusterName), "namespaces", sanitizeProcessName(namespaceName), "services", sanitizeProcessName(serviceName)+".publish-lease.json")
}

func serviceOwnerKeyPath(configPath, clusterName, namespaceName, serviceName string) string {
	return filepath.Join(filepath.Dir(configPath), "clusters", sanitizeProcessName(clusterName), "namespaces", sanitizeProcessName(namespaceName), "services", sanitizeProcessName(serviceName)+".owner.key")
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

	svc := namespace.Services[cfg.Service.Name]
	changed := false
	if svc.ServiceID != "" && svc.ServiceOwnerKeyFile == "" {
		return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("service %q is missing service_owner_key_file", cfg.Service.Name)
	}
	if svc.ServiceID == "" {
		ownerKeyPath := svc.ServiceOwnerKeyFile
		if ownerKeyPath == "" {
			ownerKeyPath = serviceOwnerKeyPath(configPath, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name)
		}
		identity, _, err := serviceidentity.Ensure(ownerKeyPath)
		if err != nil {
			return cfg, cfgpkg.NamespaceService{}, err
		}
		svc.ServiceID = identity.ServiceID
		svc.ServiceOwnerKeyFile = ownerKeyPath
		changed = true
	} else {
		if err := serviceidentity.ValidateServiceID(svc.ServiceID); err != nil {
			return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("service %q has invalid service_id: %w", cfg.Service.Name, err)
		}
		if svc.ServiceOwnerKeyFile != "" {
			identity, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
			if err != nil {
				return cfg, cfgpkg.NamespaceService{}, err
			}
			if identity.ServiceID != svc.ServiceID {
				return cfg, cfgpkg.NamespaceService{}, fmt.Errorf("service %q identity mismatch in cluster %q namespace %q: service_id=%q want %q", cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceID, identity.ServiceID)
			}
		}
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
	if svc.ServicePublishLeaseFile == "" {
		svc.ServicePublishLeaseFile = servicePublishLeasePath(configPath, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name)
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

func mintLocalServicePublishLease(cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) error {
	if svc.ServiceClaimFile == "" || svc.ServicePublishLeaseFile == "" {
		return errors.New("service claim and publish lease files are required")
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
	owner, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
	if err != nil {
		return fmt.Errorf("load service owner key: %w", err)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return err
	}
	req, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:             cluster.ClusterID,
		NamespaceID:           namespaceName,
		ServiceID:             svc.ServiceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:       servicePeerID.String(),
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 randomNonce(),
	}, owner.PrivateKey)
	if err != nil {
		return err
	}
	artifacts, err := grantspkg.BuildApprovalArtifacts(privKey, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, servicePeerID.String(), 365*24*time.Hour, attachPublishLeaseTTL(), req.RequestedCapabilities, req.ServicePublicKey, req.Nonce, req.ServiceOwnerSignature)
	if err != nil {
		return err
	}
	if err := writeServiceClaimFile(svc.ServiceClaimFile, artifacts.ServiceClaim); err != nil {
		return err
	}
	if err := writePublishLeaseFile(svc.ServicePublishLeaseFile, artifacts.PublishLease); err != nil {
		return err
	}
	return nil
}

func attachPublishLeaseTTL() time.Duration {
	if v := strings.TrimSpace(os.Getenv("TUBO_PUBLISH_LEASE_TTL")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return grantspkg.ServiceShareDefaultTTL
}

func attachPublishLeaseRenewBefore(ttl time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv("TUBO_PUBLISH_LEASE_RENEW_BEFORE")); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	if ttl <= 0 {
		return 5 * time.Minute
	}
	before := ttl / 6
	if ttl >= 30*time.Minute {
		if before < 5*time.Minute {
			before = 5 * time.Minute
		}
		if before > 10*time.Minute {
			before = 10 * time.Minute
		}
	}
	if before < time.Second {
		before = time.Second
	}
	if before >= ttl {
		before = ttl / 2
	}
	if before < time.Second {
		before = time.Second
	}
	return before
}

type attachAuthorization struct {
	Config                   cfgpkg.Config
	Service                  cfgpkg.NamespaceService
	ServicePeerID            string
	ServiceClaimFile         string
	ServicePublishLeaseFile  string
	MembershipCapabilityFile string
	ServiceShareToken        string
	ShareRecoveryHint        string
	PublishLeaseReused       bool
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

	if err := verifyPublishLeaseFile(svc.ServicePublishLeaseFile, pub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID.String()); err == nil {
		membershipFile, err := resolveAttachMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
		if err != nil {
			return attachAuthorization{}, err
		}
		shareToken, err := buildAttachServiceShareToken(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
		if err != nil {
			return attachAuthorization{}, err
		}
		shareHint := ""
		grantPeer := svc.GrantServicePeer
		if grantPeer == "" {
			grantPeer = clusterGrantServicePeer(cluster)
		}
		if shareToken == "" {
			shareHint = attachShareRecoveryHint(cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace, grantPeer, svc.GrantRequestID)
		}
		return attachAuthorization{Config: cfg, Service: svc, ServicePeerID: servicePeerID.String(), ServiceClaimFile: svc.ServiceClaimFile, ServicePublishLeaseFile: svc.ServicePublishLeaseFile, MembershipCapabilityFile: membershipFile, ServiceShareToken: shareToken, ShareRecoveryHint: shareHint, PublishLeaseReused: true}, nil
	} else if errors.Is(err, os.ErrNotExist) || isPublishLeaseExpiredError(err) {
		if claimErr := verifyServiceClaimFile(svc.ServiceClaimFile, pub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID.String()); claimErr == nil {
			membershipFile, err := resolveAttachMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
			if err != nil {
				return attachAuthorization{}, err
			}
			shareToken, err := buildAttachServiceShareToken(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
			if err != nil {
				return attachAuthorization{}, err
			}
			grantPeer := svc.GrantServicePeer
			if grantPeer == "" {
				grantPeer = clusterGrantServicePeer(cluster)
			}
			if grantPeer != "" {
				updatedCfg, updatedSvc, refreshedShareToken, refreshErr := renewAttachPublishAuthorization(configPath, cfg, svc, servicePeerID.String())
				if refreshErr == nil {
					cfg = updatedCfg
					svc = updatedSvc
					shareToken = refreshedShareToken
					cluster = cfg.Clusters[cfg.CurrentCluster]
					return attachAuthorization{Config: cfg, Service: svc, ServicePeerID: servicePeerID.String(), ServiceClaimFile: svc.ServiceClaimFile, ServicePublishLeaseFile: svc.ServicePublishLeaseFile, MembershipCapabilityFile: membershipFile, ServiceShareToken: shareToken}, nil
				}
				if cluster.AuthorityPrivateKeyFile == "" {
					return attachAuthorization{}, refreshErr
				}
			}
			if cluster.AuthorityPrivateKeyFile == "" {
				return attachAuthorization{Config: cfg, Service: svc, ServicePeerID: servicePeerID.String(), ServiceClaimFile: svc.ServiceClaimFile, ServicePublishLeaseFile: svc.ServicePublishLeaseFile, MembershipCapabilityFile: membershipFile, ServiceShareToken: shareToken}, nil
			}
		} else if !errors.Is(claimErr, os.ErrNotExist) {
			return attachAuthorization{}, fmt.Errorf("service claim for cluster %q namespace %q service %q rejected: %w", cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, claimErr)
		}
	} else if cluster.AuthorityPrivateKeyFile == "" {
		return attachAuthorization{}, fmt.Errorf("service publish lease for cluster %q namespace %q service %q rejected: %w", cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, err)
	}

	if cluster.AuthorityPrivateKeyFile != "" {
		if err := mintLocalServicePublishLease(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc); err != nil {
			return attachAuthorization{}, err
		}
		membershipFile, err := resolveAttachMembershipCapabilityFile(configPath, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, svc.ServiceSeed)
		if err != nil {
			return attachAuthorization{}, err
		}
		shareToken, err := buildAttachServiceShareToken(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
		if err != nil {
			return attachAuthorization{}, err
		}
		return attachAuthorization{Config: cfg, Service: svc, ServicePeerID: servicePeerID.String(), ServiceClaimFile: svc.ServiceClaimFile, ServicePublishLeaseFile: svc.ServicePublishLeaseFile, MembershipCapabilityFile: membershipFile, ServiceShareToken: shareToken, MintedServiceClaim: true}, nil
	}

	grantPeer := svc.GrantServicePeer
	if grantPeer == "" {
		grantPeer = clusterGrantServicePeer(cluster)
	}
	if grantPeer != "" {
		svc.GrantServicePeer = grantPeer
		updatedCfg, updatedSvc, shareToken, err := requestPublishGrantForAttach(configPath, cfg, svc, servicePeerID.String())
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
			return attachAuthorization{Config: cfg, Service: svc, ServicePeerID: servicePeerID.String(), ServiceClaimFile: svc.ServiceClaimFile, MembershipCapabilityFile: membershipFile, ServiceShareToken: shareToken}, nil
		}
		return attachAuthorization{}, fmt.Errorf("publish grant request %q is pending; publication requires an approved publish lease", svc.GrantRequestID)
	}

	return attachAuthorization{}, noServicePublishGrantError(cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name)
}

func requestPublishGrantForAttach(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	cluster := cfg.Clusters[cfg.CurrentCluster]
	overlay, err := p2p.NewOverlayHost(p2p.OverlayHostConfig{Listen: "/ip4/127.0.0.1/tcp/0", Seed: grantsFirstNonEmpty(cfg.Node.Seed, "grant-client-"+svc.ServiceSeed), PrivateKeyFile: cfg.Network.PrivateKeyFile, PrivateKeyB64: cfg.Network.PrivateKeyB64, BootstrapPeers: cfg.Network.BootstrapPeers, RelayPeers: cfg.Network.RelayPeers, Autorelay: cfg.Network.Autorelay, HolePunching: cfg.Network.HolePunching, ForceReachability: cfg.Network.ForceReachability, Component: "grants-client"})
	if err != nil {
		return cfg, svc, "", err
	}
	defer overlay.Close()
	info, err := p2p.AddrInfoFromString(svc.GrantServicePeer)
	if err != nil {
		return cfg, svc, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	overlay.StartBootstrapRetry(ctx, 5*time.Second)
	overlay.StartRelayReservations(ctx)
	var resp grantspkg.Message
	if svc.GrantRequestID != "" {
		resp, err = grantspkg.Poll(ctx, overlay.Host, info, svc.GrantRequestID)
	} else {
		leaseReq, err := buildServicePublishLeaseRequest(configPath, cfg, svc, servicePeerID)
		if err != nil {
			return cfg, svc, "", err
		}
		resp, err = grantspkg.Submit(ctx, overlay.Host, info, grantspkg.Message{
			Type:                  grantspkg.TypeSubmit,
			Version:               grantspkg.VersionV1,
			ClusterID:             cluster.ClusterID,
			NamespaceID:           cfg.CurrentNamespace,
			ServiceName:           cfg.Service.Name,
			ServiceID:             svc.ServiceID,
			ServicePublicKey:      leaseReq.ServicePublicKey,
			ServiceOwnerSignature: leaseReq.ServiceOwnerSignature,
			ServicePeerID:         servicePeerID,
			RequestNonce:          leaseReq.Nonce,
			RequestedPermissions:  []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
			RequestedTTLSeconds:   int64(attachPublishLeaseTTL().Seconds()),
		})
	}
	if err != nil {
		return cfg, svc, "", err
	}
	shareToken, err := handleGrantClientResponse(configPath, cfg, svc, svc.GrantServicePeer, resp, servicePeerID)
	if err != nil {
		return cfg, svc, "", err
	}
	updated, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		return cfg, svc, "", err
	}
	updatedSvc := updated.Clusters[updated.CurrentCluster].Namespaces[updated.CurrentNamespace].Services[updated.Service.Name]
	authorityPub, err := discovery.ParseAuthorityPublicKey(updated.Clusters[updated.CurrentCluster].AuthorityPublicKey)
	if err != nil {
		return updated, updatedSvc, shareToken, err
	}
	if err := verifyPublishLeaseFile(updatedSvc.ServicePublishLeaseFile, authorityPub, updated.Clusters[updated.CurrentCluster].ClusterID, updated.CurrentNamespace, updatedSvc.ServiceID, servicePeerID); err != nil {
		if updatedSvc.GrantRequestID != "" {
			return updated, updatedSvc, shareToken, fmt.Errorf("publish grant request %q is pending; publication requires an approved publish lease", updatedSvc.GrantRequestID)
		}
		return updated, updatedSvc, shareToken, err
	}
	return updated, updatedSvc, shareToken, nil
}

func renewAttachPublishAuthorization(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	cluster := cfg.Clusters[cfg.CurrentCluster]
	grantPeer := svc.GrantServicePeer
	if grantPeer == "" {
		grantPeer = clusterGrantServicePeer(cluster)
	}
	if grantPeer != "" {
		svc.GrantServicePeer = grantPeer
		updatedCfg, updatedSvc, shareToken, err := requestPublishGrantForAttach(configPath, cfg, svc, servicePeerID)
		if err == nil {
			return updatedCfg, updatedSvc, shareToken, nil
		}
		if cluster.AuthorityPrivateKeyFile == "" {
			return cfg, svc, "", err
		}
	}
	if cluster.AuthorityPrivateKeyFile != "" {
		if err := mintLocalServicePublishLease(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc); err != nil {
			return cfg, svc, "", err
		}
		shareToken, err := buildAttachServiceShareToken(cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
		if err != nil {
			return cfg, svc, "", err
		}
		return cfg, svc, shareToken, nil
	}
	return cfg, svc, "", fmt.Errorf("service publish lease renewal requires a grant service peer or local authority key")
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

func buildAttachServiceShareToken(cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) (string, error) {
	if cluster.AuthorityPrivateKeyFile == "" {
		return "", nil
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
	if svc.ServicePublishLeaseFile != "" {
		if leaseBytes, err := os.ReadFile(svc.ServicePublishLeaseFile); err == nil {
			var lease grantspkg.PublishLease
			if err := json.Unmarshal(leaseBytes, &lease); err == nil {
				if artifacts, err := grantspkg.BuildShareInviteArtifactsFromLease(privKey, clusterName, lease, serviceName, grantspkg.ServiceShareDefaultTTL); err == nil {
					return artifacts.Token, nil
				}
			}
		}
	}
	return grantspkg.BuildServiceShareToken(privKey, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, grantspkg.ServiceShareDefaultTTL)
}

func printAttachShareHint(cfg cfgpkg.Config, authz attachAuthorization) {
	overlayLabel := cfg.CurrentOverlay
	if overlayLabel == joinDefaultNetworkName {
		overlayLabel = "public"
	}
	fmt.Printf("attached service %q\n", cfg.Service.Name)
	if authz.Service.ServiceID != "" {
		fmt.Printf("service id: %s\n", authz.Service.ServiceID)
	}
	fmt.Printf("scope: %s/%s/%s\n", overlayLabel, cfg.CurrentCluster, cfg.CurrentNamespace)
	if authz.PublishLeaseReused {
		fmt.Printf("publish lease: reused\n")
	}
	if strings.TrimSpace(authz.ServiceShareToken) != "" {
		fmt.Printf("share:\n  tubo connect --token %s --local 127.0.0.1:18888\n\n", authz.ServiceShareToken)
		return
	}
	if strings.TrimSpace(authz.ShareRecoveryHint) != "" {
		fmt.Printf("share: unavailable locally (no authority key available to sign a share invite)\n")
		fmt.Printf("hint: %s\n\n", authz.ShareRecoveryHint)
		return
	}
	fmt.Printf("share: unavailable (no authority key available to sign a share invite)\n")
	fmt.Printf("hint: run `tubo share service/%s --cluster %s --namespace %s` from an authority node, or retry attach on the authority node if you need a copyable connect token\n\n", cfg.Service.Name, cfg.CurrentCluster, cfg.CurrentNamespace)
}

func attachShareRecoveryHint(serviceName, clusterName, namespaceName, grantPeer, grantRequestID string) string {
	if strings.TrimSpace(grantPeer) == "" {
		return fmt.Sprintf("run `tubo share service/%s --cluster %s --namespace %s` from an authority node, or retry attach on the authority node if you need a copyable connect token", serviceName, clusterName, namespaceName)
	}
	if strings.TrimSpace(grantRequestID) != "" {
		return fmt.Sprintf("reprint the token with `tubo grants request service/%s --poll --peer %s --cluster %s --namespace %s` (request %s)", serviceName, grantPeer, clusterName, namespaceName, grantRequestID)
	}
	return fmt.Sprintf("request or poll the grant with `tubo grants request service/%s --peer %s --cluster %s --namespace %s`", serviceName, grantPeer, clusterName, namespaceName)
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

func writePublishLeaseFile(path string, lease grantspkg.PublishLease) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0600)
}

func verifyPublishLeaseFile(path string, pub ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error {
	if strings.TrimSpace(path) == "" {
		return os.ErrNotExist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var lease grantspkg.PublishLease
	if err := json.Unmarshal(b, &lease); err != nil {
		return err
	}
	return grantspkg.VerifyPublishLease(lease, pub, clusterID, namespaceID, serviceID, servicePeerID)
}

func isPublishLeaseExpiredError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "publish lease expired")
}

func readPublishLeaseFile(path string) (grantspkg.PublishLease, error) {
	if strings.TrimSpace(path) == "" {
		return grantspkg.PublishLease{}, os.ErrNotExist
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return grantspkg.PublishLease{}, err
	}
	var lease grantspkg.PublishLease
	if err := json.Unmarshal(b, &lease); err != nil {
		return grantspkg.PublishLease{}, err
	}
	return lease, nil
}

func buildServicePublishLeaseRequest(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (grantspkg.PublishLeaseRequest, error) {
	if svc.ServiceOwnerKeyFile == "" {
		return grantspkg.PublishLeaseRequest{}, errors.New("service owner key file is required")
	}
	owner, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
	if err != nil {
		return grantspkg.PublishLeaseRequest{}, err
	}
	req := grantspkg.PublishLeaseRequest{
		ClusterID:             cfg.Clusters[cfg.CurrentCluster].ClusterID,
		NamespaceID:           cfg.CurrentNamespace,
		ServiceID:             svc.ServiceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:       servicePeerID,
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 randomNonce(),
	}
	return grantspkg.SignPublishLeaseRequest(req, owner.PrivateKey)
}

func randomNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("nonce-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
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
