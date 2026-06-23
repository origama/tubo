package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
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
		return errors.New("usage: tubo grants <serve|request|pending|describe|approve|deny|history>")
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
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		return err
	}
	_, _, _, err = requestPublishGrant(*configPath, cfg, svc, servicePeerID.String(), grantRequestOptions{explicitPeer: strings.TrimSpace(*grantPeer), pollOnly: *pollOnly, requestedTTL: *ttl, responseMode: grantClientResponsePrimary})
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
			if resp.ServiceClaim == nil {
				resp.ServiceClaim = &resp.PublishLease.ServiceClaim
			}
		}
		if resp.ServiceClaim != nil {
			if err := capability.VerifyServiceClaim(*resp.ServiceClaim, pub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID); err != nil {
				return "", fmt.Errorf("approved service claim rejected: %w", err)
			}
		}
		if resp.MembershipCapability != nil {
			if err := capability.VerifyMembershipCapability(*resp.MembershipCapability, pub, cluster.ClusterID, cfg.CurrentNamespace, servicePeerID); err != nil {
				return "", fmt.Errorf("approved membership capability rejected: %w", err)
			}
		}
		if resp.PublishLease != nil {
			if err := writePublishLeaseFile(svc.ServicePublishLeaseFile, *resp.PublishLease); err != nil {
				return "", err
			}
		}
		if resp.ServiceClaim != nil {
			if err := writeServiceClaimFile(svc.ServiceClaimFile, *resp.ServiceClaim); err != nil {
				return "", err
			}
		}
		if resp.MembershipCapability != nil {
			membershipPath := serviceMembershipCapabilityPath(configPath, cfg.CurrentCluster, cfg.CurrentNamespace)
			if err := writeCapabilityFile(membershipPath, *resp.MembershipCapability); err != nil {
				return "", err
			}
		}
		svc.GrantServicePeer = grantPeer
		svc.GrantRequestID = ""
		namespace.Services[cfg.Service.Name] = svc
		cluster.Namespaces[cfg.CurrentNamespace] = namespace
		cfg.Clusters[cfg.CurrentCluster] = cluster
		if err := saveLocalConfig(configPath, cfg); err != nil {
			return "", err
		}
		if resp.ServiceShareToken != "" {
			if err := requireShareTokenEndpointForPublicDefault(cfg, resp.ServiceShareToken); err != nil {
				return "", err
			}
			if mode == grantClientResponsePrimary {
				logging.Resultf("Grant request approved.\nRequest ID: %s\nService claim saved: %s\nService publish lease saved: %s\nShare invite token: %s\n", resp.RequestID, svc.ServiceClaimFile, svc.ServicePublishLeaseFile, resp.ServiceShareToken)
			} else {
				logging.Progressf("publish authorization request approved: %s; publish authorization refreshed for service %q\n", resp.RequestID, cfg.Service.Name)
			}
		} else {
			if mode == grantClientResponsePrimary {
				logging.Resultf("Grant request approved.\nRequest ID: %s\nService claim saved: %s\nService publish lease saved: %s\n", resp.RequestID, svc.ServiceClaimFile, svc.ServicePublishLeaseFile)
			} else {
				logging.Progressf("publish authorization request approved: %s; publish authorization refreshed for service %q\n", resp.RequestID, cfg.Service.Name)
			}
		}
		return resp.ServiceShareToken, nil
	case grantspkg.TypeDenied:
		if mode == grantClientResponsePrimary {
			logging.Progressf("publish authorization request denied: %s (%s)\n", resp.RequestID, resp.Reason)
		} else {
			logging.Progressf("publish authorization request denied: %s (%s)\n", resp.RequestID, resp.Reason)
		}
		return "", fmt.Errorf("grant request %s denied: %s", resp.RequestID, resp.Reason)
	case grantspkg.TypeExpired:
		svc.GrantRequestID = ""
		namespace.Services[cfg.Service.Name] = svc
		cluster.Namespaces[cfg.CurrentNamespace] = namespace
		cfg.Clusters[cfg.CurrentCluster] = cluster
		if err := saveLocalConfig(configPath, cfg); err != nil {
			return "", err
		}
		if mode == grantClientResponsePrimary {
			logging.Progressf("publish authorization request expired and cleared: %s\n", resp.RequestID)
		} else {
			logging.Progressf("publish authorization request expired and cleared: %s\n", resp.RequestID)
		}
		return "", fmt.Errorf("grant request %s expired: %s", resp.RequestID, resp.Reason)
	default:
		return "", fmt.Errorf("unexpected grant response type %q", resp.Type)
	}
}

func grantViewFlagSet(name string, args []string) (*flag.FlagSet, *string, *string, *bool, *bool, *bool, *bool, *bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	configPath := fs.String("config", "", "")
	storePath := fs.String("store", grantspkg.DefaultStorePath(), "")
	wide := fs.Bool("wide", false, "")
	jsonOut := fs.Bool("json", false, "")
	all := fs.Bool("all", false, "")
	verbose := fs.Bool("verbose", false, "")
	verboseShort := fs.Bool("v", false, "")
	if err := fs.Parse(args); err != nil {
		return nil, nil, nil, nil, nil, nil, nil, nil, err
	}
	return fs, configPath, storePath, wide, jsonOut, all, verbose, verboseShort, nil
}

