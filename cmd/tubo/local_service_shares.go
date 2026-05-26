package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
)

const (
	serviceShareTokenPrefix     = grantspkg.ServiceShareTokenPrefix
	shareInviteRegistryFileName = "share-invite-registry.json"
)

type serviceSharePayload = grantspkg.ServiceSharePayload

const serviceShareDefaultTTL = grantspkg.ServiceShareDefaultTTL

type serviceShareResult struct {
	ClusterName string `json:"cluster_name"`
	Namespace   string `json:"namespace"`
	ServiceName string `json:"service_name"`
	ServiceID   string `json:"service_id"`
	Permission  string `json:"permission"`
	ExpiresAt   string `json:"expires_at"`
	Token       string `json:"token"`
	ConnectCmd  string `json:"connect_command"`
}

func localShareServiceCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo share service/<name> [flags]")
	}
	resource := args[0]
	fs := flag.NewFlagSet("share service", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	clusterFlag := fs.String("cluster", "", "")
	namespaceFlag := fs.String("namespace", "", "")
	expires := fs.Duration("expires", serviceShareDefaultTTL, "")
	jsonOut := fs.Bool("json", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	kind, name, err := parseLocalResourceRef(resource)
	if err != nil {
		return err
	}
	if kind != "service" {
		return fmt.Errorf("unsupported share resource %q", resource)
	}
	cfg, err := loadLocalConfigOrError(*configPath)
	if err != nil {
		return err
	}
	scope, err := resolveServiceScope(cfg, *clusterFlag, *namespaceFlag, false)
	if err != nil {
		return err
	}
	ctx, err := localWorkspace().ResolveServiceContext(*configPath, name, scope.Cluster, scope.Namespace)
	if err != nil {
		return err
	}
	cfg = ctx.Config
	scope.Cluster = ctx.ClusterName
	scope.Namespace = ctx.Namespace
	cluster := ctx.Cluster
	svc := ctx.Service
	name = ctx.Name
	serviceID := svc.ServiceID
	if serviceID == "" && svc.ServiceOwnerKeyFile != "" {
		identity, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
		if err != nil {
			return err
		}
		serviceID = identity.ServiceID
	}
	if serviceID == "" {
		serviceID, _ = serviceIdentityFor(cluster.ClusterID, scope.Namespace, name)
	}
	artifacts, err := mintServiceShareArtifacts(*configPath, cfg, cluster, scope.Cluster, scope.Namespace, name, svc, *expires)
	if err != nil {
		return err
	}
	result := serviceShareResult{
		ClusterName: scope.Cluster,
		Namespace:   scope.Namespace,
		ServiceName: name,
		ServiceID:   serviceID,
		Permission:  "connect",
		ExpiresAt:   artifacts.Payload.ExpiresAt.Format(time.RFC3339),
		Token:       artifacts.Token,
		ConnectCmd:  fmt.Sprintf("tubo connect --token %s", artifacts.Token),
	}
	if *jsonOut {
		return printJSON(result)
	}
	fmt.Printf("shared service %q in cluster %q namespace %q\n", name, scope.Cluster, scope.Namespace)
	fmt.Printf("service id: %s\n", serviceID)
	fmt.Printf("permission: connect\n")
	fmt.Printf("expires: %s\n", artifacts.Payload.ExpiresAt.Format(time.RFC3339))
	fmt.Printf("connect: %s\n", result.ConnectCmd)
	return nil
}

func mintServiceShareArtifacts(configPath string, cfg cfgpkg.Config, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService, shareTTL time.Duration) (grantspkg.ServiceShareArtifacts, error) {
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" {
		return grantspkg.ServiceShareArtifacts{}, fmt.Errorf("cluster %q is missing authority metadata", clusterName)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	requireEndpoint, err := shareTokenRequiresPublicEndpoint(cfg, clusterName, namespaceName)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	serviceEndpointAddrs := serviceEndpointAddrsForTokens(cfg, servicePeerID.String())
	grantPeers := grantServicePeersForTokens(serviceEndpointAddrs)
	useEndpointMetadata := requireEndpoint || len(grantPeers) > 0 || len(serviceEndpointAddrs) > 0
	if cluster.AuthorityPrivateKeyFile != "" {
		return mintAuthorityLocalServiceShareArtifacts(cfg, cluster, clusterName, namespaceName, serviceName, svc, shareTTL, servicePeerID.String(), serviceEndpointAddrs, grantPeers, useEndpointMetadata, requireEndpoint)
	}
	return mintDelegatedServiceShareArtifacts(configPath, cfg, cluster, clusterName, namespaceName, serviceName, svc, shareTTL, servicePeerID.String(), serviceEndpointAddrs, requireEndpoint)
}

func mintAuthorityLocalServiceShareArtifacts(cfg cfgpkg.Config, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService, shareTTL time.Duration, servicePeerID string, serviceEndpointAddrs, grantPeers []string, useEndpointMetadata, requireEndpoint bool) (grantspkg.ServiceShareArtifacts, error) {
	privKey, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, fmt.Errorf("load cluster authority key: %w", err)
	}
	pubAuthorized, err := clusterAuthorityPublicKeyString(privKey)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	if cluster.AuthorityPublicKey != pubAuthorized {
		return grantspkg.ServiceShareArtifacts{}, fmt.Errorf("cluster %q authority public key mismatch", clusterName)
	}
	var artifacts grantspkg.ServiceShareArtifacts
	if useEndpointMetadata {
		artifacts, err = grantspkg.BuildServiceShareArtifactsWithEndpoints(privKey, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, shareTTL, grantPeers, servicePeerID, serviceEndpointAddrs)
	} else {
		artifacts, err = grantspkg.BuildServiceShareArtifacts(privKey, clusterName, cluster.ClusterID, namespaceName, serviceName, svc.ServiceID, shareTTL)
	}
	if err == nil && svc.ServicePublishLeaseFile != "" {
		if leaseBytes, readErr := os.ReadFile(svc.ServicePublishLeaseFile); readErr == nil {
			var lease grantspkg.PublishLease
			if json.Unmarshal(leaseBytes, &lease) == nil {
				if useEndpointMetadata {
					if invite, inviteErr := grantspkg.BuildShareInviteArtifactsFromLeaseWithEndpoints(privKey, clusterName, lease, serviceName, shareTTL, grantPeers, servicePeerID, serviceEndpointAddrs); inviteErr == nil {
						artifacts = invite
					}
				} else if invite, inviteErr := grantspkg.BuildShareInviteArtifactsFromLease(privKey, clusterName, lease, serviceName, shareTTL); inviteErr == nil {
					artifacts = invite
				}
			}
		}
	}
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	finalToken, err := finalizeAuthorityServiceShareToken(artifacts.Token, privKey, svc.ServiceID)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	if requireEndpoint {
		if err := requireShareTokenEndpointForPublicDefault(cfg, finalToken); err != nil {
			return grantspkg.ServiceShareArtifacts{}, err
		}
	}
	artifacts.Token = finalToken
	artifacts.Payload, err = parseAndVerifyServiceShareToken(finalToken)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	return artifacts, nil
}

