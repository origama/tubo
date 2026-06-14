package main

import (
	"context"
	"os"
	"strings"

	bridge "github.com/origama/tubo/internal/app/bridge"
	edge "github.com/origama/tubo/internal/app/edge"
	relay "github.com/origama/tubo/internal/app/relay"
	service "github.com/origama/tubo/internal/app/service"
	cfgpkg "github.com/origama/tubo/internal/config"
	launcher "github.com/origama/tubo/internal/launcher"
	logging "github.com/origama/tubo/internal/logging"
)

type runtimeLauncher struct{}

func newRuntimeLauncher() runtimeLauncher { return runtimeLauncher{} }

func (runtimeLauncher) ResolveAttachAuthorization(configPath string, cfg cfgpkg.Config) (launcher.AttachAuthorization, error) {
	authz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		return launcher.AttachAuthorization{}, err
	}
	return launcher.AttachAuthorization{
		Config:                   authz.Config,
		Service:                  authz.Service,
		ServicePeerID:            authz.ServicePeerID,
		ServiceClaimFile:         authz.ServiceClaimFile,
		ServicePublishLeaseFile:  authz.ServicePublishLeaseFile,
		MembershipCapabilityFile: authz.MembershipCapabilityFile,
	}, nil
}

func (runtimeLauncher) PrintAttachShareHint(cfg cfgpkg.Config, auth launcher.AttachAuthorization) {
	printAttachShareHint(cfg, attachAuthorization{
		Config:                   auth.Config,
		Service:                  auth.Service,
		ServicePeerID:            auth.ServicePeerID,
		ServiceClaimFile:         auth.ServiceClaimFile,
		ServicePublishLeaseFile:  auth.ServicePublishLeaseFile,
		MembershipCapabilityFile: auth.MembershipCapabilityFile,
	})
}

func (runtimeLauncher) StartAttachPublishLeaseRenewal(ctx context.Context, configPath string, cfg cfgpkg.Config, svc cfgpkg.NamespaceService, servicePeerID string) service.PublishAuthorizationHandler {
	return startAttachPublishLeaseRenewal(ctx, configPath, cfg, svc, servicePeerID)
}

func (runtimeLauncher) NewEdge(ctx context.Context, cfg edge.Config) (launcher.Runner, error) {
	return edge.New(ctx, cfg)
}

func (runtimeLauncher) NewService(ctx context.Context, cfg service.Config) (launcher.Runner, error) {
	if stateFile := strings.TrimSpace(os.Getenv("TUBO_PROCESS_STATE_FILE")); stateFile != "" {
		cfg.StatusReporter = func(status service.RuntimeStatus) {
			if err := updateAttachServiceRuntimeState(stateFile, status); err != nil {
				logging.Warnf("attach runtime status update failed: %v\n", err)
			}
		}
	}
	return service.New(ctx, cfg)
}

func (runtimeLauncher) NewRelay(ctx context.Context, cfg relay.Config) (launcher.Runner, error) {
	return relay.New(ctx, cfg)
}

func (runtimeLauncher) NewBridge(ctx context.Context, cfg bridge.Config) (launcher.Runner, error) {
	return bridge.New(ctx, cfg)
}
