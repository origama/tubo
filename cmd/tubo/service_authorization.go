package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
)

const broadNamespaceWildcard = "*"

func resolveAuthorizedServiceScopes(cfg cfgpkg.Config, clusterFlag, namespaceFlag string, allNamespaces bool) ([]serviceScope, error) {
	base, err := resolveServiceScope(cfg, clusterFlag, namespaceFlag, allNamespaces)
	if err != nil {
		return nil, err
	}
	if err := cfgpkg.RequireAmbientDiscoveryScope(cfg, cfgpkg.Scope{Overlay: cfg.CurrentOverlay, Cluster: base.Cluster, Namespace: base.Namespace, AllNamespaces: allNamespaces}); err != nil {
		return nil, err
	}
	runtime, err := cfg.RequireDiscoveryRuntime()
	if err != nil {
		return nil, err
	}
	if runtime.Mode != cfgpkg.DiscoveryModeNamespaceV2 {
		return nil, errors.New("cluster/namespace discovery is required")
	}
	if base.Cluster == "" {
		return nil, errors.New("service queries require a cluster context")
	}
	if !allNamespaces {
		if err := authorizeServiceNamespace(cfg, base.Cluster, base.Namespace); err != nil {
			return nil, err
		}
		return []serviceScope{base}, nil
	}
	namespaces, err := authorizedServiceNamespaces(cfg, base.Cluster)
	if err != nil {
		return nil, err
	}
	scopes := make([]serviceScope, 0, len(namespaces))
	for _, namespace := range namespaces {
		scopes = append(scopes, serviceScope{Cluster: base.Cluster, Namespace: namespace, AllNamespaces: true})
	}
	return scopes, nil
}

func authorizedServiceNamespaces(cfg cfgpkg.Config, clusterName string) ([]string, error) {
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return nil, fmt.Errorf("cluster %q not found", clusterName)
	}
	namespaces := make([]string, 0, len(cluster.Namespaces))
	for namespace := range cluster.Namespaces {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	if len(namespaces) == 0 {
		return nil, errors.New("cluster has no namespaces configured")
	}
	for _, namespace := range namespaces {
		if err := authorizeServiceNamespace(cfg, clusterName, namespace); err != nil {
			return nil, err
		}
	}
	return namespaces, nil
}

func authorizeServiceNamespace(cfg cfgpkg.Config, clusterName, namespace string) error {
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return fmt.Errorf("cluster %q not found", clusterName)
	}
	if clusterMembershipGrantAuthorizesNamespace(cluster, clusterName, namespace) {
		return nil
	}
	capPath, err := namespaceMembershipCapabilityFile(cluster, namespace)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cluster.AuthorityPublicKey) == "" {
		return fmt.Errorf("cluster %q is missing authority public key", clusterName)
	}
	pub, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey)
	if err != nil {
		return fmt.Errorf("parse authority public key for cluster %q: %w", clusterName, err)
	}
	cap, err := loadMembershipCapability(capPath)
	if err != nil {
		return fmt.Errorf("load membership capability for %s/%s: %w", clusterName, namespace, err)
	}
	if err := capability.VerifyMembershipCapability(cap, pub, cluster.ClusterID, cap.NamespaceID, cluster.ClusterID); err != nil {
		return fmt.Errorf("membership capability for %s/%s rejected: %w", clusterName, namespace, err)
	}
	if cap.NamespaceID != namespace && cap.NamespaceID != broadNamespaceWildcard {
		return fmt.Errorf("membership capability for %s/%s does not authorize namespace %q", clusterName, namespace, namespace)
	}
	if !containsAllStrings(cap.Permissions, []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}) {
		return fmt.Errorf("membership capability for %s/%s is missing discovery permissions", clusterName, namespace)
	}
	return nil
}

func clusterMembershipGrantAuthorizesNamespace(cluster cfgpkg.Cluster, clusterName, namespace string) bool {
	grant := cluster.MembershipGrant
	if grant == nil {
		return false
	}
	if grant.ClusterName != clusterName || grant.ClusterID != cluster.ClusterID || grant.Namespace != namespace {
		return false
	}
	if grant.Role != clusterInviteDefaultRole {
		return false
	}
	if !containsAllStrings(grant.Permissions, []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}) {
		return false
	}
	if grant.ExpiresAt.IsZero() || time.Now().UTC().After(grant.ExpiresAt.UTC()) {
		return false
	}
	return true
}

func namespaceMembershipCapabilityFile(cluster cfgpkg.Cluster, namespace string) (string, error) {
	if ns, ok := cluster.Namespaces[namespace]; ok && strings.TrimSpace(ns.MembershipCapabilityFile) != "" {
		return ns.MembershipCapabilityFile, nil
	}
	if strings.TrimSpace(cluster.MembershipCapabilityFile) != "" {
		return cluster.MembershipCapabilityFile, nil
	}
	return "", fmt.Errorf("no membership capability file configured for namespace %q", namespace)
}

func loadMembershipCapability(path string) (capability.MembershipCapability, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return capability.MembershipCapability{}, err
	}
	var cap capability.MembershipCapability
	if err := json.Unmarshal(b, &cap); err != nil {
		return capability.MembershipCapability{}, err
	}
	return cap, nil
}

func containsAllStrings(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, item := range have {
		set[item] = struct{}{}
	}
	for _, item := range want {
		if _, ok := set[item]; !ok {
			return false
		}
	}
	return true
}

func discoverServicesAcrossScopes(cfg cfgpkg.Config, timeout time.Duration, scopes []serviceScope) (discoveryLookupResult, error) {
	if len(scopes) == 0 {
		return discoveryLookupResult{}, errors.New("no authorized namespaces found")
	}
	seen := make(map[string]serviceResource)
	messages := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scopedCfg := cfg
		scopedCfg.CurrentCluster = scope.Cluster
		scopedCfg.CurrentNamespace = scope.Namespace
		catalogResult, err := catalog.DiscoverServicesWithConfig(scopedCfg, timeout, false, true, toCatalogScope(scope))
		result := fromCatalogLookupResult(catalogResult)
		if err != nil {
			return discoveryLookupResult{}, fmt.Errorf("namespace %s/%s: %w", scope.Cluster, scope.Namespace, err)
		}
		if len(result.Messages) > 0 {
			messages = append(messages, fmt.Sprintf("[%s/%s] %s", scope.Cluster, scope.Namespace, strings.Join(result.Messages, "; ")))
		}
		for _, service := range result.Services {
			key := strings.Join([]string{service.Cluster, service.Namespace, service.Name, service.PeerID}, "\x00")
			seen[key] = service
		}
	}
	services := make([]serviceResource, 0, len(seen))
	for _, service := range seen {
		services = append(services, service)
	}
	sortServiceResources(services)
	base := scopes[0]
	base.AllNamespaces = true
	return discoveryLookupResult{Services: services, Messages: messages, Mode: "live", Scope: serviceScopePtr(base)}, nil
}