func mintDelegatedServiceShareArtifacts(configPath string, cfg cfgpkg.Config, cluster cfgpkg.Cluster, clusterName, namespaceName, serviceName string, svc cfgpkg.NamespaceService, shareTTL time.Duration, servicePeerID string, serviceEndpointAddrs []string, requireEndpoint bool) (grantspkg.ServiceShareArtifacts, error) {
	grantPeer := strings.TrimSpace(svc.GrantServicePeer)
	if grantPeer == "" {
		grantPeer = clusterGrantServicePeer(cluster)
	}
	if grantPeer == "" {
		return grantspkg.ServiceShareArtifacts{}, errors.New("missing grant service peer; attach or request a publish grant from an authority node first")
	}
	authorityPub, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	lease, err := readPublishLeaseFile(svc.ServicePublishLeaseFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return grantspkg.ServiceShareArtifacts{}, errors.New("service publish lease is required; attach or request a publish grant first")
		}
		return grantspkg.ServiceShareArtifacts{}, err
	}
	if err := grantspkg.VerifyPublishLease(lease, authorityPub, cluster.ClusterID, namespaceName, svc.ServiceID, servicePeerID); err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	if !strings.EqualFold(strings.TrimSpace(lease.ServiceID), strings.TrimSpace(svc.ServiceID)) {
		return grantspkg.ServiceShareArtifacts{}, fmt.Errorf("publish lease service id mismatch: got %q want %q", lease.ServiceID, svc.ServiceID)
	}
	if !grantspkg.IsRemoteDialableGrantServicePeer(grantPeer) && !strings.Contains(grantPeer, "/p2p/") {
		return grantspkg.ServiceShareArtifacts{}, fmt.Errorf("grant service peer %q is invalid", grantPeer)
	}
	if svc.ServiceOwnerKeyFile == "" {
		return grantspkg.ServiceShareArtifacts{}, errors.New("service owner key file is required for delegated share minting")
	}
	owner, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, fmt.Errorf("load service owner key: %w", err)
	}
	mintReq, err := grantspkg.SignShareMintRequest(grantspkg.ShareMintRequest{ClusterID: cluster.ClusterID, NamespaceID: namespaceName, ServiceID: svc.ServiceID, PublishLease: lease, ServicePeerID: servicePeerID, ServiceAddresses: serviceEndpointAddrs, RequestedTTLSeconds: int64(shareTTL.Seconds()), RequestNonce: randomNonce(), RequestIssuedAt: time.Now().UTC()}, owner.PrivateKey)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	overlay, err := p2p.NewOverlayHost(p2p.OverlayHostConfig{Listen: "/ip4/127.0.0.1/tcp/0", Seed: grantsFirstNonEmpty(cfg.Node.Seed, "share-mint-client-"+svc.ServiceSeed), PrivateKeyFile: cfg.Network.PrivateKeyFile, PrivateKeyB64: cfg.Network.PrivateKeyB64, BootstrapPeers: cfg.Network.BootstrapPeers, RelayPeers: cfg.Network.RelayPeers, Autorelay: cfg.Network.Autorelay, HolePunching: cfg.Network.HolePunching, ForceReachability: cfg.Network.ForceReachability, Component: "share-mint-client"})
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	defer overlay.Close()
	info, err := p2p.AddrInfoFromString(grantPeer)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, fmt.Errorf("failed to parse multiaddr %q: %w", grantPeer, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	overlay.StartBootstrapRetry(ctx, 5*time.Second)
	overlay.StartRelayReservations(ctx)
	token, err := grantspkg.MintShareInvite(ctx, overlay.Host, info, mintReq, serviceName)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	if requireEndpoint {
		if err := requireShareTokenEndpointForPublicDefault(cfg, token); err != nil {
			return grantspkg.ServiceShareArtifacts{}, err
		}
	}
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		return grantspkg.ServiceShareArtifacts{}, err
	}
	return grantspkg.ServiceShareArtifacts{Payload: payload, Token: token}, nil
}

