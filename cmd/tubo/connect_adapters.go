package main

import (
	"context"
	"time"

	bridge "github.com/origama/tubo/internal/app/bridge"
	catalog "github.com/origama/tubo/internal/catalog"
	cfgpkg "github.com/origama/tubo/internal/config"
	connectflow "github.com/origama/tubo/internal/connectflow"
	grantspkg "github.com/origama/tubo/internal/grants"
)

type connectAttempt = connectflow.Attempt

type connectCandidate = connectflow.Candidate

type connectResult struct {
	Service        string           `json:"service"`
	ServiceKind    string           `json:"service_kind,omitempty"`
	ServiceID      string           `json:"service_id,omitempty"`
	SelectedPeerID string           `json:"selected_peer_id,omitempty"`
	Local          string           `json:"local"`
	Path           string           `json:"path"`
	Scope          *serviceScope    `json:"scope,omitempty"`
	Selected       string           `json:"selected_addr,omitempty"`
	Direct         string           `json:"direct,omitempty"`
	Relay          string           `json:"relay,omitempty"`
	Attempts       []connectAttempt `json:"attempts,omitempty"`
}

type connectWorkflow struct{}

func newConnectWorkflow() connectWorkflow { return connectWorkflow{} }

func (connectWorkflow) LoadConfig(path string) (cfgpkg.Config, error) {
	return catalog.LoadDiscoveryConfig(path)
}

func (connectWorkflow) SetupShare(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error) {
	name, serviceID, scope, err := connectServiceShareSetup(serviceRef, token, cluster, namespace)
	if err != nil {
		return "", "", catalog.Scope{}, err
	}
	return name, serviceID, toCatalogScope(scope), nil
}

func (connectWorkflow) ParseServiceRef(ref string) (string, error) {
	return parseServiceRef(ref)
}

func (connectWorkflow) IsServiceID(ref string) bool { return isServiceID(ref) }

func (connectWorkflow) ResolveScope(cfg cfgpkg.Config, cluster, namespace string) (catalog.Scope, error) {
	scope, err := resolveServiceScope(cfg, cluster, namespace, false)
	if err != nil {
		return catalog.Scope{}, err
	}
	return toCatalogScope(scope), nil
}

func (connectWorkflow) ParseShareToken(token string) (connectflow.ShareTokenInfo, error) {
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		return connectflow.ShareTokenInfo{}, err
	}
	info := connectflow.ShareTokenInfo{
		JTI:                  payload.JTI,
		Cluster:              payload.ClusterName,
		ClusterID:            payload.ClusterID,
		AuthorityPublicKey:   payload.AuthorityPublicKey,
		Namespace:            payload.Namespace,
		NamespaceID:          payload.NamespaceID,
		TargetServiceID:      payload.TargetServiceID,
		DisplayNameHint:      payload.DisplayNameHint,
		ServiceKind:          payload.ServiceKind,
		ServiceEndpointPeer:  payload.ServiceEndpoint.PeerID,
		ServiceEndpointAddrs: append([]string(nil), payload.ServiceEndpoint.Addresses...),
		IssuedAt:             payload.IssuedAt,
		ExpiresAt:            payload.ExpiresAt,
		ConnectInviteToken:   token,
	}
	if payload.GrantService.Protocol == grantspkg.ProtocolID && len(payload.GrantService.Peers) > 0 {
		info.ConnectGrantPeers = append([]string(nil), payload.GrantService.Peers...)
	}
	return info, nil
}

func (connectWorkflow) EnsureShareInviteAvailable(configDir string, token connectflow.ShareTokenInfo) error {
	return ensureShareInviteAvailable(configDir, serviceSharePayload{
		JTI:                token.JTI,
		ClusterName:        token.Cluster,
		ClusterID:          token.ClusterID,
		AuthorityPublicKey: token.AuthorityPublicKey,
		Namespace:          token.Namespace,
		NamespaceID:        token.NamespaceID,
		TargetServiceID:    token.TargetServiceID,
		DisplayNameHint:    token.DisplayNameHint,
		IssuedAt:           token.IssuedAt,
		ExpiresAt:          token.ExpiresAt,
	})
}

func (connectWorkflow) ImportShareDiscoveryContext(cfg cfgpkg.Config, token connectflow.ShareTokenInfo) (cfgpkg.Config, error) {
	return importServiceShareDiscoveryContext(cfg, serviceSharePayload{
		JTI:                token.JTI,
		ClusterName:        token.Cluster,
		ClusterID:          token.ClusterID,
		AuthorityPublicKey: token.AuthorityPublicKey,
		Namespace:          token.Namespace,
		NamespaceID:        token.NamespaceID,
		TargetServiceID:    token.TargetServiceID,
		DisplayNameHint:    token.DisplayNameHint,
		IssuedAt:           token.IssuedAt,
		ExpiresAt:          token.ExpiresAt,
	})
}

func (connectWorkflow) MarkShareInviteUsed(configDir string, token connectflow.ShareTokenInfo) error {
	return markShareInviteUsed(configDir, serviceSharePayload{
		JTI:                token.JTI,
		ClusterName:        token.Cluster,
		ClusterID:          token.ClusterID,
		AuthorityPublicKey: token.AuthorityPublicKey,
		Namespace:          token.Namespace,
		NamespaceID:        token.NamespaceID,
		TargetServiceID:    token.TargetServiceID,
		DisplayNameHint:    token.DisplayNameHint,
		IssuedAt:           token.IssuedAt,
		ExpiresAt:          token.ExpiresAt,
	})
}

func (connectWorkflow) DiscoverService(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope catalog.Scope, serviceName string) (catalog.LookupResult, catalog.Service, error) {
	return catalog.DiscoverServiceWithConfig(cfg, timeout, cachedOnly, live, scope, serviceName)
}

func (connectWorkflow) DiscoverServiceExact(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope catalog.Scope, serviceName, serviceID string) (catalog.LookupResult, catalog.Service, error) {
	return catalog.DiscoverServiceExactWithConfig(cfg, timeout, cachedOnly, live, scope, serviceName, serviceID)
}

func (connectWorkflow) NewBridge(ctx context.Context, cfg bridge.Config) (*bridge.App, error) {
	return bridge.New(ctx, cfg)
}

func chooseConnectLocal(local string) (string, string, error) {
	return connectflow.ChooseLocal(local)
}

func connectCandidates(service serviceResource) ([]connectCandidate, error) {
	return connectflow.ConnectCandidates(toCatalogService(service))
}

func connectDirectMessage(service serviceResource, attempts []connectAttempt, selectedPath string) string {
	return connectflow.ConnectDirectMessage(toCatalogService(service), attempts, selectedPath)
}

func connectRelayMessage(service serviceResource, selectedAddr, selectedPath string) string {
	return connectflow.ConnectRelayMessage(toCatalogService(service), selectedAddr, selectedPath)
}

func fromConnectWorkflowResult(result connectflow.Result) connectResult {
	return connectResult{
		Service:        result.ServiceName,
		ServiceKind:    result.ServiceKind,
		ServiceID:      result.ServiceID,
		SelectedPeerID: result.ServicePeerID,
		Local:          result.LocalURL,
		Path:           result.Path,
		Scope:          fromCatalogScope(result.Scope),
		Selected:       result.SelectedAddr,
		Direct:         result.Direct,
		Relay:          result.Relay,
		Attempts:       append([]connectAttempt(nil), result.Attempts...),
	}
}
