package reachability

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestManagerProbeNowTransitionsAndRecovers(t *testing.T) {
	base := time.Unix(1_700_000_000, 0).UTC()
	calls := 0
	m := NewManager(ManagerConfig{
		Probe: ProbeFunc(func(context.Context) error {
			calls++
			if calls == 1 {
				return errors.New("grant service unavailable")
			}
			return nil
		}),
		ProbeInterval: 30 * time.Second,
		ProbeBackoff:  5 * time.Second,
		Now:           func() time.Time { return base },
		Buffer:        4,
	})

	if got := m.Snapshot(); got.Classification.State != StateUnknown || got.Classification.Class != ErrorUnknown {
		t.Fatalf("initial snapshot = %#v", got)
	}

	snap, err := m.ProbeNow(context.Background())
	if err == nil {
		t.Fatal("expected first probe to fail")
	}
	if snap.Classification.State != StateGrantUnreachable || snap.Classification.Class != ErrorTransient {
		t.Fatalf("unexpected failure snapshot: %#v", snap)
	}
	if snap.NextProbeAt == nil || !snap.NextProbeAt.Equal(base.Add(5*time.Second)) {
		t.Fatalf("unexpected failure next probe: %#v", snap.NextProbeAt)
	}
	select {
	case ev := <-m.Events():
		if ev.Type != EventStateShift || ev.Subject != string(FailureKindProbe) || ev.Classification.State != StateGrantUnreachable {
			t.Fatalf("unexpected failure event: %#v", ev)
		}
	default:
		t.Fatal("expected failure event")
	}

	snap, err = m.ProbeNow(context.Background())
	if err != nil {
		t.Fatalf("expected probe recovery, got %v", err)
	}
	if snap.Classification.State != StateHealthy || snap.Classification.Class != ErrorNone {
		t.Fatalf("unexpected recovery snapshot: %#v", snap)
	}
	if snap.NextProbeAt == nil || !snap.NextProbeAt.Equal(base.Add(30*time.Second)) {
		t.Fatalf("unexpected recovery next probe: %#v", snap.NextProbeAt)
	}
	select {
	case ev := <-m.Events():
		if ev.Type != EventRecovered || ev.Subject != string(SuccessKindProbe) || ev.Classification.State != StateHealthy {
			t.Fatalf("unexpected recovered event: %#v", ev)
		}
	default:
		t.Fatal("expected recovered event")
	}
}

func TestManagerStartProbesAndCanBeCanceled(t *testing.T) {
	probeCalled := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewManager(ManagerConfig{
		Probe: ProbeFunc(func(context.Context) error {
			select {
			case probeCalled <- struct{}{}:
			default:
			}
			return nil
		}),
		ProbeInterval: time.Hour,
		ProbeBackoff:  time.Second,
		Buffer:        4,
	})
	m.Start(ctx)
	select {
	case <-probeCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("expected start probe")
	}
	select {
	case ev := <-m.Events():
		if ev.Type != EventStateShift || ev.Classification.State != StateHealthy {
			t.Fatalf("unexpected start event: %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected start event")
	}
	cancel()
}

func TestManagerUnknownProbeFailureUsesBackoff(t *testing.T) {
	base := time.Unix(1_700_000_200, 0).UTC()
	m := NewManager(ManagerConfig{
		Probe: ProbeFunc(func(context.Context) error {
			return errors.New("unexpected probe failure")
		}),
		ProbeInterval: 30 * time.Second,
		ProbeBackoff:  5 * time.Second,
		Now:           func() time.Time { return base },
		Buffer:        4,
	})

	snap, err := m.ProbeNow(context.Background())
	if err == nil {
		t.Fatal("expected probe to fail")
	}
	if snap.Classification.State != StateUnknown || snap.Classification.Class != ErrorUnknown {
		t.Fatalf("unexpected snapshot: %#v", snap)
	}
	if snap.NextProbeAt == nil || !snap.NextProbeAt.Equal(base.Add(5*time.Second)) {
		t.Fatalf("unexpected next probe: %#v", snap.NextProbeAt)
	}
	if got := m.nextDelay(); got != 5*time.Second {
		t.Fatalf("unexpected next delay: %v", got)
	}
}

func TestManagerRecordFailureAndSuccessUpdateSnapshot(t *testing.T) {
	base := time.Unix(1_700_000_100, 0).UTC()
	m := NewManager(ManagerConfig{
		ProbeInterval: 20 * time.Second,
		ProbeBackoff:  7 * time.Second,
		Now:           func() time.Time { return base },
		Buffer:        4,
	})

	snap := m.RecordFailure(FailureKindGrant, errors.New("failed to dial grant endpoint: connection refused"))
	if snap.Classification.State != StateOfflineSuspected || snap.Classification.Class != ErrorTransient {
		t.Fatalf("unexpected transient snapshot: %#v", snap)
	}
	if snap.NextProbeAt == nil || !snap.NextProbeAt.Equal(base.Add(7*time.Second)) {
		t.Fatalf("unexpected transient next probe: %#v", snap.NextProbeAt)
	}

	snap = m.RecordSuccess(SuccessKindRelay)
	if snap.Classification.State != StateHealthy || snap.Classification.Class != ErrorNone {
		t.Fatalf("unexpected healthy snapshot: %#v", snap)
	}
	if snap.NextProbeAt == nil || !snap.NextProbeAt.Equal(base.Add(20*time.Second)) {
		t.Fatalf("unexpected healthy next probe: %#v", snap.NextProbeAt)
	}
}
