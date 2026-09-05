package usecase

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"crypto/ed25519"
	"path/filepath"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/localkey"
)

// DefaultInviteTTL is the lifetime of an exact invite minted by ShareExact.
// It is bounded by accesslease.MaxTTL (10 minutes); exact invites are
// short-lived bootstrap credentials, never Access Leases.
const DefaultInviteTTL = 10 * time.Minute

// UseCases is the L4 use-case composition root for Journey A. It owns no
// storage, no runtime, no reachability, and no authority topology: every
// capability is injected through a narrow interface.
//
// Ownership: CLI (L5-ish adapter) -> UseCases -> narrow repositories plus
// injected capabilities. No *store.Store, libp2p, discovery, Scope policy,
// or concrete runtime appears anywhere in this package.
type UseCases struct {
	// Services is the narrow Service persistence port.
	Services ServiceRepository
	// Pipes is the narrow Pipe persistence port.
	Pipes PipeRepository

	// Secrets is the directory root for private keys. Owner keys live at
	// <Secrets>/pipes/<name>.key and dedicated Exact Authority keys at
	// <Secrets>/exact-authorities/<pipeID>.key (exactauthority.KeyPath).
	Secrets string

	// Now is the injected wall clock; default is time.Now().UTC().
	Now func() time.Time

	// OwnerLocator resolves current owner runtime reachability before ShareExact
	// provisions authority state or mints an invite. A missing capability fails
	// closed; static relay or run addresses cannot bypass live locator resolution.
	OwnerLocator OwnerRedemptionLocator

	// RedemptionProtocol is the neutral redemption protocol id embedded in
	// minted OwnerBindings (e.g. "/tubo/exact-redemption/1").
	RedemptionProtocol string

	// Redemption performs exact-invite redemption over an acquired carrier.
	// nil is allowed and produces an explicit redemption_unavailable error.
	Redemption RedemptionService

	// Renewal performs post-acceptance exact renewal over an acquired carrier.
	// nil is allowed and produces an explicit renewal_unavailable error.
	Renewal RenewalService

	// Reach acquires a raw-duplex carrier toward an identified peer address.
	// It never authorizes. nil is allowed and produces an explicit
	// reachability_unavailable error.
	Reach ReachabilityAcquirer

	// Starter starts the runtime for a durable PipeSpec. nil is allowed and
	// produces an explicit runtime_unavailable error. 8B wires the real
	// PipeRuntime; 8A tests inject fakes.
	Starter PipeStarter

	// Bindings persists the durable accepted remote-Pipe binding (relationship
	// + reachability knowledge; survives lease expiry and local-Pipe removal).
	// nil is allowed for backward compatibility; AcceptExactInvite and
	// AcquireAcceptedPipeAccess fail explicitly when a binding is needed.
	Bindings RemotePipeBindingRepo

	// LeaseCache persists the ephemeral current AccessLease cache. Expired
	// cached material fails closed and simply causes reacquisition. nil is
	// allowed; acquisition then never reuses a cached lease.
	LeaseCache AccessLeaseCacheRepo
}

// New returns a UseCases value with the default wall clock. Callers must
// inject repositories and capabilities before use.
func New(services ServiceRepository, pipes PipeRepository) UseCases {
	return UseCases{
		Services: services,
		Pipes:    pipes,
		Now:      func() time.Time { return time.Now().UTC() },
	}
}

func (u UseCases) now() time.Time {
	if u.Now != nil {
		return u.Now().UTC()
	}
	return time.Now().UTC()
}

// ownerKey creates-or-loads the deterministic owner identity key for a
// durable Pipe ref. The owner key file path is the durable identity source:
// reusing the same path after restart yields the same private key and the
// same self-deriving Pipe ID.
func (u UseCases) ownerKey(ref domain.Ref) (ed25519.PrivateKey, string, error) {
	if strings.TrimSpace(u.Secrets) == "" {
		return nil, "", errors.New("invalid_input: secrets directory is not configured")
	}
	path := filepath.Join(u.Secrets, "pipes", ref.Name+".key")
	private, err := localkey.Provision(path)
	if err != nil {
		return nil, "", err
	}
	return private, path, nil
}

// redemptionAddress extracts the bounded neutral redemption peer address from
// the invite's owner binding. The exact path acquires reachability toward
// this address; there is no Scope, discovery, or membership lookup.
func redemptionAddress(invite accesslease.ExactInvite) (string, error) {
	if invite.Binding == "" {
		return "", errors.New("invalid_input: invite carries no owner binding")
	}
	binding, err := accesslease.DecodeOwnerBinding(invite.Binding)
	if err != nil {
		return "", errors.New("invalid_input: invite owner binding invalid")
	}
	if binding.RedemptionPeers == "" {
		return "", errors.New("invalid_input: invite carries no redemption peer address")
	}
	first, _, _ := strings.Cut(binding.RedemptionPeers, ",")
	if strings.TrimSpace(first) == "" {
		return "", errors.New("invalid_input: invite carries no redemption peer address")
	}
	return strings.TrimSpace(first), nil
}

// recipientRefFor derives the deterministic local Pipe ref for a remote
// invite audience, or the friendly name when one is given. The name is
// bounded to the domain resource-name pattern.
func recipientRefFor(pipeID, name string) (domain.Ref, error) {
	if name != "" {
		return domain.NewRef(domain.KindPipe, name)
	}
	refName := "connect-" + pipeID
	if len(refName) > 63 {
		refName = refName[:63]
	}
	return domain.NewRef(domain.KindPipe, refName)
}

func persistenceError(what string, err error) error {
	return fmt.Errorf("persistence_failed: %s: %w", what, err)
}

func authorizationDenied(err error) error {
	return fmt.Errorf("authorization_denied: %w", err)
}
