// Package runtime owns the Journey A exact downstream adapter. This is a
// concrete DownstreamOpener implementation that opens a remote Pipe using only
// the injected neutral openauth.AccessAcquirer capability: every downstream
// open obtains verified current/fresh short-lived access material (from the
// ephemeral lease cache or a post-acceptance renewal — opaque to L2) and
// performs the tunnel handshake. It coexists with the scoped
// newDownstreamPipeOpener; selection is at composition/adaptor only. The Pipe
// Engine stays neutral, and this L2 execution path never depends upward on L4
// (arch guards enforce it).
package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo/internal/accesslease"
	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/engine"
	"github.com/origama/tubo/internal/localkey"
	"github.com/origama/tubo/internal/node"
	"github.com/origama/tubo/internal/openauth"
	wire "github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/store"
)

// newExactDownstreamPipeOpener creates a DownstreamOpener that opens a remote
// Pipe via the injected neutral access acquirer. The opener knows nothing
// about invites, bindings, renewal, or persistence policy: it only receives
// verified current/fresh short-lived access material and performs the tunnel
// handshake with the lease encoded in it. No invite is ever required again
// after acceptance, and an expired lease simply causes reacquisition through
// the acquirer.
func newExactDownstreamPipeOpener(
	acquirer openauth.AccessAcquirer,
	state *store.Store,
	h host.Host,
	pipeID string,
) engine.DownstreamOpener {
	return func(ctx context.Context, nextSpec domain.PipeSpec, traversal engine.TraversalState) (node.Carrier, string, error) {
		if acquirer == nil {
			return nil, "", errors.New("exact_connection: access acquirer not injected")
		}
		access, err := acquirer.AcquireAccess(ctx, pipeID)
		if err != nil {
			return nil, "", err
		}
		if access.PipeID != pipeID {
			return nil, "", errors.New("exact_connection: acquired access for wrong pipe")
		}
		now := time.Now().UTC()
		lease, err := accesslease.Decode(access.AccessLease)
		if err != nil {
			return nil, "", errors.New("exact_connection: invalid acquired access lease")
		}
		if !access.ExpiresAt.After(now) || !lease.ExpiresAt.After(now) {
			// The acquired material already expired (defense in depth);
			// reacquire once through the neutral capability.
			access, err = acquirer.AcquireAccess(ctx, pipeID)
			if err != nil {
				return nil, "", err
			}
			lease, err = accesslease.Decode(access.AccessLease)
			if err != nil {
				return nil, "", errors.New("exact_connection: invalid reacquired access lease")
			}
			if access.PipeID != pipeID || !access.ExpiresAt.After(time.Now().UTC()) {
				return nil, "", errors.New("exact_connection: reacquired access lease expired")
			}
		}

		// Dial the bound locator (L1 reachability; never authorizes).
		setup, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		remotePeerID, err := node.DialPeer(setup, h, access.Locator)
		if err != nil {
			return nil, "", err
		}
		stream, err := h.NewStream(core.WithAllowLimitedConn(setup, "tubo tunnel via relay"), remotePeerID, protocol.ID(wire.TunnelID))
		if err != nil {
			return nil, "", err
		}
		streamOwned := false
		defer func() {
			if !streamOwned {
				_ = stream.Close()
			}
		}()
		_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

		clientPublic := lease.Subject

		// Perform the tunnel handshake.
		if err := wire.WriteFrame(stream, wire.ClientHello{Version: "2.0", ClientPublic: clientPublic, Capabilities: baseSessionCapabilities()}); err != nil {
			return nil, "", err
		}
		var server wire.ServerHello
		if err := wire.ReadFrame(stream, &server); err != nil {
			return nil, "", err
		}
		if server.PipeID != pipeID || server.Version != "2.0" {
			return nil, "", errors.New("tunnel_handshake_failed: pipe or version mismatch")
		}

		// Sign the challenge proof with the accepted recipient identity key.
		// Load only: provisioning a fresh key would change identity and is
		// never done after acceptance.
		keyPath := state.SecretPath("pipes", "connect-"+pipeID+".key")
		recipientPrivate, keyErr := localkey.Load(keyPath)
		if keyErr != nil {
			return nil, "", errors.New("exact_connection: accepted recipient key unavailable")
		}

		challengeProof := accesslease.SignProof(recipientPrivate, accesslease.OpenProofPurpose, server.Challenge, pipeID)

		open := wire.OpenPipe{
			PipeID:         pipeID,
			ChallengeProof: challengeProof,
			AccessLease:    access.AccessLease,
			VisitedPipeIDs: traversal.VisitedIDs,
			HopLimit:       traversal.MaxHops,
		}
		if open.HopLimit <= 0 {
			open.HopLimit = domain.DefaultMaxPipeHops
		}
		if err := wire.WriteFrame(stream, open); err != nil {
			return nil, "", err
		}
		var openResult wire.OpenResult
		if err := wire.ReadFrame(stream, &openResult); err != nil {
			return nil, "", err
		}
		if !openResult.OK {
			return nil, "", errors.New(openResult.ErrorCode + ": " + openResult.Message)
		}
		streamOwned = true
		_ = stream.SetDeadline(time.Time{})
		return node.WrapStream(stream), openResult.SessionID, nil
	}
}
