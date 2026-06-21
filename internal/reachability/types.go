package reachability

import "time"

type State string

type ErrorClass string

type EventType string

const (
	StateUnknown          State = "unknown"
	StateHealthy          State = "healthy"
	StateOfflineSuspected State = "offline_suspected"
	StateBootstrapOnly    State = "bootstrap_reachable"
	StateRelayNotReady    State = "relay_not_ready"
	StateGrantUnreachable State = "grant_unreachable"
	StateRateLimited      State = "rate_limited"
	StateAuthDenied       State = "auth_denied"
	StateConfigInvalid    State = "config_invalid"
)

const (
	ErrorNone      ErrorClass = "none"
	ErrorTransient ErrorClass = "transient"
	ErrorAuth      ErrorClass = "auth"
	ErrorConfig    ErrorClass = "config"
	ErrorUnknown   ErrorClass = "unknown"
)

const (
	EventObserved   EventType = "observed"
	EventRecovered  EventType = "recovered"
	EventStateShift EventType = "state_shift"
)

type Classification struct {
	State  State
	Class  ErrorClass
	Reason string
}

type Event struct {
	At             time.Time
	Type           EventType
	Subject        string
	Classification Classification
	Err            error
}

type Snapshot struct {
	At             time.Time
	Classification Classification
	NextProbeAt    *time.Time
	LastEvent      *Event
}

func HealthyClassification() Classification {
	return Classification{State: StateHealthy, Class: ErrorNone, Reason: string(StateHealthy)}
}

func HealthySnapshot(at time.Time) Snapshot {
	return Snapshot{At: at, Classification: HealthyClassification()}
}
