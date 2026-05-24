package config

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/discovery"
)

func TestLoadYAMLAndValidateService(t *testing.T) {
	y := `role: service
node:
  seed: s
  p2p_listen: /ip4/127.0.0.1/tcp/1
network:
  bootstrap_peers:
  - /ip4/1.2.3.4/tcp/4001/p2p/12D3KooWQbVQpzQ1r1o1YtA4ePpMTE4mZZwB9sJYQ9kJjMJzZxYb
service:
  name: api
  target: http://127.0.0.1:9000
heartbeat_interval: 5s
`
	p := t.TempDir() + "/c.yaml"
	if err := osWrite(p, y); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Network.BootstrapPeers) != 1 {
		t.Fatalf("legacy bootstrap peers lost: %#v", c.Network.BootstrapPeers)
	}
	c = Merge(Defaults(c.Role), c)
	if c.HeartbeatInterval.Duration() != 5*time.Second {
		t.Fatalf("duration not parsed")
	}
	if err := Validate(c); err != nil {
		t.Fatal(err)
	}
}

func TestLoadLegacyNetworkOnlyStillProducesSameEffectiveNetwork(t *testing.T) {
	y := `role: service
network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3KooWLegacyBootstrap
  relay_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3KooWLegacyRelay
service:
  name: api
  target: http://127.0.0.1:9000
`
	p := t.TempDir() + "/legacy.yaml"
	if err := osWrite(p, y); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	effective := Merge(Defaults(c.Role), c)
	if effective.Network.PrivateKeyFile != "/etc/p2p/swarm.key" {
		t.Fatalf("private_key_file = %q", effective.Network.PrivateKeyFile)
	}
	if !reflect.DeepEqual(effective.Network.BootstrapPeers, []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWLegacyBootstrap"}) {
		t.Fatalf("bootstrap_peers = %#v", effective.Network.BootstrapPeers)
	}
	if !reflect.DeepEqual(effective.Network.RelayPeers, []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWLegacyRelay"}) {
		t.Fatalf("relay_peers = %#v", effective.Network.RelayPeers)
	}
}

func TestLoadOverlayMaterializesEffectiveNetwork(t *testing.T) {
	y := `role: service
current_overlay: public
current_cluster: home
current_namespace: default
overlays:
  public:
    relays:
      - /ip4/1.2.3.4/tcp/4001/p2p/12D3KooWOverlayRelay
    bootstrap_peers:
      - /ip4/1.2.3.4/tcp/4001/p2p/12D3KooWOverlayBootstrap
    swarm_key_file: /etc/p2p/swarm.key
clusters:
  home:
    cluster_id: ""
    authority_public_key: ""
    capabilities: []
    namespaces:
      default: {}
service:
  name: api
  target: http://127.0.0.1:9000
`
	p := t.TempDir() + "/overlay.yaml"
	if err := osWrite(p, y); err != nil {
		t.Fatal(err)
	}
	c, err := LoadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.CurrentOverlay != "public" || c.CurrentCluster != "home" || c.CurrentNamespace != "default" {
		t.Fatalf("current context not loaded: %#v", c)
	}
	if c.Network.PrivateKeyFile != "/etc/p2p/swarm.key" {
		t.Fatalf("private_key_file = %q", c.Network.PrivateKeyFile)
	}
	if !reflect.DeepEqual(c.Network.BootstrapPeers, []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWOverlayBootstrap"}) {
		t.Fatalf("bootstrap_peers = %#v", c.Network.BootstrapPeers)
	}
	if !reflect.DeepEqual(c.Network.RelayPeers, []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWOverlayRelay"}) {
		t.Fatalf("relay_peers = %#v", c.Network.RelayPeers)
	}
	if _, ok := c.Overlays["public"]; !ok {
		t.Fatalf("overlay missing: %#v", c.Overlays)
	}
	if _, ok := c.Clusters["home"].Namespaces["default"]; !ok {
		t.Fatalf("namespace missing: %#v", c.Clusters)
	}
}

