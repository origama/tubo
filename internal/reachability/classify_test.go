package reachability

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

type timeoutError struct{ msg string }

func (e timeoutError) Error() string   { return e.msg }
func (e timeoutError) Timeout() bool   { return true }
func (e timeoutError) Temporary() bool { return true }

func TestClassifyNilErrorIsHealthy(t *testing.T) {
	got := Classify(nil)
	if got.State != StateHealthy || got.Class != ErrorNone || got.Reason != string(StateHealthy) {
		t.Fatalf("Classify(nil) = %#v", got)
	}
}

func TestClassifyTransientErrors(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		state State
	}{
		{name: "deadline", err: context.DeadlineExceeded, state: StateOfflineSuspected},
		{name: "wrapped timeout", err: fmt.Errorf("grant poll: %w", context.DeadlineExceeded), state: StateOfflineSuspected},
		{name: "net timeout", err: timeoutError{msg: "i/o timeout"}, state: StateOfflineSuspected},
		{name: "no route", err: errors.New("connect failed: network is unreachable"), state: StateOfflineSuspected},
		{name: "relay not ready", err: errors.New("relay reservation not ready"), state: StateRelayNotReady},
		{name: "grant unavailable", err: errors.New("grant service unavailable"), state: StateGrantUnreachable},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err)
			if got.State != tc.state {
				t.Fatalf("State = %q, want %q", got.State, tc.state)
			}
			if got.Class != ErrorTransient {
				t.Fatalf("Class = %q, want %q", got.Class, ErrorTransient)
			}
			if got.Reason == "" || got.Reason == string(StateUnknown) {
				t.Fatalf("unexpected reason: %#v", got)
			}
		})
	}
}

func TestClassifyAuthErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "missing connect permission", err: errors.New("membership capability is missing connect permission")},
		{name: "invite revoked", err: errors.New("membership invite revoked")},
		{name: "invite only", err: errors.New("service is invite_only; use `tubo connect --token <share-invite>`")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err)
			if got.State != StateAuthDenied || got.Class != ErrorAuth {
				t.Fatalf("Classify(%v) = %#v", tc.err, got)
			}
		})
	}
}

func TestClassifyConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "owner key not configured", err: errors.New("service owner key file is not configured")},
		{name: "public key mismatch", err: errors.New("publish lease service public key mismatch")},
		{name: "scope mismatch", err: errors.New("connect lease request scope does not match attached service")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err)
			if got.State != StateConfigInvalid || got.Class != ErrorConfig {
				t.Fatalf("Classify(%v) = %#v", tc.err, got)
			}
		})
	}
}

func TestClassifyUnknownError(t *testing.T) {
	got := Classify(errors.New("something unexpected happened"))
	if got.State != StateUnknown || got.Class != ErrorUnknown || got.Reason != string(StateUnknown) {
		t.Fatalf("unexpected classification: %#v", got)
	}
}

func TestEventAndSnapshotWrapClassification(t *testing.T) {
	at := time.Unix(123, 0).UTC()
	err := errors.New("relay reservation not ready")
	e := NewEvent(at, EventObserved, err)
	if !e.At.Equal(at) || e.Type != EventObserved || e.Err != err || e.Classification.State != StateRelayNotReady {
		t.Fatalf("unexpected event: %#v", e)
	}
	s := NewSnapshot(at, err)
	if !s.At.Equal(at) || s.Classification.State != StateRelayNotReady {
		t.Fatalf("unexpected snapshot: %#v", s)
	}
}

func TestHealthySnapshot(t *testing.T) {
	at := time.Unix(321, 0).UTC()
	s := HealthySnapshot(at)
	if !s.At.Equal(at) || s.Classification.State != StateHealthy || s.Classification.Class != ErrorNone {
		t.Fatalf("unexpected healthy snapshot: %#v", s)
	}
}

func TestTimeoutErrorImplementsNetError(t *testing.T) {
	var _ net.Error = timeoutError{msg: "i/o timeout"}
}
