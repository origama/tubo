// Package openauth is the neutral, consumer-side open-authorization contract
// at the L2 receiving boundary.
//
// Issue #48 introduces this seam so that the receiving L2 path can authorize a
// remote Pipe open without importing any concrete L3 authority, revocation,
// epoch, issuer-trust, membership/policy, or persistence implementation. The
// contract is deliberately narrow: it carries only what the receiving boundary
// needs to decide whether a local target may be opened, plus the minimal result
// the boundary must bind back to the open before dialing.
//
// The package is dependency-free except the Go standard library. It must not
// carry Scope topology, discovery/publish material, a service target, a
// PipeRecord, reachability data, issuer/revocation trust stores, membership or
// policy, or any arbitrary metadata container.
//
// Scope independence: the same contract is consumed by an exact Pipe authority
// (issue #49) and by any future Scope Authority. Tunnel/session/receiving code
// never branches on public versus scoped topology.
package openauth

import (
	"context"
	"time"
)

// MaxLeaseBytes bounds the serialized access lease accepted on the receiving
// boundary. It is applied before AuthorizeOpen is invoked and mirrors the
// prototype authority token bound (48 KiB) so the receiving file need not
// import a concrete L3 package for the size check.
const MaxLeaseBytes = 48 << 10

// OpenRequest carries only the receiving-boundary facts an authorizer needs to
// decide a remote Pipe open. It is intentionally free of Scope, discovery,
// reachability, target, PipeRecord, issuer/revocation trust, membership and
// policy fields.
type OpenRequest struct {
	// PipeID is the exact Pipe audience the caller asks to open.
	PipeID string
	// ClientPublic is the caller's identity public key string.
	ClientPublic string
	// Challenge is the fresh receiver-generated challenge.
	Challenge string
	// ChallengeProof is the caller's possession proof over the challenge and Pipe.
	ChallengeProof string
	// AccessLease is the serialized short-lived authorization credential.
	AccessLease string
	// Now is the receiver's current time, used for freshness and expiry checks.
	Now time.Time
}

// Authorization is the minimal successful open-authorization result the
// receiving boundary requires before opening any local target. The receiving
// boundary independently binds PipeID, Subject, and non-zero future ExpiresAt
// back to the open (defense-in-depth); the concrete authorizer still owns
// cryptographic validation.
type Authorization struct {
	// PipeID is the exact Pipe authorized to open.
	PipeID string
	// Subject is the authorized caller identity public key string.
	Subject string
	// Issuer is the identity of the issuing authority.
	Issuer string
	// LeaseID is the unique authorization credential identifier.
	LeaseID string
	// ExpiresAt is the time after which the authorization must be terminated.
	ExpiresAt time.Time
	// AccessEpoch is the issuing authority's observed revocation epoch.
	AccessEpoch uint64
}

// OpenAuthorizer is the consumer-side contract injected into the receiving L2
// boundary. Any returned error is treated as authorization denied and must
// never open a local target. The OpenRequest carries no concrete L3 material,
// so implementations (exact or Scope authority) can back this seam behind the
// same connectivity contract.
type OpenAuthorizer interface {
	AuthorizeOpen(ctx context.Context, req OpenRequest) (Authorization, error)
}

// AcquiredAccess is the neutral result of acquiring exact access material
// for a remote Pipe open. It carries only what the L2 exact downstream open
// path needs: the target Pipe, the bound reachability locator, the canonical
// encoded short-lived AccessLease, and its expiry fence. It intentionally
// carries no invite, binding, renewal, persistence, or L4 policy material;
// the AccessLease stays the short-lived credential the receiving boundary
// validates.
type AcquiredAccess struct {
	// PipeID is the exact Pipe the access material is for.
	PipeID string
	// Locator is the verified bound owner/authority endpoint address.
	Locator string
	// AccessLease is the canonical base64url-encoded short-lived AccessLease.
	AccessLease string
	// ExpiresAt is the lease's expiry; the opener must not open past it.
	ExpiresAt time.Time
}

// AccessAcquirer is the injected neutral capability an L2 exact downstream
// open path consumes to obtain verified current/fresh short-lived access
// material for an already-accepted remote Pipe. Implementors live at L4 or at
// the composition boundary (renewal/binding know-how); the L2 open path only
// invokes this contract and never depends upward on L4.
type AccessAcquirer interface {
	AcquireAccess(ctx context.Context, remotePipeID string) (AcquiredAccess, error)
}
