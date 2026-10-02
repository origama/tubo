package node

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	crypto "github.com/libp2p/go-libp2p/core/crypto"
)

func identityPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "identity", "host.key")
}

// TestLoadOrCreateIdentityAtomicPublish: the published identity file must
// never be a truncated or absent-key state even if the process dies between
// generation and write (temp+rename publish), and must have 0600 perms.
func TestLoadOrCreateIdentityAtomicPublish(t *testing.T) {
	path := identityPath(t)
	first, err := loadOrCreateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("identity file perms = %o, want 0600", info.Mode().Perm())
	}
	again, err := loadOrCreateIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equals(again) {
		t.Fatal("identity silently rotated on second load")
	}
	// No temp-file debris.
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".identity-") {
			t.Fatalf("identity temp file left behind: %s", e.Name())
		}
	}
}

// TestLoadOrCreateIdentityConcurrentCreationIsDeterministic: concurrent
// processes creating the same missing identity must all converge on ONE
// identity (exactly one generator, everyone else loads the winner's key).
func TestLoadOrCreateIdentityConcurrentCreationIsDeterministic(t *testing.T) {
	path := identityPath(t)
	const workers = 8
	keys := make([]crypto.PrivKey, workers)
	errs := make([]error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			keys[i], errs[i] = loadOrCreateIdentity(path)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("worker %d: %v", i, err)
		}
		if !keys[0].Equals(keys[i]) {
			t.Fatalf("worker %d observed a different host identity", i)
		}
	}
}

// TestCorruptIdentityFailsClosed: a corrupt identity file must fail loudly,
// never silently regenerate (regeneration rotates the durable host identity).
func TestCorruptIdentityFailsClosed(t *testing.T) {
	path := identityPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadOrCreateIdentity(path); err == nil {
		t.Fatal("corrupt identity silently regenerated")
	} else if !strings.Contains(err.Error(), "refusing to regenerate") {
		t.Fatalf("unexpected error: %v", err)
	}
}
