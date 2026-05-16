package main

import (
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"strings"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

const serviceShareTokenPrefix = grantspkg.ServiceShareTokenPrefix

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
	artifacts, err := grantspkg.BuildServiceShareArtifacts(privKey, scope.Cluster, cluster.ClusterID, scope.Namespace, name, serviceID, *expires)
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
	return grantspkg.ParseAndVerifyServiceShareToken(token)
}

func signServiceShareToken(payload serviceSharePayload, priv ed25519.PrivateKey) (string, error) {
	return grantspkg.SignServiceShareToken(payload, priv)
}

func isServiceShareToken(token string) bool {
	return grantspkg.IsServiceShareToken(token)
}

func importServiceShareDiscoveryContext(cfg cfgpkg.Config, payload serviceSharePayload) cfgpkg.Config {
	if cfg.Clusters == nil {
		cfg.Clusters = make(map[string]cfgpkg.Cluster)
	}
	cluster := cfg.Clusters[payload.ClusterName]
	cluster.ClusterID = payload.ClusterID
	cluster.AuthorityPublicKey = payload.AuthorityPublicKey
	if cluster.Namespaces == nil {
		cluster.Namespaces = make(map[string]cfgpkg.Namespace)
	}
	cluster.Namespaces[payload.Namespace] = cfgpkg.Namespace{}
	cluster.MembershipGrant = &cfgpkg.ClusterMembershipGrant{
		ClusterName:        payload.ClusterName,
		ClusterID:          payload.ClusterID,
		AuthorityPublicKey: payload.AuthorityPublicKey,
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
	return cfg
}
