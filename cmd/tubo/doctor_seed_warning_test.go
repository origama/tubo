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