func grantsPendingCmd(args []string) error {
	_, _, storePath, wide, jsonOut, _, verbose, verboseShort, err := grantViewFlagSet("grants pending", args)
	if err != nil {
		return err
	}
	requests, err := grantspkg.NewStore(*storePath).ListPending()
	if err != nil {
		return err
	}
	if *jsonOut {
		aliasIdx, _ := loadPeerAliasIndex()
		return printGrantListJSON("pending", *storePath, requests, summarizeGrantRequests(requests, aliasIdx))
	}
	if *wide {
		printGrantRequestsWide(requests, "Pending grant requests", *storePath, "source:")
		return nil
	}
	aliasIdx, _ := loadPeerAliasIndex()
	printGrantPendingHuman(requests, "Pending grant requests", aliasIdx, *verbose || *verboseShort)
	return nil
}

func grantsDescribeCmd(args []string) error {
	id, flagArgs := splitGrantIDArg(args)
	_, _, storePath, wide, jsonOut, _, _, _, err := grantViewFlagSet("grants describe", flagArgs)
	if err != nil {
		return err
	}
	if id == "" {
		return errors.New("usage: tubo grants describe <request-id>")
	}
	store := grantspkg.NewStore(*storePath)
	req, ok, err := store.Get(id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("grant request %q not found", id)
	}
	all, err := store.ListAll()
	if err != nil {
		return err
	}
	related := relatedGrantRequests(all, req)
	if *jsonOut {
		aliasIdx, _ := loadPeerAliasIndex()
		group := summarizeGrantRequests(related, aliasIdx)
		groupView := grantRequestGroup{}
		if len(group) > 0 {
			groupView = group[0]
		}
		return printGrantDescribeJSON(*storePath, req, groupView, related, aliasIdx.name(req.RequesterPeerID))
	}
	if *wide {
		printGrantRequestWide(req)
		return nil
	}
	printGrantRequestReview(req, related, *storePath)
	return nil
}

