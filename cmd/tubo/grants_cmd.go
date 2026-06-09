package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	logging "github.com/origama/tubo/internal/logging"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
)

func grantsCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo grants <serve|pending|describe|approve|deny|history>")
	}
	switch args[0] {
	case "serve":
		cleanArgs, detach := stripDetachArgs(args[1:])
		if detach {
			return detachGrantsServeCommand(cleanArgs)
		}
		return grantsServeCmd(cleanArgs)
	case "request":
		return grantsRequestCmd(args[1:])
	case "pending":
		return grantsPendingCmd(args[1:])
	case "describe":
		return grantsDescribeCmd(args[1:])
	case "approve":
		return grantsApproveCmd(args[1:])
	case "deny":
		return grantsDenyCmd(args[1:])
	case "history":
		return grantsHistoryCmd(args[1:])
	default:
		return fmt.Errorf("unknown grants command %q", args[0])
	}
}

func grantsFirstNonEmpty(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func clusterGrantServicePeer(cluster cfgpkg.Cluster) string {
	if cluster.MembershipGrant == nil || cluster.MembershipGrant.GrantServiceProtocol != grantspkg.ProtocolID {
		return ""
	}
	for _, peer := range cluster.MembershipGrant.GrantServicePeers {
		if strings.TrimSpace(peer) != "" {
			return strings.TrimSpace(peer)
		}
	}
	return ""
}

func shareGrantServicePeer(cluster cfgpkg.Cluster, svc cfgpkg.NamespaceService) string {
	if peer := strings.TrimSpace(svc.GrantServicePeer); peer != "" {
		return peer
	}
	return clusterGrantServicePeer(cluster)
}

func grantServicePeersForTokens(addrs []string) []string {
	return grantspkg.PreferredAdvertisedGrantServicePeers(addrs)
}

func grantsRequestCmd(args []string) error {
	serviceArg, flagArgs := splitGrantIDArg(args)
	fs := flag.NewFlagSet("grants request", flag.ContinueOnError)
	configPath := fs.String("config", "", "")
	clusterName := fs.String("cluster", "", "")
	namespaceName := fs.String("namespace", "", "")
	grantPeer := fs.String("peer", "", "")
	ttl := fs.Duration("ttl", 7*24*time.Hour, "")
	pollOnly := fs.Bool("poll", false, "")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if serviceArg == "" || !strings.HasPrefix(serviceArg, "service/") {
		return errors.New("usage: tubo grants request service/<name> --peer <multiaddr>")
	}
	serviceName := strings.TrimPrefix(serviceArg, "service/")
	cfg, err := loadLocalConfigOrError(*configPath)
	if err != nil {
		return err
	}
	if *clusterName != "" {
		cfg.CurrentCluster = *clusterName
	}
	if *namespaceName != "" {
		cfg.CurrentNamespace = *namespaceName
	}
	cfg.Service.Name = serviceName
	cfg.Service.Target = "http://127.0.0.1:1"
	cfg, svc, err := ensureAttachServiceIdentity(*configPath, cfg)
	if err != nil {
		return err
	}
	cluster := cfg.Clusters[cfg.CurrentCluster]
	if *grantPeer == "" {
		*grantPeer = svc.GrantServicePeer
	}
	if *grantPeer == "" {
		*grantPeer = clusterGrantServicePeer(cluster)
	}
	if *grantPeer == "" {
		return errors.New("missing grant service peer; pass --peer <multiaddr>")
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return err
	}
	overlay, err := p2p.NewOverlayHost(p2p.OverlayHostConfig{Listen: "/ip4/127.0.0.1/tcp/0", Seed: grantsFirstNonEmpty(cfg.Node.Seed, "grant-client-"+svc.ServiceSeed), PrivateKeyFile: cfg.Network.PrivateKeyFile, PrivateKeyB64: cfg.Network.PrivateKeyB64, BootstrapPeers: cfg.Network.BootstrapPeers, RelayPeers: cfg.Network.RelayPeers, Autorelay: cfg.Network.Autorelay, HolePunching: cfg.Network.HolePunching, ForceReachability: cfg.Network.ForceReachability, Component: "grants-client"})
	if err != nil {
		return err
	}
	defer overlay.Close()
	info, err := p2p.AddrInfoFromString(*grantPeer)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	overlay.StartBootstrapRetry(ctx, 5*time.Second)
	overlay.StartRelayReservations(ctx)
	var resp grantspkg.Message
	if *pollOnly {
		if svc.GrantRequestID == "" {
			return errors.New("no local grant request id recorded for service")
		}
		resp, err = grantspkg.Poll(ctx, overlay.Host, info, svc.GrantRequestID)
	} else {
		leaseReq, err := buildServicePublishLeaseRequest(*configPath, cfg, svc, servicePeerID.String())
		if err != nil {
			return err
		}
		submitResp, submitErr := grantspkg.Submit(ctx, overlay.Host, info, grantspkg.Message{Type: grantspkg.TypeSubmit, Version: grantspkg.VersionV1, ClusterID: cluster.ClusterID, NamespaceID: cfg.CurrentNamespace, ServiceName: serviceName, ServiceID: svc.ServiceID, ServiceKind: string(cfgpkg.NormalizeServiceKind(svc.Kind, "")), ServicePublicKey: leaseReq.ServicePublicKey, ServiceOwnerSignature: leaseReq.ServiceOwnerSignature, ServicePeerID: servicePeerID.String(), ServiceAddresses: serviceEndpointAddrsForTokens(cfg, servicePeerID.String()), RequestNonce: leaseReq.Nonce, RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, RequestedTTLSeconds: int64(ttl.Seconds())})
		if submitErr != nil {
			return submitErr
		}
		resp = submitResp
	}
	if err != nil {
		return err
	}
	_, err = handleGrantClientResponse(*configPath, cfg, svc, *grantPeer, resp, servicePeerID.String(), grantClientResponsePrimary)
	return err
}

type grantClientResponseMode int

const (
	grantClientResponsePrimary grantClientResponseMode = iota
	grantClientResponseInternal
)

func handleGrantClientResponse(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, grantPeer string, resp grantspkg.Message, servicePeerID string, mode grantClientResponseMode) (string, error) {
	cluster := cfg.Clusters[cfg.CurrentCluster]
	namespace := cluster.Namespaces[cfg.CurrentNamespace]
	switch resp.Type {
	case grantspkg.TypePending:
		svc.GrantRequestID = resp.RequestID
		svc.GrantServicePeer = grantPeer
		namespace.Services[cfg.Service.Name] = svc
		cluster.Namespaces[cfg.CurrentNamespace] = namespace
		cfg.Clusters[cfg.CurrentCluster] = cluster
		if err := saveLocalConfig(configPath, cfg); err != nil {
			return "", err
		}
		if mode == grantClientResponsePrimary {
			logging.Resultf("Grant request sent.\nRequest ID: %s\nStatus: pending\n", resp.RequestID)
		} else {
			logging.Progressf("publish authorization request pending: %s\n", resp.RequestID)
		}
		return "", nil
	case grantspkg.TypeApproved:
		if resp.ServiceClaim == nil && resp.PublishLease == nil {
			return "", errors.New("approved grant response missing publish authorization")
		}
		pub, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey)
		if err != nil {
			return "", err
		}
		if resp.PublishLease != nil {
			if err := grantspkg.VerifyPublishLease(*resp.PublishLease, pub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID); err != nil {
				return "", fmt.Errorf("approved publish lease rejected: %w", err)
			}
			if err := writePublishLeaseFile(svc.ServicePublishLeaseFile, *resp.PublishLease); err != nil {
				return "", err
			}
			if resp.ServiceClaim == nil {
				resp.ServiceClaim = &resp.PublishLease.ServiceClaim
			}
		}
		if resp.ServiceClaim != nil {
			if err := capability.VerifyServiceClaim(*resp.ServiceClaim, pub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID); err != nil {
				return "", fmt.Errorf("approved service claim rejected: %w", err)
			}
			if err := writeServiceClaimFile(svc.ServiceClaimFile, *resp.ServiceClaim); err != nil {
				return "", err
			}
		}
		svc.GrantServicePeer = grantPeer
		namespace.Services[cfg.Service.Name] = svc
		cluster.Namespaces[cfg.CurrentNamespace] = namespace
		cfg.Clusters[cfg.CurrentCluster] = cluster
		if err := saveLocalConfig(configPath, cfg); err != nil {
			return "", err
		}
		if resp.MembershipCapability != nil {
			membershipPath := serviceMembershipCapabilityPath(configPath, cfg.CurrentCluster, cfg.CurrentNamespace)
			if err := writeCapabilityFile(membershipPath, *resp.MembershipCapability); err != nil {
				return "", err
			}
		}
		if resp.ServiceShareToken != "" {
			if err := requireShareTokenEndpointForPublicDefault(cfg, resp.ServiceShareToken); err != nil {
				return "", err
			}
			if mode == grantClientResponsePrimary {
				logging.Resultf("Grant request approved.\nRequest ID: %s\nService claim saved: %s\nService publish lease saved: %s\nShare invite token: %s\n", resp.RequestID, svc.ServiceClaimFile, svc.ServicePublishLeaseFile, resp.ServiceShareToken)
			} else {
				logging.Progressf("publish authorization refreshed for service %q\n", cfg.Service.Name)
			}
		} else {
			if mode == grantClientResponsePrimary {
				logging.Resultf("Grant request approved.\nRequest ID: %s\nService claim saved: %s\nService publish lease saved: %s\n", resp.RequestID, svc.ServiceClaimFile, svc.ServicePublishLeaseFile)
			} else {
				logging.Progressf("publish authorization refreshed for service %q\n", cfg.Service.Name)
			}
		}
		return resp.ServiceShareToken, nil
	case grantspkg.TypeDenied:
		return "", fmt.Errorf("grant request %s denied: %s", resp.RequestID, resp.Reason)
	case grantspkg.TypeExpired:
		return "", fmt.Errorf("grant request %s expired: %s", resp.RequestID, resp.Reason)
	default:
		return "", fmt.Errorf("unexpected grant response type %q", resp.Type)
	}
}

