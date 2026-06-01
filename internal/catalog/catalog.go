package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
)

const DefaultTimeout = 20 * time.Second

func DiscoverServices(configPath string, timeout time.Duration, cachedOnly, live bool, scope Scope) (LookupResult, error) {
	cfg, err := LoadDiscoveryConfig(configPath)
	if err != nil {
		return LookupResult{}, err
	}
	if err := cfgpkg.RequireAmbientDiscoveryScope(cfg, cfgpkg.Scope{Overlay: cfg.CurrentOverlay, Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}); err != nil {
		return LookupResult{}, err
	}
	if _, err := cfg.RequireDiscoveryRuntime(); err != nil {
		return LookupResult{}, err
	}
	return DiscoverServicesWithConfig(cfg, timeout, cachedOnly, live, scope)
}

func DiscoverServicesWithConfig(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope Scope) (LookupResult, error) {
	if err := cfgpkg.RequireAmbientDiscoveryScope(cfg, cfgpkg.Scope{Overlay: cfg.CurrentOverlay, Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}); err != nil {
		return LookupResult{}, err
	}
	if _, err := cfg.RequireDiscoveryRuntime(); err != nil {
		return LookupResult{}, err
	}
	if !live {
		if services, adminAddr, err := FetchLocalServiceCache(cfg); err == nil {
			services = applyScopeToServices(services, scope)
			return LookupResult{Services: services, Messages: []string{fmt.Sprintf("using local cache from edge admin at %s", adminAddr)}, Mode: "cache", Scope: scopePtr(scope)}, nil
		}
		if cachedOnly {
			return LookupResult{}, errors.New("no local cache found")
		}
		if services, metadata, messages, err := FetchRemoteServiceCache(cfg, timeout); err == nil {
			messages = append([]string{"no local cache found"}, messages...)
			services = applyScopeToServices(services, scope)
			if len(services) > 0 {
				return LookupResult{Services: services, Messages: messages, Mode: "remote-query", Scope: scopePtr(scope), Metadata: metadata}, nil
			}
			services, obsErr := ObserveServices(cfg, timeout, nil)
			if obsErr != nil {
				return LookupResult{}, obsErr
			}
			services = applyScopeToServices(services, scope)
			messages = append(messages, fmt.Sprintf("starting temporary observer for %s...", timeout.String()))
			return LookupResult{Services: services, Messages: messages, Mode: "live", Scope: scopePtr(scope)}, nil
		} else {
			messages := []string{"no local cache found", fmt.Sprintf("remote discovery query failed: %v", err)}
			services, obsErr := ObserveServices(cfg, timeout, nil)
			if obsErr != nil {
				return LookupResult{}, obsErr
			}
			services = applyScopeToServices(services, scope)
			messages = append(messages, fmt.Sprintf("starting temporary observer for %s...", timeout.String()))
			return LookupResult{Services: services, Messages: messages, Mode: "live", Scope: scopePtr(scope)}, nil
		}
	}
	services, err := ObserveServices(cfg, timeout, nil)
	if err != nil {
		return LookupResult{}, err
	}
	services = applyScopeToServices(services, scope)
	messages := []string{fmt.Sprintf("starting temporary observer for %s...", timeout.String())}
	if !live {
		messages = append([]string{"no local cache found"}, messages...)
	}
	return LookupResult{Services: services, Messages: messages, Mode: "live", Scope: scopePtr(scope)}, nil
}

func DiscoverService(configPath, serviceName string, timeout time.Duration, cachedOnly, live bool, scope Scope) (LookupResult, Service, error) {
	cfg, err := LoadDiscoveryConfig(configPath)
	if err != nil {
		return LookupResult{}, Service{}, err
	}
	if err := cfgpkg.RequireAmbientDiscoveryScope(cfg, cfgpkg.Scope{Overlay: cfg.CurrentOverlay, Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}); err != nil {
		return LookupResult{}, Service{}, err
	}
	if _, err := cfg.RequireDiscoveryRuntime(); err != nil {
		return LookupResult{}, Service{}, err
	}
	return DiscoverServiceWithConfig(cfg, timeout, cachedOnly, live, scope, serviceName)
}

