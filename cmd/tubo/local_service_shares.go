package main

import (
	"bytes"
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
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" || cluster.AuthorityPrivateKeyFile == "" {
		return fmt.Errorf("cluster %q is missing authority metadata", scope.Cluster)
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
		return fmt.Errorf("cluster %q authority public key mismatch", scope.Cluster)
	}
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
	artifacts, err := grantspkg.BuildServiceShareArtifacts(privKey, scope.Cluster, cluster.ClusterID, scope.Namespace, name, serviceID, *expires)
	if err == nil && svc.ServicePublishLeaseFile != "" {
		if leaseBytes, readErr := os.ReadFile(svc.ServicePublishLeaseFile); readErr == nil {
			var lease grantspkg.PublishLease
			if json.Unmarshal(leaseBytes, &lease) == nil {
				if invite, inviteErr := grantspkg.BuildShareInviteArtifactsFromLease(privKey, scope.Cluster, lease, name, *expires); inviteErr == nil {
					artifacts = invite
				}
			}
		}
	}
	if err != nil {
		return err
	}
	finalToken, err := finalizeAuthorityServiceShareToken(artifacts.Token, privKey, serviceID)
	if err != nil {
		return err
	}
	artifacts.Token = finalToken
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
