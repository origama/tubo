package usecase

import (
	"context"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
)

// Carrier is the minimal raw full-duplex byte transport consumed by L4 use
// cases. 8B wires node.Carrier at the composition root; 8A tests use
// in-memory pipes. Reachability returns a carrier, never an authorization.
type Carrier interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

// ReachabilityAcquirer is an injected L1 capability that obtains a carrier
// toward an identified peer address. It never authorizes a Pipe open.
type ReachabilityAcquirer interface {
	Acquire(ctx context.Context, address string) (Carrier, error)
}

// RedemptionService performs exact-invite redemption over an acquired carrier
// and returns the canonical short-lived Access Lease. The concrete wire
// implementation (dial the redemption peer, challenge/response, receive the
// lease) is composed at the 8B composition root.
type RedemptionService interface {
	Redeem(ctx context.Context, carrier Carrier, invite accesslease.ExactInvite, recipientPublic string) (accesslease.AccessLease, error)
}

// RenewalRequest carries the post-acceptance renewal parameters. The original
// invite is never required again: the recipient identity and the verified
// owner-bound OwnerBinding the durable relationship carries are the proof
// anchors.
type RenewalRequest struct {
	PipeID string
	// RecipientPublic is the accepted recipient identity; renewal requires
	// possession proof by this exact key.
	RecipientPublic string
	// OwnerBinding is the canonical encoded owner-signed OwnerBinding stored
	// in the durable binding. It anchors issuer trust to the exact Pipe's
	// owner-bound Exact Authority.
	OwnerBinding string
}

// RenewalService performs post-acceptance exact renewal over an acquired
// carrier and returns a fresh short-lived AccessLease. The wire client proves
// recipient possession against a fresh server challenge; resending the
// original invite is never used for renewal.
type RenewalService interface {
	Renew(ctx context.Context, carrier Carrier, req RenewalRequest) (accesslease.AccessLease, error)
}

// OwnerRedemptionLocator resolves the current reachable exact-redemption
// locator for one running owner Pipe. It returns reachability metadata only;
// successful resolution never authorizes a remote Pipe open.
type OwnerRedemptionLocator interface {
	LocateOwner(ctx context.Context, pipeRef domain.Ref, pipeID string) (string, error)
}

// PipeStarter launches the runtime for a durable PipeSpec. 8B wires the real
// PipeRuntime; 8A tests inject fakes. The durable definition is never
// modified by starting or stopping.
type PipeStarter interface {
	Start(ctx context.Context, spec domain.PipeSpec) (StartPipeResult, error)
}

// AttachInput carries the parameters for creating a durable Service and
// Scope-less Pipe (Journey A).
type AttachInput struct {
	Name   string
	Target string // HTTP URL, TCP host:port, or unix path
	// ScopeRef is accepted for backward-compatible callers; Journey A
	// requires no Scope and the use case never consults Scope state.
	ScopeRef string
}

// AttachResult carries the durable references created by Attach.
type AttachResult struct {
	ServiceRef domain.Ref
	PipeRef    domain.Ref
	PipeID     string
	// Created is true when the Service definition was newly created.
	Created bool
	// PipeCreated is true when the Pipe definition was newly created.
	PipeCreated bool
}

// ShareExactInput carries the parameters for minting an ExactInvite from an
// owned Pipe.
type ShareExactInput struct {
	PipeRef string
}

// ShareExactResult carries the encoded exact invite and pipe metadata.
type ShareExactResult struct {
	PipeRef   domain.Ref
	PipeID    string
	Invite    string
	ExpiresAt time.Time
}

// ConnectExactInput carries the parameters for redeeming an exact invite and
// creating a durable local ingress Pipe.
type ConnectExactInput struct {
	Invite      string // canonical base64url ExactInvite
	RemotePipeID string // accepted remote Pipe ID; no invite needed (renewal)
	LocalListen string // host:port for the local ingress
	// Name is an optional friendly name for the local Pipe. When empty, a
	// deterministic name derived from the remote Pipe ID is used.
	Name string
}

// ConnectExactResult carries the durable refs of the local ingress Pipe.
type ConnectExactResult struct {
	PipeRef domain.Ref
	PipeID  string
	// TargetPipeID is the remote Pipe the local Pipe targets.
	TargetPipeID string
	// LocalListen is the effective persisted local listener; on compatible
	// reuse this is the existing listener, never the input echo.
	LocalListen string
}

// StartPipeInput carries the Pipe ref to start.
type StartPipeInput struct {
	PipeRef string
}

// StartPipeResult carries the runtime status of a started Pipe.
type StartPipeResult struct {
	PipeRef domain.Ref
	Status  string
}

// AcceptExactInviteInput carries the encoded one-time ExactInvite to accept.
type AcceptExactInviteInput struct {
	Invite string
}

// AcceptExactInviteResult carries the accepted remote Pipe identity. The use
// case persists the durable binding and caches the initial lease; it creates
// no local Pipe, no ingress, and starts no runtime.
type AcceptExactInviteResult struct {
	RemotePipeID  string
	RecipientPublic string
	// LeaseExpiresAt is the initial short-lived lease's expiry; acceptable
	// access is reacquired through the durable binding afterwards.
	LeaseExpiresAt time.Time
}

// AcceptedPipeAccess is usable exact access material for an already accepted
// remote Pipe: a verified current/fresh short-lived AccessLease plus the bound
// authority identity and locator needed to open the Pipe. It never creates an
// ingress and never mutates Pipe topology.
type AcceptedPipeAccess struct {
	RemotePipeID  string
	Locator       string
	AuthorityPublic string
	AccessLease   accesslease.AccessLease
}
