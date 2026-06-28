package main

import (
	"fmt"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

const (
	authorityPeerClassUnknown = iota
	authorityPeerClassDirect
	authorityPeerClassRelayCircuit
)

func clusterGrantServicePeers(cluster cfgpkg.Cluster) []string {
	if cluster.MembershipGrant == nil || cluster.MembershipGrant.GrantServiceProtocol != grantspkg.ProtocolID {
		return nil
	}
	return canonicalAuthorityBootstrapPeers(cluster.MembershipGrant.GrantServicePeers)
}

func canonicalAuthorityBootstrapPeers(in []string) []string {
	relayed := make([]string, 0, len(in))
	direct := make([]string, 0, len(in))
	unknown := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		peer := strings.TrimSpace(raw)
		if peer == "" {
			continue
		}
		if _, ok := seen[peer]; ok {
			continue
		}
		seen[peer] = struct{}{}
		switch authorityPeerClass(peer) {
		case authorityPeerClassRelayCircuit:
			relayed = append(relayed, peer)
		case authorityPeerClassDirect:
			direct = append(direct, peer)
		default:
			unknown = append(unknown, peer)
		}
	}
	out := make([]string, 0, len(relayed)+len(direct)+len(unknown))
	out = append(out, relayed...)
	out = append(out, direct...)
	out = append(out, unknown...)
	return out
}

func sanitizeAuthorityBootstrapPeers(in []string) []string {
	out := canonicalAuthorityBootstrapPeers(in)
	trimmed := out[:0]
	for _, peer := range out {
		if authorityPeerClass(peer) != authorityPeerClassUnknown {
			trimmed = append(trimmed, peer)
		}
	}
	return append([]string(nil), trimmed...)
}

func mergeAuthorityBootstrapPeers(existing, incoming []string) []string {
	existing = canonicalAuthorityBootstrapPeers(existing)
	incoming = canonicalAuthorityBootstrapPeers(incoming)
	if len(incoming) == 0 {
		return existing
	}
	if len(existing) == 0 {
		return incoming
	}
	if bestAuthorityPeerClass(incoming) < bestAuthorityPeerClass(existing) {
		return append(append([]string(nil), existing...), dropPeers(incoming, existing)...)
	}
	return append(append([]string(nil), incoming...), dropPeers(existing, incoming)...)
}

func authorityPeerPathSummary(peers []string) string {
	peers = canonicalAuthorityBootstrapPeers(peers)
	if len(peers) == 0 {
		return "none"
	}
	relayCount := 0
	directCount := 0
	for _, peer := range peers {
		switch authorityPeerClass(peer) {
		case authorityPeerClassRelayCircuit:
			relayCount++
		case authorityPeerClassDirect:
			directCount++
		}
	}
	unknownCount := len(peers) - relayCount - directCount
	parts := make([]string, 0, 3)
	if relayCount > 0 {
		parts = append(parts, fmt.Sprintf("relay-circuit=%d", relayCount))
	}
	if directCount > 0 {
		parts = append(parts, fmt.Sprintf("direct=%d", directCount))
	}
	if unknownCount > 0 {
		parts = append(parts, fmt.Sprintf("other=%d", unknownCount))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}

func authorityPeerClass(peer string) int {
	peer = strings.TrimSpace(peer)
	switch {
	case peer == "":
		return authorityPeerClassUnknown
	case strings.Contains(peer, "/p2p-circuit"):
		return authorityPeerClassRelayCircuit
	case grantspkg.IsRemoteDialableGrantServicePeer(peer):
		return authorityPeerClassDirect
	default:
		return authorityPeerClassUnknown
	}
}

func bestAuthorityPeerClass(peers []string) int {
	best := authorityPeerClassUnknown
	for _, peer := range peers {
		if cls := authorityPeerClass(peer); cls > best {
			best = cls
		}
	}
	return best
}