func TestMergeCombinesResourceModelWithoutAliasing(t *testing.T) {
	base := Config{
		Role: "service",
		Network: Network{
			PrivateKeyFile: "/base/swarm.key",
			BootstrapPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/base-bootstrap"},
			RelayPeers:     []string{"/ip4/1.2.3.4/tcp/4001/p2p/base-relay"},
		},
		CurrentOverlay:   "public",
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Overlays: map[string]Overlay{
			"public": {
				Relays:         []string{"/ip4/1.2.3.4/tcp/4001/p2p/base-relay"},
				BootstrapPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/base-bootstrap"},
				SwarmKeyFile:   "/base/swarm.key",
			},
		},
		Clusters: map[string]Cluster{
			"home": {
				ClusterID:    "base-cluster",
				Capabilities: []string{"discovery"},
				Namespaces: map[string]Namespace{"default": {
					Services: map[string]NamespaceService{"myapi": {ServiceID: "svc-a", ServiceSeed: "seed-a", ServiceClaimFile: "/base/claim.json"}},
				}},
			},
		},
	}
	over := Config{
		CurrentOverlay:   "manual",
		CurrentCluster:   "ops",
		CurrentNamespace: "tenant-a",
		Overlays: map[string]Overlay{
			"manual": {
				Relays:         []string{"/ip4/5.6.7.8/tcp/4001/p2p/manual-relay"},
				BootstrapPeers: []string{"/ip4/5.6.7.8/tcp/4001/p2p/manual-bootstrap"},
				SwarmKeyFile:   "/manual/swarm.key",
			},
		},
		Clusters: map[string]Cluster{
			"ops": {
				ClusterID:          "ops-cluster",
				AuthorityPublicKey: "ops-key",
				Capabilities:       []string{"ingress"},
				Namespaces: map[string]Namespace{"tenant-a": {
					Services: map[string]NamespaceService{"myapi": {ServiceID: "svc-b", ServiceSeed: "seed-b", ServiceClaimFile: "/over/claim.json"}},
				}},
			},
		},
		Network: Network{PrivateKeyFile: "/manual/swarm.key"},
	}
	got := Merge(base, over)
	if got.CurrentOverlay != "manual" || got.CurrentCluster != "ops" || got.CurrentNamespace != "tenant-a" {
		t.Fatalf("current context = %#v", got)
	}
	if len(got.Overlays) != 2 {
		t.Fatalf("overlays = %#v", got.Overlays)
	}
	if len(got.Clusters) != 2 {
		t.Fatalf("clusters = %#v", got.Clusters)
	}
	if got.Network.PrivateKeyFile != "/manual/swarm.key" {
		t.Fatalf("network private_key_file = %q", got.Network.PrivateKeyFile)
	}
	if !reflect.DeepEqual(got.Network.BootstrapPeers, []string{"/ip4/1.2.3.4/tcp/4001/p2p/base-bootstrap"}) {
		t.Fatalf("bootstrap_peers = %#v", got.Network.BootstrapPeers)
	}
	if !reflect.DeepEqual(got.Network.RelayPeers, []string{"/ip4/1.2.3.4/tcp/4001/p2p/base-relay"}) {
		t.Fatalf("relay_peers = %#v", got.Network.RelayPeers)
	}
	base.Overlays["public"] = Overlay{Relays: []string{"mutated"}}
	over.Overlays["manual"] = Overlay{Relays: []string{"mutated"}}
	if got.Overlays["public"].Relays[0] != "/ip4/1.2.3.4/tcp/4001/p2p/base-relay" {
		t.Fatalf("base overlay aliased: %#v", got.Overlays["public"])
	}
	if got.Overlays["manual"].Relays[0] != "/ip4/5.6.7.8/tcp/4001/p2p/manual-relay" {
		t.Fatalf("over overlay aliased: %#v", got.Overlays["manual"])
	}
	base.Clusters["home"].Namespaces["default"].Services["myapi"] = NamespaceService{ServiceID: "mutated", ServiceSeed: "mutated", ServiceClaimFile: "mutated"}
	over.Clusters["ops"].Namespaces["tenant-a"].Services["myapi"] = NamespaceService{ServiceID: "mutated", ServiceSeed: "mutated", ServiceClaimFile: "mutated"}
	if got.Clusters["home"].Namespaces["default"].Services["myapi"].ServiceID != "svc-a" {
		t.Fatalf("base namespace service aliased: %#v", got.Clusters["home"].Namespaces["default"].Services["myapi"])
	}
	if got.Clusters["ops"].Namespaces["tenant-a"].Services["myapi"].ServiceID != "svc-b" {
		t.Fatalf("over namespace service aliased: %#v", got.Clusters["ops"].Namespaces["tenant-a"].Services["myapi"])
	}
}

