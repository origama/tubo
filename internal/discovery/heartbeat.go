package discovery

import (
	"context"
	"log"
	"time"
)

// HeartbeatLoop periodically re-publishes a service announcement to keep
// discovery cache entries alive before their TTL expires.
type HeartbeatLoop struct {
	publisher *Publisher
	ann       Announcement
	interval  time.Duration
	stopCh    chan struct{}
}

// NewHeartbeatLoop creates a heartbeat loop that will re-publish the given
// announcement at the specified interval using the provided publisher.
func NewHeartbeatLoop(publisher *Publisher, ann Announcement, interval time.Duration) *HeartbeatLoop {
	return &HeartbeatLoop{
		publisher: publisher,
		ann:       ann,
		interval:  interval,
		stopCh:    make(chan struct{}),
	}
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
				if err := h.publisher.Publish(ctx, h.ann); err != nil {
					log.Printf("heartbeat publish failed: %v", err)
				} else {
					log.Printf("heartbeat published service %q (peer=%s)", h.ann.ServiceName, h.ann.PeerID)
				}
			}
		}
	}()
}

// Stop signals the heartbeat goroutine to exit cleanly.
func (h *HeartbeatLoop) Stop() {
	close(h.stopCh)
}
