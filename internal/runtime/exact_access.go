// Package runtime owns the Journey A composition adapters that expose the L4
// exact accepted-binding acquisition behind the neutral L2 openauth contract.
//
// exact_access.go is the ONLY runtime composition adapter that wires
// usecase.UseCases for the exact downstream open path. It is a composition
// boundary: it must not be imported by the L2 execution path files
// (internal/runtime/pipe.go, internal/runtime/exact_downstream.go) — arch
// guards enforce that. The L2 exact open path receives the resulting neutral
// openauth.AccessAcquirer and never depends upward on L4.
package runtime

import (
	"context"
	"errors"
	"sync"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/openauth"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

// useCaseAccessAcquirer wraps the L4 AcquireAcceptedPipeAccess use case
// behind the neutral openauth.AccessAcquirer contract. Reachability is wired
// lazily on first use via the injected factory so a plain (non-exact or
// scoped) pipe run never opens an extra libp2p host.
type useCaseAccessAcquirer struct {
	mu        sync.Mutex
	uc        usecase.UseCases
	makeReach func() (usecase.ReachabilityAcquirer, error)
}

// NewLazyUseCaseAccessAcquirer composes an openauth.AccessAcquirer over the
// L4 use case. makeReach is invoked once, on first acquisition, to wire the
// reachability capability (e.g. an overlay host opening the renewal protocol).
func NewLazyUseCaseAccessAcquirer(uc usecase.UseCases, makeReach func() (usecase.ReachabilityAcquirer, error)) openauth.AccessAcquirer {
	return &useCaseAccessAcquirer{uc: uc, makeReach: makeReach}
}

// AcquireAccess implements openauth.AccessAcquirer.
func (a *useCaseAccessAcquirer) AcquireAccess(ctx context.Context, remotePipeID string) (openauth.AcquiredAccess, error) {
	var zero openauth.AcquiredAccess
	a.mu.Lock()
	if a.uc.Reach == nil {
		if a.makeReach == nil {
			a.mu.Unlock()
			return zero, errors.New("reachability_unavailable: exact access reachability is not wired")
		}
		reach, err := a.makeReach()
		if err != nil {
			a.mu.Unlock()
			return zero, err
		}
		a.uc.Reach = reach
	}
	a.mu.Unlock()
	access, err := a.uc.AcquireAcceptedPipeAccess(ctx, remotePipeID)
	if err != nil {
		return zero, err
	}
	encoded, err := accesslease.Encode(access.AccessLease)
	if err != nil {
		return zero, err
	}
	return openauth.AcquiredAccess{
		PipeID:      access.RemotePipeID,
		Locator:     access.Locator,
		AccessLease: encoded,
		ExpiresAt:   access.AccessLease.ExpiresAt.UTC(),
	}, nil
}