func localRevokeServiceShareCmd(args []string) error {
	fs := flag.NewFlagSet("share revoke", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	tokenFlag := fs.String("token", "", "")
	token := ""
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		token = strings.TrimSpace(args[0])
		parseArgs = args[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return err
	}
	if token == "" {
		token = strings.TrimSpace(*tokenFlag)
	}
	if token == "" {
		if fs.NArg() != 1 {
			return errors.New("usage: tubo share revoke <share-invite> [--config <config.yaml>]")
		}
		token = strings.TrimSpace(fs.Arg(0))
	} else if fs.NArg() != 0 {
		return errors.New("usage: tubo share revoke <share-invite> [--config <config.yaml>]")
	}
	if token == "" {
		return errors.New("share invite token is required")
	}
	configDir := filepath.Dir(*configPath)
	if err := revokeServiceShareToken(configDir, token); err != nil {
		return err
	}
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		return err
	}
	fmt.Printf("revoked share invite %s\n", payload.JTI)
	return nil
}

func connectServiceShareSetup(serviceName, token, clusterFlag, namespaceFlag string) (string, string, serviceScope, error) {
	if strings.TrimSpace(token) == "" {
		return strings.TrimSpace(serviceName), "", serviceScope{Cluster: strings.TrimSpace(clusterFlag), Namespace: strings.TrimSpace(namespaceFlag)}, nil
	}
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		return "", "", serviceScope{}, err
	}
	serviceName = strings.TrimSpace(serviceName)
	if serviceName != "" && serviceName != payload.DisplayNameHint {
		return "", "", serviceScope{}, fmt.Errorf("service share is for %q, not %q", payload.DisplayNameHint, serviceName)
	}
	clusterFlag = strings.TrimSpace(clusterFlag)
	if clusterFlag != "" && clusterFlag != payload.ClusterName {
		return "", "", serviceScope{}, fmt.Errorf("service share is for cluster %q, not %q", payload.ClusterName, clusterFlag)
	}
	namespaceFlag = strings.TrimSpace(namespaceFlag)
	if namespaceFlag != "" && namespaceFlag != payload.Namespace {
		return "", "", serviceScope{}, fmt.Errorf("service share is for namespace %q, not %q", payload.Namespace, namespaceFlag)
	}
	return payload.DisplayNameHint, payload.TargetServiceID, serviceScope{Cluster: payload.ClusterName, Namespace: payload.Namespace}, nil
}

func parseAndVerifyServiceShareToken(token string) (serviceSharePayload, error) {
	return grantspkg.ParseAndVerifyServiceShareToken(token)
}

func signServiceShareToken(payload serviceSharePayload, priv ed25519.PrivateKey) (string, error) {
	return grantspkg.SignServiceShareToken(payload, priv)
}

func isServiceShareToken(token string) bool {
	return grantspkg.IsServiceShareToken(token)
}

func shareInviteRegistryPath(configDir string) string {
	return filepath.Join(configDir, shareInviteRegistryFileName)
}

