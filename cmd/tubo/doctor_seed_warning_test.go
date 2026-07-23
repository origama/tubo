package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
)

func TestDoctorWarnsWhenPeerBoundMembershipExistsButNodeSeedMissing(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	peerBound := capability.MembershipCapability{
		ClusterID:     cluster.ClusterID,
		NamespaceID:   "default",
		SubjectPeerID: "12D3KooWPeerBoundMember",
		Permissions:   []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	b, err := json.MarshalIndent(peerBound, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cluster.MembershipCapabilityFile, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	cfg.Node.Seed = ""
	warnings := doctorWarnings(cfg)
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "bound to peer id") || !strings.Contains(joined, "node.seed is not configured") {
		t.Fatalf("expected peer-bound node.seed warning, got: %v", warnings)
	}
}

func TestDoctorWarnsOnDerivableOrDemoSeeds(t *testing.T) {
	cases := []struct {
		name string
		seed string
		want string
	}{
		{name: "demo default", seed: "public-relay-seed", want: "known demo default seed"},
		{name: "service demo default", seed: "service-demo-seed", want: "known demo default seed"},
		{name: "discovery-query derivable", seed: "discovery-query-cluster-123-default", want: "derivable from public cluster identifiers"},
		{name: "grants derivable", seed: "grants-cluster-123", want: "derivable from public cluster identifiers"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := cfgpkg.Config{Node: cfgpkg.Node{Seed: tc.seed}}
			warnings := doctorWarnings(cfg)
			joined := strings.Join(warnings, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("expected warning containing %q for seed %q, got: %v", tc.want, tc.seed, warnings)
			}
		})
	}
}

func TestDoctorDoesNotWarnOnRandomSeed(t *testing.T) {
	cfg := cfgpkg.Config{Node: cfgpkg.Node{Seed: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}}
	for _, w := range doctorWarnings(cfg) {
		if strings.Contains(w, "seed") {
			t.Fatalf("unexpected seed warning for random seed: %q", w)
		}
	}
}
