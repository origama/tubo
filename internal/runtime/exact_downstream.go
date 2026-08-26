// Package runtime owns the Journey A exact downstream adapter. This is a
// concrete DownstreamOpener implementation that uses exact connection state
// (no discovery, no Scope, no membership, no PipeRecord) to open a remote
// Pipe via the tunnel handshake. It coexists with the scoped
// newDownstreamPipeOpener; selection is at composition/adaptor only.
package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/engine"
	"github.com/origama/tubo-rebuilt/internal/localkey"
	"github.com/origama/tubo-rebuilt/internal/node"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
)

// newExactDownstreamPipeOpener creates a DownstreamOpener that uses the exact
// connection state (no discovery, no Scope, no membership) to open a remote
// Pipe. It reads the AccessLease and locator from the exact connection state
// repository, performs the tunnel handshake, and returns the established
// carrier.
//
// This adapter is used by the Journey A scope-less connect path ONLY.
// The scoped newDownstreamPipeOpener remains for prototype scope paths.
func newExactDownstreamPipeOpener(
	state *store.Store,
	h host.Host,
	pipeRuntime *PipeRuntime,
	pipeID string,
) engine.DownstreamOpener {
	return func(ctx context.Context, nextSpec domain.PipeSpec, traversal engine.TraversalState) (node.Carrier, string, error) {
		// Load exact connection state for the remote Pipe.
		connState, err := state.GetExactConnectionState(ctx, pipeID)
		if err != nil {
			return nil, "", err
		}
		// Decode the AccessLease.
		lease, err := accesslease.Decode(connState.AccessLease)
		if err != nil {
			return nil, "", errors.New("exact_connection: invalid stored access lease")
		}
		// Verify the lease is not expired.
		now := time.Now().UTC()
		if !lease.ExpiresAt.After(now) {
			return nil, "", errors.New("exact_connection: access lease expired")
		}
		// Dial the locator peer.
		setup, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		remotePeerID, err := node.DialPeer(setup, h, connState.Locator)
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

		// Client public key is the lease subject (recipient identity).
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

		// Sign the challenge proof using the recipient private key.
		// The recipient key is stored at <secrets>/pipes/connect-<pipeID>.key.
		keyPath := state.SecretPath("pipes", "connect-"+pipeID+".key")
		recipientPrivate, keyErr := localkey.Load(keyPath)
		if keyErr != nil {
			// Try Provision if the key doesn't exist yet.
			recipientPrivate, keyErr = localkey.Provision(keyPath)
			if keyErr != nil {
				return nil, "", errors.New("exact_connection: recipient key unavailable")
			}
		}
		challengeProof := accesslease.SignProof(recipientPrivate, accesslease.OpenProofPurpose, server.Challenge, pipeID)

		open := wire.OpenPipe{
			PipeID:         pipeID,
			ChallengeProof: challengeProof,
			AccessLease:    connState.AccessLease,
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
