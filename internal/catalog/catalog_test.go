package catalog

import (
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
)

func TestServiceResourceFromEntryPreservesConnectMetadata(t *testing.T) {
	pid, err := peer.Decode("12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd")
	if err != nil {
		t.Fatal(err)
	}
	service := ServiceResourceFromEntry(&discovery.ServiceEntry{
		ServiceName:   "myapi",
		ServiceID:     "service-123",
		ServiceKind:   "tcp",
		ConnectPolicy: "namespace_members",
		GrantService:  &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/9.8.7.6/tcp/4001/p2p/12D3KooWGrant"}},
		PeerID:        pid,
		Addresses:     []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"},
		Capabilities:  []string{"hello-v1", "raw-tcp-v1"},
		TTL:           30 * time.Second,
		Registered:    time.Now().Add(-time.Second),
	})
	if service.ConnectPolicy != "namespace_members" {
		t.Fatalf("connect policy = %q", service.ConnectPolicy)
	}
	if service.GrantService == nil || service.GrantService.Protocol != grantspkg.ProtocolID {
		t.Fatalf("grant service = %#v", service.GrantService)
	}
	if service.ServiceKind != "tcp" || len(service.Capabilities) != 2 {
		t.Fatalf("service metadata = %#v", service)
	}
}

func TestMatchesActualScopeKeepsLegacyUserServicesButRejectsLegacySystemServices(t *testing.T) {
	cfg := cfgpkg.Config{CurrentCluster: "home", CurrentNamespace: "team", Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123"}}}
	scope := Scope{Cluster: "home", Namespace: "team"}
	services := []Service{
		{Name: "user-api", Kind: discovery.ResourceKindService},
		{Name: "grant-service", Kind: discovery.ResourceKindGrantService},
		{Name: "scoped-grant-service", Kind: discovery.ResourceKindGrantService, ClusterID: "cluster-123", NamespaceID: "team"},
	}
	filtered := filterServicesByActualScope(cfg, scope, services)
	if len(filtered) != 2 {
		t.Fatalf("filtered len = %d, want 2: %#v", len(filtered), filtered)
	}
	if filtered[0].Name != "user-api" || filtered[1].Name != "scoped-grant-service" {
		t.Fatalf("unexpected filtered services: %#v", filtered)
	}
}

func TestDiscoveryPeersPreferClusterDiscoveryQueryPeers(t *testing.T) {
	cfg := cfgpkg.Config{CurrentOverlay: "manual", CurrentCluster: "home", CurrentNamespace: "default", Network: cfgpkg.Network{BootstrapPeers: []string{"/dns4/relay.example/tcp/4001/p2p/12D3KooWRelay"}}, Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123", AuthorityPublicKey: "ssh-ed25519 AAAA", DiscoveryQueryPeers: []string{"/dns4/authority.example/tcp/4001/p2p/12D3KooWAuthority"}, MembershipGrant: &cfgpkg.ClusterMembershipGrant{GrantServicePeers: []string{"/dns4/fallback.example/tcp/4001/p2p/12D3KooWFallback"}}}}}
	peers, err := discoveryPeersForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 || peers[0] != "/dns4/authority.example/tcp/4001/p2p/12D3KooWAuthority" {
		t.Fatalf("unexpected peers: %#v", peers)
	}
}

func TestDiscoveryPeersRequireConfiguredClusterAuthorityPeer(t *testing.T) {
	cfg := cfgpkg.Config{CurrentOverlay: "manual", CurrentCluster: "home", CurrentNamespace: "default", Network: cfgpkg.Network{RelayPeers: []string{"/dns4/relay.example/tcp/4001/p2p/12D3KooWRelay"}}, Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123", AuthorityPublicKey: "ssh-ed25519 AAAA", AuthorityPrivateKeyFile: "/tmp/authority.key", MembershipCapabilityFile: "/tmp/membership.cap.json"}}}
	if _, err := discoveryPeersForConfig(cfg); err == nil || !strings.Contains(err.Error(), "no discovery authority/cache peer configured for cluster \"home\" namespace \"default\"") {
		t.Fatalf("expected missing authority peer error, got %v", err)
	}
}

func TestServiceFromQueryServicePreservesConnectMetadata(t *testing.T) {
	service := ServiceFromQueryService(discoveryquery.Service{
		Kind:          "service",
		ServiceKind:   "tcp",
		Name:          "myapi",
		ServiceID:     "service-123",
		ConnectPolicy: "invite_only",
		GrantService:  &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/9.8.7.6/tcp/4001/p2p/12D3KooWGrant"}},
		PeerID:        "12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd",
		Addresses:     []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWBDXSkfRCux8NFenVRDUKQLUDPC4LAbaB6x1bpm8YBHLd"},
		Status:        "online",
		Path:          "direct",
		TTLSeconds:    30,
		Capabilities:  []string{"hello-v1", "raw-tcp-v1"},
		RegisteredAt:  time.Now().UTC().Format(time.RFC3339),
	})
	if service.ConnectPolicy != "invite_only" {
		t.Fatalf("connect policy = %q", service.ConnectPolicy)
	}
	if service.GrantService == nil || len(service.GrantService.Peers) != 1 {
		t.Fatalf("grant service = %#v", service.GrantService)
	}
	if service.ServiceKind != "tcp" || len(service.Capabilities) != 2 {
		t.Fatalf("service metadata = %#v", service)
	}
}
