package connectflow

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	bridge "github.com/origama/tubo/internal/app/bridge"
	capability "github.com/origama/tubo/internal/capability"
	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/logging"
)

// TestConnectBridgeCandidateRetryPreservesSeed verifies that when connect
// falls back from one candidate to another, each retry uses the same
// bridge.Config.Seed (so the libp2p peer id stays stable across candidates
// and matches the peer-bound namespace membership capability).
func TestConnectBridgeCandidateRetryPreservesSeed(t *testing.T) {
	service := catalog.Service{
		Name:             "svc",
		ServiceID:        "svc-1",
		DirectAddresses:  []string{"/ip4/10.0.0.1/tcp/4101/p2p/peer-a", "/ip4/10.0.0.2/tcp/4101/p2p/peer-a"},
		RelayedAddresses: []string{"/ip4/1.2.3.4/tcp/4001/p2p/relay/p2p-circuit/p2p/peer-a"},
	}
	base := bridge.Config{
		Seed:                "stable-node-seed-1234567890abcdef",
		ConnectAccessLease:  &grantspkg.ConnectAccessLease{ServiceID: "svc-1"},
		ConnectRefreshLease: &grantspkg.ConnectRefreshLease{ServiceID: "svc-1"},
	}
	var seeds []string
	_, _, _, _, err := ConnectBridge(context.Background(), func(_ context.Context, cfg bridge.Config) (*bridge.App, error) {
		seeds = append(seeds, cfg.Seed)
		if len(seeds) < 3 {
			return nil, errors.New("candidate failed")
		}
		return &bridge.App{}, nil
	}, base, service)
	if err != nil {
		t.Fatalf("ConnectBridge: %v", err)
	}
	if len(seeds) < 2 {
		t.Fatalf("expected at least 2 candidate attempts, got %d", len(seeds))
	}
	for i, seed := range seeds {
		if seed != base.Seed {
			t.Fatalf("candidate attempt %d used seed %q, want %q", i, seed, base.Seed)
		}
	}
}

// TestWarnPeerBoundMembershipWithoutSeedEmitsOnce verifies the diagnostic
// warning emission logic in isolation.
func TestWarnPeerBoundMembershipWithoutSeedEmitsOnce(t *testing.T) {
	cases := []struct {
		name       string
		cfg        cfgpkg.Config
		membership *capability.MembershipCapability
		wantWarn   bool
	}{
		{
			name:       "peer-bound and no seed emits warning",
			cfg:        cfgpkg.Config{Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-abc"}}},
			membership: &capability.MembershipCapability{SubjectPeerID: "12D3KooWMemberPeer", ClusterID: "cluster-abc", NamespaceID: "default", ExpiresAt: time.Now().Add(time.Hour)},
			wantWarn:   true,
		},
		{
			name:       "cluster-scoped membership does not warn",
			cfg:        cfgpkg.Config{Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-abc"}}},
			membership: &capability.MembershipCapability{SubjectPeerID: "cluster-abc", ClusterID: "cluster-abc", NamespaceID: "default", ExpiresAt: time.Now().Add(time.Hour)},
			wantWarn:   false,
		},
		{
			name:       "seed present suppresses warning",
			cfg:        cfgpkg.Config{Node: cfgpkg.Node{Seed: "explicit-seed"}, Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-abc"}}},
			membership: &capability.MembershipCapability{SubjectPeerID: "12D3KooWMemberPeer", ClusterID: "cluster-abc", NamespaceID: "default", ExpiresAt: time.Now().Add(time.Hour)},
			wantWarn:   false,
		},
		{
			name:       "nil membership does not warn",
			cfg:        cfgpkg.Config{Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-abc"}}},
			membership: nil,
			wantWarn:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetPeerBoundMembershipWarnedForTest()
			oldCfg := logging.Current()
			if err := logging.Configure(logging.Config{Runtime: true, Verbosity: 1}); err != nil {
				t.Fatalf("configure logging: %v", err)
			}
			defer func() { _ = logging.Configure(oldCfg) }()
			oldStderr := os.Stderr
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Stderr = w
			warnPeerBoundMembershipWithoutSeed(tc.cfg, catalog.Scope{Cluster: "home", Namespace: "default"}, tc.membership)
			// Second call must not emit a duplicate warning.
			warnPeerBoundMembershipWithoutSeed(tc.cfg, catalog.Scope{Cluster: "home", Namespace: "default"}, tc.membership)
			_ = w.Close()
			os.Stderr = oldStderr
			out, _ := io.ReadAll(r)
			_ = r.Close()
			text := string(out)
			hasWarn := strings.Contains(text, "namespace membership capability is bound to peer id")
			if hasWarn != tc.wantWarn {
				t.Fatalf("warn emitted=%v want=%v, output=%q", hasWarn, tc.wantWarn, text)
			}
			if tc.wantWarn && strings.Count(text, "namespace membership capability is bound to peer id") != 1 {
				t.Fatalf("expected exactly one warning emission, got: %q", text)
			}
		})
	}
}