func grantsHistoryCmd(args []string) error {
	_, _, storePath, wide, jsonOut, all, verbose, verboseShort, err := grantViewFlagSet("grants history", args)
	if err != nil {
		return err
	}
	requests, err := grantspkg.NewStore(*storePath).ListAll()
	if err != nil {
		return err
	}
	if *jsonOut {
		aliasIdx, _ := loadPeerAliasIndex()
		return printGrantListJSON("history", *storePath, requests, summarizeGrantRequests(requests, aliasIdx))
	}
	if *wide {
		printGrantRequestsWide(requests, "Grant request history", *storePath, "history source:")
		return nil
	}
	printGrantHistoryHuman(requests, "Grant history", *storePath, *all, *verbose || *verboseShort)
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

type trackedDurationFlag struct {
	value time.Duration
	set   bool
}

func (f *trackedDurationFlag) String() string {
	return f.value.String()
}

func (f *trackedDurationFlag) Set(s string) error {
	d, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	f.value = d
	f.set = true
	return nil
}

func rejectAmbiguousGrantTTL(args []string) error {
	for _, arg := range args {
		switch {
		case arg == "--ttl", strings.HasPrefix(arg, "--ttl="), arg == "-ttl", strings.HasPrefix(arg, "-ttl="):
			return errors.New("--ttl is ambiguous; use --claim-ttl and optionally --publish-lease-ttl / --share-ttl")
		}
	}
	return nil
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 || (b > 0 && b < a) {
		return b
	}
	return a
}

func grantsApproveCmd(args []string) error {
	id, flagArgs := splitGrantIDArg(args)
	if err := rejectAmbiguousGrantTTL(flagArgs); err != nil {
		return err
	}
	fs := flag.NewFlagSet("grants approve", flag.ContinueOnError)
	configPath := fs.String("config", "", "")
	storePath := fs.String("store", grantspkg.DefaultStorePath(), "")
	claimTTL := &trackedDurationFlag{value: 7 * 24 * time.Hour}
	publishLeaseTTL := &trackedDurationFlag{}
	shareTTL := &trackedDurationFlag{}
	fs.Var(claimTTL, "claim-ttl", "")
	fs.Var(publishLeaseTTL, "publish-lease-ttl", "")
	fs.Var(shareTTL, "share-ttl", "")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if id == "" {
		return errors.New("usage: tubo grants approve <request-id> --claim-ttl 7d")
	}
	if claimTTL.value <= 0 {
		return errors.New("--claim-ttl must be greater than 0")
	}
	if !publishLeaseTTL.set {
		publishLeaseTTL.value = claimTTL.value
	}
	if publishLeaseTTL.value <= 0 {
		return errors.New("--publish-lease-ttl must be greater than 0")
	}
	if publishLeaseTTL.value > claimTTL.value {
		return fmt.Errorf("--publish-lease-ttl %s cannot be greater than --claim-ttl %s", publishLeaseTTL.value, claimTTL.value)
	}
	if !shareTTL.set {
		shareTTL.value = minDuration(grantspkg.ServiceShareDefaultTTL, publishLeaseTTL.value)
	}
	if shareTTL.value <= 0 {
		return errors.New("--share-ttl must be greater than 0")
	}
	if shareTTL.value > publishLeaseTTL.value {
		return fmt.Errorf("--share-ttl %s cannot be greater than --publish-lease-ttl %s", shareTTL.value, publishLeaseTTL.value)
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
	artifacts, err := buildManualApprovalArtifacts(priv, req, claimTTL.value, publishLeaseTTL.value, shareTTL.value)
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

func buildManualApprovalArtifacts(priv ed25519.PrivateKey, req grantspkg.Request, claimTTL, publishLeaseTTL, shareTTL time.Duration) (grantspkg.ApprovalArtifacts, error) {
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(priv, grantspkg.PublishLeaseRequest{
		Version:               grantspkg.PublishLeaseVersion,
		Kind:                  grantspkg.PublishLeaseRequestKind,
		ClusterID:             req.ClusterID,
		NamespaceID:           req.NamespaceID,
		ServiceID:             req.ServiceID,
		ServicePublicKey:      req.ServicePublicKey,
		PublisherPeerID:       req.ServicePeerID,
		RequestedCapabilities: append([]string(nil), req.RequestedPermissions...),
		Nonce:                 req.RequestNonce,
		ServiceOwnerSignature: append([]byte(nil), req.ServiceOwnerSignature...),
	}, req.ServiceName, claimTTL, publishLeaseTTL)
	if err != nil {
		return grantspkg.ApprovalArtifacts{}, err
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     req.ClusterID,
		NamespaceID:   req.NamespaceID,
		SubjectPeerID: req.ServicePeerID,
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
		},
		ExpiresAt: leaseArtifacts.ServiceClaim.ExpiresAt,
	}, priv)
	if err != nil {
		return grantspkg.ApprovalArtifacts{}, err
	}
	shareArtifacts, err := grantspkg.BuildShareInviteArtifactsFromLeaseWithEndpoints(priv, req.ClusterName, leaseArtifacts.Lease, req.ServiceName, shareTTL, nil, req.ServicePeerID, nil)
	if err != nil {
		shareArtifacts, err = grantspkg.BuildServiceShareArtifactsWithEndpoints(priv, req.ClusterName, req.ClusterID, req.NamespaceID, req.ServiceName, req.ServiceID, shareTTL, nil, req.ServicePeerID, nil)
		if err != nil {
			return grantspkg.ApprovalArtifacts{}, err
		}
	}
	shareArtifacts.Token, err = grantspkg.ReissueServiceShareTokenWithKind(shareArtifacts.Token, priv, req.ServiceKind)
	if err != nil {
		return grantspkg.ApprovalArtifacts{}, err
	}
	return grantspkg.ApprovalArtifacts{ServiceClaim: leaseArtifacts.ServiceClaim, PublishLease: leaseArtifacts.Lease, MembershipCapability: membership, ServiceShareToken: shareArtifacts.Token}, nil
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
	// Configure runtime logging (enables log.Printf output) when running as a
	// detached child process. Without this, the standard logger writes to
	// io.Discard because the non-runtime logging config set in main() has
	// Verbosity=0 and Runtime=false.
	if os.Getenv("TUBO_DETACHED_CHILD") == "1" {
		_ = logging.Configure(logging.Config{Verbosity: 1, Runtime: true})
	}
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
	logging.Warnf("grant service running in foreground; press Ctrl+C to stop\n")
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
	log.Printf("grants serve started pid=%d peer=%s protocol=%s store=%s", os.Getpid(), host.ID(), grantspkg.ProtocolID, *storePath)
	for _, addr := range overlay.ReachableAddrs() {
		if strings.Contains(addr, "/p2p-circuit") {
			log.Printf("relay addr: %s", addr)
			continue
		}
		log.Printf("addr: %s", addr)
	}
	<-ctx.Done()
	log.Printf("grants serve stopped")
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
					log.Printf("grant service discovery publish failed: %v", err)
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
			log.Printf("grant service discovery announce failed peer=%s: %v", info.ID, err)
		} else {
			log.Printf("grant service discovery announced peer=%s service=%s", info.ID, service.ServiceID)
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
		ID:           "process/" + name,
		Kind:         "process",
		Command:      "grants serve",
		Name:         name,
		Purpose:      "discovery-authority",
		Capabilities: processCapabilitiesForCommand("grants serve"),
		Cluster:      clusterName,
		Namespace:    namespaceName,
		Local:        listen,
		LogFile:      filepath.Join(processLogDir(), name+".log"),
		StateFile:    filepath.Join(processStateDir(), name+".json"),
		PIDFile:      filepath.Join(processRunDir(), name+".pid"),
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
