package reachability

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"
)

func Classify(err error) Classification {
	if err == nil {
		return HealthyClassification()
	}
	if isConfigError(err) {
		return Classification{State: StateConfigInvalid, Class: ErrorConfig, Reason: reasonForError(err)}
	}
	if isAuthError(err) {
		return Classification{State: StateAuthDenied, Class: ErrorAuth, Reason: reasonForError(err)}
	}
	if state, ok := transientState(err); ok {
		return Classification{State: state, Class: ErrorTransient, Reason: reasonForError(err)}
	}
	return Classification{State: StateUnknown, Class: ErrorUnknown, Reason: reasonForError(err)}
}

func NewEvent(at time.Time, typ EventType, err error) Event {
	return Event{At: at, Type: typ, Classification: Classify(err), Err: err}
}

func NewSnapshot(at time.Time, err error) Snapshot {
	return Snapshot{At: at, Classification: Classify(err)}
}

func isConfigError(err error) bool {
	msg := normalizedMessage(err)
	if msg == "" {
		return false
	}
	phrases := []string{
		"not configured",
		"config invalid",
		"configuration invalid",
		"service owner key file is not configured",
		"publish lease file is not configured",
		"authority private key file is not configured",
		"service public key mismatch",
		"scope does not match attached service",
	}
	return containsAny(msg, phrases)
}

func isAuthError(err error) bool {
	msg := normalizedMessage(err)
	if msg == "" {
		return false
	}
	phrases := []string{
		"missing connect permission",
		"membership capability is missing connect permission",
		"membership invite is missing connect permission",
		"membership invite revoked",
		"invite revoked",
		"service is invite_only",
		"required permissions",
		"denied:",
	}
	return containsAny(msg, phrases)
}

func transientState(err error) (State, bool) {
	msg := normalizedMessage(err)
	if msg == "" {
		return StateUnknown, false
	}
	switch {
	case containsAny(msg, []string{"relay reservation not ready", "relay not ready"}):
		return StateRelayNotReady, true
	case containsAny(msg, []string{"grant service unavailable", "grant endpoint unavailable", "grant service not ready", "grant endpoint not ready", "grant service unreachable"}):
		return StateGrantUnreachable, true
	case isNetworkTransient(err, msg):
		return StateOfflineSuspected, true
	default:
		return StateUnknown, false
	}
}

func isNetworkTransient(err error, msg string) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	phrases := []string{
		"timeout",
		"timed out",
		"context deadline exceeded",
		"network is unreachable",
		"no route to host",
		"connection refused",
		"failed to dial",
		"dial tcp",
		"i/o timeout",
		"temporary network failure",
		"broken pipe",
	}
	return containsAny(msg, phrases)
}

func reasonForError(err error) string {
	msg := normalizedMessage(err)
	if msg == "" {
		return string(StateUnknown)
	}
	switch {
	case containsAny(msg, []string{"relay reservation not ready", "relay not ready"}):
		return string(StateRelayNotReady)
	case containsAny(msg, []string{"grant service unavailable", "grant endpoint unavailable", "grant service not ready", "grant endpoint not ready", "grant service unreachable"}):
		return string(StateGrantUnreachable)
	case isConfigError(err):
		return string(StateConfigInvalid)
	case isAuthError(err):
		return string(StateAuthDenied)
	case isNetworkTransient(err, msg):
		return string(StateOfflineSuspected)
	default:
		return string(StateUnknown)
	}
}

func normalizedMessage(err error) string {
	if err == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(err.Error()))
}

func containsAny(msg string, phrases []string) bool {
	for _, phrase := range phrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}
