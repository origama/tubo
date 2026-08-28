package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/authority"
	"github.com/origama/tubo-rebuilt/internal/discovery"
	"github.com/origama/tubo-rebuilt/internal/domain"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
)

func putWatchRecord(t *testing.T, ctx context.Context, state *store.Store, scopeRaw, name string, member bool) string {
	t.Helper()
	scope, err := parseScope(scopeRaw)
	if err != nil {
		t.Fatal(err)
	}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerPublic, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pipeID := authority.PipeID(ownerPublic)
	grant, err := authority.NewToken("pipe.publish", *scope, pipeID, pipeID, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	encodedGrant, err := authority.Encode(grant)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := discovery.PipeRecord{Version: 2, PipeID: pipeID, DisplayName: name, RuntimePeerID: "peer", Scope: *scope, Protocol: wire.TunnelID, Capabilities: []string{"raw-duplex"}, Addresses: []string{"/ip4/127.0.0.1/tcp/1/p2p/peer"}, Visibility: domain.VisibilityDiscoverable, Access: domain.AccessMembers, PublishGrant: encodedGrant, IssuedAt: now, ExpiresAt: now.Add(time.Hour), Sequence: 1}
	if err := record.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	payload, err := record.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if member {
		if err := state.PutMembership(ctx, store.Membership{Scope: scopeRaw, Role: "member", AuthorityPublic: authority.PublicString(authorityPublic)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.PutRecord(ctx, store.Record{Scope: scopeRaw, PipeID: pipeID, Name: name, Payload: payload, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence, Discovered: true}); err != nil {
		t.Fatal(err)
	}
	return pipeID
}

func TestDiffPipeViewsEmitsExplicitDeterministicEventTypes(t *testing.T) {
	at := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	expiredAt := at.Add(-time.Second)
	previous := map[string]pipeView{
		"mars/default\x00pip_a":       {ID: "pip_a", Name: "degraded", Scope: "mars/default", Status: "ready", Source: "network", Freshness: "fresh"},
		"mars/default\x00pip_b":       {ID: "pip_b", Name: "recovered", Scope: "mars/default", Status: "degraded", Source: "network", Freshness: "stale"},
		"solar/default\x00pip_c":      {ID: "pip_c", Name: "expired", Scope: "solar/default", Status: "available", Source: "network", Freshness: "fresh", ExpiresAt: &expiredAt},
		"solar/default\x00pip_d":      {ID: "pip_d", Name: "removed", Scope: "solar/default", Status: "available", Source: "network", Freshness: "fresh"},
		"venus/default\x00pip_hidden": {ID: "pip_hidden", Name: "hidden", Scope: "venus/default", Status: "available", Source: "network", Freshness: "fresh"},
	}
	current := map[string]pipeView{
		"mars/default\x00pip_a":       {ID: "pip_a", Name: "degraded", Scope: "mars/default", Status: "degraded", Source: "network", Freshness: "stale"},
		"mars/default\x00pip_b":       {ID: "pip_b", Name: "recovered", Scope: "mars/default", Status: "ready", Source: "network", Freshness: "fresh"},
		"venus/default\x00pip_new":    {ID: "pip_new", Name: "added", Scope: "venus/default", Status: "available", Source: "network", Freshness: "missing"},
		"venus/default\x00pip_hidden": {ID: "pip_hidden", Name: "hidden", Scope: "venus/default", Status: "available", Source: "network", Freshness: "stale"},
	}
	events := diffPipeViews(previous, current, at)
	if len(events) != 6 {
		t.Fatalf("event count = %d: %#v", len(events), events)
	}
	want := []struct{ eventType, pipeID string }{
		{"degraded", "pip_a"},
		{"recovered", "pip_b"},
		{"expired", "pip_c"},
		{"removed", "pip_d"},
		{"updated", "pip_hidden"},
		{"added", "pip_new"},
	}
	for index, expected := range want {
		event := events[index]
		if event.EventType != expected.eventType || event.PipeID != expected.pipeID {
			t.Fatalf("event[%d] = %#v, want type=%s pipe=%s", index, event, expected.eventType, expected.pipeID)
		}
		if !event.ObservedAt.Equal(at) {
			t.Fatalf("observed timestamp changed: %#v", event)
		}
	}
	if strings.Join(events[0].Changed, ",") != "status,freshness" || strings.Join(events[1].Changed, ",") != "status,freshness" {
		t.Fatalf("status event evidence = degraded:%#v recovered:%#v", events[0], events[1])
	}
}

func TestDiffPipeViewsClassifiesExpiredLocalNetworkProjection(t *testing.T) {
	at := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	expiredAt := at.Add(-time.Second)
	previous := map[string]pipeView{
		"solar/default\x00pip_local": {ID: "pip_local", Name: "local", Scope: "solar/default", Source: "local+network", Status: "ready", Freshness: "fresh", ExpiresAt: &expiredAt},
	}
	current := map[string]pipeView{
		"solar/default\x00pip_local": {ID: "pip_local", Name: "local", Scope: "solar/default", Source: "local", Status: "ready", Freshness: "fresh"},
	}
	events := diffPipeViews(previous, current, at)
	if len(events) != 1 || events[0].EventType != "expired" || strings.Join(events[0].Changed, ",") != "source" {
		t.Fatalf("expired local+network projection = %#v", events)
	}
}

func TestWatchPipesAuthorizedMultiScopeAndDuplicateNames(t *testing.T) {
	state, ctx := openPipeViewState(t)
	solarID := putWatchRecord(t, ctx, state, "solar/default", "shared", true)
	marsID := putWatchRecord(t, ctx, state, "mars/default", "shared", true)
	unauthorizedID := putWatchRecord(t, ctx, state, "venus/default", "hidden", false)

	items, err := buildPipeViews(ctx, state, "", map[string]string{"solar/default": "fresh", "mars/default": "stale"})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("authorized view count = %d: %#v", len(items), items)
	}
	seen := map[string]string{}
	for _, item := range items {
		seen[item.ID] = item.Scope
		if item.ID == unauthorizedID || item.Scope == "venus/default" {
			t.Fatalf("unauthorized scope leaked: %#v", item)
		}
	}
	if seen[solarID] != "solar/default" || seen[marsID] != "mars/default" {
		t.Fatalf("duplicate names collapsed or scope lost: %#v", seen)
	}

	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	writer := &cancelAfterWatchWriter{limit: 2, cancel: cancel}
	var errOut bytes.Buffer
	app := App{Out: writer, Err: &errOut}
	done := make(chan error, 1)
	go func() { done <- app.watchPipes(cancelCtx, state, "", 500*time.Millisecond, true) }()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("watch wrote stderr: %q", errOut.String())
	}
	lines := strings.Split(strings.TrimSpace(writer.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("NDJSON event lines = %d: %q", len(lines), writer.String())
	}
	for _, line := range lines {
		var event pipeWatchEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("not JSONL: %v: %q", err, line)
		}
		if event.EventType != "added" || event.Item == nil || event.Scope == "venus/default" {
			t.Fatalf("unexpected initial event: %#v", event)
		}
	}
}

type cancelAfterWatchWriter struct {
	mu     sync.Mutex
	buf    bytes.Buffer
	writes int
	limit  int
	cancel context.CancelFunc
}

func (w *cancelAfterWatchWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(value)
	w.writes++
	if w.writes == w.limit {
		w.cancel()
	}
	return n, err
}

func (w *cancelAfterWatchWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

type blockingWatchWriter struct {
	mu      sync.Mutex
	writes  int
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockingWatchWriter) Write(value []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	w.mu.Unlock()
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(value), nil
}

func (w *blockingWatchWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func TestWatchPipesSlowConsumerBackpressuresWithoutQueue(t *testing.T) {
	state, ctx := openPipeViewState(t)
	putWatchRecord(t, ctx, state, "solar/default", "one", true)
	cancelCtx, cancel := context.WithCancel(ctx)
	writer := &blockingWatchWriter{entered: make(chan struct{}), release: make(chan struct{})}
	app := App{Out: writer, Err: &bytes.Buffer{}}
	done := make(chan error, 1)
	go func() { done <- app.watchPipes(cancelCtx, state, "", 500*time.Millisecond, false) }()
	select {
	case <-writer.entered:
	case <-time.After(time.Second):
		t.Fatal("watch did not emit initial event")
	}
	time.Sleep(1100 * time.Millisecond)
	if writes := writer.count(); writes != 1 {
		t.Fatalf("slow consumer accumulated %d writes; expected synchronous backpressure", writes)
	}
	cancel()
	close(writer.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("watch did not clean up after consumer release and context cancellation")
	}
}

func TestWatchPipesCLIValidation(t *testing.T) {
	state, ctx := openPipeViewState(t)
	app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	if err := app.runCommandForTest(t, ctx, state, []string{"watch", "pipes", "--interval", "499ms"}); err == nil || !strings.Contains(err.Error(), "at least 500ms") {
		t.Fatalf("minimum interval error = %v", err)
	}
	if err := app.runCommandForTest(t, ctx, state, []string{"watch", "services"}); err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Fatalf("watch routing error = %v", err)
	}
}
