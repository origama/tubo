package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	clusterinvite "github.com/origama/tubo/internal/clusterinvite"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	logging "github.com/origama/tubo/internal/logging"
	workspace "github.com/origama/tubo/internal/workspace"
	"golang.org/x/crypto/ssh"
)

const (
	clusterInviteTokenPrefix        = clusterinvite.TokenPrefix
	clusterInviteKind               = clusterinvite.Kind
	clusterInviteVersion            = clusterinvite.Version
	clusterInviteDefaultTTL         = 7 * 24 * time.Hour
	clusterInviteDefaultRole        = clusterinvite.RoleMember
	clusterInviteViewerRole         = clusterinvite.RoleViewer
	clusterInviteGrantRequesterRole = clusterinvite.RoleGrantRequester
	clusterInviteGrantRequestPerm   = clusterinvite.GrantRequestPermission
)

type clusterInviteGrant = clusterinvite.Grant

type clusterInviteGrantService = clusterinvite.GrantService

type clusterInvitePayload = clusterinvite.Payload

type clusterShareResult struct {
	ClusterName string `json:"cluster_name"`
	Namespace   string `json:"namespace"`
	Permission  string `json:"permission"`
	ExpiresAt   string `json:"expires_at"`
	Token       string `json:"token"`
	JoinCommand string `json:"join_command"`
}

type clusterJoinResult struct {
	ConfigPath  string                        `json:"config_path"`
	ClusterName string                        `json:"cluster_name"`
	Namespace   string                        `json:"namespace"`
	Grant       cfgpkg.ClusterMembershipGrant `json:"grant"`
}

func clusterMembershipGrantMetadata(payload clusterInvitePayload) cfgpkg.ClusterMembershipGrant {
	return cfgpkg.ClusterMembershipGrant{
		InviteVersion:        payload.Version,
		InviteID:             payload.JTI,
		ClusterName:          payload.ClusterName,
		ClusterID:            payload.ClusterID,
		AuthorityPublicKey:   payload.AuthorityPublicKey,
		Namespace:            payload.Namespace,
		Role:                 payload.Grant.Role,
		Permissions:          append([]string(nil), payload.Grant.Permissions...),
		GrantServiceProtocol: payload.GrantService.Protocol,
		GrantServicePeers:    append([]string(nil), payload.GrantService.Peers...),
		IssuedAt:             payload.IssuedAt,
		ExpiresAt:            payload.ExpiresAt,
	}
}

func localShareCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo share cluster/<name>|service/<name>|revoke [flags]")
	}
	resource := args[0]
	if resource == "revoke" {
		return localRevokeServiceShareCmd(args[1:])
	}
	fs := flag.NewFlagSet("share", flag.ContinueOnError)
	configPath := fs.String("config", defaultTuboConfigPath(), "")
	clusterFlag := fs.String("cluster", "", "")
	namespace := fs.String("namespace", "", "")
	permission := fs.String("permission", clusterInviteDefaultRole, "")
	role := fs.String("role", "", "")
	grantPeer := fs.String("grant-peer", "", "")
	expires := fs.Duration("expires", clusterInviteDefaultTTL, "")
	jsonOut := fs.Bool("json", false, "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	_ = clusterFlag
	kind, name, err := parseLocalResourceRef(resource)
	if err != nil {
		return err
	}
	switch kind {
	case "cluster":
	case "service":
		return localShareServiceCmd(args)
	default:
		return fmt.Errorf("unsupported share resource %q", resource)
	}
	cfg, err := loadLocalConfigOrError(*configPath)
	if err != nil {
		return err
	}
	cluster, ok := cfg.Clusters[name]
	if !ok {
		return fmt.Errorf("cluster %q not found", name)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" {
		return fmt.Errorf("cluster %q is missing identity metadata", name)
	}
	privKey, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return fmt.Errorf("load cluster authority key: %w", err)
	}
	pubAuthorized, err := clusterAuthorityPublicKeyString(privKey)
	if err != nil {
		return err
	}
	if cluster.AuthorityPublicKey != "" && cluster.AuthorityPublicKey != pubAuthorized {
		return fmt.Errorf("cluster %q authority public key mismatch", name)
	}
	selectedNamespace, err := chooseClusterInviteNamespace(cfg, name, cluster, *namespace)
	if err != nil {
		return err
	}
	requestedRole := *permission
	if *role != "" {
		requestedRole = *role
	}
	grant, err := invitationGrantForPermission(requestedRole)
	if err != nil {
		return err
	}
	grantPeers, err := parseGrantServicePeers(*grantPeer)
	if err != nil {
		return err
	}
	if grant.Role == clusterInviteGrantRequesterRole && len(grantPeers) == 0 {
		return errors.New("grant-requester cluster invite requires --grant-peer <multiaddr>")
	}
	jti, err := newClusterInviteJTI()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	discoveryEntry, err := clusterInviteDiscoveryEntry(cluster, selectedNamespace)
	if err != nil {
		return err
	}
	payload := clusterInvitePayload{
		Version:            clusterInviteVersion,
		Kind:               clusterInviteKind,
		JTI:                jti,
		ClusterName:        name,
		ClusterID:          cluster.ClusterID,
		AuthorityPublicKey: cluster.AuthorityPublicKey,
		Namespace:          selectedNamespace,
		Discovery:          discoveryEntry,
		Grant:              grant,
		IssuedAt:           now,
		ExpiresAt:          now.Add(*expires),
	}
	if len(grantPeers) > 0 {
		payload.GrantService = clusterInviteGrantService{Protocol: grantspkg.ProtocolID, Peers: grantPeers}
	}
	membershipPayload, err := clusterinvite.MembershipGrantPayloadFromInvite(payload)
	if err != nil {
		return err
	}
	membershipToken, err := signClusterInviteToken(membershipPayload, privKey)
	if err != nil {
		return err
	}
	payload.MembershipToken = membershipToken
	token, err := signClusterInviteToken(payload, privKey)
	if err != nil {
		return err
	}
	result := clusterShareResult{
		ClusterName: name,
		Namespace:   selectedNamespace,
		Permission:  grant.Role,
		ExpiresAt:   payload.ExpiresAt.Format(time.RFC3339),
		Token:       token,
		JoinCommand: fmt.Sprintf("tubo join cluster/%s --token %s", name, token),
	}
	if *jsonOut {
		return printJSON(result)
	}
	logging.Resultf("shared cluster %q\n", name)
	logging.Resultf("namespace: %s\n", selectedNamespace)
	logging.Resultf("permission: %s\n", grant.Role)
	logging.Resultf("expires: %s\n", payload.ExpiresAt.Format(time.RFC3339))
	logging.Resultf("join: %s\n", result.JoinCommand)
	return nil
}

func parseClusterInviteJoin(args []string, tokenFlag string) (clusterName string, token string, ok bool, err error) {
	if tokenFlag != "" {
		if len(args) > 1 {
			return "", "", false, errors.New("usage: tubo join [cluster/<name>] --token <cluster-invite> [flags]")
		}
		if len(args) == 1 {
			if !strings.HasPrefix(args[0], "cluster/") {
				return "", "", false, fmt.Errorf("unsupported join resource %q", args[0])
			}
			clusterName = strings.TrimPrefix(args[0], "cluster/")
		}
		return clusterName, tokenFlag, true, nil
	}
	if len(args) != 1 {
		return "", "", false, nil
	}
	if isClusterInviteToken(args[0]) {
		return "", args[0], true, nil
	}
	if strings.HasPrefix(args[0], "cluster/") {
		return "", "", false, fmt.Errorf("cluster invite join for %s requires --token", args[0])
	}
	return "", "", false, nil
}

