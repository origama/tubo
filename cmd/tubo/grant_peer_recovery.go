package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	logging "github.com/origama/tubo/internal/logging"
	"github.com/origama/tubo/internal/p2p"
)

type grantRequestOptions struct {
	explicitPeer         string
	pollOnly             bool
	requireApprovedLease bool
	requestedTTL         time.Duration
	responseMode         grantClientResponseMode
}

var discoverGrantServicePeersFn = discoverGrantServicePeers

func requestPublishGrant(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string, opts grantRequestOptions) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	if peer := strings.TrimSpace(opts.explicitPeer); peer != "" {
		return requestPublishGrantOnPeer(configPath, cfg, svc, servicePeerID, peer, opts)
	}
	return requestPublishGrantWithRecovery(configPath, cfg, svc, servicePeerID, opts)
}

func grantDiscoveryAvailable(cfg cfgpkg.Config) bool {
	return len(clusterDiscoveryQueryPeers(cfg)) > 0 && (strings.TrimSpace(cfg.Network.PrivateKeyFile) != "" || strings.TrimSpace(cfg.Network.PrivateKeyB64) != "")
}

func requestPublishGrantWithRecovery(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string, opts grantRequestOptions) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	cluster := cfg.Clusters[cfg.CurrentCluster]
	configuredPeers := uniqueStrings(append([]string{strings.TrimSpace(svc.GrantServicePeer)}, clusterGrantServicePeers(cluster)...))
	configuredPeers = filterNonEmptyStrings(configuredPeers)
	if len(configuredPeers) == 0 {
		if !grantDiscoveryAvailable(cfg) {
			return cfg, svc, "", missingGrantServicePeerBootstrapError(cfg)
		}
		return requestPublishGrantFromDiscovery(configPath, cfg, svc, servicePeerID, opts, nil)
	}
	var lastErr error
	for _, peer := range configuredPeers {
		updatedCfg, updatedSvc, shareToken, err := requestPublishGrantOnPeer(configPath, cfg, svc, servicePeerID, peer, opts)
		if err == nil {
			return updatedCfg, updatedSvc, shareToken, nil
		}
		if !isGrantPeerRetryableError(err) {
			return updatedCfg, updatedSvc, shareToken, err
		}
		lastErr = err
	}
	if !grantDiscoveryAvailable(cfg) {
		return cfg, svc, "", authorityPeerUnreachableError("grant-service request", configuredPeers, lastErr)
	}
	return requestPublishGrantFromDiscovery(configPath, cfg, svc, servicePeerID, opts, lastErr)
}

func requestPublishGrantFromDiscovery(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string, opts grantRequestOptions, initialErr error) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	freshPeers, discoveryErr := discoverGrantServicePeersFn(configPath, cfg)
	if discoveryErr != nil {
		if initialErr != nil {
			return cfg, svc, "", fmt.Errorf("configured grant service peer failed: %w; discovery query failed: %v", initialErr, discoveryErr)
		}
		return cfg, svc, "", discoveryErr
	}
	freshPeers = filterNonEmptyStrings(uniqueStrings(freshPeers))
	if len(freshPeers) == 0 {
		if initialErr != nil {
			return cfg, svc, "", fmt.Errorf("configured grant service peer failed: %w; no fresh grant-service endpoint discovered for cluster %q namespace %q", initialErr, cfg.CurrentCluster, cfg.CurrentNamespace)
		}
		return cfg, svc, "", missingGrantServicePeerBootstrapError(cfg)
	}
	if initialErr != nil {
		freshPeers = dropPeers(freshPeers, []string{svc.GrantServicePeer, clusterGrantServicePeer(cfg.Clusters[cfg.CurrentCluster])})
	}
	if len(freshPeers) == 0 {
		if initialErr != nil {
			return cfg, svc, "", fmt.Errorf("configured grant service peer failed: %w; discovery returned no fresh grant-service endpoint for cluster %q namespace %q", initialErr, cfg.CurrentCluster, cfg.CurrentNamespace)
		}
		return cfg, svc, "", missingGrantServicePeerBootstrapError(cfg)
	}
	var lastErr error
	for _, peer := range freshPeers {
		updatedCfg, updatedSvc, shareToken, err := requestPublishGrantOnPeer(configPath, cfg, svc, servicePeerID, peer, opts)
		if err == nil {
			return updatedCfg, updatedSvc, shareToken, nil
		}
		if !isGrantPeerRetryableError(err) {
			return updatedCfg, updatedSvc, shareToken, err
		}
		lastErr = err
	}
	if initialErr != nil {
		return cfg, svc, "", fmt.Errorf("configured grant service peer failed: %w; rediscovered grant service peers %s also failed: %w", initialErr, strings.Join(freshPeers, ", "), lastErr)
	}
	return cfg, svc, "", lastErr
}

