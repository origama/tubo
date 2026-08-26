package localkey

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

func TestProvisionAndLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "owner.key")
	first, err := Provision(path)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(first, loaded) {
		t.Fatal("loaded key differs")
	}
	if got, want := modeOf(t, filepath.Dir(path)), os.FileMode(0o700); got != want {
		t.Fatalf("dir mode=%o want %o", got, want)
	}
	if got, want := modeOf(t, path), os.FileMode(0o600); got != want {
		t.Fatalf("file mode=%o want %o", got, want)
	}
}

func TestLoadRejectsUnsafeModes(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "secrets", "owner.key")
	private := writeKey(t, path)
	if _, err := Load(path); err != nil {
		t.Fatalf("initial load: %v", err)
	}

	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unsafe file mode err=%v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(private)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("unsafe dir mode err=%v", err)
	}
}

func TestLoadRejectsFinalSymlink(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "elsewhere.key")
	writeKey(t, target)
	if err := os.Symlink(target, filepath.Join(dir, "owner.key")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "owner.key")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("symlink err=%v", err)
	}
}

func TestLoadRejectsSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeKey(t, filepath.Join(realDir, "owner.key"))
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(linkDir, "owner.key")); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("dir symlink err=%v", err)
	}
}

func TestLoadRejectsCorruptOversizedNoncanonical(t *testing.T) {
	cases := map[string][]byte{
		"corrupt":      bytes.Repeat([]byte("."), encodedPrivateKeyLen),
		"oversized":    bytes.Repeat([]byte("A"), encodedPrivateKeyLen+1),
		"noncanonical": append([]byte(base64.RawURLEncoding.EncodeToString(newKey(t))), '\n'),
		"partial":      []byte(base64.RawURLEncoding.EncodeToString(newKey(t))[:encodedPrivateKeyLen-1]),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "secrets", "owner.key")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestLoadMissingCreatesZeroFiles(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "secrets", "owner.key")
	if _, err := Load(path); err == nil {
		t.Fatal("missing load succeeded")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Load created entries: %v", entryNames(entries))
	}
}

func TestConcurrentFirstProvisionOneDurableKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "owner.key")
	const workers = 16
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(workers)
	keys := make(chan ed25519.PrivateKey, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			ready.Done()
			<-start
			key, err := Provision(path)
			if err != nil {
				errs <- err
				return
			}
			keys <- key
		}()
	}
	ready.Wait()
	close(start)
	var first ed25519.PrivateKey
	for i := 0; i < workers; i++ {
		select {
		case err := <-errs:
			t.Fatalf("provision error: %v", err)
		case key := <-keys:
			if first == nil {
				first = key
			} else if !bytes.Equal(first, key) {
				t.Fatal("concurrent provision returned different keys")
			}
		}
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("load winner: %v", err)
	}
	if !bytes.Equal(first, loaded) {
		t.Fatal("durable key differs from returned key")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := entryNames(entries); len(got) != 1 || got[0] != "owner.key" {
		t.Fatalf("entries=%v want [owner.key]", got)
	}
}

func TestInjectedWriteAndSyncFailuresDoNotSucceed(t *testing.T) {
	cases := map[string]*testHooks{
		"write":     {write: func(*os.File, []byte) (int, error) { return 0, errors.New("injected write") }},
		"file-sync": {syncFile: func(*os.File) error { return errors.New("injected file sync") }},
		"dir-sync":  {syncDir: func(*os.File) error { return errors.New("injected dir sync") }},
	}
	for name, hook := range cases {
		t.Run(name, func(t *testing.T) {
			setTestHooks(hook)
			defer setTestHooks(nil)
			path := filepath.Join(t.TempDir(), "secrets", "owner.key")
			if _, err := Provision(path); err == nil {
				t.Fatal("provision succeeded despite injected failure")
			}
			if _, err := Load(path); err == nil {
				t.Fatal("failed provision left loadable key")
			}
		})
	}
}

func TestLoserWaitsForDurablePublication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secrets", "owner.key")
	published := make(chan struct{})
	allowSync := make(chan struct{})
	loserAttemptedLock := make(chan struct{})
	var lockAttempts atomic.Int32
	setTestHooks(&testHooks{
		beforeLock: func() {
			if lockAttempts.Add(1) == 2 {
				close(loserAttemptedLock)
			}
		},
		afterPublishBeforeDirSync: func() {
			close(published)
			<-allowSync
		},
	})
	defer setTestHooks(nil)

	winnerResult := make(chan error, 1)
	go func() {
		_, err := Provision(path)
		winnerResult <- err
	}()
	<-published

	loserResult := make(chan error, 1)
	go func() {
		_, err := Provision(path)
		loserResult <- err
	}()
	<-loserAttemptedLock
	select {
	case err := <-loserResult:
		t.Fatalf("loser returned before durable publication: %v", err)
	default:
	}
	close(allowSync)
	if err := <-winnerResult; err != nil {
		t.Fatalf("winner: %v", err)
	}
	if err := <-loserResult; err != nil {
		t.Fatalf("loser: %v", err)
	}
}

func writeKey(t *testing.T, path string) ed25519.PrivateKey {
	t.Helper()
	private := newKey(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(base64.RawURLEncoding.EncodeToString(private)), 0o600); err != nil {
		t.Fatal(err)
	}
	return private
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private
}

func modeOf(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

func entryNames(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

// TestParseCanonicalPrivateRejectsInconsistentPublicSuffix proves P2
// hardening: a 64-byte Ed25519 private key whose last 32 bytes (public key
// suffix) are not the canonical key derived from its 32-byte seed must be
// rejected. Without this check a corrupt or adversarial key file with an
// arbitrary public suffix would be accepted, silently defeating identity and
// signature verification.
func TestParseCanonicalPrivateRejectsInconsistentPublicSuffix(t *testing.T) {
	// Generate a real key so we have a valid seed and public suffix.
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Serialize the real key as canonical base64url.
	good := base64.RawURLEncoding.EncodeToString([]byte(private))
	if _, err := parseCanonicalPrivate([]byte(good)); err != nil {
		t.Fatalf("good key rejected: %v", err)
	}

	// Flip one byte in the public suffix (last 32 bytes) so it no longer
	// matches the seed-derived public key.
	tampered := make([]byte, ed25519.PrivateKeySize)
	copy(tampered, private)
	tampered[ed25519.SeedSize+5] ^= 0xff
	bad := base64.RawURLEncoding.EncodeToString(tampered)
	if _, err := parseCanonicalPrivate([]byte(bad)); err == nil {
		t.Fatal("tampered public suffix accepted")
	}

	// Also test: a key whose seed and public are both arbitrary unrelated
	// 32-byte values.
	randomPri := make([]byte, ed25519.PrivateKeySize)
	if _, err := rand.Read(randomPri); err != nil {
		t.Fatal(err)
	}
	// Write out the first 32 bytes (fake seed) and last 32 bytes (fake
	// public) independently so there is zero chance of accidental match.
	sepSeed := make([]byte, 32)
	sepPub := make([]byte, 32)
	if _, err := rand.Read(sepSeed); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(sepPub); err != nil {
		t.Fatal(err)
	}
	copy(randomPri[:32], sepSeed)
	copy(randomPri[32:], sepPub)
	arbitrary := base64.RawURLEncoding.EncodeToString(randomPri)
	if _, err := parseCanonicalPrivate([]byte(arbitrary)); err == nil {
		t.Fatal("arbitrary seed+public accepted")
	}
}
