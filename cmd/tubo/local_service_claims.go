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
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	serviceapp "github.com/origama/tubo/internal/app/service"
	attachauth "github.com/origama/tubo/internal/attachauth"
	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	logging "github.com/origama/tubo/internal/logging"
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
	artifacts, err := grantspkg.BuildApprovalArtifacts(privKey, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, servicePeerID.String(), string(cfgpkg.NormalizeServiceKind(svc.Kind, "")), 365*24*time.Hour, attachPublishLeaseTTL(), req.RequestedCapabilities, req.ServicePublicKey, req.Nonce, req.ServiceOwnerSignature)
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

type attachPublishAuthorizationCoordinator struct {
	mu            sync.Mutex
	configPath    string
	cfg           cfgpkg.Config
	svc           cfgpkg.NamespaceService
	servicePeerID string
	backoff       time.Duration
	nextAttempt   time.Time
	now           func() time.Time
	renew         func(string, cfgpkg.Config, cfgpkg.NamespaceService, string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error)
}

func newAttachPublishAuthorizationCoordinator(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) *attachPublishAuthorizationCoordinator {
	return &attachPublishAuthorizationCoordinator{configPath: configPath, cfg: cfg, svc: svc, servicePeerID: servicePeerID, backoff: 5 * time.Second, now: time.Now, renew: renewAttachPublishAuthorization}
}

func (c *attachPublishAuthorizationCoordinator) handle(ctx context.Context, req serviceapp.PublishAuthorizationRequest) serviceapp.PublishAuthorizationResult {
	if c == nil {
		return serviceapp.PublishAuthorizationResult{Outcome: serviceapp.PublishAuthorizationOutcomeSkipped, Message: "coordinator unavailable"}
	}
	if ctx.Err() != nil {
		return serviceapp.PublishAuthorizationResult{Outcome: serviceapp.PublishAuthorizationOutcomeSkipped, Message: ctx.Err().Error()}
	}
	reason := req.Reason
	if reason != serviceapp.AnnouncementBlockedPublishLeaseMissing && reason != serviceapp.AnnouncementBlockedPublishLeaseExpired && reason != serviceapp.AnnouncementBlockedPublishLeaseInvalid && reason != serviceapp.AnnouncementBlockedMembershipCapabilityMissing && reason != serviceapp.AnnouncementBlockedMembershipCapabilityInvalid {
		return serviceapp.PublishAuthorizationResult{Outcome: serviceapp.PublishAuthorizationOutcomeSkipped, Message: "publish authorization not required for this announcement block"}
	}
	if cfg, svc, err := refreshAttachAuthorizationMaterial(c.configPath, c.cfg); err == nil {
		c.cfg = cfg
		c.svc = svc
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.now != nil {
		now = c.now()
	}
	if !c.nextAttempt.IsZero() && now.Before(c.nextAttempt) {
		nextAttempt := c.nextAttempt
		return serviceapp.PublishAuthorizationResult{Outcome: serviceapp.PublishAuthorizationOutcomeSkipped, Message: fmt.Sprintf("retry backoff active until %s", nextAttempt.UTC().Format(time.RFC3339)), RetryAfter: &nextAttempt}
	}
	renew := c.renew
	if renew == nil {
		renew = renewAttachPublishAuthorization
	}
	cfg, svc, shareToken, err := renew(c.configPath, c.cfg, c.svc, c.servicePeerID)
	if err == nil {
		c.cfg = cfg
		c.svc = svc
		c.nextAttempt = time.Time{}
		if strings.TrimSpace(shareToken) != "" {
			logging.Progressf("publish authorization refreshed for service %q\n", c.cfg.Service.Name)
		}
		return serviceapp.PublishAuthorizationResult{Outcome: serviceapp.PublishAuthorizationOutcomeReady, Message: "publish authorization refreshed"}
	}
	c.cfg = cfg
	c.svc = svc
	outcome := classifyPublishAuthorizationOutcome(err)
	switch outcome {
	case serviceapp.PublishAuthorizationOutcomePending:
		c.nextAttempt = now.Add(c.backoff)
	case serviceapp.PublishAuthorizationOutcomeDenied:
		c.nextAttempt = now.Add(30 * time.Second)
	case serviceapp.PublishAuthorizationOutcomeUnreachable:
		c.nextAttempt = now.Add(c.backoff)
	case serviceapp.PublishAuthorizationOutcomeRetryable:
		c.nextAttempt = now.Add(c.backoff)
	default:
		c.nextAttempt = now.Add(c.backoff)
	}
	nextAttempt := c.nextAttempt
	return serviceapp.PublishAuthorizationResult{Outcome: outcome, Message: err.Error(), RetryAfter: &nextAttempt}
}

func refreshAttachAuthorizationMaterial(configPath string, cfg cfgpkg.Config) (cfgpkg.Config, cfgpkg.NamespaceService, error) {
	loaded, err := loadLocalConfigOrError(configPath)
	if err != nil {
		return cfg, cfgpkg.NamespaceService{}, err
	}
	cfg = loaded
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		return cfg, cfgpkg.NamespaceService{}, err
	}
	cfg = seedDiscoveredGrantServicePeer(configPath, cfg)
	return cfg, svc, nil
}

