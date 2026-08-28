package cli

import (
	"strings"
)

// cliError carries a user-facing message plus an optional one-line next-action
// hint. The hint renders as "Try: <hint>" on stderr by the command entry
// point; plain errors render as their message only.
type cliError struct {
	msg string
	try string
}

func (e cliError) Error() string { return e.msg }

// asCliError finds the nearest cliError in an unwrap chain.
func asCliError(err error) (cliError, bool) {
	for err != nil {
		if v, ok := err.(cliError); ok {
			return v, true
		}
		if v, ok := err.(*cliError); ok {
			return *v, true
		}
		unwrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return cliError{}, false
		}
		err = unwrapped.Unwrap()
	}
	return cliError{}, false
}

// withTry attaches a concise one-line next action to err. Existing cliError
// values keep their hint.
func withTry(err error, try string) error {
	if err == nil {
		return nil
	}
	if existing, ok := asCliError(err); ok {
		if existing.try != "" {
			return err
		}
		return cliError{msg: existing.msg, try: try}
	}
	return cliError{msg: err.Error(), try: try}
}

// Hint returns the "Try:" action for an error, if any.
func Hint(err error) string {
	if value, ok := asCliError(err); ok {
		return value.try
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