func localJoinClusterInviteCmd(args []string) error {
	fs := flag.NewFlagSet("join cluster", flag.ContinueOnError)
	configDir := fs.String("config-dir", defaultTuboConfigDir(), "")
	force := fs.Bool("force", false, "")
	jsonOut := fs.Bool("json", false, "")
	tokenFlag := fs.String("token", "", "")
	clusterArg := ""
	parseArgs := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		clusterArg = args[0]
		parseArgs = args[1:]
	}
	if err := fs.Parse(parseArgs); err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return errors.New("usage: tubo join [cluster/<name>] --token <cluster-invite> [flags]; or tubo join <cluster-invite>")
	}
	clusterName := ""
	token := strings.TrimSpace(*tokenFlag)
	if clusterArg != "" {
		switch {
		case isClusterInviteToken(clusterArg):
			if token != "" && token != clusterArg {
				return errors.New("cluster invite token specified twice")
			}
			token = clusterArg
		case strings.HasPrefix(clusterArg, "cluster/"):
			clusterName = strings.TrimPrefix(clusterArg, "cluster/")
		default:
			return fmt.Errorf("unsupported join resource %q", clusterArg)
		}
	}
	if clusterArg == "" && len(fs.Args()) == 1 {
		switch {
		case isClusterInviteToken(fs.Args()[0]):
			if token != "" && token != fs.Args()[0] {
				return errors.New("cluster invite token specified twice")
			}
			token = fs.Args()[0]
		case strings.HasPrefix(fs.Args()[0], "cluster/"):
			clusterName = strings.TrimPrefix(fs.Args()[0], "cluster/")
		default:
			return fmt.Errorf("unsupported join resource %q", fs.Args()[0])
		}
	}
	if token == "" {
		if clusterName != "" {
			return fmt.Errorf("cluster invite join for %s requires --token", clusterName)
		}
		return errors.New("usage: tubo join [cluster/<name>] --token <cluster-invite> [flags]; or tubo join <cluster-invite>")
	}
	payload, err := parseAndVerifyClusterInviteToken(token)
	if err != nil {
		return err
	}
	if clusterName != "" && clusterName != payload.ClusterName {
		return fmt.Errorf("cluster invite is for %q, not %q", payload.ClusterName, clusterName)
	}
	if err := installClusterInviteConfig(*configDir, payload, token, *force); err != nil {
		return err
	}
	result := clusterJoinResult{
		ConfigPath:  filepath.Join(*configDir, "config.yaml"),
		ClusterName: payload.ClusterName,
		Namespace:   payload.Namespace,
		Grant:       clusterMembershipGrantMetadata(payload),
	}
	if *jsonOut {
		return printJSON(result)
	}
	logging.Resultf("joined cluster %q\n", payload.ClusterName)
	logging.Resultf("namespace: %s\n", payload.Namespace)
	logging.Resultf("grant: %s\n", payload.Grant.Role)
	logging.Resultf("config: %s\n", result.ConfigPath)
	return nil
}

func installClusterInviteConfig(configDir string, payload clusterInvitePayload, token string, force bool) error {
	if payload.ClusterName == "" {
		return errors.New("cluster invite is missing cluster name")
	}
	if payload.ClusterID == "" {
		return errors.New("cluster invite is missing cluster id")
	}
	if payload.AuthorityPublicKey == "" {
		return errors.New("cluster invite is missing authority public key")
	}
	if payload.Namespace == "" {
		return errors.New("cluster invite is missing namespace")
	}
	configPath := filepath.Join(configDir, "config.yaml")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		return err
	}
	registry, err := loadClusterInviteRegistry(configDir)
	if err != nil {
		return err
	}
	if registry[payload.JTI] {
		return fmt.Errorf("cluster invite %q was already used locally", payload.JTI)
	}
	existing, err := cfgpkg.LoadFile(configPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Bug fix: the --force guard used to block join whenever config.yaml already
	// existed, forcing users to always pass --force to add a second cluster.
	// Cluster-invite join is always additive (merge), so the guard is wrong here.
	// We only reject if there is a real conflict (different cluster_id or authority key).
	if existing.Clusters != nil {
		if current, ok := existing.Clusters[payload.ClusterName]; ok {
			if current.ClusterID != "" && current.ClusterID != payload.ClusterID {
				return fmt.Errorf("cluster %q already exists with a different cluster id", payload.ClusterName)
			}
			if current.AuthorityPublicKey != "" && current.AuthorityPublicKey != payload.AuthorityPublicKey {
				return fmt.Errorf("cluster %q already exists with a different authority key", payload.ClusterName)
			}
		}
	}
	joined := cfgpkg.Merge(existing, cfgpkg.Config{})
	if joined.Clusters == nil {
		joined.Clusters = make(map[string]cfgpkg.Cluster)
	}
	cluster := joined.Clusters[payload.ClusterName]
	if cluster.Namespaces == nil {
		cluster.Namespaces = make(map[string]cfgpkg.Namespace)
	}
	cluster.ClusterID = payload.ClusterID
	cluster.AuthorityPublicKey = payload.AuthorityPublicKey
	// Bug fix: preserve the existing namespace entry (discovery policy, connect
	// policy, services, capability files) instead of overwriting with an empty
	// struct. We only add the namespace if it does not exist yet.
	if _, nsExists := cluster.Namespaces[payload.Namespace]; !nsExists {
		cluster.Namespaces[payload.Namespace] = cfgpkg.Namespace{}
	}
	installedRef, err := installClusterInviteDiscoveryEntry(configDir, payload.ClusterName, payload.Namespace, payload.Discovery)
	if err != nil {
		return err
	}
	membershipTokenFile, err := installClusterInviteMembershipToken(configDir, payload)
	if err != nil {
		return err
	}
	joinedNamespace := cluster.Namespaces[payload.Namespace]
	joinedNamespace.Discovery = cfgpkg.NamespaceDiscoveryEnabled
	if joinedNamespace.ConnectPolicy == "" {
		joinedNamespace.ConnectPolicy = cfgpkg.ConnectPolicyNamespaceMember
	}
	joinedNamespace.DiscoverySecretCurrent = installedRef
	joinedNamespace.DiscoverySecretPrevious = nil
	cluster.Namespaces[payload.Namespace] = joinedNamespace
	_ = token
	grant := clusterMembershipGrantMetadata(payload)
	grant.InviteTokenFile = membershipTokenFile
	cluster.MembershipGrant = &grant
	joined.Clusters[payload.ClusterName] = cluster
	// Bug fix: do not unconditionally overwrite the current cluster/namespace.
	// Only switch context if there is no cluster selected yet, so that existing
	// work on another cluster is not silently disrupted.
	if joined.CurrentCluster == "" {
		joined.CurrentCluster = payload.ClusterName
		joined.CurrentNamespace = payload.Namespace
	}
	if err := cfgpkg.WriteFile(configPath, joined, true); err != nil {
		return err
	}
	registry[payload.JTI] = true
	return saveClusterInviteRegistry(configDir, registry)
}

