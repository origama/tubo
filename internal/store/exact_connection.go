// Package store owns the durable consumer-side accepted remote-Pipe binding
// and the ephemeral AccessLease cache for the exact (Journey A) path.
//
// Durability split (schema 12):
//   - remote_pipe_bindings: the durable accepted relationship — remote PipeID,
//     owner-bound Exact Authority identity, verified owner binding, verified
//     renewal/redemption locator, accepted recipient reference, source. It has
//     NO local ingress/listen dependency and never holds the AccessLease.
//   - exact_lease_cache: current short-lived AccessLease cache. Expired cached
//     material fails closed (treated as absent) and simply causes reacquisition
//     through the durable binding; the cache is never the source of truth.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/origama/tubo/internal/domain"
)

// GetRemotePipeBinding retrieves the durable accepted binding for a remote
// PipeID. Returns domain.ErrNotFound when no binding exists. Corrupt state
// fails closed with an error rather than returning a usable binding.
func (s *Store) GetRemotePipeBinding(ctx context.Context, remotePipeID string) (domain.RemotePipeBinding, error) {
	var b domain.RemotePipeBinding
	var created, updated string
	err := s.db.QueryRowContext(ctx,
		`SELECT remote_pipe_id, authority_public, owner_binding, locator, recipient_public, source, created_at, updated_at FROM remote_pipe_bindings WHERE remote_pipe_id=?`,
		remotePipeID,
	).Scan(&b.RemotePipeID, &b.AuthorityPublic, &b.OwnerBinding, &b.Locator, &b.RecipientPublic, &b.Source, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.RemotePipeBinding{}, fmt.Errorf("%w: remote pipe binding for %s", domain.ErrNotFound, remotePipeID)
	}
	if err != nil {
		return domain.RemotePipeBinding{}, err
	}
	b.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return domain.RemotePipeBinding{}, err
	}
	b.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	if err != nil {
		return domain.RemotePipeBinding{}, err
	}
	if err := b.Validate(); err != nil {
		return domain.RemotePipeBinding{}, fmt.Errorf("corrupt remote pipe binding for %s: %w", remotePipeID, err)
	}
	return b, nil
}

// PutRemotePipeBinding stores or replaces the durable binding for a remote
// PipeID. An invalid binding is rejected before any write.
func (s *Store) PutRemotePipeBinding(ctx context.Context, b domain.RemotePipeBinding) error {
	if err := b.Validate(); err != nil {
		return err
	}
	now := time.Now().UTC()
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	b.UpdatedAt = now
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO remote_pipe_bindings(remote_pipe_id, authority_public, owner_binding, locator, recipient_public, source, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?)
		 ON CONFLICT(remote_pipe_id) DO UPDATE SET authority_public=excluded.authority_public, owner_binding=excluded.owner_binding, locator=excluded.locator, recipient_public=excluded.recipient_public, source=excluded.source, created_at=excluded.created_at, updated_at=excluded.updated_at`,
		b.RemotePipeID, b.AuthorityPublic, b.OwnerBinding, b.Locator, b.RecipientPublic, b.Source, b.CreatedAt.UTC().Format(time.RFC3339Nano), b.UpdatedAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// DeleteRemotePipeBinding removes the durable binding for a remote PipeID. No
// error is returned when the binding does not exist.
func (s *Store) DeleteRemotePipeBinding(ctx context.Context, remotePipeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM remote_pipe_bindings WHERE remote_pipe_id=?`, remotePipeID)
	return err
}

// GetAccessLeaseCache retrieves the cached AccessLease for a remote PipeID.
// Expired cached material fails closed: it is deleted and reported as
// ErrNotFound so the caller reacquires through the durable binding. Corrupt
// cached material also fails closed.
func (s *Store) GetAccessLeaseCache(ctx context.Context, remotePipeID string) (domain.AccessLeaseCache, error) {
	var c domain.AccessLeaseCache
	var expires string
	err := s.db.QueryRowContext(ctx,
		`SELECT remote_pipe_id, access_lease, expires_at FROM exact_lease_cache WHERE remote_pipe_id=?`,
		remotePipeID,
	).Scan(&c.RemotePipeID, &c.AccessLease, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.AccessLeaseCache{}, fmt.Errorf("%w: access lease cache for %s", domain.ErrNotFound, remotePipeID)
	}
	if err != nil {
		return domain.AccessLeaseCache{}, err
	}
	c.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	if err != nil {
		return domain.AccessLeaseCache{}, err
	}
	if c.ExpiresAt.IsZero() || !c.ExpiresAt.After(time.Now().UTC()) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM exact_lease_cache WHERE remote_pipe_id=?`, remotePipeID)
		return domain.AccessLeaseCache{}, fmt.Errorf("%w: access lease cache expired for %s", domain.ErrNotFound, remotePipeID)
	}
	if c.RemotePipeID == "" || c.AccessLease == "" {
		return domain.AccessLeaseCache{}, errors.New("corrupt access lease cache")
	}
	return c, nil
}

// PutAccessLeaseCache stores or replaces the ephemeral lease cache. An empty
// or zero expiry is rejected: the cache is meaningless without a fence.
func (s *Store) PutAccessLeaseCache(ctx context.Context, c domain.AccessLeaseCache) error {
	if c.RemotePipeID == "" || c.AccessLease == "" || c.ExpiresAt.IsZero() {
		return errors.New("invalid access lease cache: missing required fields")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO exact_lease_cache(remote_pipe_id, access_lease, expires_at) VALUES(?,?,?)
		 ON CONFLICT(remote_pipe_id) DO UPDATE SET access_lease=excluded.access_lease, expires_at=excluded.expires_at`,
		c.RemotePipeID, c.AccessLease, c.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
	return err
}

// DeleteAccessLeaseCache removes the ephemeral lease cache entry for a remote
// PipeID. Removing the cache never removes the durable binding.
func (s *Store) DeleteAccessLeaseCache(ctx context.Context, remotePipeID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM exact_lease_cache WHERE remote_pipe_id=?`, remotePipeID)
	return err
}