func DiscoverServiceWithConfig(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope Scope, serviceName string) (LookupResult, Service, error) {
	if err := cfgpkg.RequireAmbientDiscoveryScope(cfg, cfgpkg.Scope{Overlay: cfg.CurrentOverlay, Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}); err != nil {
		return LookupResult{}, Service{}, err
	}
	if _, err := cfg.RequireDiscoveryRuntime(); err != nil {
		return LookupResult{}, Service{}, err
	}
	if !live {
		if services, adminAddr, err := FetchLocalServiceCache(cfg); err == nil {
			service, err := RequireService(services, serviceName)
			if err == nil {
				service = applyScope(service, scope)
				return LookupResult{Services: []Service{service}, Messages: []string{fmt.Sprintf("using local cache from edge admin at %s", adminAddr)}, Mode: "cache", Scope: scopePtr(scope)}, service, nil
			}
			if IsAmbiguousServiceError(err) {
				return LookupResult{}, Service{}, err
			}
		}
		if cachedOnly {
			return LookupResult{}, Service{}, errors.New("no local cache found")
		}
		if services, metadata, messages, err := FetchRemoteServiceCache(cfg, timeout); err == nil {
			service, err := RequireService(services, serviceName)
			if err != nil {
				if IsAmbiguousServiceError(err) {
					return LookupResult{}, Service{}, err
				}
			} else {
				messages = append([]string{"no local cache found"}, messages...)
				messages = append(messages, fmt.Sprintf("received service %s", service.Name))
				service = applyScope(service, scope)
				return LookupResult{Services: []Service{service}, Messages: messages, Mode: "remote-query", Scope: scopePtr(scope), Metadata: metadata}, service, nil
			}
		} else {
			messages := []string{"no local cache found", fmt.Sprintf("remote discovery query failed: %v", err)}
			services, obsErr := ObserveServices(cfg, timeout, nil)
			if obsErr != nil {
				return LookupResult{}, Service{}, obsErr
			}
			service, obsErr := RequireService(services, serviceName)
			if obsErr != nil {
				return LookupResult{}, Service{}, obsErr
			}
			messages = append(messages, fmt.Sprintf("starting temporary observer for %s...", timeout.String()))
			service = applyScope(service, scope)
			return LookupResult{Services: []Service{service}, Messages: messages, Mode: "live", Scope: scopePtr(scope)}, service, nil
		}
	}
	services, err := ObserveServices(cfg, timeout, nil)
	if err != nil {
		return LookupResult{}, Service{}, err
	}
	service, err := RequireService(services, serviceName)
	if err != nil {
		return LookupResult{}, Service{}, err
	}
	messages := []string{fmt.Sprintf("starting temporary observer for %s...", timeout.String())}
	if !live {
		messages = append([]string{"no local cache found"}, messages...)
	}
	service = applyScope(service, scope)
	return LookupResult{Services: []Service{service}, Messages: messages, Mode: "live", Scope: scopePtr(scope)}, service, nil
}