func requestPublishGrantOnPeer(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID, grantPeer string, opts grantRequestOptions) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	cluster := cfg.Clusters[cfg.CurrentCluster]
	overlay, err := p2p.NewOverlayHost(p2p.OverlayHostConfig{Listen: "/ip4/127.0.0.1/tcp/0", Seed: grantsFirstNonEmpty(cfg.Node.Seed, "grant-client-"+svc.ServiceSeed), PrivateKeyFile: cfg.Network.PrivateKeyFile, PrivateKeyB64: cfg.Network.PrivateKeyB64, BootstrapPeers: cfg.Network.BootstrapPeers, RelayPeers: cfg.Network.RelayPeers, Autorelay: cfg.Network.Autorelay, HolePunching: cfg.Network.HolePunching, ForceReachability: cfg.Network.ForceReachability, Component: "grants-client"})
	if err != nil {
		return cfg, svc, "", err
	}
	defer overlay.Close()
	grantPeer = strings.TrimSpace(grantPeer)
	if grantPeer == "" {
		return cfg, svc, "", errors.New("missing grant service peer; cluster scope does not advertise a discoverable grant service")
	}
	logging.Progressf("grants-client: selected peer=%s source=requestPublishGrant\n", grantPeer)
	info, err := p2p.AddrInfoFromString(grantPeer)
	if err != nil {
		return cfg, svc, "", fmt.Errorf("failed to parse multiaddr %q: %w", grantPeer, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	overlay.StartBootstrapRetry(ctx, 5*time.Second)
	overlay.StartRelayReservations(ctx)
	if len(cfg.Network.RelayPeers) > 0 {
		waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Second)
		defer waitCancel()
		for !overlay.HasRelayReservation() {
			select {
			case <-waitCtx.Done():
				logging.Progressf("grants-client: relay reservation not ready after wait, proceeding anyway\n")
			case <-time.After(200 * time.Millisecond):
				continue
			}
			break
		}
		if overlay.HasRelayReservation() {
			logging.Progressf("grants-client: relay reservation ready\n")
		}
	}
	var resp grantspkg.Message
	if svc.GrantRequestID != "" {
		logging.Progressf("grants-client: polling existing request=%s\n", svc.GrantRequestID)
		resp, err = grantspkg.Poll(ctx, overlay.Host, info, svc.GrantRequestID)
		if err == nil && resp.Type == grantspkg.TypeExpired {
			if opts.pollOnly {
				shareToken, err := handleGrantClientResponse(configPath, cfg, svc, grantPeer, resp, servicePeerID, opts.responseMode)
				if err != nil {
					return cfg, svc, shareToken, err
				}
				return cfg, svc, shareToken, nil
			}
			logging.Progressf("grants-client: pending request %s expired; clearing and resubmitting\n", svc.GrantRequestID)
			svc.GrantRequestID = ""
			cluster2 := cfg.Clusters[cfg.CurrentCluster]
			ns2 := cluster2.Namespaces[cfg.CurrentNamespace]
			if svc2, ok := ns2.Services[cfg.Service.Name]; ok {
				svc2.GrantRequestID = ""
				ns2.Services[cfg.Service.Name] = svc2
				cluster2.Namespaces[cfg.CurrentNamespace] = ns2
				cfg.Clusters[cfg.CurrentCluster] = cluster2
				_ = saveLocalConfig(configPath, cfg)
			}
			resp = grantspkg.Message{}
		}
	}
	if err != nil {
		return cfg, svc, "", err
	}
	if resp.Type == "" && svc.GrantRequestID == "" {
		if opts.pollOnly {
			return cfg, svc, "", errors.New("no local grant request id recorded for service")
		}
		logging.Progressf("grants-client: submitting new request for service=%q\n", cfg.Service.Name)
		leaseReq, err := buildServicePublishLeaseRequest(configPath, cfg, svc, servicePeerID)
		if err != nil {
			return cfg, svc, "", err
		}
		submitResp, submitErr := grantspkg.Submit(ctx, overlay.Host, info, grantspkg.Message{
			Type:                  grantspkg.TypeSubmit,
			Version:               grantspkg.VersionV1,
			ClusterID:             cluster.ClusterID,
			NamespaceID:           cfg.CurrentNamespace,
			ServiceName:           cfg.Service.Name,
			ServiceID:             svc.ServiceID,
			ServiceKind:           string(cfgpkg.NormalizeServiceKind(svc.Kind, "")),
			ServicePublicKey:      leaseReq.ServicePublicKey,
			ServiceOwnerSignature: leaseReq.ServiceOwnerSignature,
			ServicePeerID:         servicePeerID,
			ServiceAddresses:      serviceEndpointAddrsForTokens(cfg, servicePeerID),
			RequestNonce:          leaseReq.Nonce,
			RequestedPermissions:  []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
			RequestedTTLSeconds:   int64(grantRequestTTL(opts).Seconds()),
		})
		if submitErr != nil {
			return cfg, svc, "", submitErr
		}
		resp = submitResp
	}
	if err != nil {
		return cfg, svc, "", err
	}
	shareToken, err := handleGrantClientResponse(configPath, cfg, svc, grantPeer, resp, servicePeerID, opts.responseMode)
	if err != nil {
		return cfg, svc, "", err
	}
	updated, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		return cfg, svc, "", err
	}
	updatedSvc := updated.Clusters[updated.CurrentCluster].Namespaces[updated.CurrentNamespace].Services[updated.Service.Name]
	if !opts.requireApprovedLease {
		return updated, updatedSvc, shareToken, nil
	}
	authorityPub, err := discovery.ParseAuthorityPublicKey(updated.Clusters[updated.CurrentCluster].AuthorityPublicKey)
	if err != nil {
		return updated, updatedSvc, shareToken, err
	}
	if err := verifyPublishLeaseFile(updatedSvc.ServicePublishLeaseFile, authorityPub, updated.Clusters[updated.CurrentCluster].ClusterID, updated.CurrentNamespace, updatedSvc.ServiceID, servicePeerID); err != nil {
		if updatedSvc.GrantRequestID != "" {
			return updated, updatedSvc, shareToken, errors.New(grantRequestPendingError(updated.Service.Name))
		}
		return updated, updatedSvc, shareToken, err
	}
	return updated, updatedSvc, shareToken, nil
}

