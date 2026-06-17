package main

import (
	"errors"
	"os"
	"sort"
	"strings"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

func buildServiceInventory(cfg cfgpkg.Config, scope serviceScope, discovered []serviceResource) ([]serviceResource, error) {
	if strings.TrimSpace(scope.Cluster) == "" || strings.TrimSpace(scope.Namespace) == "" {
		return nil, errors.New("service inventory requires a cluster and namespace scope")
	}
	rows := make([]serviceResource, 0, len(discovered))
	for _, service := range discovered {
		service.Source = inventorySourceNetwork
		service.Status = inventoryStatusAvailable
		rows = append(rows, service)
	}
	locals, err := buildLocalServiceInventory(cfg, scope)
	if err != nil {
		return nil, err
	}
	rows = mergeServiceInventoryRows(rows, locals)
	sortServiceInventoryRows(rows)
	return rows, nil
}

func buildLocalServiceInventory(cfg cfgpkg.Config, scope serviceScope) ([]serviceResource, error) {
	cluster, ok := cfg.Clusters[scope.Cluster]
	if !ok {
		return nil, nil
	}
	namespace, ok := cluster.Namespaces[scope.Namespace]
	if !ok || len(namespace.Services) == 0 {
		return nil, nil
	}
	views, err := listProcessViews(true)
	if err != nil {
		return nil, err
	}
	viewIndex := indexServiceProcessViews(views)
	serviceNames := make([]string, 0, len(namespace.Services))
	for name := range namespace.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)
	rows := make([]serviceResource, 0, len(serviceNames))
	for _, name := range serviceNames {
		svc := namespace.Services[name]
		row, err := buildLocalServiceInventoryRow(scope, name, svc, pickLocalServiceProcessView(viewIndex, name, svc.ServiceID))
		if err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	return rows, nil
}

type serviceProcessViewIndex struct {
	byServiceID map[string]processView
	byName      map[string]processView
}

func indexServiceProcessViews(views []processView) serviceProcessViewIndex {
	idx := serviceProcessViewIndex{byServiceID: map[string]processView{}, byName: map[string]processView{}}
	for _, view := range views {
		if strings.TrimSpace(view.ResourceKind) != "service" {
			continue
		}
		if view.Command != "attach" {
			continue
		}
		if serviceID := strings.TrimSpace(view.ServiceID); serviceID != "" {
			if existing, ok := idx.byServiceID[serviceID]; !ok || serviceViewPriority(view) > serviceViewPriority(existing) {
				idx.byServiceID[serviceID] = view
			}
		}
		if serviceName := strings.TrimSpace(view.Service); serviceName != "" {
			if existing, ok := idx.byName[serviceName]; !ok || serviceViewPriority(view) > serviceViewPriority(existing) {
				idx.byName[serviceName] = view
			}
		}
	}
	return idx
}

func pickLocalServiceProcessView(idx serviceProcessViewIndex, serviceName, serviceID string) *processView {
	if serviceID = strings.TrimSpace(serviceID); serviceID != "" {
		if view, ok := idx.byServiceID[serviceID]; ok {
			return &view
		}
	}
	if serviceName = strings.TrimSpace(serviceName); serviceName != "" {
		if view, ok := idx.byName[serviceName]; ok {
			return &view
		}
	}
	return nil
}

func serviceViewPriority(view processView) int {
	switch strings.TrimSpace(view.Status) {
	case "running":
		return 3
	case "degraded":
		return 2
	case "stale":
		return 1
	default:
		return 0
	}
}

func buildLocalServiceInventoryRow(scope serviceScope, serviceName string, svc cfgpkg.NamespaceService, view *processView) (serviceResource, error) {
	row := serviceResource{
		Kind:      "service",
		Cluster:   scope.Cluster,
		Namespace: scope.Namespace,
		Name:      serviceName,
		ServiceID: strings.TrimSpace(svc.ServiceID),
		Source:    inventorySourceLocal,
	}
	if kind := strings.TrimSpace(string(svc.Kind)); kind != "" {
		row.ServiceKind = kind
	}
	if view != nil {
		row.Status = strings.TrimSpace(view.Status)
		if row.Status == "" {
			row.Status = inventoryStatusStopped
		}
		if view.ServiceID != "" {
			row.ServiceID = view.ServiceID
		}
		if view.ServiceKind != "" && row.ServiceKind == "" {
			row.ServiceKind = view.ServiceKind
		}
		row.PeerID = view.PeerID
		row.Path = view.Path
		row.RegisteredAt = view.StartedAt
		return row, nil
	}
	if localServiceDefinitionComplete(svc) {
		row.Status = inventoryStatusStopped
	} else {
		row.Status = inventoryStatusIncomplete
	}
	return row, nil
}

func localServiceDefinitionComplete(svc cfgpkg.NamespaceService) bool {
	if strings.TrimSpace(svc.ServiceID) == "" || strings.TrimSpace(svc.ServiceSeed) == "" || strings.TrimSpace(svc.Target) == "" || strings.TrimSpace(string(svc.Kind)) == "" {
		return false
	}
	for _, path := range []string{svc.ServiceOwnerKeyFile, svc.ServiceClaimFile, svc.ServicePublishLeaseFile} {
		if strings.TrimSpace(path) == "" {
			return false
		}
		if _, err := os.Stat(path); err != nil {
			return false
		}
	}
	return true
}

func sortServiceInventoryRows(rows []serviceResource) {
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		if rows[i].ServiceID != rows[j].ServiceID {
			return rows[i].ServiceID < rows[j].ServiceID
		}
		return rows[i].PeerID < rows[j].PeerID
	})
}