func DiscoverServiceExactWithConfig(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope Scope, serviceName, serviceID string) (LookupResult, Service, error) {
	if err := cfgpkg.RequireAmbientDiscoveryScope(cfg, cfgpkg.Scope{Overlay: cfg.CurrentOverlay, Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}); err != nil {
		return LookupResult{}, Service{}, err
	}
	if _, err := cfg.RequireDiscoveryRuntime(); err != nil {
		return LookupResult{}, Service{}, err
	}
	if serviceID == "" {
		return DiscoverServiceWithConfig(cfg, timeout, cachedOnly, live, scope, serviceName)
	}
	if !live {
		if services, adminAddr, err := FetchLocalServiceCache(cfg); err == nil {
			service, err := RequireServiceByID(services, serviceID)
			if err == nil {
				service = applyScope(service, scope)
				return LookupResult{Services: []Service{service}, Messages: []string{fmt.Sprintf("using local cache from edge admin at %s", adminAddr)}, Mode: "cache", Scope: scopePtr(scope)}, service, nil
			}
		}
		if cachedOnly {
			return LookupResult{}, Service{}, errors.New("no local cache found")
		}
		if services, metadata, messages, err := FetchRemoteServiceCache(cfg, timeout); err == nil {
			service, err := RequireServiceByID(services, serviceID)
			if err == nil {
				messages = append([]string{"no local cache found"}, messages...)
				messages = append(messages, fmt.Sprintf("received service %s", service.Name))
				service = applyScope(service, scope)
				return LookupResult{Services: []Service{service}, Messages: messages, Mode: "remote-query", Scope: scopePtr(scope), Metadata: metadata}, service, nil
			}
		} else {
			messages := []string{"no local cache found", fmt.Sprintf("remote discovery query failed: %v", err)}
			services, obsErr := ObserveServices(cfg, timeout, nil)
			if obsErr != nil {
				return LookupResult{}, Service{}, obsErr
			}
			service, obsErr := RequireServiceByID(services, serviceID)
			if obsErr != nil {
				return LookupResult{}, Service{}, obsErr
			}
			messages = append(messages, fmt.Sprintf("starting temporary observer for %s...", timeout.String()))
			service = applyScope(service, scope)
			return LookupResult{Services: []Service{service}, Messages: messages, Mode: "live", Scope: scopePtr(scope)}, service, nil
		}
	}
	if serviceName != "" {
		fallbackResult, fallbackService, fallbackErr := DiscoverServiceWithConfig(cfg, timeout, cachedOnly, live, scope, serviceName)
		if fallbackErr == nil {
			if fallbackService.ServiceID != "" && fallbackService.ServiceID != serviceID {
				return LookupResult{}, Service{}, fmt.Errorf("service share is for service_id %q, not %q", serviceID, fallbackService.ServiceID)
			}
			return fallbackResult, fallbackService, nil
		}
		if IsAmbiguousServiceError(fallbackErr) {
			return LookupResult{}, Service{}, fallbackErr
		}
	}
	services, err := ObserveServices(cfg, timeout, nil)
	if err != nil {
		return LookupResult{}, Service{}, err
	}
	service, err := RequireServiceByID(services, serviceID)
	if err != nil {
		if serviceName != "" {
			fallbackResult, fallbackService, fallbackErr := DiscoverServiceWithConfig(cfg, timeout, cachedOnly, live, scope, serviceName)
			if fallbackErr == nil {
				if fallbackService.ServiceID != "" && fallbackService.ServiceID != serviceID {
					return LookupResult{}, Service{}, fmt.Errorf("service share is for service_id %q, not %q", serviceID, fallbackService.ServiceID)
				}
				return fallbackResult, fallbackService, nil
			}
		}
		return LookupResult{}, Service{}, err
	}
	messages := []string{fmt.Sprintf("starting temporary observer for %s...", timeout.String())}
	if !live {
		messages = append([]string{"no local cache found"}, messages...)
	}
	messages = append(messages, fmt.Sprintf("received service %s", service.Name))
	service = applyScope(service, scope)
	if serviceName != "" && service.ServiceID != "" && service.ServiceID != serviceID {
		return LookupResult{}, Service{}, fmt.Errorf("service share is for service_id %q, not %q", serviceID, service.ServiceID)
	}
	return LookupResult{Services: []Service{service}, Messages: messages, Mode: "live", Scope: scopePtr(scope)}, service, nil
}

func LoadDiscoveryConfig(path string) (cfgpkg.Config, error) {
	cfg, err := cfgpkg.LoadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfgpkg.Config{}, fmt.Errorf("config not found at %s; run `tubo join --relay ... --swarm-key ...` first or pass --config", path)
		}
		return cfgpkg.Config{}, err
	}
	if cfg.Network.PrivateKeyFile == "" && cfg.Network.PrivateKeyB64 == "" {
		return cfgpkg.Config{}, errors.New("config is missing swarm key settings; run `tubo join --relay ... --swarm-key ...` first")
	}
	if len(cfg.Network.BootstrapPeers) == 0 && len(cfg.Network.RelayPeers) == 0 {
		return cfgpkg.Config{}, errors.New("config is missing relay/bootstrap peers; run `tubo join --relay ... --swarm-key ...` first")
	}
	return cfg, nil
}

