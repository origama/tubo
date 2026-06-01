package launcher

import (
	"context"
	"testing"
	"time"

	bridge "github.com/origama/tubo/internal/app/bridge"
	edge "github.com/origama/tubo/internal/app/edge"
	relay "github.com/origama/tubo/internal/app/relay"
	service "github.com/origama/tubo/internal/app/service"
	cfgpkg "github.com/origama/tubo/internal/config"
)

type stubRunner struct{ started bool }

func (s *stubRunner) Start(context.Context) error { s.started = true; return nil }

type stubDeps struct {
	authz        AttachAuthorization
	resolveCalls int
	printCalls   int
	renewCalls   int
	serviceCfg   service.Config
	edgeCfg      edge.Config
	relayCfg     relay.Config
	bridgeCfg    bridge.Config
	serviceRun   *stubRunner
	edgeRun      *stubRunner
	relayRun     *stubRunner
	bridgeRun    *stubRunner
}

func (s *stubDeps) ResolveAttachAuthorization(_ string, cfg cfgpkg.Config) (AttachAuthorization, error) {
	s.resolveCalls++
	return s.authz, nil
}
func (s *stubDeps) PrintAttachShareHint(cfg cfgpkg.Config, auth AttachAuthorization) { s.printCalls++ }
func (s *stubDeps) StartAttachPublishLeaseRenewal(ctx context.Context, configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) {
	s.renewCalls++
}
func (s *stubDeps) NewEdge(_ context.Context, cfg edge.Config) (Runner, error) {
	s.edgeRun = &stubRunner{}
	s.edgeCfg = cfg
	return s.edgeRun, nil
}
func (s *stubDeps) NewService(_ context.Context, cfg service.Config) (Runner, error) {
	s.serviceRun = &stubRunner{}
	s.serviceCfg = cfg
	return s.serviceRun, nil
}
func (s *stubDeps) NewRelay(_ context.Context, cfg relay.Config) (Runner, error) {
	s.relayRun = &stubRunner{}
	s.relayCfg = cfg
	return s.relayRun, nil
}
func (s *stubDeps) NewBridge(_ context.Context, cfg bridge.Config) (Runner, error) {
	s.bridgeRun = &stubRunner{}
	s.bridgeCfg = cfg
	return s.bridgeRun, nil
}

func TestRunServiceUsesAttachAuthorizationAndStartsRunner(t *testing.T) {
	cfg := cfgpkg.Config{CurrentCluster: "home", CurrentNamespace: "default"}
	cfg.Clusters = map[string]cfgpkg.Cluster{"home": {AuthorityPublicKey: "ssh-ed25519 AAA..."}}
	cfg.Node.P2PListen = "/ip4/127.0.0.1/tcp/40123"
	cfg.Network.BootstrapPeers = []string{"/ip4/1.2.3.4/tcp/4001/p2p/peer"}
	cfg.Network.RelayPeers = []string{"/ip4/1.2.3.4/tcp/4002/p2p/relay"}
	cfg.Network.Autorelay = true
	cfg.Network.HolePunching = true
	cfg.HealthListen = "127.0.0.1:8081"
	cfg.HeartbeatInterval = cfgpkg.Duration(2 * time.Second)
	cfg.Service.Name = "svc"
	cfg.Service.Target = "http://127.0.0.1:9000"
	cfg.Clusters["home"] = cfgpkg.Cluster{ClusterID: "cluster-1", AuthorityPublicKey: "ssh-ed25519 AAA...", MembershipGrant: &cfgpkg.ClusterMembershipGrant{}, Namespaces: map[string]cfgpkg.Namespace{"default": {}}}
	deps := &stubDeps{authz: AttachAuthorization{Config: cfg, Service: cfgpkg.NamespaceService{ServiceID: "svc-1", ServiceSeed: "seed-1"}, ServicePeerID: "12D3KooW...", MembershipCapabilityFile: "membership.cap", ServiceClaimFile: "claim.cap", ServicePublishLeaseFile: "lease.json"}}
	if err := Run(context.Background(), deps, "service", "/tmp/config.yaml", cfg); err != nil {
		t.Fatal(err)
	}
	if deps.resolveCalls != 1 || deps.printCalls != 1 || deps.renewCalls != 1 {
		t.Fatalf("unexpected attach calls: resolve=%d print=%d renew=%d", deps.resolveCalls, deps.printCalls, deps.renewCalls)
	}
	if deps.serviceCfg.ServiceID != "svc-1" || deps.serviceCfg.Seed != "seed-1" {
		t.Fatalf("unexpected service config: %#v", deps.serviceCfg)
	}
	if !deps.serviceRun.started {
		t.Fatal("expected service runner to start")
	}
}

