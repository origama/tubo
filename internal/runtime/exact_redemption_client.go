// Package runtime owns the Journey A client redemption service and
// reachability acquirer. These are adapters that wire the usecase-level
// interfaces to the concrete libp2p/redemption protocol at the composition
// root. They are NOT command handlers; they are constructed in App.Run and
// injected into UseCases.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo/internal/accesslease"
	"github.com/origama/tubo/internal/localkey"
	"github.com/origama/tubo/internal/node"
	wire "github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/store"
	"github.com/origama/tubo/internal/usecase"
)

// Compile-time check: these adapters satisfy the usecase interfaces.
var _ usecase.ReachabilityAcquirer = (*ExactReachabilityAcquirer)(nil)
var _ usecase.RedemptionService = (*ExactRedemptionClient)(nil)

// ExactReachabilityAcquirer opens a libp2p host from the store overlay
// config and dials the peer address, returning a raw carrier over the
// configured protocol. It is the L1→L2 adapter for Journey A.
type ExactReachabilityAcquirer struct {
	state    *store.Store
	protocol protocol.ID
	host     host.Host
}

// NewExactReachabilityAcquirer creates a reachability acquirer that opens
// a libp2p host and dials the given address. The host is opened once at
// construction and reused across Acquire calls.
func NewExactReachabilityAcquirer(ctx context.Context, state *store.Store, proto protocol.ID) (*ExactReachabilityAcquirer, error) {
	h, relayAddr, err := OpenOverlayHost(ctx, state, "exact-reach")
	if err != nil {
		return nil, fmt.Errorf("exact_reachability: open host: %w", err)
	}
	_ = relayAddr // The host is connected to the relay; we don't need the address.
	return &ExactReachabilityAcquirer{
		state:    state,
		protocol: proto,
		host:     h,
	}, nil
}

// Acquire dials the peer address and opens a stream with the configured
// protocol, returning the raw carrier.
func (a *ExactReachabilityAcquirer) Acquire(ctx context.Context, address string) (usecase.Carrier, error) {
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	remotePeerID, err := node.DialPeer(setup, a.host, address)
	if err != nil {
		return nil, fmt.Errorf("exact_reachability: dial: %w", err)
	}
	stream, err := a.host.NewStream(core.WithAllowLimitedConn(setup, "tubo redemption"), remotePeerID, a.protocol)
	if err != nil {
		return nil, fmt.Errorf("exact_reachability: new stream: %w", err)
	}
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	return node.WrapStream(stream), nil
}

// Close closes the underlying libp2p host.
func (a *ExactReachabilityAcquirer) Close() error {
	return a.host.Close()
}

// ExactRedemptionClient is the client-side redemption service for Journey A.
// It performs the challenge-response protocol over a carrier obtained from
// the reachability acquirer.
type ExactRedemptionClient struct {
	secretsDir string
}

// NewExactRedemptionClient creates a redemption client that loads the
// recipient private key from the secrets directory.
func NewExactRedemptionClient(secretsDir string) *ExactRedemptionClient {
	return &ExactRedemptionClient{secretsDir: secretsDir}
}

// Redeem performs the exact-invite redemption protocol over the carrier.
// It sends the invite, receives a challenge, signs it with the recipient
// private key, and returns the AccessLease.
func (s *ExactRedemptionClient) Redeem(ctx context.Context, carrier usecase.Carrier, invite accesslease.ExactInvite, recipientPublic string) (accesslease.AccessLease, error) {
	// Derive the recipient key path from the invite audience pipe ID.
	pipeID := invite.AudiencePipeID
	keyPath := filepath.Join(s.secretsDir, "pipes", "connect-"+pipeID+".key")

	recipientPrivate, err := localkey.Load(keyPath)
	if err != nil {
		// The key might not exist yet (first connect); try Provision.
		recipientPrivate, err = localkey.Provision(keyPath)
		if err != nil {
			return accesslease.AccessLease{}, fmt.Errorf("exact_redemption: recipient key: %w", err)
		}
	}

	// Step 1: Send the encoded invite.
	encodedInvite, err := accesslease.EncodeInvite(invite)
	if err != nil {
		return accesslease.AccessLease{}, fmt.Errorf("exact_redemption: encode invite: %w", err)
	}
	if err := wire.WriteFrame(carrier, wire.ExactRedemptionRequest{Invite: encodedInvite}); err != nil {
		return accesslease.AccessLease{}, fmt.Errorf("exact_redemption: send invite: %w", err)
	}

	// Step 2: Read the challenge.
	var challenge wire.ExactRedemptionChallenge
	if err := wire.ReadFrame(carrier, &challenge); err != nil {
		return accesslease.AccessLease{}, fmt.Errorf("exact_redemption: read challenge: %w", err)
	}
	if challenge.Challenge == "" {
		return accesslease.AccessLease{}, errors.New("exact_redemption: empty challenge")
	}

	// Step 3: Sign the challenge proof.
	proof := accesslease.SignProof(recipientPrivate, accesslease.RedeemProofPurpose, invite.JTI, invite.AudiencePipeID, challenge.Challenge)
	if err := wire.WriteFrame(carrier, wire.ExactRedemptionProof{
		RecipientPublic: recipientPublic,
		ChallengeProof:  proof,
	}); err != nil {
		return accesslease.AccessLease{}, fmt.Errorf("exact_redemption: send proof: %w", err)
	}

	// Step 4: Read the response.
	var response wire.ExactRedemptionResponse
	if err := wire.ReadFrame(carrier, &response); err != nil {
		return accesslease.AccessLease{}, fmt.Errorf("exact_redemption: read response: %w", err)
	}
	if !response.OK {
		if response.Error != "" {
			return accesslease.AccessLease{}, fmt.Errorf("exact_redemption: server denied: %s", response.Error)
		}
		return accesslease.AccessLease{}, errors.New("exact_redemption: server denied")
	}
	if response.AccessLease == "" {
		return accesslease.AccessLease{}, errors.New("exact_redemption: empty access lease")
	}

	// Decode and return the lease.
	lease, err := accesslease.Decode(response.AccessLease)
	if err != nil {
		return accesslease.AccessLease{}, fmt.Errorf("exact_redemption: decode lease: %w", err)
	}
	return lease, nil
}