func FetchLocalServiceCache(cfg cfgpkg.Config) ([]Service, string, error) {
	edgeCfg := cfgpkg.Merge(cfgpkg.Defaults("edge"), cfg)
	adminAddr := edgeCfg.Edge.AdminListen
	if adminAddr == "" {
		return nil, "", errors.New("edge admin listen address is not configured")
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Get("http://" + hostPortForHTTP(adminAddr) + "/services")
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("edge admin status %d", resp.StatusCode)
	}
	var payload AdminResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, "", err
	}
	if payload.Items == nil {
		return nil, "", errors.New("edge admin did not return service details")
	}
	for i := range payload.Items {
		payload.Items[i] = NormalizeService(payload.Items[i])
	}
	SortServices(payload.Items)
	return payload.Items, adminAddr, nil
}

func FetchRemoteServiceCache(cfg cfgpkg.Config, timeout time.Duration) ([]Service, *discoveryquery.Metadata, []string, error) {
	peers := uniqueStrings(append(append([]string{}, cfg.Network.BootstrapPeers...), cfg.Network.RelayPeers...))
	if len(peers) == 0 {
		return nil, nil, nil, errors.New("no bootstrap or relay peers configured")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load private network key: %w", err)
	}
	h, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "", psk)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create remote query host: %w", err)
	}
	defer h.Close()
	var lastErr error
	for _, raw := range peers {
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			lastErr = fmt.Errorf("invalid bootstrap peer %q: %w", raw, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		resp, err := discoveryquery.ListServices(ctx, h, info)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Error != "" {
			lastErr = errors.New(resp.Error)
			continue
		}
		services := make([]Service, 0, len(resp.Services))
		for _, service := range resp.Services {
			services = append(services, ServiceFromQueryService(service))
		}
		SortServices(services)
		metadata := resp.Metadata
		messages := []string{fmt.Sprintf("querying discovery cache from %s %s", metadata.ServedByRole, metadata.ServedBy), fmt.Sprintf("received %d services", len(services))}
		return services, &metadata, messages, nil
	}
	if lastErr == nil {
		lastErr = errors.New("remote discovery query failed")
	}
	return nil, nil, nil, lastErr
}

func FetchRemoteService(cfg cfgpkg.Config, serviceName string, timeout time.Duration) (Service, *discoveryquery.Metadata, []string, error) {
	peers := uniqueStrings(append(append([]string{}, cfg.Network.BootstrapPeers...), cfg.Network.RelayPeers...))
	if len(peers) == 0 {
		return Service{}, nil, nil, errors.New("no bootstrap or relay peers configured")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return Service{}, nil, nil, fmt.Errorf("load private network key: %w", err)
	}
	h, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "", psk)
	if err != nil {
		return Service{}, nil, nil, fmt.Errorf("create remote query host: %w", err)
	}
	defer h.Close()
	var lastErr error
	for _, raw := range peers {
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			lastErr = fmt.Errorf("invalid bootstrap peer %q: %w", raw, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		resp, err := discoveryquery.GetService(ctx, h, info, serviceName)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.Error != "" {
			lastErr = errors.New(resp.Error)
			continue
		}
		if resp.Service == nil {
			lastErr = errors.New("service not found")
			continue
		}
		service := ServiceFromQueryService(*resp.Service)
		metadata := resp.Metadata
		messages := []string{fmt.Sprintf("querying discovery cache from %s %s", metadata.ServedByRole, metadata.ServedBy), fmt.Sprintf("received service %s", service.Name)}
		return service, &metadata, messages, nil
	}
	if lastErr == nil {
		lastErr = errors.New("remote discovery query failed")
	}
	return Service{}, nil, nil, lastErr
}

