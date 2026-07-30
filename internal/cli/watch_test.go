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

func TestDiffPipeViewsDeterministicAndObservedChanges(t *testing.T) {
	at := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	previous := map[string]pipeView{
		"mars/default\x00pip_b":  {ID: "pip_b", Name: "same", Scope: "mars/default", Status: "ready", Source: "network", Freshness: "fresh"},
		"solar/default\x00pip_a": {ID: "pip_a", Name: "old", Scope: "solar/default", Status: "ready", Source: "network", Freshness: "fresh"},
	}
	current := map[string]pipeView{
		"mars/default\x00pip_b":  {ID: "pip_b", Name: "same", Scope: "mars/default", Status: "degraded", Source: "local+network", Freshness: "stale"},
		"venus/default\x00pip_c": {ID: "pip_c", Name: "new", Scope: "venus/default", Status: "available", Source: "network", Freshness: "missing"},
	}
	events := diffPipeViews(previous, current, at)
	if len(events) != 3 {
		t.Fatalf("event count = %d: %#v", len(events), events)
	}
	if events[0].Type != "changed" || events[0].Scope != "mars/default" || strings.Join(events[0].Changed, ",") != "status,source,freshness" {
		t.Fatalf("changed event = %#v", events[0])
	}
	if events[1].Type != "removed" || events[1].PipeID != "pip_a" || events[1].Previous == nil {
		t.Fatalf("removed event = %#v", events[1])
	}
	if events[2].Type != "new" || events[2].PipeID != "pip_c" || events[2].Item == nil {
		t.Fatalf("new event = %#v", events[2])
	}
	for _, event := range events {
		if !event.ObservedAt.Equal(at) {
			t.Fatalf("observed timestamp changed: %#v", event)
		}
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
		if event.Type != "new" || event.Item == nil || event.Scope == "venus/default" {
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
	if err := app.watch(ctx, state, []string{"pipes", "--interval", "499ms"}); err == nil || !strings.Contains(err.Error(), "at least 500ms") {
		t.Fatalf("minimum interval error = %v", err)
	}
	if err := app.watch(ctx, state, []string{"services"}); err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("watch routing error = %v", err)
	}
}
