package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

func TestServiceCreateReuseConflictAndPersistence(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ref, _ := domain.ParseRef("service/echo")
	spec := domain.ServiceSpec{Ref: ref, Kind: domain.ServiceHTTP, Target: "http://127.0.0.1:8080"}
	created, err := state.PutService(ctx, spec)
	if err != nil || !created {
		t.Fatalf("create: %v %v", created, err)
	}
	created, err = state.PutService(ctx, spec)
	if err != nil || created {
		t.Fatalf("reuse: %v %v", created, err)
	}
	conflict := spec
	conflict.Target = "http://127.0.0.1:8081"
	if _, err := state.PutService(ctx, conflict); err == nil {
		t.Fatal("conflicting service accepted")
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	loaded, err := state.Service(ctx, ref)
	if err != nil || loaded.Target != spec.Target {
		t.Fatalf("persisted value: %#v %v", loaded, err)
	}
}

func TestOverlayUsesAtomicStaticConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	key := filepath.Join(home, "secrets", "swarm.key")
	if err := state.PutOverlay(ctx, "/ip4/127.0.0.1/tcp/4001/p2p/peer", key, "private"); err != nil {
		t.Fatal(err)
	}
	relay, loadedKey, reachability, err := state.Overlay(ctx)
	if err != nil || relay == "" || loadedKey != key || reachability != "private" {
		t.Fatalf("overlay: %q %q %q %v", relay, loadedKey, reachability, err)
	}
	data, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "swarm_key_file:") {
		t.Fatalf("unexpected config: %s", data)
	}
	for _, name := range []string{"config.yaml", "state.db"} {
		info, err := os.Stat(filepath.Join(home, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode is %o", name, info.Mode().Perm())
		}
	}
}

func TestOpenMigratesSchema5FreshnessToAuthorityProvenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations(version,applied_at) VALUES(5,'old'); CREATE TABLE runs(primary_ref TEXT PRIMARY KEY,kind TEXT NOT NULL,pid INTEGER NOT NULL,process_identity TEXT NOT NULL,source TEXT NOT NULL,status TEXT NOT NULL,peer_id TEXT NOT NULL,address TEXT NOT NULL,started_at TEXT NOT NULL,updated_at TEXT NOT NULL); INSERT INTO runs VALUES('pipe/demo','pipe',0,'','foreground','stopped','','','old','old'); CREATE TABLE scope_freshness(scope TEXT PRIMARY KEY,last_attempt TEXT NOT NULL DEFAULT '',last_success TEXT NOT NULL DEFAULT '',source TEXT NOT NULL DEFAULT '',complete INTEGER NOT NULL DEFAULT 0,error_class TEXT NOT NULL DEFAULT '',next_retry TEXT NOT NULL DEFAULT '',snapshot_generation INTEGER NOT NULL DEFAULT 0,snapshot_high_water INTEGER NOT NULL DEFAULT 0); INSERT INTO scope_freshness VALUES('solar/default','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','pinned_authority',1,'','',9,99); CREATE TABLE records(scope TEXT NOT NULL,pipe_id TEXT NOT NULL,name TEXT NOT NULL,payload BLOB NOT NULL,expires_at TEXT NOT NULL,sequence INTEGER NOT NULL,discovered INTEGER NOT NULL,PRIMARY KEY(scope,pipe_id)); INSERT INTO records VALUES('solar/default','pip_cached','cached',x'31','2999-01-01T00:00:00Z',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	run, err := state.Run(context.Background(), "pipe/demo")
	if err != nil || run.Source != "foreground" {
		t.Fatalf("migrated run: %#v %v", run, err)
	}
	freshness, err := state.ScopeFreshness(context.Background(), "solar/default")
	if err != nil || freshness.Complete || freshness.SnapshotGeneration != 0 || freshness.SnapshotHighWater != 0 || freshness.SnapshotDigest != "" {
		t.Fatalf("migrated freshness: %#v %v", freshness, err)
	}
	if _, err := state.RecordInScope(context.Background(), "solar/default", "pip_cached"); err != nil {
		t.Fatalf("migration erased cached record: %v", err)
	}
}

