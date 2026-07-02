package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	serviceapp "github.com/origama/tubo/internal/app/service"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
)

func TestAttachPublishAuthorizationCoordinatorHandleTriggersRenewForPublishLeaseBlocks(t *testing.T) {
	t.Parallel()
	reasons := []serviceapp.AnnouncementBlockReason{
		serviceapp.AnnouncementBlockedPublishLeaseMissing,
		serviceapp.AnnouncementBlockedPublishLeaseExpired,
		serviceapp.AnnouncementBlockedPublishLeaseInvalid,
	}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			calls := 0
			c := &attachPublishAuthorizationCoordinator{
				now:     func() time.Time { return time.Unix(100, 0) },
				backoff: 5 * time.Second,
				renew: func(string, cfgpkg.Config, cfgpkg.NamespaceService, string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
					calls++
					return cfgpkg.Config{}, cfgpkg.NamespaceService{}, "", nil
				},
			}
			res := c.handle(context.Background(), serviceapp.PublishAuthorizationRequest{Reason: reason})
			if res.Outcome != serviceapp.PublishAuthorizationOutcomeReady {
				t.Fatalf("expected ready outcome, got %#v", res)
			}
			if calls != 1 {
				t.Fatalf("expected one renewal attempt, got %d", calls)
			}
		})
	}
}

func TestAttachPublishAuthorizationCoordinatorHandleBackoffPreventsDuplicateRenew(t *testing.T) {
	t.Parallel()
	now := time.Unix(200, 0)
	calls := 0
	c := &attachPublishAuthorizationCoordinator{
		now:     func() time.Time { return now },
		backoff: 10 * time.Second,
		renew: func(string, cfgpkg.Config, cfgpkg.NamespaceService, string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
			calls++
			if calls == 1 {
				return cfgpkg.Config{}, cfgpkg.NamespaceService{}, "", errors.New("publish grant request \"gr_123\" is pending; publication requires an approved publish lease")
			}
			return cfgpkg.Config{}, cfgpkg.NamespaceService{}, "", nil
		},
	}
	res := c.handle(context.Background(), serviceapp.PublishAuthorizationRequest{Reason: serviceapp.AnnouncementBlockedPublishLeaseMissing})
	if res.Outcome != serviceapp.PublishAuthorizationOutcomePending {
		t.Fatalf("expected pending outcome, got %#v", res)
	}
	if calls != 1 {
		t.Fatalf("expected one renewal call, got %d", calls)
	}
	res = c.handle(context.Background(), serviceapp.PublishAuthorizationRequest{Reason: serviceapp.AnnouncementBlockedPublishLeaseMissing})
	if res.Outcome != serviceapp.PublishAuthorizationOutcomeSkipped {
		t.Fatalf("expected skipped during backoff, got %#v", res)
	}
	if calls != 1 {
		t.Fatalf("expected no duplicate renewal during backoff, got %d calls", calls)
	}
	now = now.Add(11 * time.Second)
	res = c.handle(context.Background(), serviceapp.PublishAuthorizationRequest{Reason: serviceapp.AnnouncementBlockedPublishLeaseMissing})
	if res.Outcome != serviceapp.PublishAuthorizationOutcomeReady {
		t.Fatalf("expected ready after backoff, got %#v", res)
	}
	if calls != 2 {
		t.Fatalf("expected second renewal after backoff, got %d calls", calls)
	}
}

func TestAttachPublishAuthorizationCoordinatorSkipsGrantLoopWhenMembershipStillBlocks(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := mintLocalServicePublishLease(cfg.Clusters["home"], "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	c := &attachPublishAuthorizationCoordinator{
		configPath:    configPath,
		cfg:           cfg,
		svc:           svc,
		servicePeerID: servicePeerID.String(),
		now:           func() time.Time { return time.Unix(300, 0) },
		backoff:       5 * time.Second,
		renew: func(string, cfgpkg.Config, cfgpkg.NamespaceService, string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
			calls++
			return cfgpkg.Config{}, cfgpkg.NamespaceService{}, "", nil
		},
	}
	res := c.handle(context.Background(), serviceapp.PublishAuthorizationRequest{Reason: serviceapp.AnnouncementBlockedMembershipCapabilityInvalid, Detail: "subject peer id mismatch: got \"member-peer\" want \"service-peer\""})
	if res.Outcome != serviceapp.PublishAuthorizationOutcomeSkipped {
		t.Fatalf("expected skipped outcome, got %#v", res)
	}
	if calls != 0 {
		t.Fatalf("expected no grant renewal call, got %d", calls)
	}
	if !strings.Contains(res.Message, "subject peer id mismatch") {
		t.Fatalf("expected exact membership error, got %q", res.Message)
	}
}

