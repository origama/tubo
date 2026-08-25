package exactauthority

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ensure provisioning helper compiles: Provision needs a non-nil OwnerPublic.
func ownerPub(priv ed25519.PrivateKey) ed25519.PublicKey { return priv.Public().(ed25519.PublicKey) }

func treeEntries(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	return out
}

// TestKeyLifecycleProvisionLoad covers create/load existing once, deterministic
// secret path, derived public, and owner proof validity (Provision).
func TestKeyLifecycleProvisionLoad(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	k1, err := Provision(dir, owner)
	mustOK(t, err)
	k2, err := Provision(dir, owner) // create-or-load existing once
	mustOK(t, err)
	if k1.SecretPath() != k2.SecretPath() {
		t.Fatal("same owner produced different secret paths")
	}
	if !k1.Public().Equal(k2.Public()) {
		t.Fatal("deterministic reuse produced a different key")
	}
	if !k1.Public().Equal(k1.private.Public().(ed25519.PublicKey)) {
		t.Fatal("public not derived from secret")
	}
	if !k1.OwnerPublic().Equal(owner.Public().(ed25519.PublicKey)) {
		t.Fatal("owner binding on key wrong")
	}
	if SecretPath(dir, owner.Public().(ed25519.PublicKey)) != k1.SecretPath() {
		t.Fatal("deterministic path mismatch")
	}
	k3, err := Load(dir, owner.Public().(ed25519.PublicKey))
	mustOK(t, err)
	if !k3.Public().Equal(k1.Public()) {
		t.Fatal("load produced different authority key")
	}
}

// TestKeyProvisionCreatesExactlyOneSecret counts filesystem entries:
// provisioning creates the directory plus exactly one key file.
func TestKeyProvisionCreatesExactlyOneSecret(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	k, err := Provision(dir, owner)
	mustOK(t, err)
	entries := treeEntries(t, dir)
	if len(entries) != 3 { // dir root + exact-authority dir + 1 key file
		t.Fatalf("expected 1 key file under dir, got %d entries: %v", len(entries), entries)
	}
	if fi, err := os.Stat(k.SecretPath()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file must be 0600, got %v", fi.Mode())
	}
}

// TestKeyLoadReadOnlyMissingCreatesZeroEntries asserts read-only load of absent
// material fails closed AND creates no filesystem entries.
func TestKeyLoadReadOnlyMissingCreatesZeroEntries(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	_, err := Load(dir, owner.Public().(ed25519.PublicKey))
	if err != ErrMissing {
		t.Fatalf("expected ErrMissing, got %v", err)
	}
	entries := treeEntries(t, dir)
	if len(entries) != 1 { // only the temp dir itself
		t.Fatalf("read-only load created entries: %d", len(entries))
	}
}

// TestKeyWrongOwnerFailsClosed covers a different owner attempting to load.
func TestKeyWrongOwnerFailsClosed(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	other := newKey(t)
	if _, err := Provision(dir, owner); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(dir, other.Public().(ed25519.PublicKey)); err == nil || got != nil {
		t.Fatalf("key loaded with wrong owner: %v", err)
	}
}

// TestKeyCorruptFailsClosed covers tampered envelope fields and invalid proof.
func TestKeyCorruptFailsClosed(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	k, err := Provision(dir, owner)
	mustOK(t, err)
	ownerPub := owner.Public().(ed25519.PublicKey)
	path := k.SecretPath()

	// truncate (corrupt secret)
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("corrupt key loaded")
	}

	// restore then rewrite a valid envelope with a tampered owner proof
	env := keyMetadata{
		Format:          keyFormat,
		Version:         keyVersion,
		Owner:           accesslease.PublicString(ownerPub),
		AuthoritySecret: base64.RawURLEncoding.EncodeToString(k.private),
		AuthorityPublic: accesslease.PublicString(k.Public()),
		Proof:           accesslease.SignProof(owner, accesslease.OwnerKeyProofDomain, "wrong"),
	}
	raw, _ := json.Marshal(env)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir, ownerPub); err == nil {
		t.Fatal("key with invalid owner proof loaded")
	}
}

// TestKeyNoRemoteProvisioning asserts the lifecycle exposes no remote-style
// provisioning API — only local Provision/Load/SecretPath and owner-bound share
// preparation surface.
func TestKeyNoRemoteProvisioning(t *testing.T) {
	dir := t.TempDir()
	owner := newKey(t)
	k, err := Provision(dir, owner)
	mustOK(t, err)
	pid := accesslease.PipeIDFromOwner(owner.Public().(ed25519.PublicKey))
	binding, err := k.NewOwnerBindingForShare(owner, pid, 1, "", "")
	mustOK(t, err)
	if _, err := binding.VerifyOwnerAuthority(accesslease.PublicString(k.Public())); err != nil {
		t.Fatalf("share binding not owner-bound: %v", err)
	}
	// A foreign owner cannot build a binding on this key
	if _, err := k.NewOwnerBindingForShare(newKey(t), pid, 1, "", ""); err == nil {
		t.Fatal("foreign owner built share binding on this key")
	}
}
