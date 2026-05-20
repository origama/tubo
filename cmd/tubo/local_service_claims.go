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

	attachauth "github.com/origama/tubo/internal/attachauth"
	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	workspace "github.com/origama/tubo/internal/workspace"
)

func createLocalService(configPath, name string) error {
	result, err := localWorkspace().CreateService(configPath, name)
	if err != nil {
		return err
	}
	ctx := result.Context
	if result.AlreadyExists {
		fmt.Printf("service %q already exists in cluster %q namespace %q\n", ctx.Name, ctx.ClusterName, ctx.Namespace)
	} else {
		fmt.Printf("created service %q in cluster %q namespace %q\n", ctx.Name, ctx.ClusterName, ctx.Namespace)
	}
	fmt.Printf("service id: %s\n", ctx.Service.ServiceID)
	fmt.Printf("service seed: %s\n", ctx.Service.ServiceSeed)
	if ctx.Service.ServiceOwnerKeyFile != "" {
		fmt.Printf("service owner key file: %s\n", ctx.Service.ServiceOwnerKeyFile)
	}
	fmt.Printf("service claim file: %s\n", ctx.Service.ServiceClaimFile)
	if ctx.Service.ServicePublishLeaseFile != "" {
		fmt.Printf("service publish lease file: %s\n", ctx.Service.ServicePublishLeaseFile)
	}
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
	return workspace.DerivePaths(configPath).ServiceClaim(clusterName, namespaceName, serviceName)
}

func servicePublishLeasePath(configPath, clusterName, namespaceName, serviceName string) string {
	return workspace.DerivePaths(configPath).ServicePublishLease(clusterName, namespaceName, serviceName)
}

func serviceOwnerKeyPath(configPath, clusterName, namespaceName, serviceName string) string {
	return workspace.DerivePaths(configPath).ServiceOwnerKey(clusterName, namespaceName, serviceName)
}

func ensureAttachServiceIdentity(configPath string, cfg cfgpkg.Config) (cfgpkg.Config, cfgpkg.NamespaceService, error) {
	return localWorkspace().EnsureAttachServiceIdentity(configPath, cfg)
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
	result, err := newAttachAuthResolver().Resolve(context.Background(), attachauth.ResolveRequest{ConfigPath: configPath, Config: cfg})
	if err != nil {
		return attachAuthorization{}, err
	}
	switch result.Decision {
	case attachauth.DecisionReady:
		return attachAuthorization{Config: result.Config, Service: result.Service, ServicePeerID: result.ServicePeerID, ServiceClaimFile: result.ServiceClaimFile, ServicePublishLeaseFile: result.ServicePublishLeaseFile, MembershipCapabilityFile: result.MembershipCapabilityFile, ServiceShareToken: result.ServiceShareToken, ShareRecoveryHint: result.ShareRecoveryHint, PublishLeaseReused: result.PublishLeaseReused, MintedServiceClaim: result.MintedLocally}, nil
	case attachauth.DecisionPendingApproval, attachauth.DecisionDenied:
		if strings.TrimSpace(result.UserMessage) == "" {
			return attachAuthorization{}, errors.New("attach authorization denied")
		}
		return attachAuthorization{}, errors.New(result.UserMessage)
	case attachauth.DecisionRetryable:
		if strings.Contains(result.UserMessage, "grant request ") {
			return attachAuthorization{}, errors.New(result.UserMessage)
		}
		return attachAuthorization{}, noServicePublishGrantError(result.Config.CurrentCluster, result.Config.CurrentNamespace, result.Config.Service.Name)
	default:
		return attachAuthorization{}, errors.New("attach authorization returned an unknown decision")
	}
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
					return finalizeAuthorityServiceShareToken(artifacts.Token, privKey, svc.ServiceID)
				}
			}
		}
	}
	token, err := grantspkg.BuildServiceShareToken(privKey, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, grantspkg.ServiceShareDefaultTTL)
	if err != nil {
		return "", err
	}
	return finalizeAuthorityServiceShareToken(token, privKey, svc.ServiceID)
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
	return workspace.DerivePaths(configPath).ServiceMembershipCapability(clusterName, namespaceName)
}

func ensureServiceMembershipCapabilityFile(configPath string, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceSeed string) (string, error) {
	return localWorkspace().ResolveMembershipCapabilityFile(configPath, cluster, clusterName, namespaceName, serviceSeed)
}
