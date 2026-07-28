package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

func TestOpenMigratesSchema3RunSource(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL); INSERT INTO schema_migrations(version,applied_at) VALUES(3,'old'); CREATE TABLE runs(primary_ref TEXT PRIMARY KEY,kind TEXT NOT NULL,pid INTEGER NOT NULL,process_identity TEXT NOT NULL,status TEXT NOT NULL,peer_id TEXT NOT NULL,address TEXT NOT NULL,started_at TEXT NOT NULL,updated_at TEXT NOT NULL); INSERT INTO runs VALUES('pipe/demo','pipe',0,'','stopped','','','old','old')`); err != nil {
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
	if _, err := db.Exec(`INSERT INTO schema_migrations(version,applied_at) VALUES(5,'future')`); err != nil {
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
