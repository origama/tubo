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
		return peerBoundMembershipWithoutSeedWarnings(cfg, cluster, clusterName, namespaceName)
	}
	if membershipCapabilityAuthorizesConnect(cluster, namespaceName) {
		return peerBoundMembershipWithoutSeedWarnings(cfg, cluster, clusterName, namespaceName)
	}
	warnings := []string{fmt.Sprintf("warning: current identity lacks connect permission for discovery-enabled namespace %s/%s; `tubo connect <service>` will be denied until you import a connect-capable membership invite or rotate the namespace membership capability", clusterName, namespaceName)}
	warnings = append(warnings, peerBoundMembershipWithoutSeedWarnings(cfg, cluster, clusterName, namespaceName)...)
	return warnings
}

// peerBoundMembershipWithoutSeedWarnings surfaces a doctor warning when a
// peer-bound namespace membership capability is present but node.seed is not
// configured. In that case the local peer id is ephemeral and connect will
// be denied by namespace_members authorization even though the capability
// itself is otherwise valid.
func peerBoundMembershipWithoutSeedWarnings(cfg cfgpkg.Config, cluster cfgpkg.Cluster, clusterName, namespaceName string) []string {
	if strings.TrimSpace(cfg.Node.Seed) != "" {
		return nil
	}
	capPath, err := namespaceMembershipCapabilityFile(cluster, namespaceName)
	if err != nil {
		return nil
	}
	cap, err := loadMembershipCapability(capPath)
	if err != nil {
		return nil
	}
	subject := strings.TrimSpace(cap.SubjectPeerID)
	if subject == "" || subject == strings.TrimSpace(cluster.ClusterID) {
		return nil
	}
	return []string{fmt.Sprintf("warning: namespace membership capability for %s/%s is bound to peer id %q but node.seed is not configured; `tubo connect` will use an ephemeral libp2p peer id and be denied by namespace_members. Configure node.seed or re-issue the membership capability.", clusterName, namespaceName, subject)}
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
