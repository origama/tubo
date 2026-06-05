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
	ref = strings.TrimPrefix(ref, "service/")
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", errors.New("service name is required")
	}
	if strings.Contains(ref, "/") {
		return "", fmt.Errorf("unsupported service reference %q", ref)
	}
	return ref, nil
}

func serviceScopePtr(scope serviceScope) *serviceScope {
	if scope == (serviceScope{}) {
		return nil
	}
	copy := scope
	return &copy
}