func grantStoreFlagSet(name string, args []string) (*flag.FlagSet, *string, *string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	configPath := fs.String("config", "", "")
	storePath := fs.String("store", grantspkg.DefaultStorePath(), "")
	if err := fs.Parse(args); err != nil {
		return nil, nil, nil, err
	}
	return fs, configPath, storePath, nil
}

func grantsPendingCmd(args []string) error {
	_, _, storePath, err := grantStoreFlagSet("grants pending", args)
	if err != nil {
		return err
	}
	requests, err := grantspkg.NewStore(*storePath).ListPending()
	if err != nil {
		return err
	}
	printGrantRequests(requests)
	return nil
}

func grantsDescribeCmd(args []string) error {
	id, flagArgs := splitGrantIDArg(args)
	_, _, storePath, err := grantStoreFlagSet("grants describe", flagArgs)
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("usage: tubo grants describe <request-id>")
	}
	req, ok, err := grantspkg.NewStore(*storePath).Get(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("grant request %q not found", id)
	}
	printGrantRequest(req)
	return nil
}

func grantsHistoryCmd(args []string) error {
	_, _, storePath, err := grantStoreFlagSet("grants history", args)
	if err != nil {
		return err
	}
	requests, err := grantspkg.NewStore(*storePath).ListAll()
	if err != nil {
		return err
	}
	fmt.Printf("history source: authority/local store %s\n", *storePath)
	printGrantRequests(requests)
	return nil
}

