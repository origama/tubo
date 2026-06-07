package launcher

import (
	"context"
	"fmt"
	"time"

	bridge "github.com/origama/tubo/internal/app/bridge"
	edge "github.com/origama/tubo/internal/app/edge"
	relay "github.com/origama/tubo/internal/app/relay"
	service "github.com/origama/tubo/internal/app/service"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
)

type Runner interface {
	Start(context.Context) error
}

type AttachAuthorization struct {
	Config                   cfgpkg.Config
	Service                  cfgpkg.NamespaceService
	ServicePeerID            string
	ServiceClaimFile         string
	ServicePublishLeaseFile  string
	MembershipCapabilityFile string
}

type Deps interface {
	ResolveAttachAuthorization(configPath string, cfg cfgpkg.Config) (AttachAuthorization, error)
	PrintAttachShareHint(cfg cfgpkg.Config, auth AttachAuthorization)
	StartAttachPublishLeaseRenewal(ctx context.Context, configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string)
	NewEdge(context.Context, edge.Config) (Runner, error)
	NewService(context.Context, service.Config) (Runner, error)
	NewRelay(context.Context, relay.Config) (Runner, error)
	NewBridge(context.Context, bridge.Config) (Runner, error)
}

func Run(ctx context.Context, deps Deps, role, configPath string, cfg cfgpkg.Config) error {
	cluster := cfg.Clusters[cfg.CurrentCluster]
	switch role {
	case "edge":
		runtime, err := cfg.RequireDiscoveryRuntime()
		if err != nil {
			return err
		}
		runner, err := deps.NewEdge(ctx, edge.Config{
			HTTPListen:               cfg.Edge.Listen,
			P2PListen:                cfg.Node.P2PListen,
			Seed:                     cfg.Node.Seed,
			AdminListen:              cfg.Edge.AdminListen,
			BootstrapPeers:           cfg.Network.BootstrapPeers,
			RelayPeers:               cfg.Network.RelayPeers,
			BootstrapRetryInterval:   5 * time.Second,
			DirectStreamTimeout:      cfg.Edge.DirectStreamTimeout.Duration(),
			PrivateKeyFile:           cfg.Network.PrivateKeyFile,
			PrivateKeyB64:            cfg.Network.PrivateKeyB64,
			AuthorityPublicKey:       cluster.AuthorityPublicKey,
			DiscoveryTopic:           runtime.Topic,
			DiscoveryPreviousTopic:   runtime.PreviousTopic,
			DiscoveryMode:            runtime.Mode.String(),
			DiscoveryClusterID:       runtime.ClusterID,
			DiscoveryNamespaceID:     runtime.NamespaceID,
			DiscoveryContext:         runtime.Context,
			DiscoveryPreviousContext: runtime.PreviousContext,
		})
		if err != nil {
			return err
		}
		return runner.Start(ctx)
	case "service":
		scope, err := cfgpkg.ResolveEffectiveScope(cfg, "", "", false)
		if err != nil {
			return err
		}
		policy := cfgpkg.EffectiveScopePolicy(cfg, scope)
		discoveryEnabled := policy.Discovery != cfgpkg.NamespaceDiscoveryDisabled
		discoveryClusterID := cfg.Clusters[cfg.CurrentCluster].ClusterID
		discoveryNamespaceID := scope.Namespace
		discoveryTopic := ""
		discoveryMode := ""
		discoveryContext := (*discovery.NamespaceDiscoveryContext)(nil)
		discoveryPreviousTopic := ""
		discoveryPreviousContext := (*discovery.NamespaceDiscoveryContext)(nil)
		if discoveryEnabled {
			runtime, err := cfg.RequireDiscoveryRuntime()
			if err != nil {
				return err
			}
			discoveryClusterID = runtime.ClusterID
			discoveryNamespaceID = runtime.NamespaceID
			discoveryTopic = runtime.Topic
			discoveryMode = runtime.Mode.String()
			discoveryContext = runtime.Context
			discoveryPreviousTopic = runtime.PreviousTopic
			discoveryPreviousContext = runtime.PreviousContext
		}
		authz, err := deps.ResolveAttachAuthorization(configPath, cfg)
		if err != nil {
			return err
		}
		cfg = authz.Config
		cluster = cfg.Clusters[cfg.CurrentCluster]
		deps.PrintAttachShareHint(cfg, authz)
		deps.StartAttachPublishLeaseRenewal(ctx, configPath, cfg, authz.Service, authz.ServicePeerID)
		serviceKind := string(cfgpkg.NormalizeServiceKind(authz.Service.Kind, cfg.Service.Target))
		runner, err := deps.NewService(ctx, service.Config{
			Listen:                   cfg.Node.P2PListen,
			Seed:                     authz.Service.ServiceSeed,
			ServiceName:              cfg.Service.Name,
			ServiceKind:              serviceKind,
			ServiceID:                authz.Service.ServiceID,
			ServiceOwnerKeyFile:      authz.Service.ServiceOwnerKeyFile,
			Target:                   cfg.Service.Target,
			HealthListen:             cfg.HealthListen,
			PrivateKeyFile:           cfg.Network.PrivateKeyFile,
			PrivateKeyB64:            cfg.Network.PrivateKeyB64,
			BootstrapPeers:           cfg.Network.BootstrapPeers,
			RelayPeers:               cfg.Network.RelayPeers,
			Autorelay:                cfg.Network.Autorelay,
			HolePunching:             cfg.Network.HolePunching,
			ForceReachability:        cfg.Network.ForceReachability,
			HeartbeatInterval:        cfg.HeartbeatInterval.Duration(),
			BootstrapRetryInterval:   5 * time.Second,
			DiscoveryTopic:           discoveryTopic,
			DiscoveryPreviousTopic:   discoveryPreviousTopic,
			DiscoveryMode:            discoveryMode,
			DiscoveryClusterID:       discoveryClusterID,
			DiscoveryNamespaceID:     discoveryNamespaceID,
			DiscoveryContext:         discoveryContext,
			DiscoveryPreviousContext: discoveryPreviousContext,
			AuthorityPublicKey:       cluster.AuthorityPublicKey,
			AuthorityPrivateKeyFile:  cluster.AuthorityPrivateKeyFile,
			ClusterName:              cfg.CurrentCluster,
			ConnectPolicy:            string(policy.ConnectPolicy),
			DiscoveryEnabled:         discoveryEnabled,
			Visibility: func() string {
				if discoveryEnabled {
					return "discoverable"
				}
				return "unlisted"
			}(),
			MembershipCapabilityFile: authz.MembershipCapabilityFile,
			ServiceClaimFile:         authz.ServiceClaimFile,
			ServicePublishLeaseFile:  authz.ServicePublishLeaseFile,
		})
		if err != nil {
			return err
		}
		return runner.Start(ctx)
	case "relay":
		runner, err := deps.NewRelay(ctx, relay.Config{
			Listen:                  cfg.Node.P2PListen,
			Seed:                    cfg.Node.Seed,
			HealthListen:            cfg.Relay.HealthListen,
			PublicAddr:              cfg.Relay.PublicAddr,
			PrivateKeyFile:          cfg.Network.PrivateKeyFile,
			PrivateKeyB64:           cfg.Network.PrivateKeyB64,
			EnableRelayService:      cfg.Relay.EnableRelayService,
			EnableAutoNATService:    cfg.Relay.EnableAutoNATService,
			EnableDiscoveryPubSub:   cfg.Relay.EnableDiscoveryPubSub,
			ForceReachabilityPublic: cfg.Relay.ForceReachabilityPublic,
			PrintRunCommands:        cfg.Relay.PrintRunCommands,
			MaxReservations:         cfg.Relay.MaxReservations,
			MaxReservationsPerIP:    cfg.Relay.MaxReservationsPerIP,
			MaxReservationsPerASN:   cfg.Relay.MaxReservationsPerASN,
			MaxCircuitsPerPeer:      cfg.Relay.MaxCircuitsPerPeer,
			BufferSize:              cfg.Relay.BufferSize,
			ReservationTTL:          cfg.Relay.ReservationTTL.Duration(),
			LimitDuration:           cfg.Relay.LimitDuration.Duration(),
			LimitDataBytes:          cfg.Relay.LimitDataBytes,
		})
		if err != nil {
			return err
		}
		return runner.Start(ctx)
	case "bridge":
		runner, err := deps.NewBridge(ctx, bridge.Config{
			Listen:           cfg.Bridge.Listen,
			Seed:             cfg.Node.Seed,
			P2PListen:        cfg.Node.P2PListen,
			ServiceAddr:      cfg.Bridge.ServiceAddr,
			ServiceSeed:      cfg.Bridge.ServiceSeed,
			ServiceP2PListen: cfg.Bridge.ServiceP2PListen,
			PrivateKeyFile:   cfg.Network.PrivateKeyFile,
			PrivateKeyB64:    cfg.Network.PrivateKeyB64,
			RelayPeers:       cfg.Network.RelayPeers,
			Autorelay:        cfg.Network.Autorelay,
			HolePunching:     cfg.Network.HolePunching,
		})
		if err != nil {
			return err
		}
		return runner.Start(ctx)
	default:
		return fmt.Errorf("unsupported role %q", role)
	}
}
