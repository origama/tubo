package main

import (
	"fmt"
	"strings"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
)

func doctorWarnings(cfg cfgpkg.Config) []string {
	warnings := derivableSeedWarnings(cfg)
	clusterName := strings.TrimSpace(cfg.CurrentCluster)
	namespaceName := strings.TrimSpace(cfg.CurrentNamespace)
	if clusterName == "" || namespaceName == "" {
		return warnings
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return warnings
	}
	policy := cfgpkg.EffectiveScopePolicy(cfg, cfgpkg.Scope{Overlay: cfg.CurrentOverlay, Cluster: clusterName, Namespace: namespaceName})
	if policy.Discovery != cfgpkg.NamespaceDiscoveryEnabled || policy.ConnectPolicy != cfgpkg.ConnectPolicyNamespaceMember {
		return warnings
	}
	if clusterMembershipGrantAuthorizesConnect(cluster, clusterName, namespaceName) {
		return append(warnings, peerBoundMembershipWithoutSeedWarnings(cfg, cluster, clusterName, namespaceName)...)
	}
	if membershipCapabilityAuthorizesConnect(cluster, namespaceName) {
		return append(warnings, peerBoundMembershipWithoutSeedWarnings(cfg, cluster, clusterName, namespaceName)...)
	}
	warnings = append(warnings, fmt.Sprintf("warning: current identity lacks connect permission for discovery-enabled namespace %s/%s; `tubo connect <service>` will be denied until you import a connect-capable membership invite or rotate the namespace membership capability", clusterName, namespaceName))
	warnings = append(warnings, peerBoundMembershipWithoutSeedWarnings(cfg, cluster, clusterName, namespaceName)...)
	return warnings
}

// derivableSeedWarnings surfaces a doctor warning when a configured libp2p seed
// is derivable from public data or is a known demo default. A seed feeds the
// deterministic reader that generates the host Ed25519 private key, so a
// derivable seed is equivalent to a public private key. See #355.
func derivableSeedWarnings(cfg cfgpkg.Config) []string {
	var warnings []string
	checkSeed := func(label, seed string) {
		seed = strings.TrimSpace(seed)
		if seed == "" {
			return
		}
		switch {
		case seed == "public-relay-seed", seed == "service-demo-seed", seed == "bridge-demo-seed", seed == "edge-seed":
			warnings = append(warnings, fmt.Sprintf("warning: %s is a known demo default seed; anyone can reconstruct this libp2p private key. Generate a random persisted seed (e.g. via `tubo id from-seed` on a fresh random value, or let Tubo generate one) and persist it in a 0600 config or key file.", label))
		case strings.HasPrefix(seed, "discovery-query-"), strings.HasPrefix(seed, "grants-"):
			warnings = append(warnings, fmt.Sprintf("warning: %s is derivable from public cluster identifiers; anyone who knows the cluster id can reconstruct this libp2p private key and impersonate the PeerID. Use a random persisted seed instead.", label))
		}
	}
	checkSeed("node.seed", cfg.Node.Seed)
	checkSeed("bridge.service_seed", cfg.Bridge.ServiceSeed)
	for clusterName, cluster := range cfg.Clusters {
		for namespaceName, namespace := range cluster.Namespaces {
			for serviceName, svc := range namespace.Services {
				checkSeed(fmt.Sprintf("clusters.%s.namespaces.%s.services.%s.service_seed", clusterName, namespaceName, serviceName), svc.ServiceSeed)
			}
		}
	}
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