func grantsDenyCmd(args []string) error {
	id, flagArgs := splitGrantIDArg(args)
	fs := flag.NewFlagSet("grants deny", flag.ContinueOnError)
	storePath := fs.String("store", grantspkg.DefaultStorePath(), "")
	reason := fs.String("reason", "denied by cluster authority", "")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if id == "" {
		return errors.New("usage: tubo grants deny <request-id>")
	}
	req, err := grantspkg.NewStore(*storePath).Deny(id, *reason)
	if err != nil {
		return err
	}
	fmt.Printf("denied grant request %s\n", req.ID)
	return nil
}

func grantsApproveCmd(args []string) error {
	id, flagArgs := splitGrantIDArg(args)
	fs := flag.NewFlagSet("grants approve", flag.ContinueOnError)
	configPath := fs.String("config", "", "")
	storePath := fs.String("store", grantspkg.DefaultStorePath(), "")
	ttl := fs.Duration("ttl", 7*24*time.Hour, "")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if id == "" {
		return errors.New("usage: tubo grants approve <request-id> --ttl 7d")
	}
	store := grantspkg.NewStore(*storePath)
	req, ok, err := store.Get(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("grant request %q not found", id)
	}
	cfg, err := loadLocalConfigOrError(*configPath)
	if err != nil {
		return err
	}
	cluster, ok := cfg.Clusters[req.ClusterName]
	if !ok {
		return fmt.Errorf("cluster %q not found in config", req.ClusterName)
	}
	if cluster.AuthorityPrivateKeyFile == "" {
		return fmt.Errorf("cluster %q is missing authority private key", req.ClusterName)
	}
	if err := ensureNoApprovedServiceCollision(store, req); err != nil {
		return err
	}
	priv, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return fmt.Errorf("load cluster authority key: %w", err)
	}
	artifacts, err := grantspkg.BuildApprovalArtifacts(priv, req.ClusterName, req.ClusterID, req.NamespaceID, req.ServiceName, req.ServiceID, req.ServicePeerID, req.ServiceKind, *ttl, grantspkg.ServiceShareDefaultTTL, req.RequestedPermissions, req.ServicePublicKey, req.RequestNonce, req.ServiceOwnerSignature)
	if err != nil {
		return err
	}
	approved, err := store.Approve(req.ID, artifacts.ServiceClaim, &artifacts.PublishLease, &artifacts.MembershipCapability, artifacts.ServiceShareToken)
	if err != nil {
		return err
	}
	fmt.Printf("approved grant request %s\n", approved.ID)
	if artifacts.ServiceShareToken != "" {
		fmt.Printf("share invite token: %s\n", artifacts.ServiceShareToken)
	}
	return nil
}

