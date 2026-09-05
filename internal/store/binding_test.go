// Store-backed durable exact binding, ephemeral lease cache, and accepted
// redemption tests (schema 12). These are the persistence-level contract
// checks for the durable remote-Pipe binding model.
package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

func testBinding(remotePipeID string) domain.RemotePipeBinding {
	now := time.Now().UTC()
	return domain.RemotePipeBinding{
		RemotePipeID:    remotePipeID,
		AuthorityPublic: "b_authority_public_key_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		OwnerBinding:    "b_owner_binding_payload",
		Locator:         "/dns4/relay.test/tcp/4001/p2p/peer",
		RecipientPublic: "b_recipient_public_key_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Source:          "invite",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
}

func TestStoreRemotePipeBindingSurvivesReopen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := testBinding("pip_binding_a")
	if err := state.PutRemotePipeBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	// Consumer restart: same TUBO_HOME, fresh Store instance.
	state2, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state2.Close()
	got, err := state2.GetRemotePipeBinding(ctx, "pip_binding_a")
	if err != nil {
		t.Fatalf("binding must reload after restart: %v", err)
	}
	if got.RemotePipeID != b.RemotePipeID || got.RecipientPublic != b.RecipientPublic || got.Locator != b.Locator || got.AuthorityPublic != b.AuthorityPublic {
		t.Fatalf("reloaded binding mismatch: %#v", got)
	}
}

func TestStoreRemotePipeBindingFailsClosedOnCorrupt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	b := testBinding("pip_corrupt")
	b.Locator = "" // corrupt: invalid on read
	if err := state.PutRemotePipeBinding(ctx, b); err == nil {
		t.Fatal("corrupt binding must be rejected on write")
	}
}

func TestStoreAccessLeaseCacheExpiryFailsClosedAndKeepsBinding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	b := testBinding("pip_cache_a")
	if err := state.PutRemotePipeBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	// Un-expired cache round-trips.
	future := time.Now().UTC().Add(time.Hour)
	if err := state.PutAccessLeaseCache(ctx, domain.AccessLeaseCache{RemotePipeID: "pip_cache_a", AccessLease: "bDNBvcjIwMTJ", ExpiresAt: future}); err != nil {
		t.Fatal(err)
	}
	if c, err := state.GetAccessLeaseCache(ctx, "pip_cache_a"); err != nil {
		t.Fatalf("cache read: %v", err)
	} else if c.AccessLease != "bDNBvcjIwMTJ" {
		t.Fatalf("cache mismatch")
	}
	// Expired cache fails closed (treated as absent) and is deleted.
	past := time.Now().UTC().Add(-time.Minute)
	if err := state.PutAccessLeaseCache(ctx, domain.AccessLeaseCache{RemotePipeID: "pip_cache_a", AccessLease: "bDNBvcjIwMTI", ExpiresAt: past}); err != nil {
		t.Fatal(err)
	}
	if _, err := state.GetAccessLeaseCache(ctx, "pip_cache_a"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired cache must return ErrNotFound, got %v", err)
	}
	// The durable binding is untouched by cache expiry.
	if _, err := state.GetRemotePipeBinding(ctx, "pip_cache_a"); err != nil {
		t.Fatalf("binding must survive cache expiry: %v", err)
	}
}

func TestStoreAcceptedRelationshipDeterministic(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	// Two different invites accepted by the SAME recipient for the SAME pipe:
	// the one-time redemption history keeps both rows (anti-replay), while the
	// authoritative current relationship is exactly one row that resolves to
	// the newest accepted invite deterministically.
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_first", "pip_r", "ses_1", "b_client_one", 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_second", "pip_r", "ses_2", "b_client_one", 0, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if n, err := state.RedemptionCount(ctx, "jti_first", "pip_r"); err != nil || n != 1 {
		t.Fatalf("redemption history rows: %d %v", n, err)
	}
	jti, epoch, ok, err := state.AcceptedRelationship(ctx, "pip_r", "b_client_one")
	if err != nil || !ok || jti != "jti_second" || epoch != 0 {
		t.Fatalf("authoritative relationship: %q ep=%d ok=%v %v", jti, epoch, ok, err)
	}
	// Different recipient for the same pipe: independent relationship.
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_other", "pip_r", "ses_3", "b_client_two", 0, now); err != nil {
		t.Fatal(err)
	}
	jti, _, ok, _ = state.AcceptedRelationship(ctx, "pip_r", "b_client_two")
	if !ok || jti != "jti_other" {
		t.Fatalf("independent recipient relationship: %q %v", jti, ok)
	}
	// Different pipe: no relationship.
	_, _, ok, _ = state.AcceptedRelationship(ctx, "pip_other", "b_client_one")
	if ok {
		t.Fatal("non-accepted pipe must not resolve")
	}
	// Invite pipe resolution for scope-less revocation.
	pipeID, ok, err := state.RedemptionPipeForInvite(ctx, "jti_first")
	if err != nil || !ok || pipeID != "pip_r" {
		t.Fatalf("redemption pipe lookup: %q %v %v", pipeID, ok, err)
	}
}