func (c *attachPublishAuthorizationCoordinator) run(ctx context.Context) {
	if c == nil || strings.TrimSpace(c.svc.ServicePublishLeaseFile) == "" {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		nextAttempt := c.nextAttempt
		leasePath := c.svc.ServicePublishLeaseFile
		c.mu.Unlock()
		if !nextAttempt.IsZero() && time.Now().Before(nextAttempt) {
			wait := time.Until(nextAttempt)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		lease, err := readPublishLeaseFile(leasePath)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			reason := serviceapp.AnnouncementBlockedPublishLeaseMissing
			if !errors.Is(err, os.ErrNotExist) {
				reason = serviceapp.AnnouncementBlockedPublishLeaseInvalid
			}
			_ = c.handle(ctx, serviceapp.PublishAuthorizationRequest{Reason: reason})
			continue
		}
		renewBefore := attachPublishLeaseRenewBefore(time.Until(lease.ExpiresAt.UTC()))
		wait := time.Until(lease.ExpiresAt.UTC().Add(-renewBefore))
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		_ = c.handle(ctx, serviceapp.PublishAuthorizationRequest{Reason: serviceapp.AnnouncementBlockedPublishLeaseExpired})
		continue
	}
}

func classifyPublishAuthorizationOutcome(err error) serviceapp.PublishAuthorizationOutcome {
	if err == nil {
		return serviceapp.PublishAuthorizationOutcomeReady
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "pending") || strings.Contains(msg, "awaiting approval") || strings.Contains(msg, "request pending"):
		return serviceapp.PublishAuthorizationOutcomePending
	case strings.Contains(msg, "denied") || strings.Contains(msg, "rejected") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "revoked") || strings.Contains(msg, "permission denied"):
		return serviceapp.PublishAuthorizationOutcomeDenied
	case strings.Contains(msg, "grant service peer") || strings.Contains(msg, "failed to dial") || strings.Contains(msg, "dial backoff") || strings.Contains(msg, "unreachable") || strings.Contains(msg, "timed out") || strings.Contains(msg, "timeout") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "connection reset") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "context deadline exceeded") || strings.Contains(msg, "temporary") || strings.Contains(msg, "expired") || strings.Contains(msg, "eof"):
		return serviceapp.PublishAuthorizationOutcomeUnreachable
	default:
		return serviceapp.PublishAuthorizationOutcomeRetryable
	}
}

func resolveAttachAuthorization(configPath string, cfg cfgpkg.Config) (attachAuthorization, error) {
	var err error
	if cfg, _, err = ensureAttachServiceIdentity(configPath, cfg); err != nil {
		return attachAuthorization{}, err
	}
	cfg = seedDiscoveredGrantServicePeer(configPath, cfg)
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
		if strings.Contains(result.UserMessage, "grant service peer") || strings.Contains(result.UserMessage, "local authority key") {
			return attachAuthorization{}, fmt.Errorf("missing grant service peer: %s", result.UserMessage)
		}
		if strings.TrimSpace(result.UserMessage) != "" && result.UserMessage != "stored publish authorization requires refresh or mint" {
			// grant client was called and returned a real error — surface it instead of the generic message
			return attachAuthorization{}, fmt.Errorf("%s; run `tubo grants request service/%s --cluster %s --namespace %s` to retry manually", result.UserMessage, result.Config.Service.Name, result.Config.CurrentCluster, result.Config.CurrentNamespace)
		}
		return attachAuthorization{}, noServicePublishGrantError(result.Config.CurrentCluster, result.Config.CurrentNamespace, result.Config.Service.Name)
	default:
		return attachAuthorization{}, errors.New("attach authorization returned an unknown decision")
	}
}