func ObserveServices(cfg cfgpkg.Config, timeout time.Duration, onEvent func(WatchEvent)) ([]Service, error) {
	peers := uniqueStrings(append(append([]string{}, cfg.Network.BootstrapPeers...), cfg.Network.RelayPeers...))
	if len(peers) == 0 {
		return nil, errors.New("no bootstrap or relay peers configured")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return nil, fmt.Errorf("load private network key: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	h, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "", psk)
	if err != nil {
		return nil, fmt.Errorf("create observer host: %w", err)
	}
	defer h.Close()
	ps, err := pubsub.NewGossipSub(ctx, h, pubsub.WithFloodPublish(true))
	if err != nil {
		return nil, fmt.Errorf("create observer gossipsub: %w", err)
	}
	discoveryRuntime, err := cfg.RequireDiscoveryRuntime()
	if err != nil {
		return nil, fmt.Errorf("cluster discovery required: %w", err)
	}
	topic, err := ps.Join(discoveryRuntime.Topic)
	if err != nil {
		return nil, fmt.Errorf("join discovery topic: %w", err)
	}
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	sub := discovery.NewPubSubSubscriber(topic, cache)
	if discoveryRuntime.Mode == cfgpkg.DiscoveryModeNamespaceV2 {
		sub = discovery.NewPubSubSubscriberWithMode(topic, cache, discovery.ModeNamespaceV2, discoveryRuntime.ClusterID, discoveryRuntime.NamespaceID)
		if cluster, ok := cfg.Clusters[cfg.CurrentCluster]; ok && cluster.AuthorityPublicKey != "" {
			if raw, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey); err == nil {
				sub.SetAuthorityPublicKey(raw)
			} else {
				return nil, fmt.Errorf("parse authority public key: %w", err)
			}
		}
	}
	stopCh := sub.Start(ctx)
	defer close(stopCh)
	for _, raw := range peers {
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid bootstrap peer %q: %w", raw, err)
		}
		connectCtx, cancelConnect := context.WithTimeout(ctx, 5*time.Second)
		_ = h.Connect(connectCtx, info)
		cancelConnect()
	}
	if onEvent != nil {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case ev := <-sub.OnEvents():
					watchEvent := WatchEvent{Type: ev.Type, Name: ev.ServiceName, PeerID: ev.PeerID.String(), Path: "unknown"}
					if entry, ok := cache.Resolve(ev.ServiceName); ok {
						watchEvent.Path = PathFromAddresses(entry.Addresses)
					}
					onEvent(watchEvent)
				}
			}
		}()
	}
	<-ctx.Done()
	if err := ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return nil, err
	}
	services := ServiceResourcesFromEntries(cache.List())
	SortServices(services)
	return services, nil
}

func ServiceResourcesFromEntries(entries []*discovery.ServiceEntry) []Service {
	services := make([]Service, 0, len(entries))
	for _, entry := range entries {
		services = append(services, ServiceResourceFromEntry(entry))
	}
	return services
}

func ServiceResourceFromEntry(entry *discovery.ServiceEntry) Service {
	expiresIn := time.Until(entry.Registered.Add(entry.TTL))
	if expiresIn < 0 {
		expiresIn = 0
	}
	return NormalizeService(Service{Kind: "service", ServiceKind: entry.ServiceKind, Name: entry.ServiceName, ServiceID: entry.ServiceID, ServicePublicKey: entry.ServicePublicKey, ConnectPolicy: entry.ConnectPolicy, GrantService: grantspkg.CloneGrantServiceEndpoint(entry.GrantService), PeerID: entry.PeerID.String(), Addresses: append([]string(nil), entry.Addresses...), Status: "online", TTLSeconds: int64(entry.TTL.Seconds()), ExpiresInSeconds: int64(expiresIn.Seconds()), Capabilities: append([]string(nil), entry.Capabilities...), RegisteredAt: entry.Registered.Format(time.RFC3339)})
}

