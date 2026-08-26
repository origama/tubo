package usecase

import (
	"context"
	"errors"
	"fmt"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

// StartPipe loads a durable PipeSpec and delegates runtime launch to the
// injected PipeStarter. 8B wires the real PipeRuntime; 8A tests inject fakes.
// Starting never redefines the durable PipeSpec; failure leaves the durable
// definition intact and reports the exact error.
func (u UseCases) StartPipe(ctx context.Context, in StartPipeInput) (StartPipeResult, error) {
	spec, _, err := u.Pipes.Get(ctx, in.PipeRef)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return StartPipeResult{}, fmt.Errorf("not_found: %w", err)
		}
		return StartPipeResult{}, persistenceError("pipe", err)
	}
	if u.Starter == nil {
		return StartPipeResult{}, errors.New("runtime_unavailable: pipe runtime is not wired")
	}
	return u.Starter.Start(ctx, spec)
}
