package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
)

func grantsCmd(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: tubo grants <serve|pending|describe|approve|deny|history>")
	}
	switch args[0] {
	case "serve":
		return grantsServeCmd(args[1:])
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
		resp, err = grantspkg.Submit(ctx, overlay.Host, info, grantspkg.Message{Type: grantspkg.TypeSubmit, Version: grantspkg.VersionV1, ClusterID: cluster.ClusterID, NamespaceID: cfg.CurrentNamespace, ServiceName: serviceName, ServiceID: svc.ServiceID, ServicePeerID: servicePeerID.String(), RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, RequestedTTLSeconds: int64(ttl.Seconds())})
	}
	if err != nil {
		return err
	}
	return handleGrantClientResponse(*configPath, cfg, svc, *grantPeer, resp, servicePeerID.String())
}

func handleGrantClientResponse(configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, grantPeer string, resp grantspkg.Message, servicePeerID string) error {
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
			return err
		}
		fmt.Printf("Grant request sent.\nRequest ID: %s\nStatus: pending\n", resp.RequestID)
		return nil
	case grantspkg.TypeApproved:
		if resp.ServiceClaim == nil {
			return errors.New("approved grant response missing service claim")
		}
		pub, err := discovery.ParseAuthorityPublicKey(cluster.AuthorityPublicKey)
		if err != nil {
			return err
		}
		if err := capability.VerifyServiceClaim(*resp.ServiceClaim, pub, cluster.ClusterID, cfg.CurrentNamespace, svc.ServiceID, servicePeerID); err != nil {
			return fmt.Errorf("approved service claim rejected: %w", err)
		}
		if err := writeServiceClaimFile(svc.ServiceClaimFile, *resp.ServiceClaim); err != nil {
			return err
		}
		fmt.Printf("Grant request approved.\nRequest ID: %s\nService claim saved: %s\n", resp.RequestID, svc.ServiceClaimFile)
		return nil
	case grantspkg.TypeDenied:
		return fmt.Errorf("grant request %s denied: %s", resp.RequestID, resp.Reason)
	case grantspkg.TypeExpired:
		return fmt.Errorf("grant request %s expired: %s", resp.RequestID, resp.Reason)
	default:
		return fmt.Errorf("unexpected grant response type %q", resp.Type)
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
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{
		ClusterID:     req.ClusterID,
		NamespaceID:   req.NamespaceID,
		ServiceID:     req.ServiceID,
		SubjectPeerID: req.ServicePeerID,
		Permissions:   []string{capability.PermissionAttach, capability.PermissionAnnounce},
		ExpiresAt:     time.Now().UTC().Add(*ttl),
	}, priv)
	if err != nil {
		return err
	}
	approved, err := store.Approve(req.ID, claim)
	if err != nil {
		return err
	}
	fmt.Printf("approved grant request %s\n", approved.ID)
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
		if existing.ClusterID == req.ClusterID && existing.NamespaceID == req.NamespaceID && existing.ServiceName == req.ServiceName && existing.ServicePeerID != req.ServicePeerID {
			return fmt.Errorf("service %q in namespace %q is already approved for peer %s", req.ServiceName, req.NamespaceID, existing.ServicePeerID)
		}
	}
	return nil
}

func printGrantRequests(requests []grantspkg.Request) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tNAMESPACE\tSERVICE\tREQUESTER\tSERVICE_PEER\tEXPIRES")
	for _, req := range requests {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", req.ID, req.Status, req.NamespaceID, req.ServiceName, req.RequesterPeerID, req.ServicePeerID, req.ExpiresAt.Format(time.RFC3339))
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
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: *clusterName, ClusterID: cluster.ClusterID, NamespaceID: *namespaceName, Store: grantspkg.NewStore(*storePath)})
	if err != nil {
		return err
	}
	server.Register(host)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	overlay.StartBootstrapRetry(ctx, 5*time.Second)
	overlay.StartRelayReservations(ctx)
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
