package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/p2p"
	workspacepkg "github.com/origama/tubo/internal/workspace"
)

func TestConcurrentAuthorityPeerAndAttachMutationsPreserveBoth(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	initial := cfgpkg.Config{
		CurrentCluster:   "solar",
		CurrentNamespace: "default",
		Clusters: map[string]cfgpkg.Cluster{
			"solar": {
				ClusterID:          "cluster-solar",
				AuthorityPublicKey: "authority-public-key",
				Namespaces: map[string]cfgpkg.Namespace{
					"default": {Discovery: cfgpkg.NamespaceDiscoveryEnabled},
				},
			},
		},
	}
	if err := cfgpkg.NewConfigRepository(configPath).Write(context.Background(), initial, false); err != nil {
		t.Fatal(err)
	}

	// Authority process loads snapshot before attach commits service identity.
	staleAuthoritySnapshot, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	runtimeCfg := initial
	runtimeCfg.Service = cfgpkg.Service{Name: "lmstudio", Kind: cfgpkg.ServiceKindHTTP, Target: "http://127.0.0.1:1234"}
	runtimeCfg.HealthListen = "127.0.0.1:19091"
	runtimeCfg.Node.P2PListen = "/ip4/127.0.0.1/tcp/0"
	updatedRuntime, serviceIdentity, err := workspacepkg.Open(workspacepkg.FSStore{}).EnsureAttachServiceIdentity(configPath, runtimeCfg)
	if err != nil {
		t.Fatal(err)
	}
	updatedRuntime, err = persistResolvedAttachServiceDefinition(configPath, updatedRuntime, serviceIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if updatedRuntime.HealthListen != runtimeCfg.HealthListen {
		t.Fatalf("runtime health override lost: got %q want %q", updatedRuntime.HealthListen, runtimeCfg.HealthListen)
	}
	if updatedRuntime.Node.P2PListen != runtimeCfg.Node.P2PListen {
		t.Fatalf("runtime p2p override lost: got %q want %q", updatedRuntime.Node.P2PListen, runtimeCfg.Node.P2PListen)
	}

	authorityPeer, err := p2p.PeerIDFromSeed("concurrent-authority-peer")
	if err != nil {
		t.Fatal(err)
	}
	authorityAddr := fmt.Sprintf("/ip4/203.0.113.10/tcp/4001/p2p/%s", authorityPeer)
	if err := persistClusterDiscoveryPeers(configPath, staleAuthoritySnapshot, "solar", "cluster-solar", []string{authorityAddr}); err != nil {
		t.Fatal(err)
	}

	stored, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := stored.Clusters["solar"]
	service, serviceExists := cluster.Namespaces["default"].Services["lmstudio"]
	if !serviceExists || service.ServiceID == "" {
		t.Fatalf("attach service mutation was lost: %#v", cluster.Namespaces["default"].Services)
	}
	if len(cluster.DiscoveryQueryPeers) != 1 || cluster.DiscoveryQueryPeers[0] != authorityAddr {
		t.Fatalf("authority peer mutation was lost: %#v", cluster.DiscoveryQueryPeers)
	}
}