func TestRunServicePrefersAuthorizedServiceKind(t *testing.T) {
	cfg := cfgpkg.Config{CurrentCluster: "home", CurrentNamespace: "default"}
	cfg.Clusters = map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-1", AuthorityPublicKey: "ssh-ed25519 AAA...", MembershipGrant: &cfgpkg.ClusterMembershipGrant{}, Namespaces: map[string]cfgpkg.Namespace{"default": {}}}}
	cfg.Node.P2PListen = "/ip4/127.0.0.1/tcp/40123"
	cfg.Service.Name = "svc"
	cfg.Service.Kind = cfgpkg.ServiceKindHTTP
	cfg.Service.Target = "tcp://127.0.0.1:9443"
	deps := &stubDeps{authz: AttachAuthorization{Config: cfg, Service: cfgpkg.NamespaceService{ServiceID: "svc-1", ServiceSeed: "seed-1", Kind: cfgpkg.ServiceKindTCP}, ServicePeerID: "12D3KooW..."}}
	if err := Run(context.Background(), deps, "service", "/tmp/config.yaml", cfg); err != nil {
		t.Fatal(err)
	}
	if deps.serviceCfg.ServiceKind != "tcp" {
		t.Fatalf("service kind = %q", deps.serviceCfg.ServiceKind)
	}
}

func TestRunServiceUsesUnlistedModeForPublicDefault(t *testing.T) {
	cfg := cfgpkg.Config{CurrentOverlay: "tubo-public", CurrentCluster: "home", CurrentNamespace: "default"}
	cfg.Clusters = map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-public-2026", AuthorityPublicKey: "ssh-ed25519 AAA...", MembershipGrant: &cfgpkg.ClusterMembershipGrant{}, Namespaces: map[string]cfgpkg.Namespace{"default": {Discovery: cfgpkg.NamespaceDiscoveryDisabled, ConnectPolicy: cfgpkg.ConnectPolicyInviteOnly}}}}
	cfg.Node.P2PListen = "/ip4/127.0.0.1/tcp/40123"
	cfg.Network.BootstrapPeers = []string{"/ip4/1.2.3.4/tcp/4001/p2p/peer"}
	cfg.Network.RelayPeers = []string{"/ip4/1.2.3.4/tcp/4002/p2p/relay"}
	cfg.Network.Autorelay = true
	cfg.Network.HolePunching = true
	cfg.HealthListen = "127.0.0.1:8081"
	cfg.HeartbeatInterval = cfgpkg.Duration(2 * time.Second)
	cfg.Service.Name = "svc"
	cfg.Service.Target = "http://127.0.0.1:9000"
	deps := &stubDeps{authz: AttachAuthorization{Config: cfg, Service: cfgpkg.NamespaceService{ServiceID: "svc-1", ServiceSeed: "seed-1"}, ServicePeerID: "12D3KooW...", MembershipCapabilityFile: "membership.cap", ServiceClaimFile: "claim.cap", ServicePublishLeaseFile: "lease.json"}}
	if err := Run(context.Background(), deps, "service", "/tmp/config.yaml", cfg); err != nil {
		t.Fatal(err)
	}
	if deps.serviceCfg.DiscoveryEnabled {
		t.Fatalf("expected public default service to run unlisted, got %#v", deps.serviceCfg)
	}
	if deps.serviceCfg.Visibility != "unlisted" {
		t.Fatalf("visibility = %q", deps.serviceCfg.Visibility)
	}
	if deps.serviceCfg.DiscoveryMode != cfgpkg.DiscoveryModeNamespaceV2.String() || deps.serviceCfg.DiscoveryClusterID != "cluster-public-2026" || deps.serviceCfg.DiscoveryNamespaceID != "default" {
		t.Fatalf("unexpected discovery scope for unlisted mode: %#v", deps.serviceCfg)
	}
}

func TestRunBridgeStartsBridgeRunner(t *testing.T) {
	cfg := cfgpkg.Config{}
	cfg.Bridge.Listen = "127.0.0.1:18081"
	cfg.Bridge.ServiceAddr = "/ip4/1.2.3.4/tcp/4001/p2p/peer"
	cfg.Node.Seed = "bridge-seed"
	cfg.Node.P2PListen = "/ip4/127.0.0.1/tcp/0"
	cfg.Network.RelayPeers = []string{"/ip4/1.2.3.4/tcp/4002/p2p/relay"}
	cfg.Network.Autorelay = true
	cfg.Network.HolePunching = true
	deps := &stubDeps{}
	if err := Run(context.Background(), deps, "bridge", "", cfg); err != nil {
		t.Fatal(err)
	}
	if deps.bridgeCfg.Listen != cfg.Bridge.Listen || deps.bridgeCfg.ServiceAddr != cfg.Bridge.ServiceAddr {
		t.Fatalf("unexpected bridge config: %#v", deps.bridgeCfg)
	}
	if !deps.bridgeRun.started {
		t.Fatal("expected bridge runner to start")
	}
}
