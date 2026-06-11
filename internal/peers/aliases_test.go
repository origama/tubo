package peers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreUpsertLookupAndList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.json")
	store := NewStore(path)
	store.now = func() time.Time { return time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC) }

	alias, err := store.Upsert("12D3peer", "oripi", "verified via SSH")
	if err != nil {
		t.Fatal(err)
	}
	if alias.PeerID != "12D3peer" || alias.Name != "oripi" || alias.Note != "verified via SSH" {
		t.Fatalf("unexpected alias: %#v", alias)
	}

	got, ok, err := store.Lookup("12D3peer")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Name != "oripi" {
		t.Fatalf("unexpected lookup: ok=%v alias=%#v", ok, got)
	}

	updated, err := store.Upsert("12D3peer", "oripi-updated", "")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "oripi-updated" || updated.Note != "" {
		t.Fatalf("unexpected updated alias: %#v", updated)
	}

	store.now = func() time.Time { return time.Date(2026, 6, 8, 12, 1, 0, 0, time.UTC) }
	if _, err := store.Upsert("12D3another", "alice", ""); err != nil {
		t.Fatal(err)
	}

	list, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("unexpected list length: %#v", list)
	}
	if list[0].Name != "oripi-updated" || list[1].Name != "alice" {
		t.Fatalf("unexpected list order: %#v", list)
	}
}

func TestStoreCorruptFileFailsClearly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "aliases.json")
	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, _, err := NewStore(path).Lookup("12D3peer")
	if err == nil || !strings.Contains(err.Error(), "decode peer alias store") {
		t.Fatalf("expected corrupt store error, got %v", err)
	}
}
