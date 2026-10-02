package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/authority"
	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/domain"
	wire "github.com/origama/tubo/internal/protocol"
	appRuntime "github.com/origama/tubo/internal/runtime"
	"github.com/origama/tubo/internal/store"
)

func openPipeViewState(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = state.Close() })
	return state, ctx
}

func putPipeViewMembership(t *testing.T, ctx context.Context, state *store.Store, scope string) {
	t.Helper()
	if err := state.PutMembership(ctx, store.Membership{Scope: scope, Role: "member"}); err != nil {
		t.Fatal(err)
	}
}

func markPipeViewFresh(t *testing.T, ctx context.Context, state *store.Store, scope string) {
	t.Helper()
	if err := state.ReconcileAuthoritativeSnapshot(ctx, scope, store.AuthoritativeSnapshot{Source: store.SnapshotSourcePinnedAuthority, Complete: true, Generation: 1, HighWater: 1, Digest: strings.Repeat("0", 64)}); err != nil {
		t.Fatal(err)
	}
}

func putVerifiedPipeViewRecord(t *testing.T, ctx context.Context, state *store.Store, scopeRaw string) string {
	return putVerifiedPipeViewRecordWithTTL(t, ctx, state, scopeRaw, time.Minute)
}

func putVerifiedPipeViewRecordWithTTL(t *testing.T, ctx context.Context, state *store.Store, scopeRaw string, ttl time.Duration) string {
	t.Helper()
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	if scope.String() != scopeRaw {
		t.Fatalf("test scope %q", scopeRaw)
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
	grant, err := authority.NewToken("pipe.publish", scope, pipeID, pipeID, time.Hour)
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
	record := discovery.PipeRecord{Version: 2, PipeID: pipeID, DisplayName: "cached", RuntimePeerID: "peer", Scope: scope, Protocol: wire.TunnelID, Capabilities: []string{"raw-duplex"}, Addresses: []string{"/ip4/127.0.0.1/tcp/1/p2p/peer"}, Visibility: domain.VisibilityDiscoverable, Access: domain.AccessMembers, PublishGrant: encodedGrant, IssuedAt: now, ExpiresAt: now.Add(ttl), Sequence: 1}
	if err := record.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	payload, err := record.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.PutMembership(ctx, store.Membership{Scope: scopeRaw, Role: "member", AuthorityPublic: authority.PublicString(authorityPublic)}); err != nil {
		t.Fatal(err)
	}
	if err := state.PutRecord(ctx, store.Record{Scope: scopeRaw, PipeID: pipeID, Name: record.DisplayName, Payload: payload, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence, Discovered: true}); err != nil {
		t.Fatal(err)
	}
	return pipeID
}

func TestGetPipesFreshAndCachedOnlySkipNetworkOperation(t *testing.T) {
	state, ctx := openPipeViewState(t)
	putVerifiedPipeViewRecord(t, ctx, state, "solar/default")
	markPipeViewFresh(t, ctx, state, "solar/default")
	if err := state.PutOverlay(ctx, "/ip4/127.0.0.1/tcp/1/p2p/12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv", state.SecretPath("missing-swarm.key"), ""); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"pipes", "--json"}, {"pipes", "--cached-only", "--json"}} {
		var out, errOut bytes.Buffer
		operations := 0
		app := App{Out: &out, Err: &errOut, networkOperationObserver: func(scope string) {
			if scope != "solar/default" {
				t.Fatalf("unexpected network scope %q", scope)
			}
			operations++
		}}
		if err := app.runCommandForTest(t, ctx, state, append([]string{"get"}, args...)); err != nil {
			t.Fatal(err)
		}
		if operations != 0 {
			t.Fatalf("%v reached overlay-host boundary %d times", args, operations)
		}
		var value struct {
			Scopes []pipeScopeStatus `json:"scopes"`
		}
		if err := json.Unmarshal(out.Bytes(), &value); err != nil {
			t.Fatalf("JSON stdout: %v: %q", err, out.String())
		}
		if len(value.Scopes) != 1 || value.Scopes[0].CacheState != "fresh" || value.Scopes[0].RefreshAttempted {
			t.Fatalf("fresh status: %#v", value.Scopes)
		}
		if errOut.Len() != 0 {
			t.Fatalf("unexpected stderr: %q", errOut.String())
		}
	}
}