func ensureNoApprovedServiceCollision(store *grantspkg.Store, req grantspkg.Request) error {
	requests, err := store.ListAll()
	if err != nil {
		return err
	}
	for _, existing := range requests {
		if existing.ID == req.ID || existing.Status != grantspkg.StatusApproved {
			continue
		}
		if existing.ClusterID == req.ClusterID && existing.NamespaceID == req.NamespaceID && existing.ServiceID == req.ServiceID && existing.ServicePeerID != req.ServicePeerID {
			return fmt.Errorf("service %q in namespace %q is already approved for peer %s", req.ServiceID, req.NamespaceID, existing.ServicePeerID)
		}
	}
	return nil
}

func printGrantRequests(requests []grantspkg.Request) {
	sort.SliceStable(requests, func(i, j int) bool {
		if requests[i].ServiceID != requests[j].ServiceID {
			return requests[i].ServiceID < requests[j].ServiceID
		}
		return requests[i].RequestedAt.Before(requests[j].RequestedAt)
	})
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tSCOPE\tSERVICE\tSERVICE_ID\tREQUESTER\tSERVICE_PEER\tEXPIRES")
	for _, req := range requests {
		scope := req.ClusterName + "/" + req.NamespaceID
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", req.ID, req.Status, scope, req.ServiceName, req.ServiceID, req.RequesterPeerID, req.ServicePeerID, req.ExpiresAt.Format(time.RFC3339))
	}
	_ = w.Flush()
}

