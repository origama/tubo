package main

import (
	"context"
	"errors"
	"testing"
	"time"

	serviceapp "github.com/origama/tubo/internal/app/service"
	cfgpkg "github.com/origama/tubo/internal/config"
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