func TestOpenRejectsNewerSchema(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = state.Close()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(fmt.Sprintf(`INSERT INTO schema_migrations(version,applied_at) VALUES(%d,'future')`, SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(ctx); err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRecordSequenceRejectsReplayAndConflictingEnvelope(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	record := Record{Scope: "solar/default", PipeID: "pip_exact", Name: "echo", Payload: []byte(`{"sequence":2}`), ExpiresAt: time.Now().Add(time.Hour), Sequence: 2}
	if err := state.PutRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	older := record
	older.Sequence = 1
	older.Payload = []byte(`{"sequence":1}`)
	if err := state.PutRecord(ctx, older); err == nil {
		t.Fatal("older record replay accepted")
	}
	conflict := record
	conflict.Payload = []byte(`{"sequence":2,"tampered":true}`)
	if err := state.PutRecord(ctx, conflict); err == nil {
		t.Fatal("same-sequence conflict accepted")
	}
	if err := state.PutRecord(ctx, record); err != nil {
		t.Fatalf("identical cached record rejected: %v", err)
	}
}

func authoritativeSnapshot(generation, highWater uint64, records []Record) AuthoritativeSnapshot {
	return AuthoritativeSnapshot{Source: SnapshotSourcePinnedAuthority, Complete: true, Generation: generation, HighWater: highWater, Digest: fmt.Sprintf("%064x", generation*1000000+highWater), Records: records}
}

func TestAuthoritativeSnapshotReconciliationAndFreshness(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	first := Record{Scope: "solar/default", PipeID: "pip_first", Name: "first", Payload: []byte(`{"sequence":2}`), ExpiresAt: now.Add(time.Hour), Sequence: 2, Discovered: true}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(1, 2, []Record{first})); err != nil {
		t.Fatal(err)
	}
	freshness, err := state.ScopeFreshness(ctx, first.Scope)
	if err != nil || !freshness.Complete || freshness.Source != SnapshotSourcePinnedAuthority || freshness.LastAttempt == nil || freshness.LastSuccess == nil || freshness.SnapshotGeneration != 1 || freshness.SnapshotHighWater != 2 {
		t.Fatalf("freshness: %#v %v", freshness, err)
	}
	partial := AuthoritativeSnapshot{Source: SnapshotSourcePinnedAuthority, Complete: false, Generation: 2, HighWater: 2, Digest: fmt.Sprintf("%064x", 2)}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, partial); err == nil {
		t.Fatal("partial snapshot accepted")
	}
	if _, err := state.RecordInScope(ctx, first.Scope, first.PipeID); err != nil {
		t.Fatalf("partial snapshot changed cache: %v", err)
	}
	mixed := first
	mixed.Scope = "other/default"
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(2, 2, []Record{mixed})); err == nil {
		t.Fatal("mixed scope snapshot accepted")
	}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, AuthoritativeSnapshot{Source: "peer_cache", Complete: true, Generation: 2, HighWater: 2, Digest: fmt.Sprintf("%064x", 2)}); err == nil {
		t.Fatal("non-authoritative response deleted cache")
	}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(2, 2, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordInScope(ctx, first.Scope, first.PipeID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("complete authoritative removal: %v", err)
	}
}

func TestSnapshotFailurePersistsFreshnessWithoutChangingRecords(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	record := Record{Scope: "solar/default", PipeID: "pip_first", Name: "first", Payload: []byte(`1`), ExpiresAt: time.Now().Add(time.Hour), Sequence: 1}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, record.Scope, authoritativeSnapshot(1, 1, []Record{record})); err != nil {
		t.Fatal(err)
	}
	next := time.Now().Add(time.Minute).UTC()
	if err := state.RecordSnapshotFailure(ctx, record.Scope, SnapshotSourcePinnedAuthority, "authority_unreachable", &next); err != nil {
		t.Fatal(err)
	}
	freshness, err := state.ScopeFreshness(ctx, record.Scope)
	if err != nil || freshness.Complete || freshness.ErrorClass != "authority_unreachable" || freshness.NextRetry == nil || !freshness.NextRetry.Equal(next) {
		t.Fatalf("failure freshness: %#v %v", freshness, err)
	}
	if _, err := state.RecordInScope(ctx, record.Scope, record.PipeID); err != nil {
		t.Fatalf("failure removed valid record: %v", err)
	}
}