func printGrantRequest(req grantspkg.Request) {
	fmt.Printf("ID: %s\n", req.ID)
	fmt.Printf("Status: %s\n", req.Status)
	fmt.Printf("Cluster: %s (%s)\n", req.ClusterName, req.ClusterID)
	fmt.Printf("Namespace: %s\n", req.NamespaceID)
	fmt.Printf("Requester PeerID: %s\n", req.RequesterPeerID)
	fmt.Printf("Service: %s (%s)\n", req.ServiceName, req.ServiceID)
	fmt.Printf("Service Kind: %s\n", grantspkg.NormalizeServiceShareKind(req.ServiceKind))
	fmt.Printf("Service PeerID: %s\n", req.ServicePeerID)
	fmt.Printf("Permissions: %s\n", strings.Join(req.RequestedPermissions, ","))
	fmt.Printf("Status Expires: %s\n", req.ExpiresAt.Format(time.RFC3339))
	if req.DenialReason != "" {
		fmt.Printf("Denial Reason: %s\n", req.DenialReason)
	}
}

func splitGrantIDArg(args []string) (string, []string) {
	var id string
	flags := make([]string, 0, len(args))
	for _, arg := range args {
		if id == "" && !strings.HasPrefix(arg, "-") {
			id = arg
			continue
		}
		flags = append(flags, arg)
	}
	return id, flags
}

