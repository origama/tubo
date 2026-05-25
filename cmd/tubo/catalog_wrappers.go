package main

import (
	"time"

	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
)

func discoverServices(configPath string, timeout time.Duration, cachedOnly, live bool, scope serviceScope) (discoveryLookupResult, error) {
	result, err := catalog.DiscoverServices(configPath, timeout, cachedOnly, live, toCatalogScope(scope))
	return fromCatalogLookupResult(result), err
}

func discoverServicesWithConfig(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope serviceScope) (discoveryLookupResult, error) {
	result, err := catalog.DiscoverServicesWithConfig(cfg, timeout, cachedOnly, live, toCatalogScope(scope))
	return fromCatalogLookupResult(result), err
}

func discoverService(configPath, serviceName string, timeout time.Duration, cachedOnly, live bool, scope serviceScope) (discoveryLookupResult, serviceResource, error) {
	result, service, err := catalog.DiscoverService(configPath, serviceName, timeout, cachedOnly, live, toCatalogScope(scope))
	return fromCatalogLookupResult(result), fromCatalogService(service), err
}

func discoverServiceWithConfig(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope serviceScope, serviceName string) (discoveryLookupResult, serviceResource, error) {
	result, service, err := catalog.DiscoverServiceWithConfig(cfg, timeout, cachedOnly, live, toCatalogScope(scope), serviceName)
	return fromCatalogLookupResult(result), fromCatalogService(service), err
}

func discoverServiceExactWithConfig(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope serviceScope, serviceName, serviceID string) (discoveryLookupResult, serviceResource, error) {
	result, service, err := catalog.DiscoverServiceExactWithConfig(cfg, timeout, cachedOnly, live, toCatalogScope(scope), serviceName, serviceID)
	return fromCatalogLookupResult(result), fromCatalogService(service), err
}

func loadDiscoveryConfig(path string) (cfgpkg.Config, error) {
	return catalog.LoadDiscoveryConfig(path)
}

func fetchLocalServiceCache(cfg cfgpkg.Config) ([]serviceResource, string, error) {
	services, adminAddr, err := catalog.FetchLocalServiceCache(cfg)
	return fromCatalogServices(services), adminAddr, err
}

func fetchRemoteServiceCache(cfg cfgpkg.Config, timeout time.Duration) ([]serviceResource, *discoveryquery.Metadata, []string, error) {
	services, metadata, messages, err := catalog.FetchRemoteServiceCache(cfg, timeout)
	return fromCatalogServices(services), metadata, messages, err
}

func fetchRemoteService(cfg cfgpkg.Config, serviceName string, timeout time.Duration) (serviceResource, *discoveryquery.Metadata, []string, error) {
	service, metadata, messages, err := catalog.FetchRemoteService(cfg, serviceName, timeout)
	return fromCatalogService(service), metadata, messages, err
}

func observeServices(cfg cfgpkg.Config, timeout time.Duration, onEvent func(serviceWatchEvent)) ([]serviceResource, error) {
	var wrapped func(catalog.WatchEvent)
	if onEvent != nil {
		wrapped = func(ev catalog.WatchEvent) { onEvent(fromCatalogWatchEvent(ev)) }
	}
	services, err := catalog.ObserveServices(cfg, timeout, wrapped)
	return fromCatalogServices(services), err
}

func serviceResourcesFromEntries(entries []*discovery.ServiceEntry) []serviceResource {
	return fromCatalogServices(catalog.ServiceResourcesFromEntries(entries))
}

func serviceResourceFromEntry(entry *discovery.ServiceEntry) serviceResource {
	return fromCatalogService(catalog.ServiceResourceFromEntry(entry))
}

func serviceResourceFromQueryService(service discoveryquery.Service) serviceResource {
	return fromCatalogService(catalog.ServiceFromQueryService(service))
}

func normalizeServiceResource(service serviceResource) serviceResource {
	return fromCatalogService(catalog.NormalizeService(toCatalogService(service)))
}

func splitServiceAddresses(addresses []string) (direct []string, relayed []string) {
	return catalog.SplitAddresses(addresses)
}