func requestPublishGrantForAttach(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	return requestPublishGrant(configPath, cfg, svc, servicePeerID, grantRequestOptions{requireApprovedLease: true, responseMode: grantClientResponseInternal})
}

func renewAttachPublishAuthorization(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	cluster := cfg.Clusters[cfg.CurrentCluster]
	grantPeer := svc.GrantServicePeer
	if grantPeer == "" {
		grantPeer = clusterGrantServicePeer(cluster)
	}
	if grantPeer == "" {
		grantPeer = discoverGrantServicePeer(configPath, cfg)
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
		shareToken, err := buildAttachServiceShareToken(cfg, cluster, cfg.CurrentCluster, cfg.CurrentNamespace, cfg.Service.Name, svc)
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

func serviceEndpointAddrsForTokens(cfg cfgpkg.Config, servicePeerID string) []string {
	decodedPeerID, err := peer.Decode(strings.TrimSpace(servicePeerID))
	if err != nil {
		return nil
	}
	if len(cfg.Network.RelayPeers) > 0 {
		return grantServicePeersForTokens(p2p.MergeRelayCircuitAddrs(nil, p2p.ParseAddrInfos(cfg.Network.RelayPeers), decodedPeerID))
	}
	return nil
}

func shareTokenRequiresPublicEndpoint(cfg cfgpkg.Config, clusterName, namespaceName string) (bool, error) {
	scope, err := cfgpkg.ResolveEffectiveScope(cfg, clusterName, namespaceName, false)
	if err != nil {
		return false, err
	}
	return cfgpkg.IsPublicDefaultScope(cfg, scope), nil
}

func requireShareTokenEndpointForPublicDefault(cfg cfgpkg.Config, token string) error {
	required, err := shareTokenRequiresPublicEndpoint(cfg, "", "")
	if err != nil || !required {
		return err
	}
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		return err
	}
	if payload.ServiceEndpoint.PeerID == "" || len(payload.ServiceEndpoint.Addresses) == 0 {
		return errors.New("share invite is missing a remote-dialable service endpoint; wait for relay readiness and retry once a remote endpoint is available")
	}
	return nil
}

func buildAttachServiceShareToken(cfg cfgpkg.Config, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService) (string, error) {
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
	requireEndpoint, err := shareTokenRequiresPublicEndpoint(cfg, clusterName, namespaceName)
	if err != nil {
		return "", err
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return "", err
	}
	serviceEndpointAddrs := serviceEndpointAddrsForTokens(cfg, servicePeerID.String())
	grantPeer := shareGrantServicePeer(cluster, svc)
	grantPeers := make([]string, 0, 1)
	if grantPeer != "" {
		grantPeers = append(grantPeers, grantPeer)
	}
	useEndpointMetadata := requireEndpoint || len(grantPeers) > 0 || len(serviceEndpointAddrs) > 0
	if svc.ServicePublishLeaseFile != "" {
		if leaseBytes, err := os.ReadFile(svc.ServicePublishLeaseFile); err == nil {
			var lease grantspkg.PublishLease
			if err := json.Unmarshal(leaseBytes, &lease); err == nil {
				var artifacts grantspkg.ServiceShareArtifacts
				if useEndpointMetadata {
					artifacts, err = grantspkg.BuildShareInviteArtifactsFromLeaseWithEndpoints(privKey, clusterName, lease, serviceName, grantspkg.ServiceShareDefaultTTL, grantPeers, servicePeerID.String(), serviceEndpointAddrs)
				} else {
					artifacts, err = grantspkg.BuildShareInviteArtifactsFromLease(privKey, clusterName, lease, serviceName, grantspkg.ServiceShareDefaultTTL)
				}
				if err == nil {
					finalToken, err := finalizeAuthorityServiceShareToken(artifacts.Token, privKey, svc.ServiceID)
					if err != nil {
						return "", err
					}
					if requireEndpoint {
						if err := requireShareTokenEndpointForPublicDefault(cfg, finalToken); err != nil {
							return "", err
						}
					}
					return finalToken, nil
				}
			}
		}
	}
	var token string
	if useEndpointMetadata {
		artifacts, err := grantspkg.BuildServiceShareArtifactsWithEndpoints(privKey, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, grantspkg.ServiceShareDefaultTTL, grantPeers, servicePeerID.String(), serviceEndpointAddrs)
		if err != nil {
			return "", err
		}
		token = artifacts.Token
	} else {
		token, err = grantspkg.BuildServiceShareToken(privKey, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, grantspkg.ServiceShareDefaultTTL)
		if err != nil {
			return "", err
		}
	}
	finalToken, err := finalizeAuthorityServiceShareToken(token, privKey, svc.ServiceID)
	if err != nil {
		return "", err
	}
	if requireEndpoint {
		if err := requireShareTokenEndpointForPublicDefault(cfg, finalToken); err != nil {
			return "", err
		}
	}
	return finalToken, nil
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
	if scope, err := cfgpkg.ResolveEffectiveScope(cfg, "", "", false); err == nil {
		policy := cfgpkg.EffectiveScopePolicy(cfg, scope)
		if policy.Discovery == cfgpkg.NamespaceDiscoveryDisabled {
			fmt.Printf("visibility: unlisted\n")
			fmt.Printf("access: invite token required\n")
		}
	}
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

func seedDiscoveredGrantServicePeer(configPath string, cfg cfgpkg.Config) cfgpkg.Config {
	clusterName := strings.TrimSpace(cfg.CurrentCluster)
	namespaceName := strings.TrimSpace(cfg.CurrentNamespace)
	serviceName := strings.TrimSpace(cfg.Service.Name)
	if clusterName == "" || namespaceName == "" || serviceName == "" {
		return cfg
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return cfg
	}
	if cluster.AuthorityPrivateKeyFile != "" || clusterGrantServicePeer(cluster) != "" {
		return cfg
	}
	namespace, ok := cluster.Namespaces[namespaceName]
	if !ok {
		return cfg
	}
	svc, ok := namespace.Services[serviceName]
	if !ok || strings.TrimSpace(svc.GrantServicePeer) != "" {
		return cfg
	}
	peer := discoverGrantServicePeer(configPath, cfg)
	if peer == "" {
		logging.Progressf("grant service discovery: no grant-service record found for cluster %q namespace %q\n", clusterName, namespaceName)
		return cfg
	}
	logging.Progressf("grant service discovery: found peer=%s source=discovery cluster=%q namespace=%q\n", peer, clusterName, namespaceName)
	svc.GrantServicePeer = peer
	namespace.Services[serviceName] = svc
	cluster.Namespaces[namespaceName] = namespace
	if cluster.MembershipGrant != nil && cluster.MembershipGrant.GrantServiceProtocol == "" {
		cluster.MembershipGrant.GrantServiceProtocol = grantspkg.ProtocolID
	}
	if cluster.MembershipGrant != nil && len(cluster.MembershipGrant.GrantServicePeers) == 0 {
		cluster.MembershipGrant.GrantServicePeers = []string{peer}
	}
	cfg.Clusters[clusterName] = cluster
	_ = saveLocalConfig(configPath, cfg)
	return cfg
}

func discoverGrantServicePeer(configPath string, cfg cfgpkg.Config) string {
	peers, err := discoverGrantServicePeers(configPath, cfg)
	if err != nil || len(peers) == 0 {
		return ""
	}
	return peers[0]
}

func noServicePublishGrantError(clusterName, namespaceName, serviceName string) error {
	return fmt.Errorf("missing grant service peer for cluster %q namespace %q service %q; request a grant from a cluster authority or run attach from an authority node", clusterName, namespaceName, serviceName)
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
