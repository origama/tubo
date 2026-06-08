package connectflow

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	bridge "github.com/origama/tubo/internal/app/bridge"
	capability "github.com/origama/tubo/internal/capability"
	catalog "github.com/origama/tubo/internal/catalog"
	clusterinvite "github.com/origama/tubo/internal/clusterinvite"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/logging"
	"github.com/origama/tubo/internal/p2p"
)

type Request struct {
	ConfigPath string
	ServiceRef string
	Token      string
	Cluster    string
	Namespace  string
	Local      string
	Timeout    time.Duration
	CachedOnly bool
	Live       bool
}

type Attempt struct {
	Path   string `json:"path"`
	Addr   string `json:"addr"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

type Result struct {
	Messages      []string
	ServiceName   string
	ServiceKind   string
	ServiceID     string
	ServicePeerID string
	LocalURL      string
	Path          string
	Scope         *catalog.Scope
	SelectedAddr  string
	Direct        string
	Relay         string
	Attempts      []Attempt
	App           *bridge.App
}

type ShareTokenInfo struct {
	JTI                  string
	Cluster              string
	ClusterID            string
	AuthorityPublicKey   string
	Namespace            string
	NamespaceID          string
	TargetServiceID      string
	DisplayNameHint      string
	ServiceKind          string
	ServiceEndpointPeer  string
	ServiceEndpointAddrs []string
	IssuedAt             time.Time
	ExpiresAt            time.Time
	ConnectInviteToken   string
	ConnectGrantPeers    []string
}

type Deps interface {
	LoadConfig(path string) (cfgpkg.Config, error)
	SetupShare(serviceRef, token, cluster, namespace string) (string, string, catalog.Scope, error)
	ParseServiceRef(ref string) (string, error)
	IsServiceID(ref string) bool
	ResolveScope(cfg cfgpkg.Config, cluster, namespace string) (catalog.Scope, error)
	ParseShareToken(token string) (ShareTokenInfo, error)
	EnsureShareInviteAvailable(configDir string, token ShareTokenInfo) error
	ImportShareDiscoveryContext(cfg cfgpkg.Config, token ShareTokenInfo) (cfgpkg.Config, error)
	MarkShareInviteUsed(configDir string, token ShareTokenInfo) error
	DiscoverService(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope catalog.Scope, serviceName string) (catalog.LookupResult, catalog.Service, error)
	DiscoverServiceExact(cfg cfgpkg.Config, timeout time.Duration, cachedOnly, live bool, scope catalog.Scope, serviceName, serviceID string) (catalog.LookupResult, catalog.Service, error)
	NewBridge(context.Context, bridge.Config) (*bridge.App, error)
}

func Resolve(ctx context.Context, deps Deps, req Request) (Result, error) {
	shareToken := strings.TrimSpace(req.Token)
	serviceRef, serviceID, shareScope, err := deps.SetupShare(req.ServiceRef, shareToken, req.Cluster, req.Namespace)
	if err != nil {
		return Result{}, err
	}
	cfg, err := deps.LoadConfig(req.ConfigPath)
	if err != nil {
		return Result{}, err
	}
	cluster := req.Cluster
	namespace := req.Namespace
	var connectInviteToken string
	var connectGrantPeers []string
	var shareInfo ShareTokenInfo
	if shareToken != "" {
		info, err := deps.ParseShareToken(shareToken)
		if err != nil {
			return Result{}, err
		}
		if err := deps.EnsureShareInviteAvailable(filepath.Dir(req.ConfigPath), info); err != nil {
			return Result{}, err
		}
		cfg, err = deps.ImportShareDiscoveryContext(cfg, info)
		if err != nil {
			return Result{}, err
		}
		if err := deps.MarkShareInviteUsed(filepath.Dir(req.ConfigPath), info); err != nil {
			return Result{}, err
		}
		shareInfo = info
		cluster = info.Cluster
		namespace = info.Namespace
		shareScope = catalog.Scope{Cluster: info.Cluster, Namespace: info.Namespace}
		serviceID = info.TargetServiceID
		if serviceRef == "" {
			serviceRef = info.DisplayNameHint
		}
		connectInviteToken = info.ConnectInviteToken
		connectGrantPeers = append([]string(nil), info.ConnectGrantPeers...)
	}
	var scope catalog.Scope
	if shareToken != "" {
		scope = shareScope
	} else {
		scope, err = deps.ResolveScope(cfg, cluster, namespace)
		if err != nil {
			return Result{}, err
		}
	}
	serviceRef, err = deps.ParseServiceRef(serviceRef)
	if err != nil {
		return Result{}, err
	}
	if serviceID == "" && deps.IsServiceID(serviceRef) {
		serviceID = serviceRef
		serviceRef = ""
	}
	lookupLabel := serviceRef
	if lookupLabel == "" {
		lookupLabel = serviceID
	}
	var lookup catalog.LookupResult
	var service catalog.Service
	if shareToken != "" {
		if len(shareInfo.ServiceEndpointAddrs) == 0 || strings.TrimSpace(shareInfo.ServiceEndpointPeer) == "" {
			return Result{}, fmt.Errorf("share invite is missing a self-contained service endpoint; ask the publisher to reissue the invite")
		}
		service = catalog.Service{Name: serviceRef, ServiceID: serviceID, ServiceKind: shareInfo.ServiceKind, PeerID: shareInfo.ServiceEndpointPeer}
		service.DirectAddresses, service.RelayedAddresses = splitServiceAddresses(shareInfo.ServiceEndpointAddrs)
		if service.Name == "" {
			service.Name = shareInfo.DisplayNameHint
		}
	} else {
		lookup, service, err = deps.DiscoverServiceExact(cfg, req.Timeout, req.CachedOnly, req.Live, scope, serviceRef, serviceID)
	}
	if err != nil {
		if catalog.IsAmbiguousServiceError(err) || cfgpkg.IsAmbientDiscoveryDisabled(err) {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("service %q not found; run `tubo get services` to inspect available services", lookupLabel)
	}
	service = catalog.NormalizeService(service)
	if serviceID != "" {
		if service.ServiceID != "" && service.ServiceID != serviceID {
			return Result{}, fmt.Errorf("service share is for service_id %q, not %q", serviceID, service.ServiceID)
		}
		if service.ServiceID == "" {
			service.ServiceID = serviceID
		}
	}
	listenAddr, localURL, err := ChooseLocalForService(service.ServiceKind, req.Local)
	if err != nil {
		return Result{}, err
	}
	bridgeCfg := bridge.Config{
		Listen:             listenAddr,
		ServiceKind:        service.ServiceKind,
		Seed:               cfg.Node.Seed,
		P2PListen:          cfg.Node.P2PListen,
		PrivateKeyFile:     cfg.Network.PrivateKeyFile,
		PrivateKeyB64:      cfg.Network.PrivateKeyB64,
		RelayPeers:         cfg.Network.RelayPeers,
		Autorelay:          cfg.Network.Autorelay,
		HolePunching:       cfg.Network.HolePunching,
		ConnectInviteToken: connectInviteToken,
		ConnectGrantPeers:  connectGrantPeers,
	}
	if clusterCfg, ok := cfg.Clusters[scope.Cluster]; ok {
		bridgeCfg.ConnectAuthorityPrivateKeyFile = clusterCfg.AuthorityPrivateKeyFile
		bridgeCfg.ConnectClusterID = clusterCfg.ClusterID
		if authorityPriv, err := loadConnectAuthorityPrivateKey(clusterCfg.AuthorityPrivateKeyFile); err == nil {
			bridgeCfg.ConnectAuthorityPrivateKey = authorityPriv
		}
	} else if currentCfg, ok := cfg.Clusters[strings.TrimSpace(cfg.CurrentCluster)]; ok {
		bridgeCfg.ConnectAuthorityPrivateKeyFile = currentCfg.AuthorityPrivateKeyFile
		if bridgeCfg.ConnectClusterID == "" {
			bridgeCfg.ConnectClusterID = currentCfg.ClusterID
		}
		if authorityPriv, err := loadConnectAuthorityPrivateKey(currentCfg.AuthorityPrivateKeyFile); err == nil {
			bridgeCfg.ConnectAuthorityPrivateKey = authorityPriv
		}
	}
	if service.ServiceID != "" {
		bridgeCfg.ConnectServiceID = service.ServiceID
		bridgeCfg.ConnectNamespaceID = scope.Namespace
	}
	if shareToken != "" && len(connectGrantPeers) == 0 && strings.TrimSpace(bridgeCfg.ConnectAuthorityPrivateKeyFile) == "" && len(bridgeCfg.ConnectAuthorityPrivateKey) == 0 {
		return Result{}, fmt.Errorf("share invite does not contain a valid authorization path; ask the service owner to reissue the invite")
	}
	if shareToken == "" && service.GrantService != nil && len(service.GrantService.Peers) > 0 {
		bridgeCfg.ConnectGrantPeers = append([]string(nil), service.GrantService.Peers...)
		bridgeCfg.ConnectServiceID = service.ServiceID
		bridgeCfg.ConnectNamespaceID = scope.Namespace
		if clusterCfg, ok := cfg.Clusters[scope.Cluster]; ok {
			bridgeCfg.ConnectClusterID = clusterCfg.ClusterID
		}
		if membership, membershipGrantToken, err := loadConnectMembership(cfg, scope); err == nil {
			bridgeCfg.ConnectMembershipCapability = membership
			bridgeCfg.ConnectMembershipGrantToken = membershipGrantToken
		}
	}
	if bridgeCfg.P2PListen == "" {
		bridgeCfg.P2PListen = "/ip4/0.0.0.0/tcp/0"
	}
	if service.ServiceID != "" {
		pinnedServiceID := service.ServiceID
		pinnedServiceKind := service.ServiceKind
		bridgeCfg.ConnectRebindResolver = func(ctx context.Context) (peer.AddrInfo, string, string, error) {
			_, refreshed, err := deps.DiscoverServiceExact(cfg, req.Timeout, req.CachedOnly, req.Live, scope, service.Name, pinnedServiceID)
			if err != nil {
				return peer.AddrInfo{}, "", "", err
			}
			refreshed = catalog.NormalizeService(refreshed)
			if refreshed.ServiceID != "" && refreshed.ServiceID != pinnedServiceID {
				return peer.AddrInfo{}, "", "", fmt.Errorf("service share is for service_id %q, not %q", pinnedServiceID, refreshed.ServiceID)
			}
			if refreshed.ServiceKind != "" && refreshed.ServiceKind != pinnedServiceKind {
				return peer.AddrInfo{}, "", "", fmt.Errorf("service kind changed from %q to %q for pinned service_id %q", pinnedServiceKind, refreshed.ServiceKind, pinnedServiceID)
			}
			candidates, err := ConnectCandidates(refreshed)
			if err != nil {
				return peer.AddrInfo{}, "", "", err
			}
			if len(candidates) == 0 {
				return peer.AddrInfo{}, "", "", fmt.Errorf("service %q has no reusable addresses", refreshed.Name)
			}
			selected := candidates[0]
			addrInfo, err := p2p.AddrInfoFromString(selected.Addr)
			if err != nil {
				return peer.AddrInfo{}, "", "", err
			}
			if addrInfo.ID.String() != refreshed.PeerID {
				return peer.AddrInfo{}, "", "", fmt.Errorf("resolved peer %s does not match announced peer %s for pinned service_id %q", addrInfo.ID, refreshed.PeerID, pinnedServiceID)
			}
			return addrInfo, selected.Addr, selected.Path, nil
		}
	}

	selectedPath, selectedAddr, attempts, app, err := ConnectBridge(ctx, deps.NewBridge, bridgeCfg, service)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Messages:      append([]string(nil), lookup.Messages...),
		ServiceName:   service.Name,
		ServiceKind:   service.ServiceKind,
		ServiceID:     service.ServiceID,
		ServicePeerID: service.PeerID,
		LocalURL:      localURL,
		Path:          selectedPath,
		Scope:         scopePtr(scope),
		SelectedAddr:  selectedAddr,
		Direct:        ConnectDirectMessage(service, attempts, selectedPath),
		Relay:         ConnectRelayMessage(service, selectedAddr, selectedPath),
		Attempts:      attempts,
		App:           app,
	}, nil
}

func ChooseLocal(local string) (listenAddr string, localURL string, err error) {
	return ChooseLocalForService(string(cfgpkg.ServiceKindHTTP), local)
}

func ChooseLocalForService(serviceKind, local string) (listenAddr string, localURL string, err error) {
	kind := cfgpkg.NormalizeServiceKind(cfgpkg.ServiceKind(serviceKind), "")
	prefix := "http://"
	if kind == cfgpkg.ServiceKindTCP {
		prefix = "tcp://"
	}
	if local != "" {
		if _, _, splitErr := net.SplitHostPort(local); splitErr != nil {
			return "", "", fmt.Errorf("invalid --local %q: %w", local, splitErr)
		}
		return local, prefix + local, nil
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", err
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr, prefix + addr, nil
}

func ConnectBridge(ctx context.Context, newBridge func(context.Context, bridge.Config) (*bridge.App, error), base bridge.Config, service catalog.Service) (string, string, []Attempt, *bridge.App, error) {
	candidates, err := ConnectCandidates(service)
	if err != nil {
		return "", "", nil, nil, err
	}
	attempts := make([]Attempt, 0, len(candidates))
	total := len(candidates)
	for idx, candidate := range candidates {
		attemptNo := idx + 1
		logging.Verbosef(3, "connect candidate attempt %d/%d path=%s addr=%s\n", attemptNo, total, candidate.Path, candidate.Addr)
		cfg := base
		cfg.ServiceAddr = candidate.Addr
		cfg.SelectedAddr = candidate.Addr
		cfg.SelectedPath = candidate.Path
		app, err := newBridge(ctx, cfg)
		if err != nil {
			attempts = append(attempts, Attempt{Path: candidate.Path, Addr: candidate.Addr, Status: "failed", Error: err.Error()})
			logging.Verbosef(3, "connect candidate failed %d/%d path=%s addr=%s err=%q\n", attemptNo, total, candidate.Path, candidate.Addr, err.Error())
			continue
		}
		attempts = append(attempts, Attempt{Path: candidate.Path, Addr: candidate.Addr, Status: "selected"})
		logging.Verbosef(3, "connect candidate selected %d/%d path=%s addr=%s\n", attemptNo, total, candidate.Path, candidate.Addr)
		return candidate.Path, candidate.Addr, attempts, app, nil
	}
	return "", "", attempts, nil, fmt.Errorf("connect to service %q failed: %s", service.Name, SummarizeAttempts(attempts))
}

type Candidate struct {
	Path string
	Addr string
}

func ConnectCandidates(service catalog.Service) ([]Candidate, error) {
	service = catalog.NormalizeService(service)
	if len(service.DirectAddresses) == 0 && len(service.RelayedAddresses) == 0 {
		return nil, fmt.Errorf("service %q has no announced addresses", service.Name)
	}
	candidates := make([]Candidate, 0, len(service.DirectAddresses)+len(service.RelayedAddresses))
	for _, addr := range service.DirectAddresses {
		if IsUnusableDirectAddress(addr) {
			continue
		}
		candidates = append(candidates, Candidate{Path: "direct", Addr: addr})
	}
	for _, addr := range service.RelayedAddresses {
		candidates = append(candidates, Candidate{Path: "relayed", Addr: addr})
	}
	return candidates, nil
}

func IsUnusableDirectAddress(addr string) bool {
	return strings.Contains(addr, "/ip4/127.") || strings.Contains(addr, "/ip4/0.0.0.0/") || strings.Contains(addr, "/ip6/::1/") || strings.Contains(addr, "/ip6/::/") || strings.Contains(addr, "/dns4/localhost/") || strings.Contains(addr, "/dns6/localhost/")
}

func SummarizeAttempts(attempts []Attempt) string {
	parts := make([]string, 0, len(attempts))
	for _, attempt := range attempts {
		if attempt.Status == "selected" {
			parts = append(parts, fmt.Sprintf("%s succeeded", attempt.Path))
			continue
		}
		parts = append(parts, fmt.Sprintf("%s failed (%s)", attempt.Path, attempt.Error))
	}
	if len(parts) == 0 {
		return "no dial attempts"
	}
	return strings.Join(parts, "; ")
}

func ConnectDirectMessage(service catalog.Service, attempts []Attempt, selectedPath string) string {
	service = catalog.NormalizeService(service)
	usableDirect := 0
	for _, addr := range service.DirectAddresses {
		if !IsUnusableDirectAddress(addr) {
			usableDirect++
		}
	}
	if len(service.DirectAddresses) == 0 {
		return "unavailable, no direct addresses advertised"
	}
	if usableDirect == 0 {
		return "unavailable, only loopback/unspecified direct addresses advertised"
	}
	if selectedPath == "direct" {
		return "selected"
	}
	for _, attempt := range attempts {
		if attempt.Path == "direct" && attempt.Status == "failed" {
			if len(service.RelayedAddresses) > 0 {
				return "attempted, failed; relay selected and hole punching may still upgrade later"
			}
			return "attempted, failed"
		}
	}
	return "available"
}

func ConnectRelayMessage(service catalog.Service, selectedAddr, selectedPath string) string {
	service = catalog.NormalizeService(service)
	if len(service.RelayedAddresses) == 0 {
		return ""
	}
	if selectedPath == "direct" {
		return "available as fallback"
	}
	if selectedAddr != "" {
		return selectedAddr
	}
	return "selected"
}

func loadConnectMembership(cfg cfgpkg.Config, scope catalog.Scope) (*capability.MembershipCapability, string, error) {
	cluster, ok := cfg.Clusters[scope.Cluster]
	if !ok {
		return nil, "", fmt.Errorf("cluster %q not found", scope.Cluster)
	}
	membership, capErr := loadConnectMembershipCapability(cfg, scope)
	if membership != nil && containsConnectPermission(membership.Permissions) {
		return membership, "", nil
	}
	if grant := cluster.MembershipGrant; grant != nil {
		if token, err := loadConnectMembershipGrantToken(*grant, scope, cluster.ClusterID); err == nil {
			return nil, token, nil
		} else if membership == nil {
			capErr = err
		}
	}
	if membership != nil {
		return membership, "", nil
	}
	return nil, "", capErr
}

func loadConnectMembershipCapability(cfg cfgpkg.Config, scope catalog.Scope) (*capability.MembershipCapability, error) {
	cluster, ok := cfg.Clusters[scope.Cluster]
	if !ok {
		return nil, fmt.Errorf("cluster %q not found", scope.Cluster)
	}
	path := strings.TrimSpace(cluster.MembershipCapabilityFile)
	if ns, ok := cluster.Namespaces[scope.Namespace]; ok && strings.TrimSpace(ns.MembershipCapabilityFile) != "" {
		path = strings.TrimSpace(ns.MembershipCapabilityFile)
	}
	if path == "" {
		return nil, fmt.Errorf("no membership capability file configured for %s/%s", scope.Cluster, scope.Namespace)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var membership capability.MembershipCapability
	if err := json.Unmarshal(b, &membership); err != nil {
		return nil, err
	}
	return &membership, nil
}

func loadConnectMembershipGrantToken(grant cfgpkg.ClusterMembershipGrant, scope catalog.Scope, clusterID string) (string, error) {
	candidates := make([]string, 0, 2)
	if strings.TrimSpace(grant.InviteToken) != "" {
		candidates = append(candidates, strings.TrimSpace(grant.InviteToken))
	}
	if strings.TrimSpace(grant.InviteTokenFile) != "" {
		b, err := os.ReadFile(strings.TrimSpace(grant.InviteTokenFile))
		if err != nil {
			return "", err
		}
		if token := strings.TrimSpace(string(b)); token != "" {
			candidates = append(candidates, token)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no membership grant token configured for %s/%s", scope.Cluster, scope.Namespace)
	}
	for _, token := range candidates {
		payload, err := clusterinvite.ParseAndVerifyToken(token)
		if err != nil {
			continue
		}
		if payload.ClusterName == scope.Cluster && payload.ClusterID == clusterID && payload.Namespace == scope.Namespace {
			return token, nil
		}
	}
	return "", fmt.Errorf("no usable membership grant token configured for %s/%s", scope.Cluster, scope.Namespace)
}

func containsConnectPermission(perms []string) bool {
	for _, perm := range perms {
		if perm == capability.PermissionConnect {
			return true
		}
	}
	return false
}

func loadConnectAuthorityPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("cluster authority private key is not PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return k, nil
	case *ed25519.PrivateKey:
		return *k, nil
	default:
		return nil, fmt.Errorf("unsupported cluster authority private key type %T", key)
	}
}

func splitServiceAddresses(addresses []string) (direct []string, relayed []string) {
	for _, addr := range addresses {
		if strings.Contains(addr, "/p2p-circuit") {
			relayed = append(relayed, addr)
			continue
		}
		direct = append(direct, addr)
	}
	return direct, relayed
}

func scopePtr(scope catalog.Scope) *catalog.Scope {
	copy := scope
	return &copy
}
