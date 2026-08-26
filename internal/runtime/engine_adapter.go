package runtime

import (
	"context"
	"errors"
	"net"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/engine"
	"github.com/origama/tubo-rebuilt/internal/node"
	"github.com/origama/tubo-rebuilt/internal/store"
)

// newPipeEngine creates an Engine wired to the given Store for resolution.
// This is the composition-root adapter: the Engine itself has no Store
// dependency — only the injected resolvers do.
func newPipeEngine(state *store.Store) *engine.Engine {
	return engine.New(
		func(ref domain.Ref) (domain.ServiceSpec, error) {
			return state.Service(context.Background(), ref)
		},
		func(ref domain.Ref) (domain.PipeSpec, error) {
			spec, _, _, err := state.Pipe(context.Background(), ref.String())
			return spec, err
		},
		func(spec domain.ServiceSpec) (net.Conn, error) {
			return dialService(spec)
		},
	)
}

// newDownstreamPipeOpener creates a DownstreamOpener wired to the Store and
// the existing runtime infrastructure. This opens a remote Pipe hop with
// full tunnel handshake, access lease acquisition, and authorization.
//
// In the A0 Step 7 scope, this opener handles the first-hop remote Pipe
// open. Full multi-hop remote traversal (where each hop independently
// acquires a lease) is wired here as a narrow consumer-side injection.
func newDownstreamPipeOpener(state *store.Store, eng *engine.Engine) engine.DownstreamOpener {
	return func(ctx context.Context, nextSpec domain.PipeSpec, state engine.TraversalState) (node.Carrier, string, error) {
		// The downstream opener must independently authorize the hop.
		// It acquires the carrier via reachability, performs the tunnel
		// handshake, and returns the carrier.
		//
		// For A0 Step 7, this is a stub that returns an error indicating
		// the downstream open is not yet fully wired. The full integration
		// with reachability, lease acquisition, and tunnel handshake is
		// completed when the prototype branching is replaced.
		_ = state
		_ = nextSpec
		return nil, "", errDownstreamPipeNotWired
	}
}

// errDownstreamPipeNotWired is returned when the downstream pipe opener
// is called but the full remote open path is not yet wired. This is a
// temporary error during the transition from prototype to Engine-based
// composition.
var errDownstreamPipeNotWired = errors.New("downstream pipe open not wired in this adapter")

// engineForwardTarget is a convenience function that wraps Engine.Forward
// for the single-hop case used in the tunnel server. It resolves the
// target and returns the connection or carrier.
func engineForwardTarget(ctx context.Context, eng *engine.Engine, spec domain.PipeSpec, state engine.TraversalState, opener engine.DownstreamOpener) (engine.ForwardResult, error) {
	return eng.Forward(ctx, spec, state, opener)
}