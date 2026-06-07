package connectflow

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	bridge "github.com/origama/tubo/internal/app/bridge"
	capability "github.com/origama/tubo/internal/capability"
	catalog "github.com/origama/tubo/internal/catalog"
	clusterinvite "github.com/origama/tubo/internal/clusterinvite"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"golang.org/x/crypto/ssh"
)

type stubDeps struct {
	loadConfig           func(string) (cfgpkg.Config, error)
	setupShare           func(string, string, string, string) (string, string, catalog.Scope, error)
	parseServiceRef      func(string) (string, error)
	isServiceID          func(string) bool
	resolveScope         func(cfgpkg.Config, string, string) (catalog.Scope, error)
	parseShareToken      func(string) (ShareTokenInfo, error)
	ensureInvite         func(string, ShareTokenInfo) error
	importDiscovery      func(cfgpkg.Config, ShareTokenInfo) (cfgpkg.Config, error)
	markInvite           func(string, ShareTokenInfo) error
	discoverService      func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error)
	discoverServiceExact func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error)
	newBridge            func(context.Context, bridge.Config) (*bridge.App, error)
}

func (s stubDeps) LoadConfig(path string) (cfgpkg.Config, error) { return s.loadConfig(path) }
func (s stubDeps) SetupShare(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
	return s.setupShare(serviceRef, token, cluster, namespace)
}
func (s stubDeps) ParseServiceRef(ref string) (string, error) { return s.parseServiceRef(ref) }
func (s stubDeps) IsServiceID(ref string) bool                { return s.isServiceID(ref) }
func (s stubDeps) ResolveScope(cfg cfgpkg.Config, cluster, namespace string) (catalog.Scope, error) {
	return s.resolveScope(cfg, cluster, namespace)
}
func (s stubDeps) ParseShareToken(token string) (ShareTokenInfo, error) {
	return s.parseShareToken(token)
}
func (s stubDeps) EnsureShareInviteAvailable(configDir string, token ShareTokenInfo) error {
	return s.ensureInvite(configDir, token)
}
func (s stubDeps) ImportShareDiscoveryContext(cfg cfgpkg.Config, token ShareTokenInfo) (cfgpkg.Config, error) {
	return s.importDiscovery(cfg, token)
}
func (s stubDeps) MarkShareInviteUsed(configDir string, token ShareTokenInfo) error {
	return s.markInvite(configDir, token)
}
func (s stubDeps) DiscoverService(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope catalog.Scope, serviceName string) (catalog.LookupResult, catalog.Service, error) {
	return s.discoverService(cfg, timeout, cachedOnly, live, scope, serviceName)
}
func (s stubDeps) DiscoverServiceExact(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope catalog.Scope, serviceName, serviceID string) (catalog.LookupResult, catalog.Service, error) {
	return s.discoverServiceExact(cfg, timeout, cachedOnly, live, scope, serviceName, serviceID)
}
func (s stubDeps) NewBridge(ctx context.Context, cfg bridge.Config) (*bridge.App, error) {
	return s.newBridge(ctx, cfg)
}

func TestConnectBridgeDoesNotReacquireLeaseForFailedCandidates(t *testing.T) {
	service := catalog.Service{
		Name:             "svc",
		ServiceID:        "svc-1",
		DirectAddresses:  []string{"/ip4/10.0.0.1/tcp/4101/p2p/peer-a", "/ip4/10.0.0.2/tcp/4101/p2p/peer-a"},
		RelayedAddresses: []string{"/ip4/1.2.3.4/tcp/4001/p2p/relay/p2p-circuit/p2p/peer-a"},
	}
	base := bridge.Config{ConnectAccessLease: &grantspkg.ConnectAccessLease{ServiceID: "svc-1"}, ConnectRefreshLease: &grantspkg.ConnectRefreshLease{ServiceID: "svc-1"}}
	var seen []bridge.Config
	app, err := func() (*bridge.App, error) {
		_, _, _, app, err := ConnectBridge(context.Background(), func(_ context.Context, cfg bridge.Config) (*bridge.App, error) {
			seen = append(seen, cfg)
			if len(seen) == 1 {
				return nil, errors.New("direct failed")
			}
			return &bridge.App{}, nil
		}, base, service)
		return app, err
	}()
	if err != nil {
		t.Fatal(err)
	}
	if app == nil {
		t.Fatal("expected app")
	}
	if len(seen) != 2 {
		t.Fatalf("attempts = %d", len(seen))
	}
	for i, cfg := range seen {
		if cfg.ConnectAccessLease == nil || cfg.ConnectRefreshLease == nil {
			t.Fatalf("attempt %d lost connect lease reuse: %#v", i, cfg)
		}
	}
}

