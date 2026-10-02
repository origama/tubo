package cli

import (
	"context"
	"testing"

	"github.com/origama/tubo/internal/store"
)

// runCommandForTest resolves and dispatches a full command line against an
// existing store; tests use it to exercise handlers through the same spec and
// parser path the real binary uses.
func (a App) runCommandForTest(t *testing.T, ctx context.Context, state *store.Store, args []string) error {
	t.Helper()
	spec, rest, err := resolveCommand(args)
	if err != nil {
		return err
	}
	f, err := parseCommand(spec, rest)
	if err != nil {
		return err
	}
	return a.dispatch(ctx, state, spec, f)
}

// parseCommandForTest resolves and parses without dispatching.
func parseCommandForTest(t *testing.T, args []string) (flags, error) {
	t.Helper()
	spec, rest, err := resolveCommand(args)
	if err != nil {
		return flags{}, err
	}
	return parseCommand(spec, rest)
}