func grantsServeCmd(args []string) error {
	fs := flag.NewFlagSet("grants serve", flag.ContinueOnError)
	configPath := fs.String("config", "", "")
	clusterName := fs.String("cluster", "", "")
	namespaceName := fs.String("namespace", "", "")
	listen := fs.String("p2p-listen", "", "")
	seed := fs.String("seed", "", "")
	storePath := fs.String("store", grantspkg.DefaultStorePath(), "")
	revocationsPath := fs.String("revocations", grantspkg.DefaultRevocationStorePath(), "")
	autoApprove := fs.Bool("public-auto-approve", false, "")
	claimTTL := fs.Duration("claim-ttl", 24*time.Hour, "")
	shareTTL := fs.Duration("share-ttl", time.Hour, "")
	connectAccessTTL := fs.Duration("connect-access-ttl", grantspkg.DefaultConnectAccessLeaseTTL, "")
	connectRefreshTTL := fs.Duration("connect-refresh-ttl", grantspkg.DefaultConnectRefreshLeaseTTL, "")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadLocalConfigOrError(*configPath)
	if err != nil {
		return err
	}
	if *clusterName == "" {
		*clusterName = cfg.CurrentCluster
	}
	if *namespaceName == "" {
		*namespaceName = cfg.CurrentNamespace
	}
	if *clusterName == "" || *namespaceName == "" {
		return errors.New("grants serve requires a cluster and namespace context")
	}
	cluster, ok := cfg.Clusters[*clusterName]
	if !ok {
		return fmt.Errorf("cluster %q not found", *clusterName)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" || cluster.AuthorityPrivateKeyFile == "" {
		return fmt.Errorf("cluster %q is missing authority metadata", *clusterName)
	}
	if *listen == "" {
		*listen = cfg.Node.P2PListen
	}
	if *listen == "" {
		*listen = "/ip4/0.0.0.0/tcp/0"
	}
	if *seed == "" {
		*seed = cfg.Node.Seed
	}
	if *seed == "" {
		*seed = "grants-" + cluster.ClusterID
	}
	overlay, err := p2p.NewOverlayHost(p2p.OverlayHostConfig{Listen: *listen, Seed: *seed, PrivateKeyFile: cfg.Network.PrivateKeyFile, PrivateKeyB64: cfg.Network.PrivateKeyB64, BootstrapPeers: cfg.Network.BootstrapPeers, RelayPeers: cfg.Network.RelayPeers, Autorelay: cfg.Network.Autorelay, HolePunching: cfg.Network.HolePunching, ForceReachability: cfg.Network.ForceReachability, Component: "grants"})
	if err != nil {
		return err
	}
	defer overlay.Close()
	host := overlay.Host
	priv, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return fmt.Errorf("load cluster authority key: %w", err)
	}
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: *clusterName, ClusterID: cluster.ClusterID, NamespaceID: *namespaceName, Store: grantspkg.NewStore(*storePath), AutoApprove: *autoApprove, AuthorityPrivateKey: priv, ClaimTTL: *claimTTL, ServiceShareTTL: *shareTTL, GrantServicePeersProvider: func() []string {
		return grantServicePeersForTokens(overlay.ReachableAddrs())
	}, ConnectAccessTTL: *connectAccessTTL, ConnectRefreshTTL: *connectRefreshTTL, Revocations: grantspkg.NewRevocationStore(*revocationsPath)})
	if err != nil {
		return err
	}
	server.Register(host)
	state, cleanup, err := registerCurrentProcess(grantsServeProcessState(*clusterName, *namespaceName, *listen))
	if err != nil {
		return err
	}
	defer func() {
		if cleanup != nil {
			if err := cleanup(); err != nil {
				logging.Warnf("foreground process cleanup failed: %v\n", err)
			}
		}
	}()
	_ = state
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	overlay.StartBootstrapRetry(ctx, 5*time.Second)
	overlay.StartRelayReservations(ctx)
	scopedCfg := cfg
	scopedCfg.CurrentCluster = *clusterName
	scopedCfg.CurrentNamespace = *namespaceName
	if err := publishGrantServiceDiscovery(ctx, host, overlay, priv, scopedCfg, *claimTTL); err != nil {
		return err
	}
	fmt.Printf("grant service listening peer=%s protocol=%s store=%s\n", host.ID(), grantspkg.ProtocolID, *storePath)
	for _, addr := range overlay.ReachableAddrs() {
		if strings.Contains(addr, "/p2p-circuit") {
			fmt.Printf("relay addr: %s\n", addr)
			continue
		}
		fmt.Printf("addr: %s\n", addr)
	}
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)
	return nil
}

func publishGrantServiceDiscovery(ctx context.Context, h host.Host, overlay *p2p.OverlayHost, authorityPriv ed25519.PrivateKey, cfg cfgpkg.Config, claimTTL time.Duration) error {
	scope, err := cfgpkg.ResolveEffectiveScope(cfg, cfg.CurrentCluster, cfg.CurrentNamespace, false)
	if err != nil {
		return err
	}
	policy := cfgpkg.EffectiveScopePolicy(cfg, scope)
	if policy.Discovery == cfgpkg.NamespaceDiscoveryDisabled {
		logging.Warnf("grant service discovery publication disabled for scope %s/%s; grant-service will not be discoverable via `tubo get services --system`\n", cfg.CurrentCluster, cfg.CurrentNamespace)
		return nil
	}
	runtime, err := cfg.RequireDiscoveryRuntime()
	if err != nil {
		return fmt.Errorf("grant service discovery publication requires a valid discovery runtime for scope %s/%s: %w", cfg.CurrentCluster, cfg.CurrentNamespace, err)
	}
	if runtime.Context == nil {
		return fmt.Errorf("grant service discovery publication requires a discovery context for scope %s/%s", cfg.CurrentCluster, cfg.CurrentNamespace)
	}
	gs, err := pubsub.NewGossipSub(ctx, h, pubsub.WithFloodPublish(true))
	if err != nil {
		return err
	}
	topic, err := gs.Join(runtime.Topic)
	if err != nil {
		return err
	}
	priv := h.Peerstore().PrivKey(h.ID())
	if priv == nil {
		return fmt.Errorf("no private key for peer")
	}
	publisher := discovery.NewPublisher(topic, priv)
	publish := func() error {
		service, ann, err := buildGrantServiceDiscoveryArtifacts(runtime, h, overlay, authorityPriv, claimTTL)
		if err != nil {
			return err
		}
		if err := publisher.PublishV3(ctx, ann); err != nil {
			return err
		}
		syncGrantServiceAnnouncementToPeers(ctx, h, cfg, service)
		return nil
	}
	if err := publish(); err != nil {
		return err
	}
	refreshEvery := grantServiceDiscoveryRefreshInterval(claimTTL)
	go func() {
		ticker := time.NewTicker(refreshEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := publish(); err != nil {
					logging.Warnf("grant service discovery publish failed: %v\n", err)
				}
			}
		}
	}()
	return nil
}