func TestSnapshotRejectsRollbackAndPreservesNewerConcurrentRecord(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	first := Record{Scope: "solar/default", PipeID: "pip_first", Name: "first", Payload: []byte(`1`), ExpiresAt: now.Add(time.Hour), Sequence: 1}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(2, 2, []Record{first})); err != nil {
		t.Fatal(err)
	}
	newer := Record{Scope: first.Scope, PipeID: "pip_newer", Name: "newer", Payload: []byte(`3`), ExpiresAt: now.Add(time.Hour), Sequence: 3}
	if err := state.PutRecord(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(1, 1, []Record{first})); err == nil {
		t.Fatal("rollback snapshot generation accepted")
	}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(3, 3, []Record{first, newer})); err != nil {
		t.Fatal(err)
	}
	if _, err := state.RecordInScope(ctx, first.Scope, newer.PipeID); err != nil {
		t.Fatalf("newer record lost during stale snapshot: %v", err)
	}
}

func TestConcurrentSnapshotReconciliationsPreserveRecordAndFreshness(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	record := Record{Scope: "solar/default", PipeID: "pip_shared", Name: "shared", Payload: []byte(`1`), ExpiresAt: time.Now().Add(time.Hour), Sequence: 1}
	var wg sync.WaitGroup
	errors := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errors <- state.ReconcileAuthoritativeSnapshot(ctx, record.Scope, authoritativeSnapshot(1, 1, []Record{record}))
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := state.RecordInScope(ctx, record.Scope, record.PipeID); err != nil {
		t.Fatalf("concurrent reconcile lost record: %v", err)
	}
	freshness, err := state.ScopeFreshness(ctx, record.Scope)
	if err != nil || freshness.SnapshotGeneration != 1 || freshness.LastSuccess == nil || !freshness.Complete {
		t.Fatalf("concurrent freshness: %#v %v", freshness, err)
	}
}

func TestConcurrentNewerGenerationReconciliationWins(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	first := Record{Scope: "solar/default", PipeID: "pip_shared", Name: "shared", Payload: []byte(`one`), ExpiresAt: now.Add(time.Hour), Sequence: 1, Discovered: true}
	newer := first
	newer.Payload, newer.Sequence = []byte(`two`), 2
	errs := make(chan error, 2)
	go func() {
		errs <- state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(1, 1, []Record{first}))
	}()
	go func() {
		errs <- state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(2, 2, []Record{newer}))
	}()
	firstErr, secondErr := <-errs, <-errs
	if firstErr != nil && !strings.Contains(firstErr.Error(), "rollback authoritative snapshot generation") {
		t.Fatalf("unexpected older generation result: %v", firstErr)
	}
	if secondErr != nil && !strings.Contains(secondErr.Error(), "rollback authoritative snapshot generation") {
		t.Fatalf("unexpected newer generation result: %v", secondErr)
	}
	stored, err := state.RecordInScope(ctx, first.Scope, first.PipeID)
	if err != nil || string(stored.Payload) != string(newer.Payload) {
		t.Fatalf("newer generation did not win: %#v %v", stored, err)
	}
	freshness, err := state.ScopeFreshness(ctx, first.Scope)
	if err != nil || freshness.SnapshotGeneration != 2 {
		t.Fatalf("newer freshness not retained: %#v %v", freshness, err)
	}
}

func TestLocalAuthorityPublishFetchRepublishGenerationPersistsAcrossRestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := state.EnsureAuthoritySnapshot(ctx, "solar/default"); err != nil {
		t.Fatal(err)
	}
	record := Record{Scope: "solar/default", PipeID: "pip_authority", Name: "authority", Payload: []byte(`1`), ExpiresAt: time.Now().Add(time.Hour), Sequence: 1}
	if err := state.PutAuthoritativeRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	_, generation, highWater, complete, err := state.AuthoritySnapshot(ctx, record.Scope, MaxSnapshotRecords)
	if err != nil || !complete || generation != 2 || highWater != 2 {
		t.Fatalf("authority snapshot after publish: generation=%d high_water=%d complete=%t err=%v", generation, highWater, complete, err)
	}
	updated := record
	updated.Sequence, updated.Payload = 2, []byte(`runtime-updated`)
	if err := state.PutAuthoritativeRecord(ctx, updated); err != nil {
		t.Fatal(err)
	}
	records, generation, highWater, complete, err := state.AuthoritySnapshot(ctx, record.Scope, MaxSnapshotRecords)
	if err != nil || !complete || generation != 3 || highWater != 3 || len(records) != 1 || string(records[0].Payload) != string(updated.Payload) {
		t.Fatalf("authority snapshot after republish: records=%#v generation=%d high_water=%d complete=%t err=%v", records, generation, highWater, complete, err)
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state, err = Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	_, generation, highWater, complete, err = state.AuthoritySnapshot(ctx, record.Scope, MaxSnapshotRecords)
	if err != nil || !complete || generation != 3 || highWater != 3 {
		t.Fatalf("authority snapshot after restart: generation=%d high_water=%d complete=%t err=%v", generation, highWater, complete, err)
	}
}

