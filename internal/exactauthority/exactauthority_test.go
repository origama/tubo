package exactauthority_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/exactauthority"
)

func TestExactAuthorityFirstProvisionReload(t *testing.T) {
	owner := newKey(t)
	pipeID := accesslease.PipeIDFromOwner(owner.Public().(ed25519.PublicKey))
	path := authorityPath(t, pipeID)

	workflow := exactauthority.LocalOwnerWorkflow{KeyPath: path, PipeID: pipeID, Epoch: 9, RedemptionPeers: "relay.example:4001", RedemptionProtocol: "/tubo/exact-redemption/1"}
	first, err := workflow.Provision(owner)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	loader := exactauthority.Loader{KeyPath: path, Binding: first.Binding}
	loaded, err := loader.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !bytes.Equal(first.Private, loaded.Private) {
		t.Fatal("loaded authority key differs")
	}
	if loaded.Owner.PipeID != pipeID || loaded.Owner.AuthorityKey != accesslease.PublicString(first.Private.Public().(ed25519.PublicKey)) {
		t.Fatalf("wrong owner binding: %#v", loaded.Owner)
	}
}

func TestOwnerAndExactAuthorityKeysAreDistinct(t *testing.T) {
	owner := newKey(t)
	pipeID := accesslease.PipeIDFromOwner(owner.Public().(ed25519.PublicKey))
	path := authorityPath(t, pipeID)
	auth, err := exactauthority.LocalOwnerWorkflow{KeyPath: path, PipeID: pipeID}.Provision(owner)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	if bytes.Equal(owner.Public().(ed25519.PublicKey), auth.Private.Public().(ed25519.PublicKey)) {
		t.Fatal("exact authority reused owner key")
	}
}

func TestOwnerKeyCannotServeAsExactAuthorityKey(t *testing.T) {
	owner := newKey(t)
	pipeID := accesslease.PipeIDFromOwner(owner.Public().(ed25519.PublicKey))
	binding, err := accesslease.NewOwnerBinding(owner, owner.Public().(ed25519.PublicKey), pipeID, 1, "", "")
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	path := authorityPath(t, pipeID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(accessleaseTestPrivateString(owner)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = exactauthority.Loader{KeyPath: path, Binding: binding}.Load()
	if err == nil {
		t.Fatal("owner key loaded as dedicated exact authority")
	}
}

func TestMismatchedKeyMaterialFailsClosed(t *testing.T) {
	owner := newKey(t)
	pipeID := accesslease.PipeIDFromOwner(owner.Public().(ed25519.PublicKey))
	goodPath := authorityPath(t, pipeID)
	first, err := exactauthority.LocalOwnerWorkflow{KeyPath: goodPath, PipeID: pipeID}.Provision(owner)
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	badPath := authorityPath(t, pipeID)
	otherOwner := newKey(t)
	otherPipeID := accesslease.PipeIDFromOwner(otherOwner.Public().(ed25519.PublicKey))
	otherPath := authorityPath(t, otherPipeID)
	other, err := exactauthority.LocalOwnerWorkflow{KeyPath: otherPath, PipeID: otherPipeID}.Provision(otherOwner)
	if err != nil {
		t.Fatalf("other provision: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(badPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte(accessleaseTestPrivateString(other.Private)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (exactauthority.Loader{KeyPath: badPath, Binding: first.Binding}).Load(); err == nil {
		t.Fatal("mismatched authority key loaded")
	}
}

func TestWrongOwnerCannotObtainOwnerBindingForPipe(t *testing.T) {
	owner := newKey(t)
	wrong := newKey(t)
	pipeID := accesslease.PipeIDFromOwner(owner.Public().(ed25519.PublicKey))
	path := authorityPath(t, pipeID)
	_, err := exactauthority.LocalOwnerWorkflow{KeyPath: path, PipeID: pipeID}.Provision(wrong)
	if err == nil {
		t.Fatal("wrong owner provisioned foreign Pipe authority")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("wrong owner created key path: %v", statErr)
	}
}

func TestReadOnlyLoaderExposesNoProvisioningCapability(t *testing.T) {
	var surface any = exactauthority.Loader{}
	if _, ok := surface.(interface {
		Provision(ed25519.PrivateKey) (exactauthority.Authority, error)
	}); ok {
		t.Fatal("read-only loader exposes Provision")
	}
	if _, ok := surface.(interface {
		Load() (exactauthority.Authority, error)
	}); !ok {
		t.Fatal("read-only loader lacks Load")
	}
}

func authorityPath(t *testing.T, pipeID string) string {
	t.Helper()
	path, err := exactauthority.KeyPath(filepath.Join(t.TempDir(), "secrets"), pipeID)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func accessleaseTestPrivateString(private ed25519.PrivateKey) string {
	return base64.RawURLEncoding.EncodeToString(private)
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private
}