func TestGetPipesRefreshModesAndFailureFallback(t *testing.T) {
	state, ctx := openPipeViewState(t)
	putPipeViewMembership(t, ctx, state, "solar/default")
	putPipeViewMembership(t, ctx, state, "mars/default")
	markPipeViewFresh(t, ctx, state, "solar/default")
	pipeID := putVerifiedPipeViewRecord(t, ctx, state, "solar/default")

	var out, errOut bytes.Buffer
	calls := map[string]appRuntime.FetchRecordsOptions{}
	app := App{Out: &out, Err: &errOut, fetchRecords: func(_ context.Context, _ *store.Store, scope string, options appRuntime.FetchRecordsOptions) error {
		calls[scope] = options
		if scope == "mars/default" {
			return errors.New("authority_unreachable: test outage")
		}
		return nil
	}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--json"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls["mars/default"].Force {
		t.Fatalf("default calls = %#v; fresh scope must not refresh", calls)
	}
	var defaultValue struct {
		Items  []pipeView        `json:"items"`
		Scopes []pipeScopeStatus `json:"scopes"`
	}
	if err := json.Unmarshal(out.Bytes(), &defaultValue); err != nil {
		t.Fatal(err)
	}
	var mars pipeScopeStatus
	for _, status := range defaultValue.Scopes {
		if status.Scope == "mars/default" {
			mars = status
		}
	}
	if len(defaultValue.Scopes) != 2 || mars.RefreshError == nil || mars.RefreshError.Class != "authority_unreachable" {
		t.Fatalf("scoped failure missing: %#v", defaultValue.Scopes)
	}
	if len(defaultValue.Items) != 1 || defaultValue.Items[0].ID != pipeID || defaultValue.Items[0].Scope != "solar/default" {
		t.Fatalf("fresh scope row hidden by mars failure: %#v", defaultValue.Items)
	}

	out.Reset()
	errOut.Reset()
	calls = map[string]appRuntime.FetchRecordsOptions{}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--refresh"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || !calls["solar/default"].Force || !calls["mars/default"].Force {
		t.Fatalf("forced calls = %#v", calls)
	}
	if !strings.Contains(errOut.String(), "scope=mars/default") || strings.Contains(out.String(), "warning:") {
		t.Fatalf("human diagnostics split failed: stdout=%q stderr=%q", out.String(), errOut.String())
	}
}

func TestGetPipesCachedOnlyNoMembershipAndFlagConflict(t *testing.T) {
	state, ctx := openPipeViewState(t)
	var out, errOut bytes.Buffer
	calls := 0
	app := App{Out: &out, Err: &errOut, fetchRecords: func(context.Context, *store.Store, string, appRuntime.FetchRecordsOptions) error {
		calls++
		return nil
	}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--cached-only", "--json"}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("local-only listing refreshed %d times", calls)
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &value); err != nil || len(value) != 2 {
		t.Fatalf("JSON stdout must contain one typed value: %v %q", err, out.String())
	}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--refresh", "--cached-only"}); err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("flag conflict: %v", err)
	}
}

func TestGetPipesCachedOnlySkipsRetryCooldown(t *testing.T) {
	state, ctx := openPipeViewState(t)
	putPipeViewMembership(t, ctx, state, "solar/default")
	retry := time.Now().UTC().Add(time.Minute)
	if err := state.RecordSnapshotFailure(ctx, "solar/default", store.SnapshotSourcePinnedAuthority, "authority_unreachable", &retry); err != nil {
		t.Fatal(err)
	}
	calls := 0
	app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, fetchRecords: func(context.Context, *store.Store, string, appRuntime.FetchRecordsOptions) error {
		calls++
		return nil
	}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--cached-only"}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("cached-only bypassed cooldown with refresh call")
	}
}