func TestSnapshotWriteFailureRollsBackRecordsAndFreshness(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	first := Record{Scope: "solar/default", PipeID: "pip_first", Name: "first", Payload: []byte(`1`), ExpiresAt: now.Add(time.Hour), Sequence: 1}
	second := Record{Scope: first.Scope, PipeID: "pip_second", Name: "second", Payload: []byte(`1`), ExpiresAt: now.Add(time.Hour), Sequence: 1}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(1, 1, []Record{first, second})); err != nil {
		t.Fatal(err)
	}
	updated := first
	updated.Sequence, updated.Payload = 2, []byte(`2`)
	conflict := second
	conflict.Payload = []byte(`conflict`)
	if err := state.ReconcileAuthoritativeSnapshot(ctx, first.Scope, authoritativeSnapshot(2, 2, []Record{updated, conflict})); err == nil {
		t.Fatal("conflicting write accepted")
	}
	after, err := state.RecordInScope(ctx, first.Scope, first.PipeID)
	if err != nil || string(after.Payload) != string(first.Payload) {
		t.Fatalf("failed snapshot partially committed record: %#v %v", after, err)
	}
	freshness, err := state.ScopeFreshness(ctx, first.Scope)
	if err != nil || freshness.SnapshotGeneration != 1 {
		t.Fatalf("failed snapshot partially committed freshness: %#v %v", freshness, err)
	}
}

