package runtime

import (
	"context"
	"errors"
	"net"

	"github.com/libp2p/go-libp2p/core/host"

	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/engine"
	"github.com/origama/tubo/internal/node"
	"github.com/origama/tubo/internal/store"
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

// newDownstreamPipeOpener creates a DownstreamOpener wired to the Store, the
// serving process's libp2p host, and the PipeRuntime. This opens a remote
// Pipe hop with the full tunnel handshake, access lease acquisition, and the
// traversal state propagated from the Engine.
//
// The opener is the composition-root seam for per-hop authorization: it
// acquires the Access Lease for the downstream Pipe through the existing
// scope-authority lease request, performs reachability/dial, and hands back
// only the established carrier. The Engine never sees lease or discovery
// material.
func newDownstreamPipeOpener(state *store.Store, h host.Host, pipeRuntime *PipeRuntime) engine.DownstreamOpener {
	leases := &localLeaseCache{}
	return func(ctx context.Context, nextSpec domain.PipeSpec, traversal engine.TraversalState) (node.Carrier, string, error) {
		recordStored, err := state.Record(ctx, nextSpec.Ref.Name)
		if err != nil {
			return nil, "", err
		}
		record, err := ParseVerifiedRecord(ctx, state, recordStored)
		if err != nil {
			return nil, "", err
		}
		return openRemotePipeCarrier(ctx, state, h, record, traversal, leases)
	}
}

// errDownstreamPipeNotWired is returned when a downstream pipe open is
// attempted without lease/reachability material. It fails closed with the
// generic denial shape the Engine maps to target_unreachable.
var errDownstreamPipeNotWired = errors.New("downstream pipe open not wired in this adapter")