func grantRequestPendingError(serviceName string) string {
	return fmt.Sprintf("grant request pending; approve it, then rerun tubo start service/%s", serviceName)
}

func grantRequestTTL(opts grantRequestOptions) time.Duration {
	if opts.requestedTTL > 0 {
		return opts.requestedTTL
	}
	return attachPublishLeaseTTL()
}

func isGrantPeerRetryableError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "denied") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "revoked") || strings.Contains(msg, "permission denied") || strings.Contains(msg, "unauthorized") {
		return false
	}
	switch {
	case strings.Contains(msg, "failed to parse multiaddr"):
		return true
	case strings.Contains(msg, "failed to dial"):
		return true
	case strings.Contains(msg, "all dials failed"):
		return true
	case strings.Contains(msg, "no_reservation"):
		return true
	case strings.Contains(msg, "dial backoff"):
		return true
	case strings.Contains(msg, "context deadline exceeded"):
		return true
	case strings.Contains(msg, "timed out"):
		return true
	case strings.Contains(msg, "timeout"):
		return true
	case strings.Contains(msg, "i/o timeout"):
		return true
	case strings.Contains(msg, "connection refused"):
		return true
	case strings.Contains(msg, "connection reset"):
		return true
	case (strings.Contains(msg, "protocol not supported") || strings.Contains(msg, "protocols not supported") || strings.Contains(msg, "failed to negotiate protocol")) && strings.Contains(msg, grantspkg.ProtocolID):
		return true
	case strings.Contains(msg, "eof"):
		return true
	case strings.Contains(msg, "temporary"):
		return true
	case strings.Contains(msg, "unreachable"):
		return true
	default:
		return false
	}
}

func discoverGrantServicePeers(configPath string, cfg cfgpkg.Config) ([]string, error) {
	discoveryPeers := clusterDiscoveryQueryPeers(cfg)
	if len(discoveryPeers) == 0 {
		return nil, missingDiscoveryQueryPeerError(cfg.CurrentCluster)
	}
	scope := serviceScope{Cluster: cfg.CurrentCluster, Namespace: cfg.CurrentNamespace}
	result, err := discoverServices(configPath, 5*time.Second, false, false, scope)
	if err != nil {
		logging.Progressf("grant service discovery: discovery query failed: %v\n", err)
		return nil, authorityPeerUnreachableError("discovery query", discoveryPeers, err)
	}
	logging.Progressf("grant service discovery: query returned %d services (mode=%s)\n", len(result.Services), result.Mode)
	clusterID := ""
	if cluster, ok := cfg.Clusters[cfg.CurrentCluster]; ok {
		clusterID = strings.TrimSpace(cluster.ClusterID)
	}
	peers := grantServicePeersFromDiscoveryResults(result.Services, clusterID, cfg.CurrentNamespace)
	return uniqueStrings(filterNonEmptyStrings(peers)), nil
}

