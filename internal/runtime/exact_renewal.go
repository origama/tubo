// Package runtime owns the Journey A exact renewal client and host-reusing
// carrier acquirer. These are adapters that wire the usecase-level interfaces
// to the concrete libp2p/renewal protocol at the composition root.
package runtime

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/localkey"
	"github.com/origama/tubo-rebuilt/internal/node"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

// Compile-time check: the renewal client satisfies the usecase port.
var _ usecase.RenewalService = (*ExactRenewalClient)(nil)

// ExactRenewalClient performs the /tubo/exact-renewal/1 challenge exchange
// over an acquired carrier: request (Pipe + accepted recipient + owner-bound
// binding) -> server challenge -> recipient possession proof -> fresh
// short-lived AccessLease. It never requires the original invite.
//
// The recipient private key must already exist and match the accepted binding:
// provisioning a fresh key here would silently change identity and is never
// done (Load only).
type ExactRenewalClient struct {
	secretsDir string
}

// NewExactRenewalClient creates a renewal client that loads the accepted
// recipient private key from the secrets directory.
func NewExactRenewalClient(secretsDir string) *ExactRenewalClient {
	return &ExactRenewalClient{secretsDir: secretsDir}
}

// Renew implements usecase.RenewalService.
func (s *ExactRenewalClient) Renew(ctx context.Context, carrier usecase.Carrier, req usecase.RenewalRequest) (accesslease.AccessLease, error) {
	var zero accesslease.AccessLease
	if req.PipeID == "" || req.RecipientPublic == "" || req.OwnerBinding == "" {
		return zero, errors.New("exact_renewal: invalid request")
	}
	if ctx == nil || carrier == nil {
		return zero, errors.New("exact_renewal: missing carrier")
	}
	recipientPrivate, err := localkey.Load(filepath.Join(s.secretsDir, "pipes", "connect-"+req.PipeID+".key"))
	if err != nil {
		return zero, fmt.Errorf("exact_renewal: recipient key unavailable: %w", err)
	}
	want := accesslease.PublicString(recipientPrivate.Public().(ed25519.PublicKey))
	if want != req.RecipientPublic {
		return zero, errors.New("exact_renewal: recipient key does not match accepted binding")
	}

	// Step 1: bounded request.
	if err := wire.WriteFrame(carrier, wire.ExactRenewalRequest{
		PipeID:          req.PipeID,
		RecipientPublic: req.RecipientPublic,
		OwnerBinding:    req.OwnerBinding,
	}); err != nil {
		return zero, fmt.Errorf("exact_renewal: send request: %w", err)
	}

	// Step 2: read the fresh server challenge.
	var challenge wire.ExactRenewalChallenge
	if err := wire.ReadFrame(carrier, &challenge); err != nil {
		return zero, fmt.Errorf("exact_renewal: read challenge: %w", err)
	}
	if challenge.Challenge == "" {
		return zero, errors.New("exact_renewal: empty challenge")
	}

	// Step 3: sign the challenge possession proof with the accepted identity.
	proof := accesslease.SignProof(recipientPrivate, accesslease.RenewProofPurpose, req.PipeID, challenge.Challenge)
	if err := wire.WriteFrame(carrier, wire.ExactRenewalProof{ChallengeProof: proof}); err != nil {
		return zero, fmt.Errorf("exact_renewal: send proof: %w", err)
	}

	// Step 4: read the response and decode the fresh lease.
	var response wire.ExactRenewalResponse
	if err := wire.ReadFrame(carrier, &response); err != nil {
		return zero, fmt.Errorf("exact_renewal: read response: %w", err)
	}
	if !response.OK {
		if response.Error != "" {
			return zero, fmt.Errorf("exact_renewal: server denied: %s", response.Error)
		}
		return zero, errors.New("exact_renewal: server denied")
	}
	if response.AccessLease == "" {
		return zero, errors.New("exact_renewal: empty access lease")
	}
	lease, err := accesslease.Decode(response.AccessLease)
	if err != nil {
		return zero, fmt.Errorf("exact_renewal: decode lease: %w", err)
	}
	runtimeInfof("exact lease renewed pipe=%s jti=%s expires=%s", req.PipeID, lease.JTI, lease.ExpiresAt.Format(time.RFC3339))
	return lease, nil
}

// hostCarrierAcquirer dials a peer over an already-open libp2p host and opens
// the configured protocol stream. It lets the detached runtime reuse its own
// host's relay reachability for renewal instead of opening a second host.
type hostCarrierAcquirer struct {
	h      host.Host
	protoID protocol.ID
}

// newHostCarrierAcquirer creates a reachability acquirer over the existing
// host. Close is a no-op: the host is owned by the caller.
func newHostCarrierAcquirer(h host.Host, protoID protocol.ID) *hostCarrierAcquirer {
	return &hostCarrierAcquirer{h: h, protoID: protoID}
}

// Acquire implements usecase.ReachabilityAcquirer.
func (a *hostCarrierAcquirer) Acquire(ctx context.Context, address string) (usecase.Carrier, error) {
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	remotePeerID, err := node.DialPeer(setup, a.h, address)
	if err != nil {
		return nil, fmt.Errorf("host_reachability: dial: %w", err)
	}
	stream, err := a.h.NewStream(core.WithAllowLimitedConn(setup, "tubo exact renewal"), remotePeerID, a.protoID)
	if err != nil {
		return nil, fmt.Errorf("host_reachability: new stream: %w", err)
	}
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	return node.WrapStream(stream), nil
}

// exactLeaseTTL returns the configured short-lived exact AccessLease lifetime
// for redemption/renewal minting at the owner side. It is bounded to
// (0, MaxTTL] and defaults to MaxTTL (10 minutes). Operators may shorten it
// (e.g. TUBO_EXACT_LEASE_TTL=3s) for test harnesses that must cross the
// expiry boundary; the lease always remains short-lived, and revocation
// enforcement at the receiving boundary is unchanged.
func exactLeaseTTL() time.Duration {
	if raw := os.Getenv("TUBO_EXACT_LEASE_TTL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 && d <= accesslease.MaxTTL {
			return d
		}
	}
	return accesslease.MaxTTL
}