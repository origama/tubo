package usecase

import (
	"context"

	"github.com/origama/tubo/internal/domain"
)

// ServiceRepository is the narrow persistence port for durable Service
// definitions. It lives beside its consumer (usecase package), not in a
// generic ports package.
type ServiceRepository interface {
	// Upsert saves a ServiceSpec. created=true indicates a new record.
	// When the spec already exists with identical content, Upsert returns
	// (false, nil). A conflicting existing spec is rejected (error).
	// Spec validation is the caller's responsibility; the repository
	// must propagate invalid input before writing.
	Upsert(ctx context.Context, spec domain.ServiceSpec) (created bool, err error)

	// Get retrieves a ServiceSpec by ref. Returns domain.ErrNotFound when
	// the resource does not exist.
	Get(ctx context.Context, ref domain.Ref) (domain.ServiceSpec, error)

	// Delete removes a ServiceSpec by ref. Returns domain.ErrNotFound if
	// the resource does not exist.
	Delete(ctx context.Context, ref domain.Ref) error
}

// PipeRepository is the narrow persistence port for durable Pipe definitions.
type PipeRepository interface {
	// Upsert saves a PipeSpec with its owner key file path. created=true
	// indicates a new record. When the spec already exists with identical
	// content, Upsert returns (false, nil). A conflicting existing spec is
	// rejected (error).
	Upsert(ctx context.Context, spec domain.PipeSpec, ownerKeyFile string) (created bool, err error)

	// Get retrieves a PipeSpec by ref or pipe_id string. The third return
	// value is the owner key file path. Returns domain.ErrNotFound when
	// the resource does not exist.
	Get(ctx context.Context, identifier string) (spec domain.PipeSpec, keyFile string, err error)

	// Delete removes a PipeSpec by ref. Returns domain.ErrNotFound if the
	// resource does not exist.
	Delete(ctx context.Context, ref string) error
}

// RemotePipeBindingRepo is the narrow consumer-owned persistence port for the
// durable accepted remote-Pipe binding. The binding is long-lived
// relationship/reachability knowledge needed to reacquire exact access; it
// never depends on a local ingress Pipe ref and never holds the AccessLease.
// It is NOT a generic credential repository or a PipeSpec field.
type RemotePipeBindingRepo interface {
	// Get retrieves the durable binding for a remote PipeID. Returns
	// domain.ErrNotFound when no accepted binding exists.
	Get(ctx context.Context, remotePipeID string) (domain.RemotePipeBinding, error)

	// Put stores or replaces the durable binding for a remote PipeID.
	Put(ctx context.Context, b domain.RemotePipeBinding) error

	// Delete removes the durable binding for a remote PipeID.
	Delete(ctx context.Context, remotePipeID string) error
}

// AccessLeaseCacheRepo is the narrow consumer-owned persistence port for the
// ephemeral short-lived AccessLease cache. The cache is runtime authorization
// material only; it is never the source of truth for the accepted
// relationship, and an expired cached lease simply causes reacquisition.
type AccessLeaseCacheRepo interface {
	// Get retrieves the cached lease for a remote PipeID. Returns
	// domain.ErrNotFound when absent or expired (fail closed).
	Get(ctx context.Context, remotePipeID string) (domain.AccessLeaseCache, error)

	// Put stores or replaces the cached lease for a remote PipeID.
	Put(ctx context.Context, c domain.AccessLeaseCache) error

	// Delete removes the cached lease for a remote PipeID.
	Delete(ctx context.Context, remotePipeID string) error
}
