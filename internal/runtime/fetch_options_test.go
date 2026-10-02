package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/store"
)

func TestFetchRecordsWithOptionsForceBypassesOnlyRetryCooldown(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	retry := time.Now().UTC().Add(time.Minute)
	if err := state.RecordSnapshotFailure(ctx, "solar/default", store.SnapshotSourcePinnedAuthority, "authority_unreachable", &retry); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchRecords(ctx, state, "solar/default"); err == nil || !strings.Contains(err.Error(), "discovery_retry_after") {
		t.Fatalf("default refresh ignored cooldown: %v", err)
	}
	if _, err := FetchRecordsWithOptions(ctx, state, "solar/default", FetchRecordsOptions{Force: true}); err == nil || strings.Contains(err.Error(), "discovery_retry_after") {
		t.Fatalf("forced refresh did not bypass only cooldown: %v", err)
	}
}