func TestEnvCSVAndMerge(t *testing.T) {
	g := func(k string) string {
		m := map[string]string{"BOOTSTRAP_PEERS": "a, b,,c", "SERVICE_NAME": "svc"}
		return m[k]
	}
	c := Merge(Defaults("service"), Env(g, "service"))
	if len(c.Network.BootstrapPeers) != 3 {
		t.Fatalf("csv=%v", c.Network.BootstrapPeers)
	}
	if c.Service.Name != "svc" {
		t.Fatal(c.Service.Name)
	}
}

func TestDiscoveryRuntimeSelectsOpaqueNamespaceTopicForClusterMode(t *testing.T) {
	cfg := Config{
		CurrentCluster:   "home",
		CurrentNamespace: "tenant-a",
		Clusters: map[string]Cluster{
			"home": {
				ClusterID:                "cluster-123",
				AuthorityPublicKey:       "ssh-ed25519 AAAA",
				MembershipCapabilityFile: "/tmp/cap.json",
			},
		},
	}
	runtime := cfg.DiscoveryRuntime()
	if runtime.Mode != DiscoveryModeNamespaceV2 {
		t.Fatalf("mode = %q", runtime.Mode)
	}
	if runtime.Topic != discovery.NamespaceTopic("cluster-123", "tenant-a") {
		t.Fatalf("topic = %q", runtime.Topic)
	}
	if runtime.ClusterID != "cluster-123" || runtime.NamespaceID != "tenant-a" {
		t.Fatalf("runtime = %#v", runtime)
	}
	issuer, ok := cfg.ScopeIssuer("home", "tenant-a")
	if !ok || issuer.AuthorityPublicKey != "ssh-ed25519 AAAA" || issuer.ClusterName != "home" || issuer.NamespaceName != "tenant-a" {
		t.Fatalf("issuer = %#v ok=%t", issuer, ok)
	}
}

func TestDiscoveryRuntimeWithoutClusterIdentityReturnsZeroValue(t *testing.T) {
	cfg := Config{CurrentCluster: "home", CurrentNamespace: "default", Clusters: map[string]Cluster{"home": {Namespaces: map[string]Namespace{"default": {}}}}}
	runtime := cfg.DiscoveryRuntime()
	if runtime != (DiscoveryRuntime{}) {
		t.Fatalf("runtime = %#v", runtime)
	}
	if _, err := cfg.RequireDiscoveryRuntime(); err == nil {
		t.Fatal("expected discovery runtime requirement error")
	}
}

