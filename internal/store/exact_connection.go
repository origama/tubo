// Package store owns the exact connection state persistence for Journey A.
// This is a narrow repository for remote PipeID + verified locator + AccessLease
// + expiry. It is NOT a generic credential/runtime-state framework, a second
// Pipe model, a fake PipeRecord, or a Scope shim. Expired material fails
// closed. No lease refresh in A0.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

// ExactConnectionState stores the transient authorization material needed to
// open a remote Pipe via the exact (Scope-less) path. It is deliberately
// separate from PipeSpec (durable) and from connect_leases (Scope-required).
type ExactConnectionState struct {
	// RemotePipeID is the PipeID of the remote Pipe (the invite audience).
	RemotePipeID string `json:"remote_pipe_id"`
	// Locator is the verified peer address from the invite's OwnerBinding
	// redemption peer address.
	Locator string `json:"locator"`
	// AccessLease is the canonical base64url-encoded AccessLease.
	AccessLease string `json:"access_lease"`
	// ExpiresAt is the lease expiry; zero means no lease stored.
	ExpiresAt time.Time `json:"expires_at"`
	// LocalPipeRef is the ref of the local Pipe that targets this remote.
	LocalPipeRef string `json:"local_pipe_ref"`
}

// GetExactConnectionState retrieves the exact connection state for a remote
// PipeID. Returns domain.ErrNotFound when no state exists or the lease is
// expired (fail closed).
func (s *Store) GetExactConnectionState(ctx context.Context, remotePipeID string) (ExactConnectionState, error) {
	var state ExactConnectionState
	var expires string
	err := s.db.QueryRowContext(ctx,
		`SELECT remote_pipe_id, locator, access_lease, expires_at, local_pipe_ref FROM exact_connections WHERE remote_pipe_id=?`,
		remotePipeID,
	).Scan(&state.RemotePipeID, &state.Locator, &state.AccessLease, &expires, &state.LocalPipeRef)
	if errors.Is(err, sql.ErrNoRows) {
		return ExactConnectionState{}, fmt.Errorf("%w: exact connection state for %s", domain.ErrNotFound, remotePipeID)
	}
	if err != nil {
		return ExactConnectionState{}, err
	}
	state.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return ExactConnectionState{}, err
	}
	// Expired material fails closed: return not-found so caller treats it as
	// needing re-authorization.
	if !state.ExpiresAt.IsZero() && !state.ExpiresAt.After(time.Now().UTC()) {
		return ExactConnectionState{}, fmt.Errorf("%w: exact connection lease expired for %s", domain.ErrNotFound, remotePipeID)
	}
	return state, nil
}

// PutExactConnectionState stores the exact connection state. It replaces any
// existing state for the same remotePipeID. An empty or zero expiry stores
// the state as-is (caller responsibility to validate).
func (s *Store) PutExactConnectionState(ctx context.Context, state ExactConnectionState) error {
	if state.RemotePipeID == "" || state.AccessLease == "" || state.Locator == "" {
		return errors.New("invalid exact connection state: missing required fields")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO exact_connections(remote_pipe_id, locator, access_lease, expires_at, local_pipe_ref) VALUES(?,?,?,?,?)
		 ON CONFLICT(remote_pipe_id) DO UPDATE SET locator=excluded.locator, access_lease=excluded.access_lease, expires_at=excluded.expires_at, local_pipe_ref=excluded.local_pipe_ref`,
		state.RemotePipeID, state.Locator, state.AccessLease, state.ExpiresAt.UTC().Format(time.RFC3339Nano), state.LocalPipeRef,
	)
	return err
}

// DeleteExactConnectionState removes the exact connection state for a remote
// PipeID. No error is returned if the state does not exist.
func (s *Store) DeleteExactConnectionState(ctx context.Context, remotePipeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM exact_connections WHERE remote_pipe_id=?`, remotePipeID)
	return err
}