func TestConnectResolvePassesAuthorityKeyForShareInvites(t *testing.T) {
	cfg := cfgpkg.Config{
		CurrentOverlay:   "tubo-public",
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Clusters: map[string]cfgpkg.Cluster{
			"home": {AuthorityPrivateKeyFile: "/work/clusters/home/authority.key", ClusterID: "cluster-123", Namespaces: map[string]cfgpkg.Namespace{"default": {}}},
		},
	}
	var got bridge.Config
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfg, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "svc-1", catalog.Scope{Cluster: "home", Namespace: "default"}, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope: func(cfgpkg.Config, string, string) (catalog.Scope, error) {
			return catalog.Scope{Cluster: "home", Namespace: "default"}, nil
		},
		parseShareToken: func(string) (ShareTokenInfo, error) {
			return ShareTokenInfo{Cluster: "home", Namespace: "default", TargetServiceID: "svc-1", ServiceKind: "http", ServiceEndpointPeer: "12D3KooWFake", ServiceEndpointAddrs: []string{"/ip4/1.2.3.4/tcp/1/p2p/12D3KooWFake"}, ConnectInviteToken: "token"}, nil
		},
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("unexpected discover")
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("unexpected discover exact")
		},
		newBridge: func(_ context.Context, bridgeCfg bridge.Config) (*bridge.App, error) {
			got = bridgeCfg
			return &bridge.App{}, nil
		},
	}
	if _, err := Resolve(context.Background(), deps, Request{ConfigPath: "/tmp/config.yaml", Token: "token", Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if got.ConnectAuthorityPrivateKeyFile == "" {
		t.Fatal("expected authority key file to be passed to bridge")
	}
}

func TestResolveBuildsBridgeFromSelfContainedServiceEndpoint(t *testing.T) {
	scope := catalog.Scope{Cluster: "cluster-a", Namespace: "default"}
	cfg := cfgpkg.Config{}
	cfg.Node.Seed = "bridge-seed"
	cfg.Network.RelayPeers = []string{"/ip4/1.2.3.4/tcp/4001/p2p/peer"}
	serviceAddr := "/ip4/10.0.0.9/tcp/4101/p2p/peer-a"
	var selected bridge.Config
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfg, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "svc-1", scope, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope:    func(cfgpkg.Config, string, string) (catalog.Scope, error) { return scope, nil },
		parseShareToken: func(string) (ShareTokenInfo, error) {
			return ShareTokenInfo{Cluster: scope.Cluster, Namespace: scope.Namespace, TargetServiceID: "svc-1", DisplayNameHint: "svc", ServiceKind: "http", ServiceEndpointPeer: "12D3KooWService", ServiceEndpointAddrs: []string{serviceAddr}}, nil
		},
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			t.Fatal("discoverService should not be called for self-contained token")
			return catalog.LookupResult{}, catalog.Service{}, nil
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			t.Fatal("discoverServiceExact should not be called for self-contained token")
			return catalog.LookupResult{}, catalog.Service{}, nil
		},
		newBridge: func(_ context.Context, bridgeCfg bridge.Config) (*bridge.App, error) {
			selected = bridgeCfg
			return &bridge.App{}, nil
		},
	}
	result, err := Resolve(context.Background(), deps, Request{ConfigPath: "/tmp/config.yaml", ServiceRef: "svc", Token: "token", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceName != "svc" || result.ServiceID != "svc-1" {
		t.Fatalf("unexpected result identity: %#v", result)
	}
	if result.Path != "direct" || result.SelectedAddr != serviceAddr {
		t.Fatalf("unexpected result: %#v", result)
	}
	if selected.ServiceAddr != serviceAddr || selected.SelectedAddr != serviceAddr {
		t.Fatalf("selected service addr = %#v", selected)
	}
}

func TestResolveConfiguresPinnedServiceRebindResolver(t *testing.T) {
	initialHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "connect-rebind-initial", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer initialHost.Close()
	refreshedHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "connect-rebind-refreshed", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer refreshedHost.Close()
	scope := catalog.Scope{Cluster: "cluster-a", Namespace: "default"}
	cfg := cfgpkg.Config{}
	cfg.Node.Seed = "bridge-seed"
	initialAddr := "/ip4/10.0.0.9/tcp/4101/p2p/" + initialHost.ID().String()
	refreshedAddr := "/ip4/10.0.0.10/tcp/4101/p2p/" + refreshedHost.ID().String()
	initial := catalog.Service{Name: "svc", ServiceID: "svc-1", PeerID: initialHost.ID().String(), ServiceKind: "tcp", DirectAddresses: []string{initialAddr}}
	refreshed := catalog.Service{Name: "svc", ServiceID: "svc-1", PeerID: refreshedHost.ID().String(), ServiceKind: "tcp", DirectAddresses: []string{refreshedAddr}}
	var resolver func(context.Context) (peer.AddrInfo, string, string, error)
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfg, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "svc-1", scope, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope:    func(cfgpkg.Config, string, string) (catalog.Scope, error) { return scope, nil },
		parseShareToken: func(string) (ShareTokenInfo, error) { return ShareTokenInfo{}, errors.New("not a share token") },
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, initial, nil
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, refreshed, nil
		},
		newBridge: func(_ context.Context, bridgeCfg bridge.Config) (*bridge.App, error) {
			resolver = bridgeCfg.ConnectRebindResolver
			if bridgeCfg.SelectedAddr == "" {
				t.Fatal("missing selected addr")
			}
			if bridgeCfg.SelectedPath == "" {
				t.Fatal("missing selected path")
			}
			return &bridge.App{}, nil
		},
	}
	result, err := Resolve(context.Background(), deps, Request{ConfigPath: "/tmp/config.yaml", ServiceRef: "svc", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if resolver == nil {
		t.Fatal("expected rebind resolver")
	}
	info, addr, path, err := resolver(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.ID.String() != refreshed.PeerID {
		t.Fatalf("rebinding peer = %s, want %s", info.ID, refreshed.PeerID)
	}
	if addr != refreshedAddr || path != "direct" {
		t.Fatalf("rebinding addr/path = %s/%s", addr, path)
	}
	if result.ServiceID != "svc-1" || result.SelectedAddr == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolveUsesSelfContainedTokenEndpointWithoutDiscovery(t *testing.T) {
	serviceAddr := "/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService"
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfgpkg.Config{}, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "svc-1", catalog.Scope{Cluster: "home", Namespace: "default"}, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope: func(cfgpkg.Config, string, string) (catalog.Scope, error) {
			return catalog.Scope{}, cfgpkg.ErrAmbientDiscoveryDisabled
		},
		parseShareToken: func(string) (ShareTokenInfo, error) {
			return ShareTokenInfo{Cluster: "home", Namespace: "default", TargetServiceID: "svc-1", DisplayNameHint: "svc", ServiceEndpointPeer: "12D3KooWService", ServiceEndpointAddrs: []string{serviceAddr}}, nil
		},
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			t.Fatal("discoverService should not be called for self-contained token")
			return catalog.LookupResult{}, catalog.Service{}, nil
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			t.Fatal("discoverServiceExact should not be called for self-contained token")
			return catalog.LookupResult{}, catalog.Service{}, nil
		},
		newBridge: func(context.Context, bridge.Config) (*bridge.App, error) { return &bridge.App{}, nil },
	}
	result, err := Resolve(context.Background(), deps, Request{ConfigPath: "/tmp/config.yaml", ServiceRef: "svc", Token: "token", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceID != "svc-1" || result.ServiceName != "svc" || result.SelectedAddr != serviceAddr {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolveUsesTCPServiceKindFromSelfContainedToken(t *testing.T) {
	serviceAddr := "/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService"
	var selected bridge.Config
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfgpkg.Config{}, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "svc-1", catalog.Scope{Cluster: "home", Namespace: "default"}, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope: func(cfgpkg.Config, string, string) (catalog.Scope, error) {
			return catalog.Scope{}, cfgpkg.ErrAmbientDiscoveryDisabled
		},
		parseShareToken: func(string) (ShareTokenInfo, error) {
			return ShareTokenInfo{Cluster: "home", Namespace: "default", TargetServiceID: "svc-1", DisplayNameHint: "svc", ServiceKind: "tcp", ServiceEndpointPeer: "12D3KooWService", ServiceEndpointAddrs: []string{serviceAddr}}, nil
		},
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			t.Fatal("discoverService should not be called for self-contained token")
			return catalog.LookupResult{}, catalog.Service{}, nil
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			t.Fatal("discoverServiceExact should not be called for self-contained token")
			return catalog.LookupResult{}, catalog.Service{}, nil
		},
		newBridge: func(_ context.Context, cfg bridge.Config) (*bridge.App, error) {
			selected = cfg
			return &bridge.App{}, nil
		},
	}
	result, err := Resolve(context.Background(), deps, Request{ConfigPath: "/tmp/config.yaml", ServiceRef: "svc", Token: "token", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceKind != "tcp" || !strings.HasPrefix(result.LocalURL, "tcp://") {
		t.Fatalf("unexpected tcp result: %#v", result)
	}
	if selected.ServiceKind != "tcp" {
		t.Fatalf("bridge service kind = %q", selected.ServiceKind)
	}
}

func TestResolveBuildsBridgeFromTokenEndpointWithoutAmbientDiscovery(t *testing.T) {
	serviceAddr := "/ip4/10.0.0.9/tcp/4101/p2p/peer-a"
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfgpkg.Config{}, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "svc-1", catalog.Scope{Cluster: "home", Namespace: "default"}, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope: func(cfgpkg.Config, string, string) (catalog.Scope, error) {
			return catalog.Scope{}, cfgpkg.ErrAmbientDiscoveryDisabled
		},
		parseShareToken: func(string) (ShareTokenInfo, error) {
			return ShareTokenInfo{Cluster: "home", Namespace: "default", TargetServiceID: "svc-1", DisplayNameHint: "svc", ServiceKind: "http", ServiceEndpointPeer: "12D3KooWService", ServiceEndpointAddrs: []string{serviceAddr}}, nil
		},
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			t.Fatal("discoverService should not be called for self-contained token")
			return catalog.LookupResult{}, catalog.Service{}, nil
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			t.Fatal("discoverServiceExact should not be called for self-contained token")
			return catalog.LookupResult{}, catalog.Service{}, nil
		},
		newBridge: func(context.Context, bridge.Config) (*bridge.App, error) { return &bridge.App{}, nil },
	}
	result, err := Resolve(context.Background(), deps, Request{ConfigPath: "/tmp/config.yaml", ServiceRef: "svc", Token: "token", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceID != "svc-1" || result.ServiceName != "svc" || result.SelectedAddr != serviceAddr {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestResolveRejectsLegacyTokenWithoutEndpointInPublicDefault(t *testing.T) {
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) {
			return cfgpkg.Config{CurrentOverlay: "tubo-public", Overlays: map[string]cfgpkg.Overlay{"tubo-public": {Kind: cfgpkg.OverlayKindPublicBundle, PublicDefaultCluster: "home", PublicDefaultNamespace: "default"}}}, nil
		},
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "svc-1", catalog.Scope{Cluster: "home", Namespace: "default"}, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope:    func(cfgpkg.Config, string, string) (catalog.Scope, error) { return catalog.Scope{}, nil },
		parseShareToken: func(string) (ShareTokenInfo, error) {
			return ShareTokenInfo{Cluster: "home", Namespace: "default", TargetServiceID: "svc-1", DisplayNameHint: "svc"}, nil
		},
		ensureInvite: func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) {
			cfg.CurrentCluster = "home"
			cfg.CurrentNamespace = "default"
			return cfg, nil
		},
		markInvite: func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("should not discover")
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("should not discover")
		},
		newBridge: func(context.Context, bridge.Config) (*bridge.App, error) { return &bridge.App{}, nil },
	}
	_, err := Resolve(context.Background(), deps, Request{ConfigPath: "/tmp/config.yaml", ServiceRef: "svc", Token: "token", Timeout: time.Second})
	if err == nil || !strings.Contains(err.Error(), "missing a self-contained service endpoint") {
		t.Fatalf("expected compatibility error, got %v", err)
	}
}

func TestResolveConfiguresDiscoveryGrantEndpointConnectFlow(t *testing.T) {
	tmp := t.TempDir()
	capPath := tmp + "/membership.cap.json"
	membership := capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: "cluster-123", Permissions: []string{capability.PermissionConnect}}
	b, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	scope := catalog.Scope{Cluster: "home", Namespace: "default"}
	cfg := cfgpkg.Config{Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123", MembershipCapabilityFile: capPath, Namespaces: map[string]cfgpkg.Namespace{"default": {MembershipCapabilityFile: capPath}}}}}
	service := catalog.Service{Name: "svc", ServiceID: "svc-1", GrantService: &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/grant"}}, DirectAddresses: []string{"/ip4/10.0.0.9/tcp/4101/p2p/peer-a"}}
	var selected bridge.Config
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfg, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "", scope, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope:    func(cfgpkg.Config, string, string) (catalog.Scope, error) { return scope, nil },
		parseShareToken: func(string) (ShareTokenInfo, error) { return ShareTokenInfo{}, nil },
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("unused")
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, service, nil
		},
		newBridge: func(_ context.Context, bridgeCfg bridge.Config) (*bridge.App, error) {
			selected = bridgeCfg
			return &bridge.App{}, nil
		},
	}
	if _, err := Resolve(context.Background(), deps, Request{ConfigPath: tmp + "/config.yaml", ServiceRef: "svc", Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if selected.ConnectClusterID != "cluster-123" || selected.ConnectNamespaceID != "default" || selected.ConnectServiceID != "svc-1" {
		t.Fatalf("unexpected direct connect scope: %#v", selected)
	}
	if len(selected.ConnectGrantPeers) != 1 || selected.ConnectGrantPeers[0] != service.GrantService.Peers[0] {
		t.Fatalf("unexpected grant peers: %#v", selected.ConnectGrantPeers)
	}
	if selected.ConnectMembershipCapability == nil || len(selected.ConnectMembershipCapability.Permissions) != 1 || selected.ConnectMembershipCapability.Permissions[0] != capability.PermissionConnect {
		t.Fatalf("unexpected membership capability: %#v", selected.ConnectMembershipCapability)
	}
}

func TestResolveFallsBackToMembershipGrantTokenForDiscoveryConnect(t *testing.T) {
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSSH, err := ssh.NewPublicKey(authPub)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := clusterinvite.GrantForRole(clusterinvite.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	discoverySecret, err := cfgpkg.GenerateSecretBytes(cfgpkg.NamespaceDiscoverySecretLength)
	if err != nil {
		t.Fatal(err)
	}
	invitePayload := clusterinvite.Payload{Version: clusterinvite.Version, Kind: clusterinvite.Kind, JTI: "join-1", ClusterName: "home", ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authSSH))), Namespace: "default", Discovery: &clusterinvite.NamespaceDiscoveryEntry{Version: "v1", Type: cfgpkg.SecretTypeNamespaceDiscovery, KeyID: "nsdk_join", Secret: base64.RawURLEncoding.EncodeToString(discoverySecret), CreatedAt: time.Now().UTC()}, Grant: grant, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	token, err := clusterinvite.SignToken(invitePayload, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	scope := catalog.Scope{Cluster: "home", Namespace: "default"}
	cfg := cfgpkg.Config{Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123", MembershipGrant: &cfgpkg.ClusterMembershipGrant{InviteToken: token, ClusterName: "home", ClusterID: "cluster-123", Namespace: "default", Permissions: append([]string(nil), grant.Permissions...), ExpiresAt: time.Now().Add(time.Hour)}, Namespaces: map[string]cfgpkg.Namespace{"default": {}}}}}
	service := catalog.Service{Name: "svc", ServiceID: "svc-1", GrantService: &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/grant"}}, DirectAddresses: []string{"/ip4/10.0.0.9/tcp/4101/p2p/peer-a"}}
	var selected bridge.Config
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfg, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "", scope, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope:    func(cfgpkg.Config, string, string) (catalog.Scope, error) { return scope, nil },
		parseShareToken: func(string) (ShareTokenInfo, error) { return ShareTokenInfo{}, nil },
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("unused")
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, service, nil
		},
		newBridge: func(_ context.Context, bridgeCfg bridge.Config) (*bridge.App, error) {
			selected = bridgeCfg
			return &bridge.App{}, nil
		},
	}
	if _, err := Resolve(context.Background(), deps, Request{ConfigPath: t.TempDir() + "/config.yaml", ServiceRef: "svc", Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if selected.ConnectMembershipCapability != nil {
		t.Fatalf("expected membership grant token path, got capability %#v", selected.ConnectMembershipCapability)
	}
	if selected.ConnectMembershipGrantToken != token {
		t.Fatalf("unexpected membership grant token: %q", selected.ConnectMembershipGrantToken)
	}
}

func TestResolveLoadsMembershipGrantTokenFromFileForDiscoveryConnect(t *testing.T) {
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSSH, err := ssh.NewPublicKey(authPub)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := clusterinvite.GrantForRole(clusterinvite.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	invitePayload := clusterinvite.Payload{Version: clusterinvite.Version, Kind: clusterinvite.MembershipGrantKind, JTI: "join-file-1", ClusterName: "home", ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authSSH))), Namespace: "default", Grant: grant, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	token, err := clusterinvite.SignToken(invitePayload, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	tokenPath := tmp + "/membership-grant.token"
	if err := os.WriteFile(tokenPath, []byte(token+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scope := catalog.Scope{Cluster: "home", Namespace: "default"}
	cfg := cfgpkg.Config{Clusters: map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123", MembershipGrant: &cfgpkg.ClusterMembershipGrant{InviteTokenFile: tokenPath, ClusterName: "home", ClusterID: "cluster-123", Namespace: "default", Permissions: append([]string(nil), grant.Permissions...), ExpiresAt: time.Now().Add(time.Hour)}, Namespaces: map[string]cfgpkg.Namespace{"default": {}}}}}
	service := catalog.Service{Name: "svc", ServiceID: "svc-1", GrantService: &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/grant"}}, DirectAddresses: []string{"/ip4/10.0.0.9/tcp/4101/p2p/peer-a"}}
	var selected bridge.Config
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfg, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "", scope, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope:    func(cfgpkg.Config, string, string) (catalog.Scope, error) { return scope, nil },
		parseShareToken: func(string) (ShareTokenInfo, error) { return ShareTokenInfo{}, nil },
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("unused")
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, service, nil
		},
		newBridge: func(_ context.Context, bridgeCfg bridge.Config) (*bridge.App, error) {
			selected = bridgeCfg
			return &bridge.App{}, nil
		},
	}
	if _, err := Resolve(context.Background(), deps, Request{ConfigPath: tmp + "/config.yaml", ServiceRef: "svc", Timeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	if selected.ConnectMembershipGrantToken != token {
		t.Fatalf("unexpected membership grant token from file: %q", selected.ConnectMembershipGrantToken)
	}
}

func TestConnectCandidatesAndMessages(t *testing.T) {
	service := catalog.Service{
		Name:             "svc",
		DirectAddresses:  []string{"/ip4/127.0.0.1/tcp/4001/p2p/local", "/ip4/10.0.0.9/tcp/4101/p2p/peer-a"},
		RelayedAddresses: []string{"/ip4/1.2.3.4/tcp/4001/p2p/relay/p2p-circuit/p2p/peer-a"},
	}
	candidates, err := ConnectCandidates(service)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 || candidates[0].Path != "direct" || candidates[1].Path != "relayed" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
	attempts := []Attempt{{Path: "direct", Addr: service.DirectAddresses[1], Status: "failed", Error: "timeout"}, {Path: "relayed", Addr: service.RelayedAddresses[0], Status: "selected"}}
	if got := ConnectDirectMessage(service, attempts, "relayed"); got != "attempted, failed; relay selected and hole punching may still upgrade later" {
		t.Fatalf("direct message = %q", got)
	}
	if got := ConnectRelayMessage(service, service.RelayedAddresses[0], "relayed"); got != service.RelayedAddresses[0] {
		t.Fatalf("relay message = %q", got)
	}
}