func grantServicePeersFromDiscoveryResults(services []serviceResource, clusterID, namespaceID string) []string {
	type grantServiceCandidate struct {
		service      serviceResource
		registeredAt time.Time
	}
	candidates := make([]grantServiceCandidate, 0, 4)
	for _, service := range services {
		if !isSystemServiceResource(service) || !hasStrictSystemServiceScope(service) {
			continue
		}
		if strings.TrimSpace(service.Name) != "grant-service" {
			continue
		}
		if strings.TrimSpace(clusterID) == "" || strings.TrimSpace(service.ClusterID) != clusterID {
			continue
		}
		if strings.TrimSpace(namespaceID) == "" || strings.TrimSpace(service.NamespaceID) != strings.TrimSpace(namespaceID) {
			continue
		}
		if service.ExpiresInSeconds <= 0 {
			continue
		}
		registeredAt, _ := time.Parse(time.RFC3339, strings.TrimSpace(service.RegisteredAt))
		candidates = append(candidates, grantServiceCandidate{service: service, registeredAt: registeredAt})
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].service.ExpiresInSeconds != candidates[j].service.ExpiresInSeconds {
			return candidates[i].service.ExpiresInSeconds > candidates[j].service.ExpiresInSeconds
		}
		if !candidates[i].registeredAt.Equal(candidates[j].registeredAt) {
			return candidates[i].registeredAt.After(candidates[j].registeredAt)
		}
		if candidates[i].service.ServiceID != candidates[j].service.ServiceID {
			return candidates[i].service.ServiceID < candidates[j].service.ServiceID
		}
		return candidates[i].service.PeerID < candidates[j].service.PeerID
	})
	peers := make([]string, 0, 4)
	for _, candidate := range candidates {
		candidatePeers := grantServicePeersFromResource(candidate.service)
		if len(candidatePeers) == 0 {
			logging.Progressf("grant service discovery: grant-service record found but has no usable peer address (cluster=%q namespace=%q kind=%q grantService=%v addrs=%v)\n", candidate.service.ClusterID, candidate.service.NamespaceID, candidate.service.Kind, candidate.service.GrantService, candidate.service.Addresses)
			continue
		}
		peers = append(peers, candidatePeers...)
	}
	return peers
}

func grantServicePeersFromResource(service serviceResource) []string {
	if service.GrantService != nil && len(service.GrantService.Peers) > 0 {
		peers := make([]string, 0, len(service.GrantService.Peers))
		for _, peer := range service.GrantService.Peers {
			if peer = strings.TrimSpace(peer); peer != "" {
				peers = append(peers, peer)
			}
		}
		return peers
	}
	peers := make([]string, 0, len(service.Addresses))
	for _, addr := range service.Addresses {
		if addr = strings.TrimSpace(addr); addr != "" {
			peers = append(peers, addr)
		}
	}
	return peers
}

func filterNonEmptyStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, item := range in {
		if strings.TrimSpace(item) != "" {
			out = append(out, strings.TrimSpace(item))
		}
	}
	return out
}

func clusterDiscoveryQueryPeers(cfg cfgpkg.Config) []string {
	clusterName := strings.TrimSpace(cfg.CurrentCluster)
	if clusterName == "" {
		return nil
	}
	cluster, ok := cfg.Clusters[clusterName]
	if !ok {
		return nil
	}
	return canonicalAuthorityBootstrapPeers(cluster.DiscoveryQueryPeers)
}

func missingDiscoveryQueryPeerError(clusterName string) error {
	clusterName = strings.TrimSpace(clusterName)
	if clusterName == "" {
		return errors.New("missing discovery query peer; run `tubo start cluster/<name>` on the authority and reissue/rejoin invite")
	}
	return fmt.Errorf("missing discovery query peer for cluster %q; run `tubo start cluster/%s` on the authority and reissue/rejoin invite", clusterName, clusterName)
}

func missingGrantServicePeerBootstrapError(cfg cfgpkg.Config) error {
	clusterName := strings.TrimSpace(cfg.CurrentCluster)
	if len(clusterDiscoveryQueryPeers(cfg)) == 0 {
		return missingDiscoveryQueryPeerError(clusterName)
	}
	if clusterName == "" {
		return errors.New("missing grant service peer; the invite/config lacks grant-service endpoints")
	}
	return fmt.Errorf("missing grant service peer for cluster %q; the invite/config lacks grant-service endpoints", clusterName)
}

func authorityPeerUnreachableError(operation string, peers []string, err error) error {
	peers = canonicalAuthorityBootstrapPeers(peers)
	if len(peers) == 0 {
		return err
	}
	operation = strings.TrimSpace(operation)
	if operation == "" {
		operation = "authority peer"
	}
	return fmt.Errorf("%s failed: authority peer unreachable (candidates=%d path_classes=%s): %w", operation, len(peers), authorityPeerPathSummary(peers), err)
}

func dropPeers(in []string, remove []string) []string {
	blocked := make(map[string]struct{}, len(remove))
	for _, item := range remove {
		if item = strings.TrimSpace(item); item != "" {
			blocked[item] = struct{}{}
		}
	}
	out := make([]string, 0, len(in))
	for _, item := range in {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := blocked[item]; ok {
			continue
		}
		out = append(out, item)
	}
	return out
}
