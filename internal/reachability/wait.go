package reachability

import (
	"context"
	"time"
)

func WaitForRecovered(ctx context.Context, events <-chan Event, delay time.Duration) bool {
	if delay <= 0 {
		select {
		case <-ctx.Done():
		default:
		}
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return false
		case ev, ok := <-events:
			if !ok {
				return false
			}
			if ev.Type == EventRecovered {
				return true
			}
		}
	}
}
