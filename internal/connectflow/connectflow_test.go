package connectflow

import (
	"context"
	"errors"
	"testing"
	"time"

	bridge "github.com/origama/tubo/internal/app/bridge"
	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
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

func TestResolveFallsBackToExactLookupAndBuildsBridge(t *testing.T) {
	scope := catalog.Scope{Cluster: "cluster-a", Namespace: "default"}
	cfg := cfgpkg.Config{}
	cfg.Node.Seed = "bridge-seed"
	cfg.Network.RelayPeers = []string{"/ip4/1.2.3.4/tcp/4001/p2p/peer"}
	service := catalog.Service{
		Name:             "svc",
		ServiceID:        "svc-1",
		DirectAddresses:  []string{"/ip4/10.0.0.9/tcp/4101/p2p/peer-a"},
		RelayedAddresses: []string{"/ip4/1.2.3.4/tcp/4001/p2p/relay/p2p-circuit/p2p/peer-a"},
	}
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
			return ShareTokenInfo{Cluster: scope.Cluster, Namespace: scope.Namespace, TargetServiceID: "svc-1", DisplayNameHint: "svc"}, nil
		},
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("not found by name")
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{Messages: []string{"using remote query"}}, service, nil
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
	if result.Path != "direct" {
		t.Fatalf("path = %q", result.Path)
	}
	if result.Direct != "selected" {
		t.Fatalf("direct = %q", result.Direct)
	}
	if result.Relay != "available as fallback" {
		t.Fatalf("relay = %q", result.Relay)
	}
	if len(result.Messages) != 1 || result.Messages[0] != "using remote query" {
		t.Fatalf("messages = %#v", result.Messages)
	}
	if selected.ServiceAddr != service.DirectAddresses[0] {
		t.Fatalf("selected service addr = %q", selected.ServiceAddr)
	}
}

func TestResolveBypassesAmbientDiscoveryScopeForTokenFlow(t *testing.T) {
	service := catalog.Service{
		Name:            "svc",
		ServiceID:       "svc-1",
		DirectAddresses: []string{"/ip4/10.0.0.9/tcp/4101/p2p/peer-a"},
	}
	deps := stubDeps{
		loadConfig: func(string) (cfgpkg.Config, error) { return cfgpkg.Config{}, nil },
		setupShare: func(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
			return serviceRef, "svc-1", catalog.Scope{Cluster: "home", Namespace: "default"}, nil
		},
		parseServiceRef: func(ref string) (string, error) { return ref, nil },
		isServiceID:     func(string) bool { return false },
		resolveScope:    func(cfgpkg.Config, string, string) (catalog.Scope, error) { return catalog.Scope{}, cfgpkg.ErrAmbientDiscoveryDisabled },
		parseShareToken: func(string) (ShareTokenInfo, error) {
			return ShareTokenInfo{Cluster: "home", Namespace: "default", TargetServiceID: "svc-1", DisplayNameHint: "svc"}, nil
		},
		ensureInvite:    func(string, ShareTokenInfo) error { return nil },
		importDiscovery: func(cfg cfgpkg.Config, _ ShareTokenInfo) (cfgpkg.Config, error) { return cfg, nil },
		markInvite:      func(string, ShareTokenInfo) error { return nil },
		discoverService: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, catalog.Service{}, errors.New("not found by name")
		},
		discoverServiceExact: func(cfgpkg.Config, time.Duration, bool, bool, catalog.Scope, string, string) (catalog.LookupResult, catalog.Service, error) {
			return catalog.LookupResult{}, service, nil
		},
		newBridge: func(context.Context, bridge.Config) (*bridge.App, error) { return &bridge.App{}, nil },
	}
	result, err := Resolve(context.Background(), deps, Request{ConfigPath: "/tmp/config.yaml", ServiceRef: "svc", Token: "token", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if result.ServiceID != "svc-1" || result.ServiceName != "svc" {
		t.Fatalf("unexpected result: %#v", result)
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
