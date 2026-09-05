package store

import (
	"context"
	"fmt"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

// ServiceRepositoryContract is the consumer-owned narrow Service persistence
// contract (duplicated here so internal/store does not import internal/usecase).
// Consumers (internal/usecase) define the same shape as their port.
type ServiceRepositoryContract interface {
	Upsert(ctx context.Context, spec domain.ServiceSpec) (created bool, err error)
	Get(ctx context.Context, ref domain.Ref) (domain.ServiceSpec, error)
	Delete(ctx context.Context, ref domain.Ref) error
}

// PipeRepositoryContract is the consumer-owned narrow Pipe persistence
// contract (duplicated here so internal/store does not import internal/usecase).
type PipeRepositoryContract interface {
	Upsert(ctx context.Context, spec domain.PipeSpec, ownerKeyFile string) (created bool, err error)
	Get(ctx context.Context, identifier string) (spec domain.PipeSpec, keyFile string, err error)
	Delete(ctx context.Context, ref string) error
}

// Compile-time checks: *Store-backed adapters satisfy the contracts.
var _ ServiceRepositoryContract = (*ServiceRepoAdapter)(nil)
var _ PipeRepositoryContract = (*PipeRepoAdapter)(nil)
var _ RemotePipeBindingRepoContract = (*RemotePipeBindingAdapter)(nil)
var _ AccessLeaseCacheRepoContract = (*AccessLeaseCacheAdapter)(nil)

// ServiceRepoAdapter adapts *Store to the consumer-owned narrow Service
// persistence contract (ServiceRepositoryContract; the same shape is the
// ServiceRepository port beside internal/usecase).
type ServiceRepoAdapter struct {
	s *Store
}

// NewServiceRepoAdapter returns the adapter or nil when s is nil.
func NewServiceRepoAdapter(s *Store) *ServiceRepoAdapter {
	if s == nil {
		return nil
	}
	return &ServiceRepoAdapter{s: s}
}

func (a *ServiceRepoAdapter) Upsert(ctx context.Context, spec domain.ServiceSpec) (bool, error) {
	return a.s.PutService(ctx, spec)
}

func (a *ServiceRepoAdapter) Get(ctx context.Context, ref domain.Ref) (domain.ServiceSpec, error) {
	return a.s.Service(ctx, ref)
}

func (a *ServiceRepoAdapter) Delete(ctx context.Context, ref domain.Ref) error {
	return a.s.DeleteService(ctx, ref.String())
}

// PipeRepoAdapter adapts *Store to the consumer-owned narrow Pipe
// persistence contract (PipeRepositoryContract; the same shape is the
// PipeRepository port beside internal/usecase).
type PipeRepoAdapter struct {
	s *Store
}

// NewPipeRepoAdapter returns the adapter or nil when s is nil.
func NewPipeRepoAdapter(s *Store) *PipeRepoAdapter {
	if s == nil {
		return nil
	}
	return &PipeRepoAdapter{s: s}
}

func (a *PipeRepoAdapter) Upsert(ctx context.Context, spec domain.PipeSpec, ownerKeyFile string) (bool, error) {
	return a.s.PutPipe(ctx, spec, ownerKeyFile, "")
}

func (a *PipeRepoAdapter) Get(ctx context.Context, identifier string) (domain.PipeSpec, string, error) {
	spec, key, _, err := a.s.Pipe(ctx, identifier)
	if err != nil {
		return domain.PipeSpec{}, "", fmt.Errorf("pipe %s: %w", identifier, err)
	}
	return spec, key, nil
}

func (a *PipeRepoAdapter) Delete(ctx context.Context, ref string) error {
	_, err := a.s.DeletePipe(ctx, ref)
	return err
}

// RemotePipeBindingRepoContract is the consumer-owned narrow durable accepted
// remote-Pipe binding persistence contract (duplicated here so internal/store
// does not import internal/usecase).
type RemotePipeBindingRepoContract interface {
	Get(ctx context.Context, remotePipeID string) (domain.RemotePipeBinding, error)
	Put(ctx context.Context, b domain.RemotePipeBinding) error
	Delete(ctx context.Context, remotePipeID string) error
}

// AccessLeaseCacheRepoContract is the consumer-owned narrow ephemeral
// AccessLease cache persistence contract (duplicated here so internal/store
// does not import internal/usecase).
type AccessLeaseCacheRepoContract interface {
	Get(ctx context.Context, remotePipeID string) (domain.AccessLeaseCache, error)
	Put(ctx context.Context, c domain.AccessLeaseCache) error
	Delete(ctx context.Context, remotePipeID string) error
}

// RemotePipeBindingAdapter adapts *Store to the consumer-owned durable binding
// contract.
type RemotePipeBindingAdapter struct {
	s *Store
}

// NewRemotePipeBindingAdapter returns the adapter or nil when s is nil.
func NewRemotePipeBindingAdapter(s *Store) *RemotePipeBindingAdapter {
	if s == nil {
		return nil
	}
	return &RemotePipeBindingAdapter{s: s}
}

func (a *RemotePipeBindingAdapter) Get(ctx context.Context, remotePipeID string) (domain.RemotePipeBinding, error) {
	return a.s.GetRemotePipeBinding(ctx, remotePipeID)
}

func (a *RemotePipeBindingAdapter) Put(ctx context.Context, b domain.RemotePipeBinding) error {
	return a.s.PutRemotePipeBinding(ctx, b)
}

func (a *RemotePipeBindingAdapter) Delete(ctx context.Context, remotePipeID string) error {
	return a.s.DeleteRemotePipeBinding(ctx, remotePipeID)
}

// AccessLeaseCacheAdapter adapts *Store to the consumer-owned ephemeral cache
// contract.
type AccessLeaseCacheAdapter struct {
	s *Store
}

// NewAccessLeaseCacheAdapter returns the adapter or nil when s is nil.
func NewAccessLeaseCacheAdapter(s *Store) *AccessLeaseCacheAdapter {
	if s == nil {
		return nil
	}
	return &AccessLeaseCacheAdapter{s: s}
}

func (a *AccessLeaseCacheAdapter) Get(ctx context.Context, remotePipeID string) (domain.AccessLeaseCache, error) {
	return a.s.GetAccessLeaseCache(ctx, remotePipeID)
}

func (a *AccessLeaseCacheAdapter) Put(ctx context.Context, c domain.AccessLeaseCache) error {
	return a.s.PutAccessLeaseCache(ctx, c)
}

func (a *AccessLeaseCacheAdapter) Delete(ctx context.Context, remotePipeID string) error {
	return a.s.DeleteAccessLeaseCache(ctx, remotePipeID)
}
