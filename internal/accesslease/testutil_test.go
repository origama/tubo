package accesslease_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

func fixNow() time.Time {
	return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func pubStr(priv ed25519.PrivateKey) string {
	return accesslease.PublicString(priv.Public().(ed25519.PublicKey))
}

func rawJSON(t *testing.T, encoded string) []byte {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mutateDoc(t *testing.T, encoded string, fn func(map[string]any)) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(rawJSON(t, encoded), &doc); err != nil {
		t.Fatalf("unmarshal doc: %v", err)
	}
	fn(doc)
	return encodeJSON(doc)
}

func encodeJSON(v any) string {
	rebuilt, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(rebuilt)
}

func mustReject[T any](t *testing.T, decode func(string) (T, error), enc string) {
	t.Helper()
	if _, err := decode(enc); err == nil {
		t.Fatalf("expected strict decode to reject")
	}
}

// makeOwner returns a valid owner key + its self-deriving PipeID and authority,
// plus the owner-signed binding for share preparation.
func makeOwner(t *testing.T) (ed25519.PrivateKey, ed25519.PublicKey, string, ed25519.PrivateKey, accesslease.OwnerBinding) {
	t.Helper()
	owner := newKey(t)
	auth := newKey(t)
	ownerPub := owner.Public().(ed25519.PublicKey)
	authPub := auth.Public().(ed25519.PublicKey)
	pipeID := accesslease.PipeIDFromOwner(ownerPub)
	binding, err := accesslease.NewOwnerBinding(owner, authPub, pipeID, 7, "relay-a.example:4001,relay-b.example:4001", "/tubo/red/1.0")
	if err != nil {
		t.Fatalf("new binding: %v", err)
	}
	return owner, ownerPub, pipeID, auth, binding
}

func mustVerifyOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("expected verification to pass: %v", err)
	}
}

func mustVerifyDeny(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected verification to fail closed")
	}
}
