package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	"golang.org/x/crypto/ssh"
)

const (
	serviceShareTokenPrefix = "tubo-service-share-v1."
	serviceShareKind        = "service-share"
	serviceShareVersion     = "v1"
	serviceShareDefaultTTL  = time.Hour
)

type serviceSharePayload struct {
	Version            string                       `json:"version"`
	Kind               string                       `json:"kind"`
	ClusterName        string                       `json:"cluster_name"`
	ClusterID          string                       `json:"cluster_id"`
	AuthorityPublicKey string                       `json:"authority_public_key"`
	Namespace          string                       `json:"namespace"`
	NamespaceID        string                       `json:"namespace_id"`
	ServiceName        string                       `json:"service_name"`
	ServiceID          string                       `json:"service_id"`
	Grant              capability.ConnectCapability `json:"grant"`
	IssuedAt           time.Time                    `json:"issued_at"`
	ExpiresAt          time.Time                    `json:"expires_at"`
}

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
	cluster, ok := cfg.Clusters[scope.Cluster]
	if !ok {
		return fmt.Errorf("cluster %q not found", scope.Cluster)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" || cluster.AuthorityPrivateKeyFile == "" {
		return fmt.Errorf("cluster %q is missing authority metadata", scope.Cluster)
	}
	if cluster.Namespaces == nil {
		return fmt.Errorf("cluster %q has no namespaces configured", scope.Cluster)
	}
	namespace, ok := cluster.Namespaces[scope.Namespace]
	if !ok {
		return fmt.Errorf("namespace %q not found in cluster %q", scope.Namespace, scope.Cluster)
	}
	if namespace.Services == nil {
		return fmt.Errorf("namespace %q has no services configured", scope.Namespace)
	}
	svc, ok := namespace.Services[name]
	if !ok {
		return fmt.Errorf("service %q not found in cluster %q namespace %q", name, scope.Cluster, scope.Namespace)
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
	if serviceID == "" {
		serviceID, _ = serviceIdentityFor(cluster.ClusterID, scope.Namespace, name)
	}
	grant, err := capability.SignConnectCapability(capability.ConnectCapability{
		ClusterID:     cluster.ClusterID,
		NamespaceID:   scope.Namespace,
		ServiceID:     serviceID,
		SubjectPeerID: "",
		Permissions:   []string{capability.PermissionConnect},
		ExpiresAt:     time.Now().UTC().Add(*expires),
	}, privKey)
	if err != nil {
		return err
	}
	payload := serviceSharePayload{
		Version:            serviceShareVersion,
		Kind:               serviceShareKind,
		ClusterName:        scope.Cluster,
		ClusterID:          cluster.ClusterID,
		AuthorityPublicKey: cluster.AuthorityPublicKey,
		Namespace:          scope.Namespace,
		NamespaceID:        scope.Namespace,
		ServiceName:        name,
		ServiceID:          serviceID,
		Grant:              grant,
		IssuedAt:           time.Now().UTC(),
		ExpiresAt:          grant.ExpiresAt,
	}
	token, err := signServiceShareToken(payload, privKey)
	if err != nil {
		return err
	}
	result := serviceShareResult{
		ClusterName: scope.Cluster,
		Namespace:   scope.Namespace,
		ServiceName: name,
		ServiceID:   serviceID,
		Permission:  "connect",
		ExpiresAt:   payload.ExpiresAt.Format(time.RFC3339),
		Token:       token,
		ConnectCmd:  fmt.Sprintf("tubo connect --token %s", token),
	}
	if *jsonOut {
		return printJSON(result)
	}
	fmt.Printf("shared service %q in cluster %q namespace %q\n", name, scope.Cluster, scope.Namespace)
	fmt.Printf("service id: %s\n", serviceID)
	fmt.Printf("permission: connect\n")
	fmt.Printf("expires: %s\n", payload.ExpiresAt.Format(time.RFC3339))
	fmt.Printf("connect: %s\n", result.ConnectCmd)
	return nil
}

