package usecase

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
)

// Attach creates a durable Service definition and a Scope-less owned Pipe
// targeting it (Journey A). It never starts a runtime: 8A defines the
// composition; 8B wires StartPipe.
//
// Mutations and ordering:
//  1. Validate and upsert the durable Service definition.
//  2. Create-or-load the Pipe owner identity key (durable identity source).
//  3. Upsert the Scope-less Pipe definition targeting the Service.
//
// Failure semantics (no generic transaction coordinator):
//   - Invalid input fails before any write.
//   - Service upsert failure fails closed before any Pipe write.
//   - Pipe owner-key failure leaves the durable Service definition in
//     place and reports persistence_failed explicitly.
//   - Pipe upsert failure leaves the durable Service definition in place,
//     creates no Pipe definition, and reports persistence_failed explicitly.
//
// When a Pipe definition already exists for the same ref, its identity
// (PipeID, CreatedAt) is preserved; the use case never redefines it.
func (u UseCases) Attach(ctx context.Context, in AttachInput) (AttachResult, error) {
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.Target) == "" {
		return AttachResult{}, errors.New("invalid_input: attach requires name and target")
	}
	service, err := serviceSpecFromTarget(in.Name, in.Target)
	if err != nil {
		return AttachResult{}, fmt.Errorf("invalid_input: %w", err)
	}
	// Step 1: durable Service definition first, so a Pipe never outlives its
	// target Service definition.
	created, err := u.Services.Upsert(ctx, service)
	if err != nil {
		return AttachResult{}, persistenceError("service", err)
	}
	pipeRef, err := domain.NewRef(domain.KindPipe, in.Name)
	if err != nil {
		return AttachResult{}, fmt.Errorf("invalid_input: %w", err)
	}
	// Step 2: Pipe owner identity. Deterministic path per ref; create-or-load.
	owner, ownerKeyFile, err := u.ownerKey(pipeRef)
	if err != nil {
		return AttachResult{}, persistenceError("pipe owner key", err)
	}
	now := u.now()
	spec := domain.PipeSpec{
		Ref:     pipeRef,
		PipeID:  accesslease.PipeIDFromOwner(owner.Public().(ed25519.PublicKey)),
		Ingress: domain.PipeIngress{Kind: domain.IngressTubo, Protocol: "raw"},
		Target:  service.Ref,
		// Journey A exact share: published but not discoverable, invite-only.
		Visibility: domain.VisibilityUnlisted,
		Access:     domain.AccessInviteOnly,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	// Preserve the durable identity of an existing definition for the ref.
	if existing, existingKey, existingErr := u.Pipes.Get(ctx, pipeRef.String()); existingErr == nil {
		spec.PipeID = existing.PipeID
		spec.CreatedAt = existing.CreatedAt
		spec.UpdatedAt = existing.UpdatedAt
		ownerKeyFile = existingKey
	} else if !errors.Is(existingErr, domain.ErrNotFound) {
		return AttachResult{}, u.pipesGetError(existingErr)
	}
	// Step 3: durable Pipe definition.
	if _, err := u.Pipes.Upsert(ctx, spec, ownerKeyFile); err != nil {
		return AttachResult{}, persistenceError("pipe", err)
	}
	return AttachResult{
		ServiceRef: service.Ref,
		PipeRef:    pipeRef,
		PipeID:     spec.PipeID,
		Created:    created,
	}, nil
}

func (u UseCases) pipesGetError(err error) error {
	return persistenceError("pipe", err)
}

// serviceSpecFromTarget validates the attach target and returns the durable
// Service definition. Target schemes are http/https/tcp/unix per domain.
func serviceSpecFromTarget(name, target string) (domain.ServiceSpec, error) {
	ref, err := domain.NewRef(domain.KindService, name)
	if err != nil {
		return domain.ServiceSpec{}, err
	}
	scheme := strings.ToLower(strings.SplitN(target, ":", 2)[0])
	kind := domain.ServiceKind(scheme)
	if scheme == "https" {
		kind = domain.ServiceHTTP
	}
	spec := domain.ServiceSpec{Ref: ref, Kind: kind, Target: target}
	return spec, spec.Validate()
}