func grantServiceDiscoveryRefreshInterval(claimTTL time.Duration) time.Duration {
	interval := claimTTL / 2
	switch {
	case interval <= 0:
		interval = 5 * time.Second
	case interval > 5*time.Second:
		interval = 5 * time.Second
	case interval < 2*time.Second:
		interval = 2 * time.Second
	}
	return interval
}

func buildGrantServiceDiscoveryArtifacts(runtime cfgpkg.DiscoveryRuntime, h host.Host, overlay *p2p.OverlayHost, authorityPriv ed25519.PrivateKey, claimTTL time.Duration) (discoveryquery.Service, discovery.AnnouncementV3, error) {
	if runtime.Context == nil {
		return discoveryquery.Service{}, discovery.AnnouncementV3{}, fmt.Errorf("missing discovery context for namespace %s/%s", runtime.ClusterID, runtime.NamespaceID)
	}
	pubKey := h.Peerstore().PubKey(h.ID())
	if pubKey == nil {
		return discoveryquery.Service{}, discovery.AnnouncementV3{}, fmt.Errorf("missing public key for peer %s", h.ID())
	}
	rawPub, err := pubKey.Raw()
	if err != nil {
		return discoveryquery.Service{}, discovery.AnnouncementV3{}, err
	}
	servicePub := ed25519.PublicKey(rawPub)
	serviceID := serviceidentity.ServiceIDFromPublicKey(servicePub)
	if claimTTL <= 0 {
		claimTTL = 24 * time.Hour
	}
	now := time.Now().UTC()
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: runtime.ClusterID, NamespaceID: runtime.NamespaceID, SubjectPeerID: h.ID().String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}, ExpiresAt: now.Add(claimTTL)}, authorityPriv)
	if err != nil {
		return discoveryquery.Service{}, discovery.AnnouncementV3{}, err
	}
	serviceClaim, err := capability.SignServiceClaim(capability.ServiceClaim{ClusterID: runtime.ClusterID, NamespaceID: runtime.NamespaceID, ServiceID: serviceID, SubjectPeerID: h.ID().String(), Permissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, ExpiresAt: now.Add(claimTTL)}, authorityPriv)
	if err != nil {
		return discoveryquery.Service{}, discovery.AnnouncementV3{}, err
	}
	addrs := append([]string(nil), overlay.ReachableAddrs()...)
	grantPeers := grantServicePeersForTokens(addrs)
	payload := discovery.AnnouncementV3Payload{ClusterID: runtime.ClusterID, NamespaceID: runtime.NamespaceID, Kind: discovery.ResourceKindGrantService, ServiceName: "grant-service", ServiceKind: "grant-service", ServiceID: serviceID, ServicePublicKey: serviceidentity.EncodePublicKey(servicePub), ConnectPolicy: "system", GrantService: &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: append([]string(nil), grantPeers...)}, Addresses: addrs, MembershipCapability: mustMarshalJSON(membership), ServiceClaim: mustMarshalJSON(serviceClaim), Capabilities: []string{"grant-service"}, RegisteredAt: now}
	service := discoveryquery.Service{Kind: discovery.ResourceKindGrantService, ClusterID: runtime.ClusterID, NamespaceID: runtime.NamespaceID, ServiceKind: "grant-service", Name: "grant-service", ServiceID: serviceID, ServicePublicKey: serviceidentity.EncodePublicKey(servicePub), ConnectPolicy: "system", GrantService: grantspkg.CloneGrantServiceEndpoint(payload.GrantService), PeerID: h.ID().String(), Addresses: append([]string(nil), addrs...), Status: "online", Path: "unknown", TTLSeconds: int64(claimTTL.Seconds()), Capabilities: []string{"grant-service"}, RegisteredAt: now.Format(time.RFC3339)}
	ann, err := discovery.NewAnnouncementV3(*runtime.Context, h.ID(), claimTTL, payload)
	if err != nil {
		return discoveryquery.Service{}, discovery.AnnouncementV3{}, err
	}
	return service, ann, nil
}

