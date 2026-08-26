package usecase

import (
	"context"

	"github.com/origama/tubo-rebuilt/internal/domain"
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