func signServiceShareToken(payload serviceSharePayload, priv ed25519.PrivateKey) (string, error) {
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payloadBytes)
	return serviceShareTokenPrefix + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func connectServiceShareSetup(serviceName, token, clusterFlag, namespaceFlag string) (string, serviceScope, error) {
	if strings.TrimSpace(token) == "" {
		return strings.TrimSpace(serviceName), serviceScope{Cluster: strings.TrimSpace(clusterFlag), Namespace: strings.TrimSpace(namespaceFlag)}, nil
	}
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		return "", serviceScope{}, err
	}
	serviceName = strings.TrimSpace(serviceName)
	if serviceName != "" && serviceName != payload.ServiceName {
		return "", serviceScope{}, fmt.Errorf("service share is for %q, not %q", payload.ServiceName, serviceName)
	}
	clusterFlag = strings.TrimSpace(clusterFlag)
	if clusterFlag != "" && clusterFlag != payload.ClusterName {
		return "", serviceScope{}, fmt.Errorf("service share is for cluster %q, not %q", payload.ClusterName, clusterFlag)
	}
	namespaceFlag = strings.TrimSpace(namespaceFlag)
	if namespaceFlag != "" && namespaceFlag != payload.Namespace {
		return "", serviceScope{}, fmt.Errorf("service share is for namespace %q, not %q", payload.Namespace, namespaceFlag)
	}
	return payload.ServiceName, serviceScope{Cluster: payload.ClusterName, Namespace: payload.Namespace}, nil
}

func parseAndVerifyServiceShareToken(token string) (serviceSharePayload, error) {
	if !isServiceShareToken(token) {
		return serviceSharePayload{}, fmt.Errorf("invalid service share token")
	}
	encoded := strings.TrimPrefix(token, serviceShareTokenPrefix)
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return serviceSharePayload{}, fmt.Errorf("invalid service share token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return serviceSharePayload{}, fmt.Errorf("decode service share payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return serviceSharePayload{}, fmt.Errorf("decode service share signature: %w", err)
	}
	var payload serviceSharePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return serviceSharePayload{}, fmt.Errorf("decode service share payload json: %w", err)
	}
	if payload.Version != serviceShareVersion {
		return serviceSharePayload{}, fmt.Errorf("unsupported service share version %q", payload.Version)
	}
	if payload.Kind != serviceShareKind {
		return serviceSharePayload{}, fmt.Errorf("unsupported service share kind %q", payload.Kind)
	}
	if payload.ClusterName == "" || payload.ClusterID == "" || payload.AuthorityPublicKey == "" || payload.Namespace == "" || payload.NamespaceID == "" || payload.ServiceName == "" || payload.ServiceID == "" {
		return serviceSharePayload{}, errors.New("service share is missing required fields")
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(payload.AuthorityPublicKey))
	if err != nil {
		return serviceSharePayload{}, fmt.Errorf("parse service share authority public key: %w", err)
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		return serviceSharePayload{}, errors.New("service share authority key does not expose a crypto public key")
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return serviceSharePayload{}, fmt.Errorf("service share authority key is not ed25519: %T", cryptoPub.CryptoPublicKey())
	}
	if !ed25519.Verify(edPub, payloadBytes, sig) {
		return serviceSharePayload{}, errors.New("invalid service share signature")
	}
	if time.Now().UTC().After(payload.ExpiresAt.UTC()) {
		return serviceSharePayload{}, errors.New("service share expired")
	}
	if !payload.IssuedAt.IsZero() && payload.ExpiresAt.Before(payload.IssuedAt) {
		return serviceSharePayload{}, errors.New("service share expires before it was issued")
	}
	if err := capability.VerifyConnectCapability(payload.Grant, edPub, payload.ClusterID, payload.NamespaceID, payload.ServiceID, ""); err != nil {
		return serviceSharePayload{}, err
	}
	if !payload.Grant.ExpiresAt.UTC().Equal(payload.ExpiresAt.UTC()) {
		return serviceSharePayload{}, errors.New("service share expiry mismatch")
	}
	if len(payload.Grant.Permissions) != 1 || payload.Grant.Permissions[0] != capability.PermissionConnect {
		return serviceSharePayload{}, errors.New("service share must be connect-only")
	}
	if payload.Grant.ClusterID != payload.ClusterID || payload.Grant.NamespaceID != payload.NamespaceID || payload.Grant.ServiceID != payload.ServiceID {
		return serviceSharePayload{}, errors.New("service share grant scope mismatch")
	}
	return payload, nil
}

func isServiceShareToken(token string) bool {
	return strings.HasPrefix(token, serviceShareTokenPrefix)
}