func syncGrantServiceAnnouncementToPeers(ctx context.Context, h host.Host, cfg cfgpkg.Config, service discoveryquery.Service) {
	peers := append([]string(nil), cfg.Network.BootstrapPeers...)
	peers = append(peers, cfg.Network.RelayPeers...)
	seen := make(map[string]struct{}, len(peers))
	for _, raw := range peers {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			continue
		}
		if _, err := discoveryquery.AnnounceService(ctx, h, info, service); err != nil {
			logging.Warnf("grant service discovery announce failed peer=%s: %v\n", info.ID, err)
		} else {
			logging.Progressf("grant service discovery announced peer=%s service=%s\n", info.ID, service.ServiceID)
		}
	}
}

func mustMarshalJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func grantsServeProcessState(clusterName, namespaceName, listen string) detachedProcessState {
	name := "grants-serve-" + sanitizeProcessName(clusterName+"-"+namespaceName)
	return detachedProcessState{
		ID:        "process/" + name,
		Kind:      "process",
		Command:   "grants serve",
		Name:      name,
		Cluster:   clusterName,
		Namespace: namespaceName,
		Local:     listen,
		LogFile:   filepath.Join(processLogDir(), name+".log"),
		StateFile: filepath.Join(processStateDir(), name+".json"),
		PIDFile:   filepath.Join(processRunDir(), name+".pid"),
	}
}

// detachGrantsServeCommand launches "tubo grants serve" as a detached background
// process, routing stdout+stderr to a log file readable via "tubo logs".
func detachGrantsServeCommand(args []string) error {
	// Parse only the flags we need to build the process state name and log path.
	// The full flag set is re-parsed by the detached child.
	fs := flag.NewFlagSet("grants serve (detach)", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", "", "")
	clusterName := fs.String("cluster", "", "")
	namespaceName := fs.String("namespace", "", "")
	listen := fs.String("p2p-listen", "/ip4/0.0.0.0/tcp/0", "")
	// Ignore unknown flags — pass them through to the child as-is.
	_ = fs.Parse(args)
	if *clusterName == "" || *namespaceName == "" {
		cfg, err := loadLocalConfigOrError(*configPath)
		if err == nil {
			if *clusterName == "" {
				*clusterName = cfg.CurrentCluster
			}
			if *namespaceName == "" {
				*namespaceName = cfg.CurrentNamespace
			}
		}
	}
	if *clusterName == "" || *namespaceName == "" {
		return errors.New("grants serve requires a cluster and namespace context (--cluster / --namespace or a config with current_cluster set)")
	}
	state := grantsServeProcessState(*clusterName, *namespaceName, *listen)
	spec := detachedSpec{
		State:     state,
		ChildArgs: append([]string{"grants", "serve"}, args...),
	}
	started, err := startDetachedProcess(spec)
	if err != nil {
		return err
	}
	printDetachedSummary("grants serve", started)
	return nil
}
