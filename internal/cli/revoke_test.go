// CLI `revoke invite <jti>` family-resolution regression tests.
//
// Deterministic rule (store.ResolveRevokeInvite):
//   - JTI redeemed through the EXACT (Journey A) path (kind='exact')
//     -> subject_type=exact.invite, scope = resolved exact Pipe ID;
//   - otherwise (legacy scoped share.invite / unknown)
//     -> subject_type=share.invite, scope = current Scope.
//   - a persistence error during resolution fails closed and never falls back
//     to the scoped family.
package cli

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/store"
)

// TestRevokeExactInviteUsesExactInviteAndPipeScope drives the real CLI
// `revoke invite <jti>` for an accepted EXACT invite and proves the persisted
// revocation uses subject_type=exact.invite under the resolved exact Pipe ID
// (never the current Scope, even when one exists).
func TestRevokeExactInviteUsesExactInviteAndPipeScope(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	seed, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	adapter := store.NewExactAuthorityRevocationState(seed, "pip_exact_rev")
	if _, err := adapter.RedeemInviteAndAccept(ctx, "jti_exact_rev", "pip_exact_rev", "ses_rev", "b_client_rev", 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// A scope also exists; the exact branch must NOT use the current scope.
	if err := seed.PutScope(ctx, store.Scope{Name: "solar/default", AuthorityPublic: "b_auth_pub", AuthorityKeyFile: "/nope", NamespaceKeyFile: "/nope"}); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()

	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	if err := app.Run(ctx, []string{"revoke", "invite", "jti_exact_rev"}); err != nil {
		t.Fatalf("revoke exact invite: %v", err)
	}

	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	values, err := state.Revocations(ctx, "pip_exact_rev", []string{"jti_exact_rev"})
	if err != nil || len(values) != 1 {
		t.Fatal("exact revocation row must exist under the exact Pipe scope")
	}
	if values[0].SubjectType != "exact.invite" {
		t.Fatalf("exact revoke subject type: %q", values[0].SubjectType)
	}
	// The exact renewal/authorizer check reads exactly this row.
	revoked, err := state.IsRevoked(ctx, "pip_exact_rev", "exact.invite", "jti_exact_rev")
	if err != nil || !revoked {
		t.Fatalf("exact.invite revocation not visible to renewal: revoked=%v %v", revoked, err)
	}
	// No share.invite row may have been written for the JTI.
	scoped, err := state.Revocations(ctx, "solar/default", []string{"jti_exact_rev"})
	if err == nil && len(scoped) != 0 {
		t.Fatal("exact invite must not leak a scoped share.invite revocation")
	}
}

// TestRevokeScopedInviteUsesShareInviteAndCurrentScope drives the real CLI for
// a SCOPED (legacy share.invite) redemption and proves the persisted
// revocation uses subject_type=share.invite in the current Scope.
func TestRevokeScopedInviteUsesShareInviteAndCurrentScope(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	seed, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.PutScope(ctx, store.Scope{Name: "solar/default", AuthorityPublic: "b_auth_pub", AuthorityKeyFile: "/nope", NamespaceKeyFile: "/nope"}); err != nil {
		t.Fatal(err)
	}
	// CurrentScope resolves through config.yaml first; seed the same overlay
	// config a real `join overlay` would write.
	if err := seed.PutOverlay(ctx, "/ip4/127.0.0.1/tcp/1/p2p/peer", "/tmp/swarm.key", "private"); err != nil {
		t.Fatal(err)
	}
	// Scoped share-invite redemption: kind='scoped' (the authority path calls
	// store.RedeemInvite, never the exact accept).
	if _, err := seed.RedeemInvite(ctx, "jti_scoped_rev", "pip_scoped_rev", "ses_scoped", "b_client_scoped"); err != nil {
		t.Fatal(err)
	}
	_ = seed.Close()

	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	if err := app.Run(ctx, []string{"revoke", "invite", "jti_scoped_rev"}); err != nil {
		t.Fatalf("revoke scoped invite: %v", err)
	}

	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	values, err := state.Revocations(ctx, "solar/default", []string{"jti_scoped_rev"})
	if err != nil || len(values) != 1 {
		t.Fatal("scoped revocation row must exist under the current Scope")
	}
	if values[0].SubjectType != "share.invite" {
		t.Fatalf("scoped revoke subject type: %q", values[0].SubjectType)
	}
	// The scoped authority redemption path checks exactly this row.
	revoked, err := state.IsRevoked(ctx, "solar/default", "share.invite", "jti_scoped_rev")
	if err != nil || !revoked {
		t.Fatalf("share.invite revocation not visible to scoped authority: revoked=%v %v", revoked, err)
	}
}

// TestRevokeMixedInvitesResolveIndependently proves exact and scoped invites
// resolve deterministically and independently.
func TestRevokeMixedInvitesResolveIndependently(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_mix_exact", "pip_mix_exact", "ses_1", "b_client_one", 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RedeemInvite(ctx, "jti_mix_scoped", "pip_mix_scoped", "ses_2", "b_client_one"); err != nil {
		t.Fatal(err)
	}
	kind1, scope1, err := state.ResolveRevokeInvite(ctx, "jti_mix_exact")
	if err != nil || kind1 != "exact" || scope1 != "pip_mix_exact" {
		t.Fatalf("exact resolution: %q %q %v", kind1, scope1, err)
	}
	kind2, _, err := state.ResolveRevokeInvite(ctx, "jti_mix_scoped")
	if err != nil || kind2 != "scoped" {
		t.Fatalf("scoped resolution: %q %v", kind2, err)
	}
}

// TestRevokeExactResolutionPersistenceErrorFailsClosed drives the actual CLI
// handler with a poisoned evidence table and proves the command fails closed
// with no scoped fallback and no revocation write.
func TestRevokeExactResolutionPersistenceErrorFailsClosed(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := state.RedeemInviteAndAccept(ctx, "jti_poison", "pip_poison", "ses_1", "b_client_one", 0, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Poison the exact-evidence table from a second sqlite connection over the
	// SAME state file: the store's next resolution query fails with "no such
	// table". SQLite DDL is visible to all connections on the file.
	home := os.Getenv("TUBO_HOME")
	poison, err := sql.Open("sqlite", "file:"+filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := poison.Exec(`DROP TABLE invite_redemptions`); err != nil {
		t.Fatal(err)
	}
	_ = poison.Close()
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	f, fErr := parseCommandForTest(t, []string{"revoke", "invite", "jti_poison"})
	if fErr != nil {
		t.Fatal(fErr)
	}
	revokeErr := app.revoke(ctx, state, f)
	if revokeErr == nil {
		t.Fatal("revoke must fail closed when exact resolution errors")
	}
	if !strings.Contains(revokeErr.Error(), "persistence_failed") {
		t.Fatalf("revoke error must be persistence_failed, got %v", revokeErr)
	}
	// No revocation row may exist anywhere for this JTI (no scoped fallback).
	values, err := state.Revocations(ctx, "pip_poison", []string{"jti_poison"})
	if err == nil && len(values) != 0 {
		t.Fatalf("revocation written despite lookup failure: %+v", values)
	}
}
