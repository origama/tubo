package cli

import (
	"errors"
	"strings"
)

// cliError carries a user-facing message plus an optional one-line next-action
// hint. The hint renders as "Try: <hint>" on stderr by the top-level command
// entry; normal errors render as their plain message.
type cliError struct {
	msg string
	try string
}

func (e cliError) Error() string { return e.msg }

func (e cliError) tryHint() string { return e.try }

// withTry attaches a concise one-line next action to err. Existing cliError
// values keep their hint.
func withTry(err error, try string) error {
	if err == nil {
		return nil
	}
	var existing cliError
	if errors.As(err, &existing) {
		if existing.try != "" {
			return err
		}
		return cliError{msg: existing.msg, try: try}
	}
	wrapped := &cliError{msg: err.Error(), try: try}
	return wrapped
}

// tryHint returns the "Try:" line for an error, if any.
func tryHint(err error) string {
	var e cliError
	if errors.As(err, &e) {
		return e.try
	}
	return ""
}

// hintForClass attaches a deterministic next-action hint for well-known
// lower-layer error classes, when the message already carries the class. Safe
// hints only: never echo credentials, paths, or addresses.
func hintForClass(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not_found"):
		return withTry(err, "check what exists: tubo get pipes; then choose a valid ref")
	case strings.Contains(msg, "authorization_denied"):
		return withTry(err, "use a fresh invite from 'tubo share pipe/<name>'; invites are one-time and short-lived")
	case strings.Contains(msg, "runtime_unavailable"):
		return withTry(err, "start the runtime first: tubo start pipe/<name>")
	case strings.Contains(msg, "conflict:") && strings.Contains(msg, "already running"):
		return withTry(err, "stop it first: tubo stop <ref>")
	}
	return err
}
