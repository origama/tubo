package catalog

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	libhost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
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

func TestDiscoveryPeerAttemptTimeoutCapsPerPeerBudget(t *testing.T) {
	if got := discoveryPeerAttemptTimeout(30*time.Second, 4); got != 5*time.Second {
		t.Fatalf("discoveryPeerAttemptTimeout() = %s, want 5s", got)
	}
	if got := discoveryPeerAttemptTimeout(2*time.Second, 4); got != time.Second {
		t.Fatalf("discoveryPeerAttemptTimeout() short total = %s, want 1s", got)
	}
	if got := discoveryPeerAttemptTimeout(90*time.Second, 2); got != 5*time.Second {
		t.Fatalf("discoveryPeerAttemptTimeout() capped = %s, want 5s", got)
	}
}

func TestFetchRemoteServiceCacheFallsBackAcrossPeersWithinPerPeerTimeout(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "swarm.key")
	if err := os.WriteFile(keyPath, []byte("/key/swarm/psk/1.0.0/\n/base16/\n0000000000000000000000000000000000000000000000000000000000000000\n"), 0600); err != nil {
		t.Fatal(err)
	}
	peerA, err := p2p.PeerIDFromSeed("catalog-peer-a")
	if err != nil {
		t.Fatal(err)
	}
	peerB, err := p2p.PeerIDFromSeed("catalog-peer-b")
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{Network: cfgpkg.Network{PrivateKeyFile: keyPath, BootstrapPeers: []string{fmt.Sprintf("/ip4/127.0.0.1/tcp/4001/p2p/%s", peerA), fmt.Sprintf("/ip4/127.0.0.1/tcp/4002/p2p/%s", peerB)}}}
	old := listServicesWithAuthorizationFunc
	defer func() { listServicesWithAuthorizationFunc = old }()
	type attempt struct {
		peerID  string
		timeout time.Duration
	}
	attempts := make([]attempt, 0, 2)
	listServicesWithAuthorizationFunc = func(ctx context.Context, _ libhost.Host, info peer.AddrInfo, _ *capability.MembershipCapability, _ string) (discoveryquery.Response, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("missing query deadline")
		}
		attempts = append(attempts, attempt{peerID: info.ID.String(), timeout: time.Until(deadline)})
		if info.ID == peerA {
			return discoveryquery.Response{}, context.DeadlineExceeded
		}
		return discoveryquery.Response{Metadata: discoveryquery.Metadata{ServedBy: peerB.String(), ServedByRole: "authority"}, Services: []discoveryquery.Service{{Name: "myapi", PeerID: peerB.String(), Status: "online", Path: "direct", TTLSeconds: 30, ExpiresInSeconds: 30}}}, nil
	}
	services, metadata, messages, err := FetchRemoteServiceCache(cfg, 12*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if metadata == nil || metadata.ServedByRole != "authority" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	if len(services) != 1 || services[0].Name != "myapi" {
		t.Fatalf("unexpected services: %#v", services)
	}
	if len(attempts) != 2 {
		t.Fatalf("attempts = %#v, want 2", attempts)
	}
	if attempts[0].timeout > 5*time.Second+500*time.Millisecond || attempts[0].timeout < 4*time.Second {
		t.Fatalf("first attempt timeout = %s, want near 5s", attempts[0].timeout)
	}
	if attempts[1].timeout <= attempts[0].timeout {
		t.Fatalf("second attempt timeout = %s, want more remaining budget than first after fast failure %s", attempts[1].timeout, attempts[0].timeout)
	}
	joined := strings.Join(messages, "\n")
	for _, want := range []string{"querying cluster discovery peer 1/2", "discovery peer 1/2 (direct) timed out", "querying cluster discovery peer 2/2", "received 1 records from cluster discovery authority"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("messages missing %q: %s", want, joined)
		}
	}
}

func TestRemoteAttemptsDoNotClassifyProtocolErrorsAsUnreachable(t *testing.T) {
	attempts := []remoteQueryAttempt{{Peer: "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWPeer", PathClass: "direct", Err: fmt.Errorf("open discovery query stream: protocol not supported")}}
	if remoteAttemptsAllUnreachable(attempts) {
		t.Fatal("protocol negotiation failures should not be reported as unreachable peers")
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
