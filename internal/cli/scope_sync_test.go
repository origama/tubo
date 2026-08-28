package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/store"
)

func decodeScopeSync(t *testing.T, output []byte, get bool) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(output, &value); err != nil {
		t.Fatal(err)
	}
	if get {
		items, ok := value["items"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("scope list: %#v", value)
		}
		value, ok = items[0].(map[string]any)
		if !ok {
			t.Fatalf("scope row: %#v", items[0])
		}
	}
	sync, ok := value["sync"].(map[string]any)
	if !ok {
		t.Fatalf("sync projection: %#v", value["sync"])
	}
	return sync
}

func runScopeStatus(t *testing.T, app App, ctx context.Context, state *store.Store, get bool) (map[string]any, string) {
	t.Helper()
	var out bytes.Buffer
	app.Out = &out
	if get {
		if err := app.runCommandForTest(t, ctx, state, []string{"get", "scopes", "--json"}); err != nil {
			t.Fatal(err)
		}
	} else if err := app.runCommandForTest(t, ctx, state, []string{"inspect", "scope/solar/default", "--json"}); err != nil {
		t.Fatal(err)
	}
	return decodeScopeSync(t, out.Bytes(), get), out.String()
}

func TestScopeSyncStatusProjectsActiveAndExpiredLeaseWithoutSecrets(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if err := state.PutScope(ctx, store.Scope{Name: "solar/default", AuthorityPublic: "public"}); err != nil {
		t.Fatal(err)
	}
	lease, acquired, err := state.AcquireScopeSyncLease(ctx, "solar/default", "pipe/member", "pid:private-command", time.Now().UTC(), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("lease: %t %v", acquired, err)
	}
	next := time.Now().UTC().Add(time.Minute)
	if err := state.RecordSnapshotFailureWithLease(ctx, lease, store.SnapshotSourcePinnedAuthority, "authority_unreachable", &next); err != nil {
		t.Fatal(err)
	}
	app := App{Err: &bytes.Buffer{}}
	for _, get := range []bool{false, true} {
		sync, output := runScopeStatus(t, app, ctx, state, get)
		if sync["owner_ref"] != "pipe/member" || sync["error_class"] != "authority_unreachable" || sync["lease_expires_at"] == nil || sync["next_retry"] == nil {
			t.Fatalf("active sync get=%t: %#v", get, sync)
		}
		if strings.Contains(output, lease.Token) || strings.Contains(output, lease.ProcessIdentity) {
			t.Fatalf("active status leaked secret: %s", output)
		}
	}
	if released, err := state.ReleaseScopeSyncLease(ctx, lease); err != nil || !released {
		t.Fatalf("release active lease: %t %v", released, err)
	}
	// Persist an already-expired row. Status must omit owner/expiry while
	// retaining non-secret discovery freshness.
	expired, acquired, err := state.AcquireScopeSyncLease(ctx, "solar/default", "pipe/expired", "pid:expired-private-command", time.Now().UTC().Add(-2*time.Minute), time.Minute)
	if err != nil || !acquired {
		t.Fatalf("expired lease: %t %v", acquired, err)
	}
	for _, get := range []bool{false, true} {
		sync, output := runScopeStatus(t, app, ctx, state, get)
		if _, ok := sync["owner_ref"]; ok {
			t.Fatalf("expired owner exposed get=%t: %#v", get, sync)
		}
		if _, ok := sync["lease_expires_at"]; ok {
			t.Fatalf("expired expiry exposed get=%t: %#v", get, sync)
		}
		if sync["error_class"] != "authority_unreachable" || sync["next_retry"] == nil {
			t.Fatalf("freshness lost get=%t: %#v", get, sync)
		}
		if strings.Contains(output, expired.Token) || strings.Contains(output, expired.ProcessIdentity) {
			t.Fatalf("expired status leaked secret: %s", output)
		}
	}
}