func TestAuthorityExpiryAdvancesSnapshotBeforeConsumerDeletion(t *testing.T) {
	authorityHome, consumerHome := t.TempDir(), t.TempDir()
	ctx := context.Background()
	t.Setenv("TUBO_HOME", authorityHome)
	authorityState, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scope := "solar/default"
	if err := authorityState.EnsureAuthoritySnapshot(ctx, scope); err != nil {
		t.Fatal(err)
	}
	record := Record{Scope: scope, PipeID: "pip_expiring", Name: "expiring", Payload: []byte(`1`), ExpiresAt: time.Now().Add(5 * time.Second), Sequence: 1}
	if err := authorityState.PutAuthoritativeRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	records, generation, highWater, complete, err := authorityState.AuthoritySnapshot(ctx, scope, MaxSnapshotRecords)
	if err != nil || !complete || generation != 2 || highWater != 2 {
		t.Fatalf("initial authority snapshot: records=%#v generation=%d high_water=%d complete=%t err=%v", records, generation, highWater, complete, err)
	}
	if err := authorityState.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUBO_HOME", consumerHome)
	consumerState, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumerState.ReconcileAuthoritativeSnapshot(ctx, scope, authoritativeSnapshot(generation, highWater, records)); err != nil {
		t.Fatal(err)
	}
	if err := consumerState.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUBO_HOME", authorityHome)
	authorityState, err = Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := authorityState.db.ExecContext(ctx, `UPDATE records SET expires_at=? WHERE scope=? AND pipe_id=?`, time.Now().UTC().Add(-time.Millisecond).Format(time.RFC3339Nano), scope, record.PipeID)
	if err != nil {
		t.Fatal(err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		t.Fatalf("expire authority record: changed=%d err=%v", changed, err)
	}
	if pruned, err := authorityState.PruneExpiredRecords(ctx, time.Now()); err != nil || pruned != 0 {
		t.Fatalf("generic cache prune touched authority record: pruned=%d err=%v", pruned, err)
	}
	records, generation, highWater, complete, err = authorityState.AuthoritySnapshot(ctx, scope, MaxSnapshotRecords)
	if err != nil || !complete || len(records) != 0 || generation != 3 || highWater != 3 {
		t.Fatalf("expired authority snapshot: records=%#v generation=%d high_water=%d complete=%t err=%v", records, generation, highWater, complete, err)
	}
	if err := authorityState.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TUBO_HOME", consumerHome)
	consumerState, err = Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer consumerState.Close()
	if err := consumerState.ReconcileAuthoritativeSnapshot(ctx, scope, authoritativeSnapshot(generation, highWater, records)); err != nil {
		t.Fatal(err)
	}
	if _, err := consumerState.RecordInScope(ctx, scope, record.PipeID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("refreshed expiry snapshot retained record: %v", err)
	}
}

func TestReconciliationDeletesOnlyDiscoveredCacheRows(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	local := Record{Scope: "solar/default", PipeID: "pip_local", Name: "local", Payload: []byte(`local`), ExpiresAt: now.Add(time.Hour), Sequence: 1}
	cached := Record{Scope: local.Scope, PipeID: "pip_cached", Name: "cached", Payload: []byte(`cache`), ExpiresAt: now.Add(time.Hour), Sequence: 1, Discovered: true}
	remoteVersionOfLocal := local
	remoteVersionOfLocal.Payload, remoteVersionOfLocal.Sequence, remoteVersionOfLocal.Discovered = []byte(`remote`), 2, true
	if err := state.PutRecord(ctx, local); err != nil {
		t.Fatal(err)
	}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, local.Scope, authoritativeSnapshot(1, 1, []Record{cached, remoteVersionOfLocal})); err != nil {
		t.Fatal(err)
	}
	if err := state.ReconcileAuthoritativeSnapshot(ctx, local.Scope, authoritativeSnapshot(2, 2, nil)); err != nil {
		t.Fatal(err)
	}
	storedLocal, err := state.RecordInScope(ctx, local.Scope, local.PipeID)
	if err != nil || string(storedLocal.Payload) != string(local.Payload) || storedLocal.Discovered {
		t.Fatalf("reconciliation replaced local row: %#v %v", storedLocal, err)
	}
	if _, err := state.RecordInScope(ctx, cached.Scope, cached.PipeID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reconciliation retained absent discovered cache row: %v", err)
	}
}

func TestExpiredRecordsArePruned(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	record := Record{Scope: "solar/default", PipeID: "pip_expired", Name: "expired", Payload: []byte(`1`), ExpiresAt: time.Now().Add(50 * time.Millisecond), Sequence: 1, Discovered: true}
	if err := state.PutRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	time.Sleep(75 * time.Millisecond)
	if records, err := state.Records(ctx, record.Scope); err != nil || len(records) != 0 {
		t.Fatalf("expired records: %#v %v", records, err)
	}
	if _, err := state.RecordInScope(ctx, record.Scope, record.PipeID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired record resolved: %v", err)
	}
}

