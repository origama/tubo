package main

import (
	"errors"
	"fmt"
	"strings"
)

type serviceScope struct {
	Cluster       string `json:"cluster,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"all_namespaces,omitempty"`
}

func parseServiceRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("service name is required")
	}
	if strings.HasPrefix(ref, "service/") {
		ref = strings.TrimPrefix(ref, "service/")
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("service name is required")
	}
	if strings.Contains(ref, "/") {
		return "", fmt.Errorf("unsupported service reference %q", ref)
	}
	return ref, nil
}

func applyServiceScope(service serviceResource, scope serviceScope) serviceResource {
	service.Cluster = scope.Cluster
	service.Namespace = scope.Namespace
	return service
}

func applyServiceScopeToResources(items []serviceResource, scope serviceScope) []serviceResource {
	if len(items) == 0 {
		return items
	}
	for i := range items {
		items[i] = applyServiceScope(items[i], scope)
	}
	return items
}

func serviceScopePtr(scope serviceScope) *serviceScope {
	if scope == (serviceScope{}) {
		return nil
	}
	copy := scope
	return &copy
}