func TestGetPipesCachedOnlyExcludesUnverifiableCache(t *testing.T) {
	state, ctx := openPipeViewState(t)
	putPipeViewMembership(t, ctx, state, "solar/default")
	if err := state.PutRecord(ctx, store.Record{Scope: "solar/default", PipeID: "pip_bad", Name: "bad", Payload: []byte(`{}`), ExpiresAt: time.Now().UTC().Add(time.Minute), Sequence: 1, Discovered: true}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}, fetchRecords: func(context.Context, *store.Store, string, appRuntime.FetchRecordsOptions) error {
		t.Fatal("cached-only attempted refresh")
		return nil
	}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--cached-only", "--json"}); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Items []pipeView `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &value); err != nil || len(value.Items) != 0 {
		t.Fatalf("unverifiable cache leaked: %v %#v", err, value.Items)
	}
}

func TestGetPipesRefreshFailureKeepsVerifiedCache(t *testing.T) {
	state, ctx := openPipeViewState(t)
	pipeID := putVerifiedPipeViewRecord(t, ctx, state, "solar/default")
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}, fetchRecords: func(context.Context, *store.Store, string, appRuntime.FetchRecordsOptions) error {
		return errors.New("authority_unreachable: test outage")
	}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--json"}); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Items  []pipeView        `json:"items"`
		Scopes []pipeScopeStatus `json:"scopes"`
	}
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 1 || value.Items[0].ID != pipeID || value.Items[0].Source != "network" || value.Scopes[0].RefreshError == nil {
		t.Fatalf("verified-cache fallback: %#v %#v", value.Items, value.Scopes)
	}
}

func TestGetPipesLocalRowsSurviveRefreshFailure(t *testing.T) {
	state, ctx := openPipeViewState(t)
	ref, _ := domain.NewRef(domain.KindPipe, "local")
	target, _ := domain.NewRef(domain.KindPipe, "pip_remote")
	if _, err := state.PutPipe(ctx, domain.PipeSpec{Ref: ref, PipeID: "pip_local", Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:1"}, Target: target, Visibility: domain.VisibilityPrivate, Access: domain.AccessMembers, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, "", ""); err != nil {
		t.Fatal(err)
	}
	putPipeViewMembership(t, ctx, state, "solar/default")
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut, fetchRecords: func(context.Context, *store.Store, string, appRuntime.FetchRecordsOptions) error {
		return errors.New("authority_unreachable: test outage")
	}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--json"}); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Items  []pipeView        `json:"items"`
		Scopes []pipeScopeStatus `json:"scopes"`
	}
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 1 || value.Items[0].Source != "local" || value.Scopes[0].RefreshError == nil {
		t.Fatalf("failure fallback: %#v %#v", value.Items, value.Scopes)
	}
}

func TestGetPipesExactFreshnessBoundaryRefreshes(t *testing.T) {
	state, ctx := openPipeViewState(t)
	putPipeViewMembership(t, ctx, state, "solar/default")
	markPipeViewFresh(t, ctx, state, "solar/default")
	freshness, err := state.ScopeFreshness(ctx, "solar/default")
	if err != nil || freshness.LastSuccess == nil {
		t.Fatalf("freshness: %#v %v", freshness, err)
	}
	calls := 0
	app := App{
		Out: &bytes.Buffer{}, Err: &bytes.Buffer{},
		now: func() time.Time { return freshness.LastSuccess.Add(pipeCacheFreshTTL) },
		fetchRecords: func(context.Context, *store.Store, string, appRuntime.FetchRecordsOptions) error {
			calls++
			return errors.New("authority_unreachable: test outage")
		},
	}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--json"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("exact five-minute boundary remained fresh; refresh calls=%d", calls)
	}
}

