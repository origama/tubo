package authority

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnerPrivateKeyFirstProvisionReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "pipes", "demo.key")
	first, err := GeneratePrivate(path)
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	loaded, err := ReadPrivate(path)
	if err != nil {
		t.Fatalf("read owner key: %v", err)
	}
	if !bytes.Equal(first, loaded) {
		t.Fatal("reloaded owner key differs")
	}
	pipeID := PipeID(loaded.Public().(ed25519.PublicKey))
	if pipeID == "" || pipeID[:4] != "pip_" {
		t.Fatalf("invalid pipe id %q", pipeID)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o want 0600", info.Mode().Perm())
	}
}