func TestResolveEffectiveScopePrefersExplicitOverridesAndCurrentContext(t *testing.T) {
	cfg := Config{CurrentOverlay: "tubo-public", CurrentCluster: "home", CurrentNamespace: "default"}
	scope, err := ResolveEffectiveScope(cfg, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Overlay != "tubo-public" || scope.Cluster != "home" || scope.Namespace != "default" || scope.AllNamespaces {
		t.Fatalf("scope = %#v", scope)
	}
	override, err := ResolveEffectiveScope(cfg, "ops", "metrics", false)
	if err != nil {
		t.Fatal(err)
	}
	if override.Overlay != "tubo-public" || override.Cluster != "ops" || override.Namespace != "metrics" {
		t.Fatalf("override = %#v", override)
	}
	all, err := ResolveEffectiveScope(cfg, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !all.AllNamespaces || all.Namespace != "" {
		t.Fatalf("all namespaces scope = %#v", all)
	}
	if _, err := ResolveEffectiveScope(Config{}, "", "metrics", false); err == nil {
		t.Fatal("expected missing cluster error")
	}
}

func TestIsPublicDefaultScopeUsesOverlayMetadataNotJustHomeDefaultNames(t *testing.T) {
	cfg := Config{
		CurrentOverlay:   "tubo-public",
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Overlays: map[string]Overlay{
			"tubo-public": {
				Kind:                   OverlayKindPublicBundle,
				PublicDefaultCluster:   "home",
				PublicDefaultNamespace: "default",
			},
			"manual": {
				PublicDefaultCluster:   "home",
				PublicDefaultNamespace: "default",
			},
		},
		Clusters: map[string]Cluster{
			"home": {Namespaces: map[string]Namespace{"default": {}}},
		},
	}
	publicScope, err := ResolveEffectiveScope(cfg, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !IsPublicDefaultScope(cfg, publicScope) {
		t.Fatalf("expected public default scope, got %#v", publicScope)
	}
	policy := EffectiveScopePolicy(cfg, publicScope)
	if !policy.PublicDefault || policy.Discovery != NamespaceDiscoveryDisabled || policy.ConnectPolicy != ConnectPolicyInviteOnly {
		t.Fatalf("expected invite-only public default policy, got %#v", policy)
	}
	customScope := Scope{Overlay: "tubo-public", Cluster: "team-origama", Namespace: "lab"}
	if IsPublicDefaultScope(cfg, customScope) {
		t.Fatalf("custom public-overlay scope must not be treated as public default: %#v", customScope)
	}
	privateScope := Scope{Overlay: "manual", Cluster: "home", Namespace: "default"}
	if IsPublicDefaultScope(cfg, privateScope) {
		t.Fatalf("private/manual home/default must not be treated as public default: %#v", privateScope)
	}
	if IsPublicDefaultScope(cfg, Scope{Overlay: "tubo-public", Cluster: "home", Namespace: "", AllNamespaces: true}) {
		t.Fatal("all-namespaces scope must not be treated as public default")
	}
	legacy := Config{CurrentOverlay: "tubo-public", CurrentCluster: "home", CurrentNamespace: "default", Overlays: map[string]Overlay{"tubo-public": {}}, Clusters: map[string]Cluster{"home": {Namespaces: map[string]Namespace{"default": {}}}}}
	legacyScope, err := ResolveEffectiveScope(legacy, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if IsPublicDefaultScope(legacy, legacyScope) {
		t.Fatalf("overlay without explicit public metadata must not be treated as public default: %#v", legacyScope)
	}
}

func TestEffectiveScopePolicyUsesNamespaceDefaultsOutsidePublicDefault(t *testing.T) {
	cfg := Config{
		CurrentOverlay:   "manual",
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Overlays:         map[string]Overlay{"manual": {}},
		Clusters: map[string]Cluster{
			"home": {Namespaces: map[string]Namespace{
				"default": {},
				"lab":     {Discovery: NamespaceDiscoveryDisabled, ConnectPolicy: ConnectPolicyPublic},
			}},
		},
	}
	defaultScope, err := ResolveEffectiveScope(cfg, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	policy := EffectiveScopePolicy(cfg, defaultScope)
	if policy.PublicDefault || policy.Discovery != NamespaceDiscoveryEnabled || policy.ConnectPolicy != ConnectPolicyNamespaceMember {
		t.Fatalf("unexpected default custom policy: %#v", policy)
	}
	labScope, err := ResolveEffectiveScope(cfg, "home", "lab", false)
	if err != nil {
		t.Fatal(err)
	}
	policy = EffectiveScopePolicy(cfg, labScope)
	if policy.PublicDefault || policy.Discovery != NamespaceDiscoveryDisabled || policy.ConnectPolicy != ConnectPolicyPublic {
		t.Fatalf("unexpected explicit namespace policy: %#v", policy)
	}
}

func TestValidateRejectsUnknownNamespacePolicies(t *testing.T) {
	cfg := Defaults("service")
	cfg.Clusters = map[string]Cluster{"home": {Namespaces: map[string]Namespace{"default": {Discovery: "bogus"}}}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "clusters.home.namespaces.default.discovery") {
		t.Fatalf("expected discovery validation error, got %v", err)
	}
	cfg.Clusters["home"] = Cluster{Namespaces: map[string]Namespace{"default": {ConnectPolicy: "bogus"}}}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "clusters.home.namespaces.default.connect_policy") {
		t.Fatalf("expected connect_policy validation error, got %v", err)
	}
}

func TestValidateRequired(t *testing.T) {
	c := Defaults("bridge")
	if err := Validate(c); err == nil || !strings.Contains(err.Error(), "service_addr") {
		t.Fatalf("err=%v", err)
	}
}
func TestMaskSecrets(t *testing.T) {
	c := Defaults("service")
	c.Network.PrivateKeyB64 = "secret"
	if Mask(c).Network.PrivateKeyB64 != "" {
		t.Fatal("secret not masked")
	}
}

func osWrite(p, s string) error { return os.WriteFile(p, []byte(s), 0600) }