func ServiceFromQueryService(service discoveryquery.Service) Service {
	return NormalizeService(Service{Kind: service.Kind, ServiceKind: service.ServiceKind, Name: service.Name, ServiceID: service.ServiceID, ServicePublicKey: service.ServicePublicKey, ConnectPolicy: service.ConnectPolicy, GrantService: grantspkg.CloneGrantServiceEndpoint(service.GrantService), PeerID: service.PeerID, Addresses: append([]string(nil), service.Addresses...), DirectAddresses: append([]string(nil), service.DirectAddresses...), RelayedAddresses: append([]string(nil), service.RelayedAddresses...), Status: service.Status, Path: service.Path, TTLSeconds: service.TTLSeconds, ExpiresInSeconds: service.ExpiresInSeconds, Capabilities: append([]string(nil), service.Capabilities...), RegisteredAt: service.RegisteredAt})
}

func NormalizeService(service Service) Service {
	if strings.TrimSpace(service.ServiceKind) == "" {
		service.ServiceKind = string(cfgpkg.ServiceKindHTTP)
	}
	addresses := append([]string(nil), service.Addresses...)
	if len(addresses) == 0 {
		addresses = append(addresses, service.DirectAddresses...)
		addresses = append(addresses, service.RelayedAddresses...)
	}
	direct, relayed := SplitAddresses(addresses)
	service.Addresses = addresses
	service.DirectAddresses = direct
	service.RelayedAddresses = relayed
	service.Path = PathFromAddresses(addresses)
	if service.Capabilities == nil {
		service.Capabilities = []string{}
	}
	return service
}

func SplitAddresses(addresses []string) (direct []string, relayed []string) {
	for _, addr := range addresses {
		if strings.Contains(addr, "/p2p-circuit/") {
			relayed = append(relayed, addr)
			continue
		}
		direct = append(direct, addr)
	}
	return direct, relayed
}

func PathFromAddresses(addresses []string) string {
	direct, relayed := SplitAddresses(addresses)
	switch {
	case len(direct) > 0:
		return "direct"
	case len(relayed) > 0:
		return "relayed"
	default:
		return "unknown"
	}
}

func SortServices(items []Service) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		if items[i].ServiceID != items[j].ServiceID {
			return items[i].ServiceID < items[j].ServiceID
		}
		return items[i].PeerID < items[j].PeerID
	})
}

func RequireService(services []Service, name string) (Service, error) {
	matches := make([]Service, 0, 2)
	for _, service := range services {
		if service.Name == name {
			matches = append(matches, service)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return Service{}, ambiguousServiceNameErrorf(name, matches)
	}
	return Service{}, fmt.Errorf("service %q not found", name)
}

func RequireServiceByID(services []Service, serviceID string) (Service, error) {
	for _, service := range services {
		if service.ServiceID == serviceID {
			return service, nil
		}
	}
	return Service{}, fmt.Errorf("service %q not found", serviceID)
}

func IsAmbiguousServiceError(err error) bool {
	_, ok := err.(AmbiguousServiceNameError)
	return ok
}

func ambiguousServiceNameErrorf(name string, matches []Service) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Multiple services named %q found.\nUse:\n", name)
	for _, service := range matches {
		if service.ServiceID == "" {
			fmt.Fprintf(&b, "  tubo connect service/%s  # peer %s\n", service.Name, service.PeerID)
			continue
		}
		fmt.Fprintf(&b, "  tubo connect service/%s\n", service.ServiceID)
	}
	b.WriteString("Or use a verified alias.")
	return AmbiguousServiceNameError(b.String())
}

func applyScope(service Service, scope Scope) Service {
	service.Cluster = scope.Cluster
	service.Namespace = scope.Namespace
	return service
}

func applyScopeToServices(items []Service, scope Scope) []Service {
	if len(items) == 0 {
		return items
	}
	for i := range items {
		items[i] = applyScope(items[i], scope)
	}
	return items
}

func scopePtr(scope Scope) *Scope {
	if scope == (Scope{}) {
		return nil
	}
	copy := scope
	return &copy
}

func hostPortForHTTP(addr string) string {
	if strings.HasPrefix(addr, "[") {
		return addr
	}
	if host, port, err := strings.Cut(addr, ":"); err && strings.Contains(host, ":") {
		return "[" + host + "]:" + port
	}
	return addr
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}