func loadClusterInviteRegistry(configDir string) (map[string]bool, error) {
	path := filepath.Join(configDir, "invite-registry.json")
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

func saveClusterInviteRegistry(configDir string, registry map[string]bool) error {
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	b, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(configDir, "invite-registry.json"), append(b, '\n'), 0600)
}

func invitationGrantForPermission(permission string) (clusterInviteGrant, error) {
	return clusterinvite.GrantForRole(permission)
}

func parseGrantServicePeers(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	peers := make([]string, 0, len(parts))
	for _, part := range parts {
		peer := strings.TrimSpace(part)
		if peer == "" {
			continue
		}
		if !strings.Contains(peer, "/p2p/") {
			return nil, fmt.Errorf("grant service peer %q must include /p2p/<peer-id>", peer)
		}
		peers = append(peers, peer)
	}
	return peers, nil
}

func newClusterInviteJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "ci_" + hex.EncodeToString(b[:]), nil
}

func chooseClusterInviteNamespace(cfg cfgpkg.Config, clusterName string, cluster cfgpkg.Cluster, explicit string) (string, error) {
	if explicit != "" {
		if cluster.Namespaces == nil {
			return "", fmt.Errorf("cluster %q has no namespaces configured", clusterName)
		}
		if _, ok := cluster.Namespaces[explicit]; !ok {
			return "", fmt.Errorf("namespace %q not found in cluster %q", explicit, clusterName)
		}
		return explicit, nil
	}
	if cfg.CurrentCluster == clusterName && cfg.CurrentNamespace != "" {
		if cluster.Namespaces != nil {
			if _, ok := cluster.Namespaces[cfg.CurrentNamespace]; ok {
				return cfg.CurrentNamespace, nil
			}
		}
	}
	if cluster.Namespaces != nil {
		if _, ok := cluster.Namespaces["default"]; ok {
			return "default", nil
		}
		names := make([]string, 0, len(cluster.Namespaces))
		for name := range cluster.Namespaces {
			names = append(names, name)
		}
		sort.Strings(names)
		if len(names) > 0 {
			return names[0], nil
		}
	}
	return "", fmt.Errorf("cluster %q has no namespaces configured", clusterName)
}

func loadClusterAuthorityPrivateKey(path string) (ed25519.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("cluster authority private key file is not configured")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("cluster authority private key is not PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return k, nil
	case *ed25519.PrivateKey:
		return *k, nil
	default:
		return nil, fmt.Errorf("unsupported cluster authority private key type %T", key)
	}
}

func clusterAuthorityPublicKeyString(priv ed25519.PrivateKey) (string, error) {
	pubKey, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey))), nil
}

func signClusterInviteToken(payload clusterInvitePayload, priv ed25519.PrivateKey) (string, error) {
	return clusterinvite.SignToken(payload, priv)
}

func parseAndVerifyClusterInviteToken(token string) (clusterInvitePayload, error) {
	return clusterinvite.ParseAndVerifyClusterInviteToken(token)
}

func validateClusterInviteGrant(payload clusterInvitePayload) error {
	return clusterinvite.ValidatePayload(payload)
}