func TestGetPipesForcedRefreshReachesOverlayBoundaryPastCooldown(t *testing.T) {
	state, ctx := openPipeViewState(t)
	putVerifiedPipeViewRecord(t, ctx, state, "solar/default")
	if err := state.PutOverlay(ctx, "/ip4/127.0.0.1/tcp/1/p2p/12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv", state.SecretPath("missing-swarm.key"), ""); err != nil {
		t.Fatal(err)
	}
	retry := time.Now().UTC().Add(time.Minute)
	if err := state.RecordSnapshotFailure(ctx, "solar/default", store.SnapshotSourcePinnedAuthority, "authority_unreachable", &retry); err != nil {
		t.Fatal(err)
	}
	operations := 0
	app := App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, networkOperationObserver: func(scope string) {
		if scope != "solar/default" {
			t.Fatalf("unexpected boundary scope %q", scope)
		}
		operations++
	}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--cached-only", "--json"}); err != nil {
		t.Fatal(err)
	}
	if operations != 0 {
		t.Fatalf("stale cached-only reached overlay-host boundary: %d", operations)
	}
	started := time.Now()
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--refresh", "--json"}); err != nil {
		t.Fatal(err)
	}
	if operations != 1 {
		t.Fatalf("forced refresh did not reach overlay-host boundary: %d", operations)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("outage fallback exceeded bounded local failure: %s", elapsed)
	}
}

func TestGetPipesCachedOnlyExcludesExpiredRecord(t *testing.T) {
	state, ctx := openPipeViewState(t)
	putVerifiedPipeViewRecordWithTTL(t, ctx, state, "solar/default", 100*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}, networkOperationObserver: func(string) {
		t.Fatal("cached-only reached overlay-host boundary")
	}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--cached-only", "--json"}); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Items []pipeView `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &value); err != nil || len(value.Items) != 0 {
		t.Fatalf("expired cached row projected: %v %#v", err, value.Items)
	}
}

func TestGetPipesScopeFilterKeepsOnlySelectedScopedAndUnscopedLocalRows(t *testing.T) {
	state, ctx := openPipeViewState(t)
	for _, test := range []struct {
		name, pipeID, scope string
	}{
		{name: "solar", pipeID: "pip_solar", scope: "solar/default"},
		{name: "mars", pipeID: "pip_mars", scope: "mars/default"},
		{name: "local", pipeID: "pip_local"},
	} {
		ref, _ := domain.NewRef(domain.KindPipe, test.name)
		target, _ := domain.NewRef(domain.KindPipe, "pip_target")
		spec := domain.PipeSpec{Ref: ref, PipeID: test.pipeID, Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:1"}, Target: target, Visibility: domain.VisibilityPrivate, Access: domain.AccessMembers, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
		if test.scope != "" {
			scope, err := parseScope(test.scope)
			if err != nil {
				t.Fatal(err)
			}
			spec.Scope = scope
		}
		if _, err := state.PutPipe(ctx, spec, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "pipes", "--scope", "solar/default", "--json"}); err != nil {
		t.Fatal(err)
	}
	var value struct {
		Items []pipeView `json:"items"`
	}
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if len(value.Items) != 2 || value.Items[0].ID != "pip_local" || value.Items[1].ID != "pip_solar" {
		t.Fatalf("scope filter leaked another scoped local pipe: %#v", value.Items)
	}
}

func TestGetPipesRejectsObsoleteLiveFlag(t *testing.T) {
	state, ctx := openPipeViewState(t)
	for _, args := range [][]string{
		{"get", "pipes", "--live"},
		{"get", "pipes", "--live", "ignored"},
		{"get", "pipes", "--live=ignored"},
	} {
		if err := (App{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}).runCommandForTest(t, ctx, state, args); err == nil || err.Error() != "invalid_input: --live is obsolete; use --refresh" {
			t.Fatalf("%v error = %v", args, err)
		}
	}
}
