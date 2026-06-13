package reachability

import (
	"context"
	"sync"
	"time"
)

type ManagerConfig struct {
	Probe         Probe
	ProbeInterval time.Duration
	ProbeBackoff  time.Duration
	Now           func() time.Time
	Buffer        int
}

type Manager struct {
	mu            sync.RWMutex
	probeMu       sync.Mutex
	snapshot      Snapshot
	events        chan Event
	probe         Probe
	probeInterval time.Duration
	probeBackoff  time.Duration
	now           func() time.Time
}

func NewManager(cfg ManagerConfig) *Manager {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	interval := cfg.ProbeInterval
	backoff := cfg.ProbeBackoff
	if backoff <= 0 {
		backoff = interval
	}
	buf := cfg.Buffer
	if buf <= 0 {
		buf = 16
	}
	at := now()
	return &Manager{
		snapshot:      Snapshot{At: at, Classification: Classification{State: StateUnknown, Class: ErrorUnknown, Reason: string(StateUnknown)}},
		events:        make(chan Event, buf),
		probe:         cfg.Probe,
		probeInterval: interval,
		probeBackoff:  backoff,
		now:           now,
	}
}

func (m *Manager) Start(ctx context.Context) {
	if m == nil || m.probe == nil || m.probeInterval <= 0 {
		return
	}
	go m.run(ctx)
}

func (m *Manager) run(ctx context.Context) {
	if _, err := m.ProbeNow(ctx); err != nil && ctx.Err() != nil {
		return
	}
	timer := time.NewTimer(m.nextDelay())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := m.ProbeNow(ctx); err != nil && ctx.Err() != nil {
			return
		}
		if delay := m.nextDelay(); delay > 0 {
			timer.Reset(delay)
		}
	}
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return cloneSnapshot(m.snapshot)
}

func (m *Manager) Events() <-chan Event {
	if m == nil {
		return nil
	}
	return m.events
}

func (m *Manager) RecordFailure(kind FailureKind, err error) Snapshot {
	if m == nil {
		return Snapshot{}
	}
	return m.record(kind, err, Classify(err))
}

func (m *Manager) RecordSuccess(kind SuccessKind) Snapshot {
	if m == nil {
		return Snapshot{}
	}
	return m.record(kind, nil, HealthyClassification())
}

func (m *Manager) ProbeNow(ctx context.Context) (Snapshot, error) {
	if m == nil || m.probe == nil {
		return m.Snapshot(), nil
	}
	m.probeMu.Lock()
	defer m.probeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return m.Snapshot(), err
	}
	err := m.probe.Probe(ctx)
	if err != nil {
		return m.RecordFailure(FailureKindProbe, err), err
	}
	return m.RecordSuccess(SuccessKindProbe), nil
}

func (m *Manager) record(kind any, err error, classification Classification) Snapshot {
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.snapshot.Classification
	event := Event{At: now, Type: EventObserved, Subject: kindLabel(kind), Classification: classification, Err: err}
	m.snapshot.At = now
	m.snapshot.Classification = classification
	m.snapshot.NextProbeAt = m.nextProbeAtLocked(now, classification)
	m.snapshot.LastEvent = &event
	if shouldEmitTransition(prev, classification) {
		switch {
		case isRecovered(prev, classification):
			event.Type = EventRecovered
		default:
			event.Type = EventStateShift
		}
		m.snapshot.LastEvent = &event
		m.emit(event)
	}
	return cloneSnapshot(m.snapshot)
}

func (m *Manager) nextDelay() time.Duration {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.snapshot.Classification.Class == ErrorNone {
		return m.probeInterval
	}
	return m.probeBackoff
}

func (m *Manager) nextProbeAtLocked(now time.Time, classification Classification) *time.Time {
	if m.probeInterval <= 0 && m.probeBackoff <= 0 {
		return nil
	}
	delay := m.probeInterval
	if classification.Class != ErrorNone {
		delay = m.probeBackoff
	}
	if delay <= 0 {
		return nil
	}
	t := now.Add(delay).UTC()
	return &t
}

func (m *Manager) emit(ev Event) {
	select {
	case m.events <- ev:
	default:
	}
}

func cloneSnapshot(in Snapshot) Snapshot {
	out := in
	if in.NextProbeAt != nil {
		t := *in.NextProbeAt
		out.NextProbeAt = &t
	}
	if in.LastEvent != nil {
		ev := *in.LastEvent
		out.LastEvent = &ev
	}
	return out
}

func shouldEmitTransition(prev, next Classification) bool {
	if prev == next {
		return false
	}
	if prev.Class == ErrorNone && next.Class == ErrorNone {
		return false
	}
	return true
}

func isRecovered(prev, next Classification) bool {
	if next.Class != ErrorNone {
		return false
	}
	switch prev.Class {
	case ErrorTransient, ErrorAuth, ErrorConfig:
		return true
	default:
		return false
	}
}

func kindLabel(kind any) string {
	switch v := kind.(type) {
	case FailureKind:
		return string(v)
	case SuccessKind:
		return string(v)
	case string:
		return v
	default:
		return "unknown"
	}
}
