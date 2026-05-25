package main

import (
	"fmt"
	"strings"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
)

func doctorWarnings(cfg cfgpkg.Config) []string {
	clusterName := strings.TrimSpace(cfg.CurrentCluster)
	namespaceName := strings.TrimSpace(cfg.CurrentNamespace)
	if clusterName == "" || namespaceName == "" {
		return nil
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return nil
	}
	policy := cfgpkg.EffectiveScopePolicy(cfg, cfgpkg.Scope{Overlay: cfg.CurrentOverlay, Cluster: clusterName, Namespace: namespaceName})
	if policy.Discovery != cfgpkg.NamespaceDiscoveryEnabled || policy.ConnectPolicy != cfgpkg.ConnectPolicyNamespaceMember {
		return nil
	}
	if clusterMembershipGrantAuthorizesConnect(cluster, clusterName, namespaceName) {
		return nil
	}
	if membershipCapabilityAuthorizesConnect(cluster, namespaceName) {
		return nil
	}
	return []string{fmt.Sprintf("warning: current identity lacks connect permission for discovery-enabled namespace %s/%s; `tubo connect <service>` will be denied until you import a connect-capable membership invite or rotate the namespace membership capability", clusterName, namespaceName)}
}

func membershipCapabilityAuthorizesConnect(cluster cfgpkg.Cluster, namespace string) bool {
	capPath, err := namespaceMembershipCapabilityFile(cluster, namespace)
	if err != nil || strings.TrimSpace(cluster.AuthorityPublicKey) == "" {
		return false
	}
	pub, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey)
	if err != nil {
		return false
	}
	cap, err := loadMembershipCapability(capPath)
	if err != nil {
		return false
	}
	for _, candidateNamespace := range []string{namespace, broadNamespaceWildcard} {
		if err := capability.VerifyMembershipCapability(cap, pub, cluster.ClusterID, candidateNamespace, cluster.ClusterID); err == nil {
			return containsAllStrings(cap.Permissions, []string{capability.PermissionConnect})
		}
	}
	return false
}