func TestResolveAttachAuthorizationPersistsServiceDefinition(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	if _, err := resolveAttachAuthorization(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	persisted, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := persisted.Clusters["home"].Namespaces["default"].Services["myapi"]; !ok {
		t.Fatalf("attach authorization did not persist service definition: %#v", persisted.Clusters["home"].Namespaces["default"].Services)
	}
}

func TestRefreshAttachAuthorizationMaterialPreservesRunningServiceDefinition(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	originalSeed := svc.ServiceSeed
	originalServiceID := svc.ServiceID

	loaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := loaded.Clusters["home"]
	ns := cluster.Namespaces["default"]
	delete(ns.Services, "myapi")
	cluster.Namespaces["default"] = ns
	loaded.Clusters["home"] = cluster
	if err := saveLocalConfig(configPath, loaded); err != nil {
		t.Fatal(err)
	}

	refreshedCfg, refreshedSvc, err := refreshAttachAuthorizationMaterial(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if refreshedSvc.ServiceSeed != originalSeed || refreshedSvc.ServiceID != originalServiceID {
		t.Fatalf("refreshed service identity changed: got seed=%q id=%q want seed=%q id=%q", refreshedSvc.ServiceSeed, refreshedSvc.ServiceID, originalSeed, originalServiceID)
	}
	persisted := refreshedCfg.Clusters["home"].Namespaces["default"].Services["myapi"]
	if persisted.ServiceSeed != originalSeed || persisted.ServiceID != originalServiceID {
		t.Fatalf("running service definition was not restored in config: %#v", persisted)
	}
}

func TestRenewAttachPublishAuthorizationMintsLocalLeaseForRunningPeer(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	runningPeer, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		t.Fatal(err)
	}
	staleSvc := svc
	staleSvc.ServiceSeed = "service-seed-that-does-not-match-running-peer"

	updatedCfg, updatedSvc, _, err := renewAttachPublishAuthorization(configPath, cfg, staleSvc, runningPeer.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyCurrentAttachPublishLease(updatedCfg, updatedSvc, runningPeer.String()); err != nil {
		t.Fatalf("renewed lease does not verify for running peer: %v", err)
	}
	active, err := grantspkg.NewStore(grantspkg.DefaultStorePath()).HasActivePublishLease(updatedCfg.Clusters[updatedCfg.CurrentCluster].ClusterID, updatedCfg.CurrentNamespace, updatedSvc.ServiceID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("local renewal did not record active publish authorization for cluster grant server")
	}
}

func TestClassifyPublishAuthorizationOutcome(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want serviceapp.PublishAuthorizationOutcome
	}{
		{name: "pending", err: errors.New("publish grant request \"gr_123\" is pending; publication requires an approved publish lease"), want: serviceapp.PublishAuthorizationOutcomePending},
		{name: "denied", err: errors.New("publish grant request denied by authority"), want: serviceapp.PublishAuthorizationOutcomeDenied},
		{name: "unreachable", err: errors.New("failed to dial grant service peer: connection refused"), want: serviceapp.PublishAuthorizationOutcomeUnreachable},
		{name: "retryable", err: errors.New("unexpected libp2p stream reset"), want: serviceapp.PublishAuthorizationOutcomeRetryable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPublishAuthorizationOutcome(tc.err); got != tc.want {
				t.Fatalf("expected %s, got %s", tc.want, got)
			}
		})
	}
}
