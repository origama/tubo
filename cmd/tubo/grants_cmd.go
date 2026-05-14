package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	default:
		return fmt.Errorf("unknown grants command %q", args[0])
	}
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
	psk, _, err := p2p.LoadPrivateNetworkPSK(cfg.Network.PrivateKeyFile, cfg.Network.PrivateKeyB64)
	if err != nil {
		return err
	}
	host, err := p2p.NewHostWithSeedAndPSK(*listen, *seed, psk)
	if err != nil {
		return err
	}
	defer host.Close()
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: *clusterName, ClusterID: cluster.ClusterID, NamespaceID: *namespaceName, Store: grantspkg.NewStore(*storePath)})
	if err != nil {
		return err
	}
	server.Register(host)
	fmt.Printf("grant service listening peer=%s protocol=%s store=%s\n", host.ID(), grantspkg.ProtocolID, *storePath)
	for _, addr := range p2p.PeerAddrs(host) {
		fmt.Printf("addr: %s\n", addr)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)
	return nil
}
