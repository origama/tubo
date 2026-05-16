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

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"golang.org/x/crypto/ssh"
)

const (
	clusterInviteTokenPrefix        = "tubo-invite-v1."
	clusterInviteKind               = "cluster-invite"
	clusterInviteVersion            = "v1"
	clusterInviteDefaultTTL         = 7 * 24 * time.Hour
	clusterInviteDefaultRole        = "member"
	clusterInviteGrantRequesterRole = "grant-requester"
	clusterInviteGrantRequestPerm   = "grant:request"
)

type clusterInviteGrant struct {
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

type clusterInviteGrantService struct {
	Protocol string   `json:"protocol"`
	Peers    []string `json:"peers"`
}

type clusterInvitePayload struct {
	Version            string                    `json:"version"`
	Kind               string                    `json:"kind"`
	JTI                string                    `json:"jti"`
	ClusterName        string                    `json:"cluster_name"`
	ClusterID          string                    `json:"cluster_id"`
	AuthorityPublicKey string                    `json:"authority_public_key"`
	Namespace          string                    `json:"namespace"`
	Grant              clusterInviteGrant        `json:"grant"`
	GrantService       clusterInviteGrantService `json:"grant_service,omitempty"`
	IssuedAt           time.Time                 `json:"issued_at"`
	ExpiresAt          time.Time                 `json:"expires_at"`
}

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

func localShareCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo share cluster/<name>|service/<name> [flags]")
	}
	resource := args[0]
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
	payload := clusterInvitePayload{
		Version:            clusterInviteVersion,
		Kind:               clusterInviteKind,
		JTI:                jti,
		ClusterName:        name,
		ClusterID:          cluster.ClusterID,
		AuthorityPublicKey: cluster.AuthorityPublicKey,
		Namespace:          selectedNamespace,
		Grant:              grant,
		IssuedAt:           now,
		ExpiresAt:          now.Add(*expires),
	}
	if len(grantPeers) > 0 {
		payload.GrantService = clusterInviteGrantService{Protocol: grantspkg.ProtocolID, Peers: grantPeers}
	}
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
	fmt.Printf("shared cluster %q\n", name)
	fmt.Printf("namespace: %s\n", selectedNamespace)
	fmt.Printf("permission: %s\n", grant.Role)
	fmt.Printf("expires: %s\n", payload.ExpiresAt.Format(time.RFC3339))
	fmt.Printf("join: %s\n", result.JoinCommand)
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
		Grant: cfgpkg.ClusterMembershipGrant{
			InviteToken:          token,
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
		},
	}
	if *jsonOut {
		return printJSON(result)
	}
	fmt.Printf("joined cluster %q\n", payload.ClusterName)
	fmt.Printf("namespace: %s\n", payload.Namespace)
	fmt.Printf("grant: %s\n", payload.Grant.Role)
	fmt.Printf("config: %s\n", result.ConfigPath)
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
	if !force {
		if _, err := os.Stat(configPath); err == nil {
			return fmt.Errorf("%s exists (use --force)", configPath)
		}
	}
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
	cluster.Namespaces[payload.Namespace] = cfgpkg.Namespace{}
	cluster.MembershipGrant = &cfgpkg.ClusterMembershipGrant{
		InviteToken:          token,
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
	joined.Clusters[payload.ClusterName] = cluster
	joined.CurrentCluster = payload.ClusterName
	joined.CurrentNamespace = payload.Namespace
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
	switch strings.TrimSpace(strings.ToLower(permission)) {
	case "", clusterInviteDefaultRole:
		return clusterInviteGrant{
			Role: clusterInviteDefaultRole,
			Permissions: []string{
				"subscribe",
				"list",
				"publish",
			},
		}, nil
	case clusterInviteGrantRequesterRole:
		return clusterInviteGrant{
			Role:        clusterInviteGrantRequesterRole,
			Permissions: []string{clusterInviteGrantRequestPerm},
		}, nil
	default:
		return clusterInviteGrant{}, fmt.Errorf("unsupported cluster invitation permission %q", permission)
	}
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
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payloadBytes)
	return clusterInviteTokenPrefix + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func parseAndVerifyClusterInviteToken(token string) (clusterInvitePayload, error) {
	if !isClusterInviteToken(token) {
		return clusterInvitePayload{}, fmt.Errorf("invalid cluster invite token")
	}
	encoded := strings.TrimPrefix(token, clusterInviteTokenPrefix)
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return clusterInvitePayload{}, fmt.Errorf("invalid cluster invite token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return clusterInvitePayload{}, fmt.Errorf("decode cluster invite payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return clusterInvitePayload{}, fmt.Errorf("decode cluster invite signature: %w", err)
	}
	var payload clusterInvitePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return clusterInvitePayload{}, fmt.Errorf("decode cluster invite payload json: %w", err)
	}
	if payload.Version != clusterInviteVersion {
		return clusterInvitePayload{}, fmt.Errorf("unsupported cluster invite version %q", payload.Version)
	}
	if payload.Kind != clusterInviteKind {
		return clusterInvitePayload{}, fmt.Errorf("unsupported cluster invite kind %q", payload.Kind)
	}
	if payload.ClusterName == "" || payload.ClusterID == "" || payload.AuthorityPublicKey == "" || payload.Namespace == "" || payload.JTI == "" {
		return clusterInvitePayload{}, errors.New("cluster invite is missing required fields")
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(payload.AuthorityPublicKey))
	if err != nil {
		return clusterInvitePayload{}, fmt.Errorf("parse cluster invite authority public key: %w", err)
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		return clusterInvitePayload{}, errors.New("cluster invite authority key does not expose a crypto public key")
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return clusterInvitePayload{}, fmt.Errorf("cluster invite authority key is not ed25519: %T", cryptoPub.CryptoPublicKey())
	}
	if !ed25519.Verify(edPub, payloadBytes, sig) {
		return clusterInvitePayload{}, errors.New("invalid cluster invite signature")
	}
	if time.Now().UTC().After(payload.ExpiresAt.UTC()) {
		return clusterInvitePayload{}, errors.New("cluster invite expired")
	}
	if !payload.IssuedAt.IsZero() && payload.ExpiresAt.Before(payload.IssuedAt) {
		return clusterInvitePayload{}, errors.New("cluster invite expires before it was issued")
	}
	if err := validateClusterInviteGrant(payload); err != nil {
		return clusterInvitePayload{}, err
	}
	return payload, nil
}

func validateClusterInviteGrant(payload clusterInvitePayload) error {
	switch payload.Grant.Role {
	case clusterInviteDefaultRole:
		if !stringSliceEqualSet(payload.Grant.Permissions, []string{"subscribe", "list", "publish"}) {
			return errors.New("cluster invite member grant has invalid permissions")
		}
	case clusterInviteGrantRequesterRole:
		if !stringSliceEqualSet(payload.Grant.Permissions, []string{clusterInviteGrantRequestPerm}) {
			return errors.New("cluster invite grant-requester has invalid permissions")
		}
		if payload.GrantService.Protocol != grantspkg.ProtocolID || len(payload.GrantService.Peers) == 0 {
			return errors.New("cluster invite grant-requester is missing grant service metadata")
		}
		for _, peer := range payload.GrantService.Peers {
			if !strings.Contains(peer, "/p2p/") {
				return fmt.Errorf("cluster invite grant service peer %q is invalid", peer)
			}
		}
	default:
		return errors.New("cluster invite is missing grant role")
	}
	return nil
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
	return strings.HasPrefix(token, clusterInviteTokenPrefix)
}