func mergeServiceInventoryRows(base, locals []serviceResource) []serviceResource {
	rows := make([]serviceResource, 0, len(base)+len(locals))
	byServiceID := map[string]int{}
	byName := map[string]int{}
	register := func(row serviceResource, idx int) {
		if serviceID := strings.TrimSpace(row.ServiceID); serviceID != "" {
			if _, ok := byServiceID[serviceID]; !ok {
				byServiceID[serviceID] = idx
			}
		}
		if name := strings.TrimSpace(row.Name); name != "" {
			if _, ok := byName[name]; !ok {
				byName[name] = idx
			}
		}
	}
	for _, row := range base {
		rows = append(rows, row)
		register(row, len(rows)-1)
	}
	for _, local := range locals {
		if idx, ok := matchServiceInventoryRow(local, byServiceID, byName); ok {
			rows[idx] = overlayServiceInventoryRow(rows[idx], local)
			register(rows[idx], idx)
			continue
		}
		rows = append(rows, local)
		register(local, len(rows)-1)
	}
	return rows
}

func matchServiceInventoryRow(local serviceResource, byServiceID, byName map[string]int) (int, bool) {
	if serviceID := strings.TrimSpace(local.ServiceID); serviceID != "" {
		if idx, ok := byServiceID[serviceID]; ok {
			return idx, true
		}
	}
	if name := strings.TrimSpace(local.Name); name != "" {
		if idx, ok := byName[name]; ok {
			return idx, true
		}
	}
	return 0, false
}

func overlayServiceInventoryRow(base, local serviceResource) serviceResource {
	if local.Source == inventorySourceLocal && (local.Status == inventoryStatusStopped || local.Status == inventoryStatusIncomplete) {
		base = clearInventoryRemoteFields(base)
	}
	if strings.TrimSpace(base.Source) == "" {
		base.Source = local.Source
	} else if strings.TrimSpace(local.Source) != "" && strings.TrimSpace(base.Source) != strings.TrimSpace(local.Source) {
		if base.Source == inventorySourceNetwork && local.Source == inventorySourceLocal {
			base.Source = local.Source + "+" + base.Source
		} else {
			base.Source = base.Source + "+" + local.Source
		}
	}
	base.Kind = firstNonEmpty(base.Kind, local.Kind)
	base.Cluster = firstNonEmpty(base.Cluster, local.Cluster)
	base.Namespace = firstNonEmpty(base.Namespace, local.Namespace)
	base.Name = firstNonEmpty(local.Name, base.Name)
	base.ServiceID = firstNonEmpty(local.ServiceID, base.ServiceID)
	base.ServiceKind = firstNonEmpty(local.ServiceKind, base.ServiceKind)
	base.ConnectPolicy = firstNonEmpty(base.ConnectPolicy, local.ConnectPolicy)
	base.ServicePublicKey = firstNonEmpty(base.ServicePublicKey, local.ServicePublicKey)
	base.GrantService = firstNonNilGrantService(base.GrantService, local.GrantService)
	if strings.TrimSpace(local.Status) != "" {
		base.Status = local.Status
	}
	return base
}

func clearInventoryRemoteFields(service serviceResource) serviceResource {
	service.ServiceKind = ""
	service.ServicePublicKey = ""
	service.GrantService = nil
	service.PeerID = ""
	service.Addresses = nil
	service.DirectAddresses = nil
	service.RelayedAddresses = nil
	service.Path = ""
	service.TTLSeconds = 0
	service.ExpiresInSeconds = 0
	service.RegisteredAt = ""
	return service
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonNilGrantService(values ...*grantspkg.GrantServiceEndpoint) *grantspkg.GrantServiceEndpoint {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

const (
	inventorySourceLocal      = "local"
	inventorySourceNetwork    = "network"
	inventoryStatusAvailable  = "available"
	inventoryStatusStopped    = "stopped"
	inventoryStatusIncomplete = "incomplete"
)