func TestStoreAcceptedRelationshipEpochAdvancesWithReaccept(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_epoch0", "pip_e", "ses_1", "b_client_one", 0, now); err != nil {
		t.Fatal(err)
	}
	// Post-revocation state: epoch advanced to 1; a fresh invite at epoch 1 is
	// accepted and REPLACES the authoritative relationship.
	if err := state.Revoke(ctx, "pip_e", "pipe-access", "pip_e", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_epoch1", "pip_e", "ses_2", "b_client_one", 1, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	jti, epoch, ok, err := state.AcceptedRelationship(ctx, "pip_e", "b_client_one")
	if err != nil || !ok || jti != "jti_epoch1" || epoch != 1 {
		t.Fatalf("relationship after re-accept: %q ep=%d ok=%v %v", jti, epoch, ok, err)
	}
	// Redemption history preserved (both one-time rows).
	if n, _ := state.RedemptionCount(ctx, "jti_epoch0", "pip_e"); n != 1 {
		t.Fatalf("old redemption history lost")
	}
}

func TestStoreRedeemInviteAndAcceptOneWinnerAndIdempotentRetry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	ok, err := state.RedeemInviteAndAccept(ctx, "jti_win", "pip_w", "ses_1", "b_client_one", 0, now)
	if err != nil || !ok {
		t.Fatalf("first redemption: %v %v", ok, err)
	}
	// Idempotent same-recipient retry: ok=true, relationship not regressed.
	ok, err = state.RedeemInviteAndAccept(ctx, "jti_win", "pip_w", "ses_2", "b_client_one", 0, now)
	if err != nil || !ok {
		t.Fatalf("idempotent retry: %v %v", ok, err)
	}
	jti, _, relOK, _ := state.AcceptedRelationship(ctx, "pip_w", "b_client_one")
	if !relOK || jti != "jti_win" {
		t.Fatalf("relationship after retry: %q %v", jti, relOK)
	}
	// A different recipient can never consume the same invite.
	_, err = state.RedeemInviteAndAccept(ctx, "jti_win", "pip_w", "ses_3", "b_client_two", 0, now)
	if !errors.Is(err, ErrInviteRedeemed) {
		t.Fatalf("different recipient must fail closed: %v", err)
	}
}

func TestStoreAcceptedRelationshipSurvivesReopen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_persist", "pip_p", "ses_1", "b_client_one", 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_ = state.Close()
	state2, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state2.Close()
	jti, _, ok, err := state2.AcceptedRelationship(ctx, "pip_p", "b_client_one")
	if err != nil || !ok || jti != "jti_persist" {
		t.Fatalf("relationship must survive restart: %q ok=%v %v", jti, ok, err)
	}
}

func TestOpenMigratesSchema11ExactConnectionsToBindingSplit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a schema-11 database that already contains the old
	// exact_connections row (remote pipe + locator + lease + local pipe ref).
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations(version,applied_at) VALUES(11,'old'); CREATE TABLE exact_connections(remote_pipe_id TEXT PRIMARY KEY, locator TEXT NOT NULL, access_lease TEXT NOT NULL, expires_at TEXT NOT NULL, local_pipe_ref TEXT NOT NULL DEFAULT ''); INSERT INTO exact_connections VALUES('pip_legacy','/ip4/1.1.1.1/tcp/1','bGV4lc2FpUsd','3026-01-01T00:00:00Z','pipe/legacy'); CREATE TABLE invite_redemptions(jti TEXT NOT NULL, pipe_id TEXT NOT NULL, redeemed_at TEXT NOT NULL, session_id TEXT NOT NULL, client_public TEXT NOT NULL, PRIMARY KEY(jti,pipe_id))`); err != nil {
		t.Fatal(err)
	}
	// SQLite refuses DROP TABLE when a view/table name is bound; no such case here.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := Open(ctx)
	if err != nil {
		t.Fatalf("migration from schema 11: %v", err)
	}
	defer state.Close()
	// Old exact_connections row is dropped (it cannot be reconstructed into a
	// verifiable durable binding) and the split tables exist.
	var count int
	if err := state.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='exact_connections'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("schema 12 must drop exact_connections")
	}
	for _, name := range []string{"remote_pipe_bindings", "exact_lease_cache"} {
		if err := state.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("schema 12 table %s missing: %v", name, err)
		}
	}
	// The accepted-relationship lookup index exists.
	if err := state.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_invite_redemptions_pipe_client'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("schema 12 redemption index missing: %v", err)
	}
	// New-style durable binding writes work post-migration.
	b := testBinding("pip_post")
	if err := state.PutRemotePipeBinding(ctx, b); err != nil {
		t.Fatalf("post-migration binding write: %v", err)
	}
	if _, err := state.GetRemotePipeBinding(ctx, "pip_post"); err != nil {
		t.Fatalf("post-migration binding read: %v", err)
	}
}