func servicePathFromAddresses(addresses []string) string { return catalog.PathFromAddresses(addresses) }

func sortServiceResources(items []serviceResource) {
	services := toCatalogServices(items)
	catalog.SortServices(services)
	copy(items, fromCatalogServices(services))
}

func requireService(services []serviceResource, name string) (serviceResource, error) {
	service, err := catalog.RequireService(toCatalogServices(services), name)
	return fromCatalogService(service), err
}

func requireServiceByID(services []serviceResource, serviceID string) (serviceResource, error) {
	service, err := catalog.RequireServiceByID(toCatalogServices(services), serviceID)
	return fromCatalogService(service), err
}

func isAmbiguousServiceError(err error) bool { return catalog.IsAmbiguousServiceError(err) }

func toCatalogScope(scope serviceScope) catalog.Scope {
	return catalog.Scope{Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}
}

func fromCatalogScope(scope *catalog.Scope) *serviceScope {
	if scope == nil {
		return nil
	}
	copy := serviceScope{Cluster: scope.Cluster, Namespace: scope.Namespace, AllNamespaces: scope.AllNamespaces}
	return &copy
}

func toCatalogService(service serviceResource) catalog.Service {
	return catalog.Service{
		Kind:             service.Kind,
		Cluster:          service.Cluster,
		Namespace:        service.Namespace,
		Name:             service.Name,
		ServiceID:        service.ServiceID,
		ServicePublicKey: service.ServicePublicKey,
		ConnectPolicy:    service.ConnectPolicy,
		GrantService:     grantspkg.CloneGrantServiceEndpoint(service.GrantService),
		PeerID:           service.PeerID,
		Addresses:        append([]string(nil), service.Addresses...),
		DirectAddresses:  append([]string(nil), service.DirectAddresses...),
		RelayedAddresses: append([]string(nil), service.RelayedAddresses...),
		Status:           service.Status,
		Path:             service.Path,
		TTLSeconds:       service.TTLSeconds,
		ExpiresInSeconds: service.ExpiresInSeconds,
		Capabilities:     append([]string(nil), service.Capabilities...),
		RegisteredAt:     service.RegisteredAt,
	}
}

func fromCatalogService(service catalog.Service) serviceResource {
	return serviceResource{
		Kind:             service.Kind,
		Cluster:          service.Cluster,
		Namespace:        service.Namespace,
		Name:             service.Name,
		ServiceID:        service.ServiceID,
		ServicePublicKey: service.ServicePublicKey,
		ConnectPolicy:    service.ConnectPolicy,
		GrantService:     grantspkg.CloneGrantServiceEndpoint(service.GrantService),
		PeerID:           service.PeerID,
		Addresses:        append([]string(nil), service.Addresses...),
		DirectAddresses:  append([]string(nil), service.DirectAddresses...),
		RelayedAddresses: append([]string(nil), service.RelayedAddresses...),
		Status:           service.Status,
		Path:             service.Path,
		TTLSeconds:       service.TTLSeconds,
		ExpiresInSeconds: service.ExpiresInSeconds,
		Capabilities:     append([]string(nil), service.Capabilities...),
		RegisteredAt:     service.RegisteredAt,
	}
}

func toCatalogServices(services []serviceResource) []catalog.Service {
	out := make([]catalog.Service, 0, len(services))
	for _, service := range services {
		out = append(out, toCatalogService(service))
	}
	return out
}

func fromCatalogServices(services []catalog.Service) []serviceResource {
	out := make([]serviceResource, 0, len(services))
	for _, service := range services {
		out = append(out, fromCatalogService(service))
	}
	return out
}

func fromCatalogLookupResult(result catalog.LookupResult) discoveryLookupResult {
	return discoveryLookupResult{Services: fromCatalogServices(result.Services), Messages: append([]string(nil), result.Messages...), Mode: result.Mode, Scope: fromCatalogScope(result.Scope), Metadata: result.Metadata}
}

func fromCatalogWatchEvent(ev catalog.WatchEvent) serviceWatchEvent {
	return serviceWatchEvent{Type: ev.Type, Name: ev.Name, PeerID: ev.PeerID, Path: ev.Path}
}
