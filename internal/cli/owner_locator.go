package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/node"
	"github.com/origama/tubo/internal/reachability"
	"github.com/origama/tubo/internal/store"
)

type ownerRedemptionLocator struct {
	state           *store.Store
	resolveDefault  func(context.Context) (*reachability.Provider, error)
	processIdentity func(int) (string, error)
}

func newOwnerRedemptionLocator(state *store.Store, resolveDefault func(context.Context) (*reachability.Provider, error)) ownerRedemptionLocator {
	if resolveDefault == nil {
		resolveDefault = reachability.ResolveDefault
	}
	return ownerRedemptionLocator{
		state:           state,
		resolveDefault:  resolveDefault,
		processIdentity: store.ProcessIdentity,
	}
}

// LocateOwner derives one canonical owner circuit from current runtime state
// plus configured or verified-default relay facts. Locator conveys reachability
// only. Missing, stopped, stale, malformed, mismatched, or non-ready state
// fails closed before ShareExact can mint an invite.
func (l ownerRedemptionLocator) LocateOwner(ctx context.Context, pipeRef domain.Ref, pipeID string) (string, error) {
	if l.state == nil {
		return "", errors.New("runtime_unavailable: runtime state is unavailable")
	}
	spec, _, _, err := l.state.Pipe(ctx, pipeRef.String())
	if err != nil {
		return "", fmt.Errorf("runtime_unavailable: load owner Pipe: %w", err)
	}
	if spec.Ref != pipeRef || spec.PipeID != pipeID {
		return "", errors.New("runtime_unavailable: runtime Pipe identity mismatch")
	}
	run, err := l.state.Run(ctx, pipeRef.String())
	if err != nil {
		return "", fmt.Errorf("runtime_unavailable: owner Pipe runtime missing: %w", err)
	}
	if run.PrimaryRef != pipeRef.String() || run.Kind != "pipe" {
		return "", errors.New("runtime_unavailable: runtime belongs to wrong Pipe")
	}
	if run.Status != "ready" {
		return "", fmt.Errorf("runtime_unavailable: owner Pipe runtime is %s", run.Status)
	}
	if run.PID <= 0 || strings.TrimSpace(run.ProcessIdentity) == "" {
		return "", errors.New("runtime_unavailable: owner Pipe runtime has no live process identity")
	}
	identity, err := l.processIdentity(run.PID)
	if err != nil || identity != run.ProcessIdentity {
		return "", errors.New("runtime_unavailable: owner Pipe runtime is stale")
	}
	ownerPeer, err := peer.Decode(run.PeerID)
	if err != nil {
		return "", fmt.Errorf("runtime_unavailable: invalid owner runtime peer: %w", err)
	}

	relay, _, _, overlayErr := l.state.Overlay(ctx)
	if overlayErr != nil {
		if !errors.Is(overlayErr, domain.ErrNotFound) {
			return "", fmt.Errorf("runtime_unavailable: load relay configuration: %w", overlayErr)
		}
		provider, resolveErr := l.resolveDefault(ctx)
		if resolveErr != nil {
			return "", fmt.Errorf("runtime_unavailable: verified default relay unavailable: %w", resolveErr)
		}
		if provider == nil {
			return "", errors.New("runtime_unavailable: verified default relay unavailable")
		}
		relay, resolveErr = provider.PrimaryAddress()
		if resolveErr != nil {
			return "", fmt.Errorf("runtime_unavailable: verified default relay malformed: %w", resolveErr)
		}
	}
	locator, err := node.CircuitAddress(relay, ownerPeer)
	if err != nil {
		return "", fmt.Errorf("runtime_unavailable: owner redemption locator malformed: %w", err)
	}
	if run.Address != locator {
		return "", errors.New("runtime_unavailable: owner Pipe runtime locator is stale or mismatched")
	}
	return locator, nil
}
