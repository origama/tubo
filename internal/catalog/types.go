package catalog

import (
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
)

type Service struct {
	Kind             string                          `json:"kind"`
	ClusterID        string                          `json:"cluster_id,omitempty"`
	NamespaceID      string                          `json:"namespace_id,omitempty"`
	ServiceKind      string                          `json:"service_kind,omitempty"`
	Cluster          string                          `json:"cluster,omitempty"`
	Namespace        string                          `json:"namespace,omitempty"`
	Name             string                          `json:"name"`
	ServiceID        string                          `json:"service_id,omitempty"`
	ServicePublicKey string                          `json:"service_public_key,omitempty"`
	ConnectPolicy    string                          `json:"connect_policy,omitempty"`
	GrantService     *grantspkg.GrantServiceEndpoint `json:"grant_service,omitempty"`
	PeerID           string                          `json:"peer_id"`
	Addresses        []string                        `json:"addresses"`
	DirectAddresses  []string                        `json:"direct_addresses"`
	RelayedAddresses []string                        `json:"relayed_addresses"`
	Status           string                          `json:"status"`
	Path             string                          `json:"path"`
	TTLSeconds       int64                           `json:"ttl_seconds"`
	ExpiresInSeconds int64                           `json:"expires_in_seconds"`
	Capabilities     []string                        `json:"capabilities"`
	RegisteredAt     string                          `json:"registered_at"`
}

type Scope struct {
	Cluster       string `json:"cluster,omitempty"`
	Namespace     string `json:"namespace,omitempty"`
	AllNamespaces bool   `json:"all_namespaces,omitempty"`
}

type LookupResult struct {
	Services []Service                `json:"services"`
	Messages []string                 `json:"messages"`
	Mode     string                   `json:"mode"`
	Scope    *Scope                   `json:"scope,omitempty"`
	Metadata *discoveryquery.Metadata `json:"metadata,omitempty"`
}

type WatchEvent struct {
	Type   string `json:"type"`
	Name   string `json:"name"`
	PeerID string `json:"peer_id"`
	Path   string `json:"path"`
}

type AdminResponse struct {
	Count int       `json:"count"`
	Items []Service `json:"items"`
}

type AmbiguousServiceNameError string

func (e AmbiguousServiceNameError) Error() string { return string(e) }