func stringSliceEqualSet(have, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	seen := make(map[string]int, len(have))
	for _, v := range have {
		seen[v]++
	}
	for _, v := range want {
		seen[v]--
		if seen[v] < 0 {
			return false
		}
	}
	return true
}

func isClusterInviteToken(token string) bool {
	return clusterinvite.IsToken(token)
}

func clusterInviteDiscoveryEntry(cluster cfgpkg.Cluster, namespace string) (*clusterinvite.NamespaceDiscoveryEntry, error) {
	ns, ok := cluster.Namespaces[namespace]
	if !ok {
		return nil, fmt.Errorf("namespace %q not found in cluster", namespace)
	}
	if ns.Discovery != cfgpkg.NamespaceDiscoveryEnabled {
		return nil, fmt.Errorf("namespace %q discovery is not enabled", namespace)
	}
	if ns.DiscoverySecretCurrent == nil {
		return nil, fmt.Errorf("namespace %q is missing discovery_secret_current", namespace)
	}
	secretBytes, err := cfgpkg.ReadNamespaceDiscoverySecretFile(ns.DiscoverySecretCurrent.File)
	if err != nil {
		return nil, fmt.Errorf("read namespace %q discovery secret: %w", namespace, err)
	}
	return &clusterinvite.NamespaceDiscoveryEntry{
		Version:   "v1",
		Type:      ns.DiscoverySecretCurrent.Type,
		KeyID:     ns.DiscoverySecretCurrent.KeyID,
		Secret:    base64.RawURLEncoding.EncodeToString(secretBytes),
		CreatedAt: ns.DiscoverySecretCurrent.CreatedAt,
		ExpiresAt: ns.DiscoverySecretCurrent.ExpiresAt,
	}, nil
}

func installClusterInviteDiscoveryEntry(configDir, clusterName, namespace string, entry *clusterinvite.NamespaceDiscoveryEntry) (*cfgpkg.ManagedSecretRef, error) {
	if entry == nil {
		return nil, errors.New("cluster invite is missing namespace discovery entry")
	}
	if err := clusterinvite.ValidateNamespaceDiscoveryEntry(*entry); err != nil {
		return nil, err
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(entry.Secret)
	if err != nil {
		return nil, err
	}
	paths := workspace.Paths{ConfigDir: configDir}
	secretPath := paths.NamespaceDiscoveryCurrentSecret(clusterName, namespace)
	secretBytes, ref, err := cfgpkg.BuildNamespaceDiscoverySecretRefFromBytes(secretPath, secretBytes, entry.KeyID, entry.CreatedAt, entry.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if ref.Type == "" {
		ref.Type = cfgpkg.SecretTypeNamespaceDiscovery
	}
	ref.Type = cfgpkg.SecretTypeNamespaceDiscovery
	if err := os.MkdirAll(filepath.Dir(secretPath), 0700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(secretPath, secretBytes, 0600); err != nil {
		return nil, err
	}
	return ref, nil
}

func installClusterInviteMembershipToken(configDir string, payload clusterInvitePayload) (string, error) {
	if strings.TrimSpace(payload.MembershipToken) == "" {
		return "", nil
	}
	membershipPayload, err := clusterinvite.ParseAndVerifyMembershipGrantToken(payload.MembershipToken)
	if err != nil {
		return "", err
	}
	if membershipPayload.ClusterName != payload.ClusterName || membershipPayload.ClusterID != payload.ClusterID || membershipPayload.Namespace != payload.Namespace {
		return "", errors.New("cluster invite membership token does not match outer cluster scope")
	}
	if membershipPayload.AuthorityPublicKey != payload.AuthorityPublicKey || membershipPayload.Grant.Role != payload.Grant.Role || !stringSliceEqualSet(membershipPayload.Grant.Permissions, payload.Grant.Permissions) || membershipPayload.GrantService.Protocol != payload.GrantService.Protocol || !stringSliceEqualSet(membershipPayload.GrantService.Peers, payload.GrantService.Peers) || !membershipPayload.IssuedAt.Equal(payload.IssuedAt) || !membershipPayload.ExpiresAt.Equal(payload.ExpiresAt) {
		return "", errors.New("cluster invite membership token does not match outer invite grant metadata")
	}
	paths := workspace.Paths{ConfigDir: configDir}
	tokenPath := filepath.Join(paths.NamespaceDir(payload.ClusterName, payload.Namespace), "membership-grant.token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0700); err != nil {
		return "", err
	}
	if err := os.WriteFile(tokenPath, []byte(payload.MembershipToken+"\n"), 0600); err != nil {
		return "", err
	}
	return tokenPath, nil
}
