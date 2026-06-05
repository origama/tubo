package service

import (
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	grantEndpointPeerBurst          = 12
	grantEndpointServiceBurst       = 48
	grantEndpointInvalidBurst       = 3
	grantEndpointWindow             = time.Minute
	grantEndpointDenyTTL            = 30 * time.Second
	grantEndpointInvalidReasonToken = "grant endpoint deny cache active for repeated invalid requests; retry later"
)

type grantEndpointAbuseConfig struct {
	now             func() time.Time
	perPeerBurst    int
	perServiceBurst int
	invalidBurst    int
	window          time.Duration
	denyTTL         time.Duration
}

type grantEndpointAbuseController struct {
	now             func() time.Time
	perPeerBurst    int
	perServiceBurst int
	invalidBurst    int
	window          time.Duration
	denyTTL         time.Duration

	mu          sync.Mutex
	peerReqs    map[peer.ID][]time.Time
	serviceReqs []time.Time
	invalidReqs map[peer.ID][]time.Time
	denyUntil   map[peer.ID]time.Time
}

func newGrantEndpointAbuseController(cfg grantEndpointAbuseConfig) *grantEndpointAbuseController {
	if cfg.now == nil {
		cfg.now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.perPeerBurst <= 0 {
		cfg.perPeerBurst = grantEndpointPeerBurst
	}
	if cfg.perServiceBurst <= 0 {
		cfg.perServiceBurst = grantEndpointServiceBurst
	}
	if cfg.invalidBurst <= 0 {
		cfg.invalidBurst = grantEndpointInvalidBurst
	}
	if cfg.window <= 0 {
		cfg.window = grantEndpointWindow
	}
	if cfg.denyTTL <= 0 {
		cfg.denyTTL = grantEndpointDenyTTL
	}
	return &grantEndpointAbuseController{now: cfg.now, perPeerBurst: cfg.perPeerBurst, perServiceBurst: cfg.perServiceBurst, invalidBurst: cfg.invalidBurst, window: cfg.window, denyTTL: cfg.denyTTL, peerReqs: map[peer.ID][]time.Time{}, invalidReqs: map[peer.ID][]time.Time{}, denyUntil: map[peer.ID]time.Time{}}
}

func (c *grantEndpointAbuseController) Allow(requester peer.ID) error {
	now := c.now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	if until, ok := c.denyUntil[requester]; ok && now.Before(until) {
		return errors.New(grantEndpointInvalidReasonToken)
	}
	if len(c.peerReqs[requester]) >= c.perPeerBurst {
		return fmt.Errorf("grant endpoint rate limit exceeded for peer; retry later")
	}
	if len(c.serviceReqs) >= c.perServiceBurst {
		return fmt.Errorf("grant endpoint rate limit exceeded for service; retry later")
	}
	c.peerReqs[requester] = append(c.peerReqs[requester], now)
	c.serviceReqs = append(c.serviceReqs, now)
	return nil
}

func (c *grantEndpointAbuseController) RecordInvalid(requester peer.ID) {
	now := c.now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	items := append(c.invalidReqs[requester], now)
	c.invalidReqs[requester] = items
	if len(items) >= c.invalidBurst {
		c.denyUntil[requester] = now.Add(c.denyTTL)
	}
}

func (c *grantEndpointAbuseController) pruneLocked(now time.Time) {
	for requester, items := range c.peerReqs {
		c.peerReqs[requester] = pruneWindow(items, now, c.window)
		if len(c.peerReqs[requester]) == 0 {
			delete(c.peerReqs, requester)
		}
	}
	c.serviceReqs = pruneWindow(c.serviceReqs, now, c.window)
	for requester, items := range c.invalidReqs {
		c.invalidReqs[requester] = pruneWindow(items, now, c.window)
		if len(c.invalidReqs[requester]) == 0 {
			delete(c.invalidReqs, requester)
		}
	}
	for requester, until := range c.denyUntil {
		if !now.Before(until) {
			delete(c.denyUntil, requester)
		}
	}
}

func pruneWindow(items []time.Time, now time.Time, window time.Duration) []time.Time {
	keep := items[:0]
	for _, item := range items {
		if now.Sub(item) < window {
			keep = append(keep, item)
		}
	}
	return keep
}

func (e *serviceGrantEndpoint) applyAbuseControls(msgType string, requester peer.ID) error {
	if e.abuse == nil {
		return nil
	}
	if err := e.abuse.Allow(requester); err != nil {
		e.logGrantDenied(msgType, requester, err.Error())
		return err
	}
	return nil
}

func (e *serviceGrantEndpoint) recordDeniedGrantRequest(msgType string, requester peer.ID, reason string) {
	if e.abuse != nil && shouldCacheGrantDenial(reason) {
		e.abuse.RecordInvalid(requester)
	}
	e.logGrantDenied(msgType, requester, reason)
}

func shouldCacheGrantDenial(reason string) bool {
	switch reason {
	case "grant endpoint rate limit exceeded for peer; retry later", "grant endpoint rate limit exceeded for service; retry later", grantEndpointInvalidReasonToken:
		return false
	default:
		return true
	}
}

func (e *serviceGrantEndpoint) logGrantDenied(msgType string, requester peer.ID, reason string) {
	log.Printf("grant endpoint denied requester=%s service_id=%s policy=%s operation=%s reason=%q", requester, e.serviceID, e.connectPolicy, msgType, reason)
}
