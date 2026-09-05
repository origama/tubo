// Package runtime owns the Journey A exact downstream adapter. This is a
// concrete DownstreamOpener implementation that opens a remote Pipe through
// the durable accepted-binding lifecycle (no discovery, no Scope, no
// membership, no PipeRecord, no invite): every downstream open obtains
// verified current/fresh short-lived access material (from the ephemeral
// lease cache or a post-acceptance renewal) and performs the tunnel handshake.
// It coexists with the scoped newDownstreamPipeOpener; selection is at
// composition/adaptor only. The Pipe Engine stays neutral.
package runtime

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

// newExactDownstreamPipeOpener creates a DownstreamOpener that opens a remote
// Pipe via the durable accepted-binding lifecycle. acquire returns verified
// current/fresh access material (cached short-lived AccessLease or post-
// acceptance renewal) for the remote Pipe; the opener dials the bound locator
// and performs the tunnel handshake with that lease. No invite is ever
// required again after acceptance, and an expired lease simply causes
// reacquisition.
func newExactDownstreamPipeOpener(
	state *store.Store,
	h host.Host,
	pipeID string,
	acquire func(ctx context.Context) (usecase.AcceptedPipeAccess, error),
) engine.DownstreamOpener {
	return func(ctx context.Context, nextSpec domain.PipeSpec, traversal engine.TraversalState) (node.Carrier, string, error) {
		access, err := acquire(ctx)
		if err != nil {
			return nil, "", err
		}
		if access.RemotePipeID != pipeID {
			return nil, "", fmt.Errorf("exact_connection: acquired access for wrong pipe %s", access.RemotePipeID)
		}
		now := time.Now().UTC()
		if !access.AccessLease.ExpiresAt.After(now) {
			// The acquired material already expired (defense in depth against
			// a tiny cache/return race); reacquire once.
			access, err = acquire(ctx)
			if err != nil {
				return nil, "", err
			}
		}
		encodedLease, err := accesslease.Encode(access.AccessLease)
		if err != nil {
			return nil, "", errors.New("exact_connection: encode access lease")
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

		clientPublic := access.AccessLease.Subject

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
			AccessLease:    encodedLease,
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