func TestRunReservationAndProcessIdentity(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.ReserveRun(ctx, "pipe/demo", "pipe"); err != nil {
		t.Fatal(err)
	}
	if err := state.ReserveRun(ctx, "pipe/demo", "pipe"); err == nil {
		t.Fatal("duplicate run reservation accepted")
	}
	if err := state.PutRun(ctx, Run{PrimaryRef: "pipe/demo", Kind: "pipe", PID: os.Getpid(), Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	run, err := state.Run(ctx, "pipe/demo")
	if err != nil || run.ProcessIdentity == "" || run.Source != "foreground" {
		t.Fatalf("run identity: %#v %v", run, err)
	}
	current, err := ProcessIdentity(os.Getpid())
	if err != nil || current != run.ProcessIdentity {
		t.Fatalf("identity mismatch: %q %q %v", current, run.ProcessIdentity, err)
	}
}

func TestSupervisorRegistryIsIndependentOfRuntimeRows(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	value := Supervisor{PrimaryRef: "authority/solar", Manager: "launchd", Label: "io.github.origama.tubo.authority.solar", UnitPath: "/tmp/tubo.plist"}
	if err := state.PutSupervisor(ctx, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.Supervisor(ctx, value.PrimaryRef)
	if err != nil || loaded.Label != value.Label || loaded.InstalledAt.IsZero() || loaded.UpdatedAt.IsZero() {
		t.Fatalf("supervisor: %#v %v", loaded, err)
	}
	if err := state.DeleteSupervisor(ctx, value.PrimaryRef); err != nil {
		t.Fatal(err)
	}
	if _, err := state.Supervisor(ctx, value.PrimaryRef); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("removed supervisor: %v", err)
	}
}

func TestConcurrentDisjointWritesPreserved(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	var wg sync.WaitGroup
	errors := make(chan error, 16)
	for i := 0; i < 16; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			ref, _ := domain.NewRef(domain.KindService, fmt.Sprintf("service-%d", i))
			_, err := state.PutService(ctx, domain.ServiceSpec{Ref: ref, Kind: domain.ServiceHTTP, Target: fmt.Sprintf("http://127.0.0.1:%d", 9000+i)})
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 16; i++ {
		ref, _ := domain.NewRef(domain.KindService, fmt.Sprintf("service-%d", i))
		if _, err := state.Service(ctx, ref); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRuntimeTelemetryAndRunCascade(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	run := Run{PrimaryRef: "relay/default", Kind: "relay", PID: os.Getpid(), Status: "ready"}
	if err := state.PutRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	stats := RuntimeStats{RunRef: run.PrimaryRef, BytesIn: 123, BytesOut: 456, RateIn: 12.5, RateOut: 45.5, ConnectedPeers: 1, OpenConnections: 2}
	peers := []RuntimePeer{{RunRef: run.PrimaryRef, PeerID: "12D3KooWPeer", Addresses: []string{"/ip4/127.0.0.1/tcp/4001"}}}
	if err := state.PutRuntimeTelemetry(ctx, stats, peers); err != nil {
		t.Fatal(err)
	}
	loaded, err := state.RuntimeStats(ctx, run.PrimaryRef)
	if err != nil || loaded.BytesIn != 123 || loaded.ConnectedPeers != 1 {
		t.Fatalf("stats: %#v %v", loaded, err)
	}
	observed, err := state.RuntimePeers(ctx)
	if err != nil || len(observed) != 1 || observed[0].PeerID != "12D3KooWPeer" {
		t.Fatalf("peers: %#v %v", observed, err)
	}
	if err := state.DeleteRun(ctx, run.PrimaryRef); err != nil {
		t.Fatal(err)
	}
	observed, err = state.RuntimePeers(ctx)
	if err != nil || len(observed) != 0 {
		t.Fatalf("cascade peers: %#v %v", observed, err)
	}
	if _, err := state.RuntimeStats(ctx, run.PrimaryRef); err == nil {
		t.Fatal("runtime stats survived run deletion")
	}
}

func TestCurrentScopePeerAliasAndTransactionalRemoval(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	key := filepath.Join(home, "swarm.key")
	if err := os.WriteFile(key, []byte("test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := state.PutOverlay(ctx, "/ip4/127.0.0.1/tcp/4001/p2p/peer", key, "private"); err != nil {
		t.Fatal(err)
	}
	if err := state.PutScope(ctx, Scope{Name: "solar/default", AuthorityPublic: "public", AuthorityKeyFile: "authority", NamespaceKeyFile: "namespace"}); err != nil {
		t.Fatal(err)
	}
	if err := state.UseScope(ctx, "solar/default"); err != nil {
		t.Fatal(err)
	}
	if current, err := state.CurrentScope(ctx); err != nil || current != "solar/default" {
		t.Fatalf("current scope: %q %v", current, err)
	}
	if _, err := state.ValidateConfig(); err != nil {
		t.Fatal(err)
	}
	alias := PeerAlias{PeerID: "12D3KooWTest", Name: "relay", Note: "operator only"}
	if err := state.PutPeerAlias(ctx, alias); err != nil {
		t.Fatal(err)
	}
	if values, err := state.PeerAliases(ctx); err != nil || len(values) != 1 || values[0].Name != "relay" {
		t.Fatalf("aliases: %#v %v", values, err)
	}
	serviceRef, _ := domain.ParseRef("service/echo")
	if _, err := state.PutService(ctx, domain.ServiceSpec{Ref: serviceRef, Kind: domain.ServiceHTTP, Target: "http://127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	pipeRef, _ := domain.ParseRef("pipe/echo")
	if _, err := state.PutPipe(ctx, domain.PipeSpec{Ref: pipeRef, PipeID: "pip_test", Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"}, Target: serviceRef, Visibility: domain.VisibilityPrivate, Access: domain.AccessMembers}, filepath.Join(home, "owner.key"), ""); err != nil {
		t.Fatal(err)
	}
	if err := state.DeleteService(ctx, serviceRef.String()); err == nil || !strings.Contains(err.Error(), "targeted") {
		t.Fatalf("dependent service removal: %v", err)
	}
	if _, err := state.DeletePipe(ctx, pipeRef.String()); err != nil {
		t.Fatal(err)
	}
	if err := state.DeleteService(ctx, serviceRef.String()); err != nil {
		t.Fatal(err)
	}
}

func TestScopeSyncLeaseFencingExpiryAndPrivacy(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	first, acquired, err := state.AcquireScopeSyncLease(ctx, "solar/default", "pipe/first", "process-first", now, time.Second)
	if err != nil || !acquired || first.Token == "" {
		t.Fatalf("first acquire: %#v %t %v", first, acquired, err)
	}
	if _, acquired, err := state.AcquireScopeSyncLease(ctx, "solar/default", "pipe/second", "process-second", now.Add(500*time.Millisecond), time.Second); err != nil || acquired {
		t.Fatalf("live lease takeover: acquired=%t err=%v", acquired, err)
	}
	second, acquired, err := state.AcquireScopeSyncLease(ctx, "solar/default", "pipe/second", "process-second", now.Add(time.Second), time.Second)
	if err != nil || !acquired || second.Token == first.Token {
		t.Fatalf("expired takeover: %#v %t %v", second, acquired, err)
	}
	if ok, err := state.RenewScopeSyncLease(ctx, first, now.Add(time.Second), time.Second); err != nil || ok {
		t.Fatalf("stale renew: ok=%t err=%v", ok, err)
	}
	if ok, err := state.ReleaseScopeSyncLease(ctx, first); err != nil || ok {
		t.Fatalf("stale release: ok=%t err=%v", ok, err)
	}
	next := now.Add(time.Hour)
	if err := state.RecordSnapshotFailureWithLease(ctx, first, SnapshotSourcePinnedAuthority, "authority_unreachable", &next); !errors.Is(err, ErrScopeSyncLeaseLost) {
		t.Fatalf("stale failure write: %v", err)
	}
	record := Record{Scope: "solar/default", PipeID: "pip_first", Name: "first", Payload: []byte(`1`), ExpiresAt: now.Add(time.Hour), Sequence: 1, Discovered: true}
	if err := state.ReconcileAuthoritativeSnapshotWithLease(ctx, first, authoritativeSnapshot(1, 1, []Record{record})); !errors.Is(err, ErrScopeSyncLeaseLost) {
		t.Fatalf("stale reconcile: %v", err)
	}
	if err := state.ReconcileAuthoritativeSnapshotWithLease(ctx, second, authoritativeSnapshot(1, 1, []Record{record})); err != nil {
		t.Fatalf("successor reconcile: %v", err)
	}
	encoded, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), second.Token) || strings.Contains(string(encoded), second.ProcessIdentity) {
		t.Fatalf("lease JSON exposed secret identity: %s", encoded)
	}
	status, err := state.ScopeSyncLease(ctx, "solar/default")
	if err != nil || status.HolderRef != "pipe/second" || !status.Active {
		t.Fatalf("lease status: %#v %v", status, err)
	}
}

func TestScopeSyncLeaseConcurrentElection(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	now := time.Now().UTC()
	var winners int32
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, acquired, err := state.AcquireScopeSyncLease(ctx, "solar/default", fmt.Sprintf("pipe/%d", i), fmt.Sprintf("process-%d", i), now, time.Minute)
			if acquired {
				atomic.AddInt32(&winners, 1)
			}
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("winners=%d", winners)
	}
}

func TestOpenMigratesSchema6ToScopeSyncLeases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations(version,applied_at) VALUES(6,'old')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	state, err := Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	var name string
	if err := state.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='scope_sync_leases'`).Scan(&name); err != nil || name != "scope_sync_leases" {
		t.Fatalf("schema 7 lease table: %q %v", name, err)
	}
	var version int
	if err := state.db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("schema version: %d %v", version, err)
	}
}
