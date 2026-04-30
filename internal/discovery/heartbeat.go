package discovery

import (
	"context"
	"log"
	"sync"
	"time"
)

// HeartbeatLoop periodically re-publishes a service announcement to keep
// discovery cache entries alive before their TTL expires.
type HeartbeatLoop struct {
	publisher   *Publisher
	interval    time.Duration
	stopCh      chan struct{}
	annProvider func() (Announcement, bool)
	mu          sync.RWMutex
}

// NewHeartbeatLoop creates a heartbeat loop that will re-publish the given
// announcement at the specified interval using the provided publisher.
func NewHeartbeatLoop(publisher *Publisher, ann Announcement, interval time.Duration) *HeartbeatLoop {
	return NewHeartbeatLoopFunc(publisher, interval, func() (Announcement, bool) {
		return ann, true
	})
}

// NewHeartbeatLoopFunc creates a heartbeat loop with a dynamic announcement provider.
func NewHeartbeatLoopFunc(publisher *Publisher, interval time.Duration, provider func() (Announcement, bool)) *HeartbeatLoop {
	return &HeartbeatLoop{
		publisher:   publisher,
		interval:    interval,
		stopCh:      make(chan struct{}),
		annProvider: provider,
	}
}

// SetAnnouncementProvider updates the announcement provider used by the heartbeat loop.
func (h *HeartbeatLoop) SetAnnouncementProvider(provider func() (Announcement, bool)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.annProvider = provider
}

func (h *HeartbeatLoop) announcement() (Announcement, bool) {
	h.mu.RLock()
	provider := h.annProvider
	h.mu.RUnlock()
	if provider == nil {
		return Announcement{}, false
	}
	return provider()
}

// PublishNow emits one announcement immediately if the provider says the node is ready.
func (h *HeartbeatLoop) PublishNow(ctx context.Context) bool {
	ann, ok := h.announcement()
	if !ok {
		return false
	}
	if err := h.publisher.Publish(ctx, ann); err != nil {
		log.Printf("heartbeat immediate publish failed: %v", err)
		return false
	}
	log.Printf("heartbeat published service %q (peer=%s)", ann.ServiceName, ann.PeerID)
	return true
}

// Start launches a goroutine that periodically re-publishes the announcement.
// It exits cleanly when ctx is cancelled or Stop() is called.
func (h *HeartbeatLoop) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(h.interval)
		defer ticker.Stop()

		log.Printf("heartbeat loop started (interval=%s)", h.interval)

		for {
			select {
			case <-ctx.Done():
				log.Println("heartbeat loop: context cancelled, stopping")
				return
			case <-h.stopCh:
				log.Println("heartbeat loop: stop signal received")
				return
			case <-ticker.C:
				ann, ok := h.announcement()
				if !ok {
					log.Printf("heartbeat skipped: service announcement not ready yet")
					continue
				}
				if err := h.publisher.Publish(ctx, ann); err != nil {
					log.Printf("heartbeat publish failed: %v", err)
				} else {
					log.Printf("heartbeat published service %q (peer=%s)", ann.ServiceName, ann.PeerID)
				}
			}
		}
	}()
}

// Stop signals the heartbeat goroutine to exit cleanly.
func (h *HeartbeatLoop) Stop() {
	close(h.stopCh)
}
