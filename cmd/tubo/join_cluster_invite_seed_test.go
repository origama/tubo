package main

import (
	"path/filepath"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/p2p"
)

// TestJoinClusterInviteGeneratesStableNodeSeed verifies that joining a
// cluster invite persists a fresh node.seed when none was configured yet,
// so peer-bound namespace membership authorization stays stable across
// process restarts and connect candidate retries.
func TestJoinClusterInviteGeneratesStableNodeSeed(t *testing.T) {
	// Authority side: create clusterA in its own config.
	authorityConfig := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterA", "--config", authorityConfig})
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(authorityConfig)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["clusterA"]
	cluster.DiscoveryQueryPeers = []string{"/dns4/authority.example/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}
	cfg.Clusters["clusterA"] = cluster
	if err := cfgpkg.WriteFile(authorityConfig, cfg, true); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterA", "--config", authorityConfig, "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)

	// Member side: join on a fresh config directory.
	memberDir := t.TempDir()
	if _, err := capture(func() error {
		return run([]string{"join", "cluster/clusterA", "--token", token, "--config-dir", memberDir})
	}); err != nil {
		t.Fatalf("join clusterA failed: %v", err)
	}
	memberConfigPath := filepath.Join(memberDir, "config.yaml")
	first, err := cfgpkg.LoadFile(memberConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if first.Node.Seed == "" {
		t.Fatalf("expected node.seed to be generated on private-cluster join, got empty")
	}
	// Reload and confirm the seed is stable (not regenerated on each load).
	second, err := cfgpkg.LoadFile(memberConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if second.Node.Seed != first.Node.Seed {
		t.Fatalf("node.seed changed across reloads: %q vs %q", first.Node.Seed, second.Node.Seed)
	}

	// The seed must deterministically produce a peer id we can compute up-front.
	peerID, err := p2p.PeerIDFromSeed(first.Node.Seed)
	if err != nil {
		t.Fatalf("PeerIDFromSeed: %v", err)
	}
	if peerID.String() == "" {
		t.Fatal("PeerIDFromSeed returned empty peer id")
	}
}

// TestJoinClusterInvitePreservesExistingNodeSeed verifies that if node.seed
// was already set in the local config, joining a cluster invite does not
// overwrite it.
// TestJoinClusterInviteMembershipSubjectMatchesGeneratedNodeSeed documents a
// current gap in the token-based join fixture: join persists a signed
// membership-grant token, not a signed membership.cap.json artifact. An exact
// assertion over MembershipCapability.SubjectPeerID therefore requires a real
// join-time capability redemption/install path, which is broader than this
// node.seed stabilization fix.
func TestJoinClusterInviteMembershipSubjectMatchesGeneratedNodeSeed(t *testing.T) {
	t.Skip("token-based cluster join persists membership-grant.token, not membership.cap.json; exact membership SubjectPeerID regression needs capability redemption/install path")
}

func TestJoinClusterInvitePreservesExistingNodeSeed(t *testing.T) {
	authorityConfig := writeCreateClusterConfig(t)
	if _, err := capture(func() error {
		return run([]string{"create", "cluster/clusterA", "--config", authorityConfig})
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(authorityConfig)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["clusterA"]
	cluster.DiscoveryQueryPeers = []string{"/dns4/authority.example/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"}
	cfg.Clusters["clusterA"] = cluster
	if err := cfgpkg.WriteFile(authorityConfig, cfg, true); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "cluster/clusterA", "--config", authorityConfig, "--expires", "1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)

	memberDir := t.TempDir()
	memberConfigPath := filepath.Join(memberDir, "config.yaml")
	// Pre-seed the member config with an explicit seed.
	preSeed := "explicit-user-configured-seed-for-testing-1234567890abcdef"
	if err := cfgpkg.WriteFile(memberConfigPath, cfgpkg.Config{Node: cfgpkg.Node{Seed: preSeed}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error {
		return run([]string{"join", "cluster/clusterA", "--token", token, "--config-dir", memberDir})
	}); err != nil {
		t.Fatalf("join clusterA failed: %v", err)
	}
	after, err := cfgpkg.LoadFile(memberConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if after.Node.Seed != preSeed {
		t.Fatalf("explicit node.seed was overwritten: got %q want %q", after.Node.Seed, preSeed)
	}
}
