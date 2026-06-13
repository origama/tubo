package reachability

import (
	"context"
	"testing"
	"time"
)

func TestWaitForRecoveredReturnsOnRecoveredEvent(t *testing.T) {
	mgr := NewManager(ManagerConfig{Buffer: 4})
	mgr.RecordFailure(FailureKindRelay, context.DeadlineExceeded)
	done := make(chan bool, 1)
	go func() {
		done <- WaitForRecovered(context.Background(), mgr.Events(), time.Hour)
	}()
	mgr.RecordSuccess(SuccessKindRelay)
	select {
	case recovered := <-done:
		if !recovered {
			t.Fatal("expected recovered wake")
		}
	case <-time.After(time.Second):
		t.Fatal("expected recovered wake")
	}
}