func resolveLocalServiceForShare(services map[string]cfgpkg.NamespaceService, ref string) (cfgpkg.NamespaceService, string, bool) {
	if svc, ok := services[ref]; ok {
		return svc, ref, true
	}
	if isServiceID(ref) {
		for name, svc := range services {
			if svc.ServiceID == ref {
				return svc, name, true
			}
		}
	}
	return cfgpkg.NamespaceService{}, "", false
}

func finalizeAuthorityServiceShareToken(token string, privKey ed25519.PrivateKey, serviceID string) (string, error) {
	store := grantspkg.NewRevocationStore(grantspkg.DefaultRevocationStorePath())
	if revoked, _, err := store.IsPublishRevoked(serviceID); err != nil {
		return "", err
	} else if revoked {
		return "", fmt.Errorf("publish revoked for service %q", serviceID)
	}
	epochs, err := store.EpochsForService(serviceID)
	if err != nil {
		return "", err
	}
	if epochs.AccessEpoch == 0 && epochs.PublishEpoch == 0 {
		return token, nil
	}
	return grantspkg.ReissueServiceShareTokenWithEpochs(token, privKey, epochs)
}

func loadShareInviteRegistry(configDir string) (map[string]bool, error) {
	path := shareInviteRegistryPath(configDir)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]bool), nil
	}
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal(b, &ids); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

func saveShareInviteRegistry(configDir string, registry map[string]bool) error {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	return os.WriteFile(shareInviteRegistryPath(configDir), append(b, '\n'), 0600)
}

func revokeServiceShareToken(configDir, token string) error {
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		return err
	}
	registry, err := loadShareInviteRegistry(configDir)
	if err != nil {
		return err
	}
	registry[payload.JTI] = true
	return saveShareInviteRegistry(configDir, registry)
}

func ensureShareInviteAvailable(configDir string, payload serviceSharePayload) error {
	registry, err := loadShareInviteRegistry(configDir)
	if err != nil {
		return err
	}
	if registry[payload.JTI] {
		return fmt.Errorf("share invite %q was revoked or already used locally", payload.JTI)
	}
	return nil
}

func markShareInviteUsed(configDir string, payload serviceSharePayload) error {
	registry, err := loadShareInviteRegistry(configDir)
	if err != nil {
		return err
	}
	registry[payload.JTI] = true
	return saveShareInviteRegistry(configDir, registry)
}

func importServiceShareDiscoveryContext(cfg cfgpkg.Config, payload serviceSharePayload) (cfgpkg.Config, error) {
	if cfg.Clusters == nil {
		cfg.Clusters = make(map[string]cfgpkg.Cluster)
	}
	if issuer, ok := cfg.ScopeIssuer(payload.ClusterName, payload.Namespace); ok {
		match, err := authorityKeysEqual(issuer.AuthorityPublicKey, payload.AuthorityPublicKey)
		if err != nil {
			return cfgpkg.Config{}, err
		}
		if !match {
			return cfgpkg.Config{}, fmt.Errorf("share invite issuer mismatch for scope %s/%s: got %q want %q", payload.ClusterName, payload.Namespace, payload.AuthorityPublicKey, issuer.AuthorityPublicKey)
		}
	}
	cluster := cfg.Clusters[payload.ClusterName]
	cluster.ClusterID = payload.ClusterID
	if cluster.AuthorityPublicKey == "" {
		cluster.AuthorityPublicKey = payload.AuthorityPublicKey
	}
	if cluster.Namespaces == nil {
		cluster.Namespaces = make(map[string]cfgpkg.Namespace)
	}
	cluster.Namespaces[payload.Namespace] = cfgpkg.Namespace{}
	cluster.MembershipGrant = &cfgpkg.ClusterMembershipGrant{
		ClusterName:        payload.ClusterName,
		ClusterID:          payload.ClusterID,
		AuthorityPublicKey: cluster.AuthorityPublicKey,
		Namespace:          payload.Namespace,
		Role:               "member",
		Permissions: []string{
			"subscribe",
			"list",
			"publish",
		},
		IssuedAt:  payload.IssuedAt,
		ExpiresAt: payload.ExpiresAt,
	}
	cfg.Clusters[payload.ClusterName] = cluster
	cfg.CurrentCluster = payload.ClusterName
	cfg.CurrentNamespace = payload.Namespace
	return cfg, nil
}

func authorityKeysEqual(a, b string) (bool, error) {
	aPub, err := discovery.ParseAuthorityPublicKey(strings.TrimSpace(a))
	if err != nil {
		return false, fmt.Errorf("parse authority public key %q: %w", a, err)
	}
	bPub, err := discovery.ParseAuthorityPublicKey(strings.TrimSpace(b))
	if err != nil {
		return false, fmt.Errorf("parse authority public key %q: %w", b, err)
	}
	return bytes.Equal(aPub, bPub), nil
}
