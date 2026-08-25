package exactauthority

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

// TestProvisionConcurrentFirstWrite proves atomic no-clobber first
// provisioning: concurrent first creators all observe the exact same persisted
// winner, no partial/corrupt/leftover temp file is produced, and the persisted
// secret later loads cleanly.
func TestProvisionConcurrentFirstWrite(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	for iter := 0; iter < 8; iter++ {
		start := make(chan struct{})
		const n = 16
		results := make([]*Key, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				results[idx], errs[idx] = Provision(dir, owner)
			}(i)
		}
		close(start)
		wg.Wait()
		first := results[0]
		for i, k := range results {
			if errs[i] != nil {
				t.Fatalf("iter %d goroutine %d provision error: %v", iter, i, errs[i])
			}
			if k == nil || !k.Public().Equal(first.Public()) {
				t.Fatalf("iter %d goroutine %d observed a different persisted key", iter, i)
			}
			if k.SecretPath() == "" {
				t.Fatalf("iter %d goroutine %d missing secret path", iter, i)
			}
		}
		// Persisted winner loads cleanly from disk (proves not partially written).
		loaded, err := Load(dir, owner.Public().(ed25519.PublicKey))
		if err != nil {
			t.Fatalf("iter %d persisted winner failed to load: %v", iter, err)
		}
		if !loaded.Public().Equal(first.Public()) {
			t.Fatalf("iter %d reload did not match winner", iter)
		}
		// Exactly one key file; no leftover temp files.
		entries := treeEntries(t, dir)
		if len(entries) != 3 { // root dir + exact-authority + 1 key file
			t.Fatalf("iter %d expected exactly one secret file, got %d entries: %v", iter, len(entries), entries)
		}
		for _, e := range entries {
			if strings.Contains(filepath.Base(e), ".tmp") {
				t.Fatalf("iter %d leftover temp file: %s", iter, e)
			}
		}
	}
}

// TestProvisionRejectsSymlinkedAuthorityDir proves a symlinked authority
// directory fails closed rather than following the redirect.
func TestProvisionRejectsSymlinkedAuthorityDir(t *testing.T) {
	if isWindows(t) {
		t.Skip("symlink creation requires privileges on Windows; precise reason: cannot create symlink dir")
	}
	base := t.TempDir()
	outside := t.TempDir()
	owner := newKey(t)
	// Make the real target directory, then replace it with a symlink.
	real := filepath.Join(base, "exact-authority")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(real); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, real); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(base, owner); err == nil {
		t.Fatal("provision accepted a symlinked authority dir")
	}
	// Load path also fails closed on a symlinked dir.
	if _, err := Load(base, owner.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("load accepted a symlinked authority dir")
	}
}

// TestProvisionRejectsSymlinkedKeyFile proves the key file itself is never
// followed as a symlink.
func TestProvisionRejectsSymlinkedKeyFile(t *testing.T) {
	if isWindows(t) {
		t.Skip("symlink creation requires privileges on Windows; skip reason: platform-specific")
	}
	dir := t.TempDir()
	owner := newKey(t)
	k, err := Provision(dir, owner)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the key file with a symlink to a different valid file.
	evil := filepath.Join(dir, "evil.key")
	if err := os.WriteFile(evil, []byte(`{"format":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(k.SecretPath()); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(evil, k.SecretPath()); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, owner.Public().(ed25519.PublicKey)); err == nil {
		t.Fatal("load accepted a symlinked key file")
	}
	if _, err := Provision(dir, owner); err == nil {
		t.Fatal("provision fast path accepted a symlinked key file")
	}
}

// TestLoadRejectsUnsafeModes proves group/other-writable key and authority-dir
// permissions fail closed.
func TestLoadRejectsUnsafeModes(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	k, err := Provision(dir, owner)
	if err != nil {
		t.Fatal(err)
	}
	ownerPub := owner.Public().(ed25519.PublicKey)

	if err := os.Chmod(k.SecretPath(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("load accepted a group/other-readable key file")
	}
	if err := os.Chmod(k.SecretPath(), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(k.SecretPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("load accepted a group/other-accessible authority dir")
	}
}

// TestLoadStrictEnvelopeRejects covers the bounded strict key envelope: unknown,
// duplicate, trailing, and nested members and overlong secret fields are
// rejected before typed unmarshal, and an oversized file is rejected by the
// byte cap.
func TestLoadStrictEnvelopeRejects(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	k, err := Provision(dir, owner)
	if err != nil {
		t.Fatal(err)
	}
	ownerPub := owner.Public().(ed25519.PublicKey)
	path := k.SecretPath()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	writeEnv := func(m map[string]any) {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// unknown field
	m := cloneJSON(env)
	m["nope"] = true
	writeEnv(m)
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("envelope with unknown field loaded")
	}
	// duplicate key
	d := []byte(`{"format":"x","format":"y"}`)
	if err := os.WriteFile(path, d, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("envelope with duplicate key loaded")
	}
	// trailing payload
	if err := os.WriteFile(path, append(append([]byte{}, raw...), []byte(" }")...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("envelope with trailing payload loaded")
	}
	// nested object
	m2 := cloneJSON(env)
	m2["owner"] = map[string]any{"x": 1}
	writeEnv(m2)
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("envelope with nested object loaded")
	}
	// overlong authority_secret (scan bound, before base64 decode)
	m3 := cloneJSON(env)
	m3["authority_secret"] = base64.RawURLEncoding.EncodeToString(make([]byte, accesslease.MaxFieldLen+1))
	writeEnv(m3)
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("envelope with overlong secret loaded")
	}
	// oversized file (total byte cap)
	big := append(append([]byte{}, raw...), []byte(`{"padding":"`+strings.Repeat("z", 20<<10)+`"}`)...)
	if err := os.WriteFile(path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("oversized envelope loaded")
	}
}

func cloneJSON(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func isWindows(t *testing.T) bool {
	t.Helper()
	return runtime.GOOS == "windows"
}
