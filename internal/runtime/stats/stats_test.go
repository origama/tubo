package stats

import (
	"errors"
	"testing"
	"time"
)

func TestCollectorTracksBytesAndActivity(t *testing.T) {
	c := New(Snapshot{Role: "connect", Kind: "http", Service: "lms"})
	c.Begin()
	c.AddTx(128)
	c.AddRx(256)
	c.Observe(200, 42*time.Millisecond)
	c.Finish(nil)

	snap := c.Snapshot()
	if snap.Role != "connect" || snap.Kind != "http" || snap.Service != "lms" {
		t.Fatalf("unexpected meta: %#v", snap)
	}
	if snap.TxBytesTotal != 128 || snap.RxBytesTotal != 256 || snap.RequestsTotal != 1 {
		t.Fatalf("unexpected counters: %#v", snap)
	}
	if snap.Completed != 1 || snap.Errors != 0 || snap.Active != 0 {
		t.Fatalf("unexpected lifecycle counters: %#v", snap)
	}
	if snap.LastStatusCode != 200 || snap.LastLatencyMS != 42 {
		t.Fatalf("unexpected request metrics: %#v", snap)
	}
	if snap.LastActivityAt == nil {
		t.Fatalf("expected last activity time: %#v", snap)
	}
}

func TestCollectorBytesDoNotTouchActivityTimestamp(t *testing.T) {
	c := New(Snapshot{Role: "service"})
	first := c.Snapshot()
	if first.LastActivityAt != nil {
		t.Fatalf("unexpected initial activity timestamp: %#v", first)
	}
	c.AddTx(1)
	c.AddRx(2)
	second := c.Snapshot()
	if second.TxBytesTotal != 1 || second.RxBytesTotal != 2 {
		t.Fatalf("unexpected byte counters: %#v", second)
	}
	if second.LastActivityAt != nil {
		t.Fatalf("byte-only updates should not stamp activity, got %#v", second.LastActivityAt)
	}
}

func TestCollectorCopiesSnapshot(t *testing.T) {
	c := New(Snapshot{Role: "service"})
	c.Begin()
	first := c.Snapshot()
	if first.LastActivityAt == nil {
		t.Fatal("expected activity timestamp")
	}
	*first.LastActivityAt = time.Time{}
	second := c.Snapshot()
	if second.LastActivityAt == nil || second.LastActivityAt.IsZero() {
		t.Fatal("snapshot should not be affected by caller mutation")
	}
}

func TestCollectorTracksErrors(t *testing.T) {
	c := New(Snapshot{})
	c.Begin()
	c.Finish(errors.New("boom"))
	snap := c.Snapshot()
	if snap.Errors != 1 || snap.Completed != 0 {
		t.Fatalf("unexpected error accounting: %#v", snap)
	}
}
