package accesslease_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

// TestPublicKeyRejectsNonCanonical proves PublicFromString fails closed on
// every non-canonical raw (unpadded) URL-safe base64url encoding of an
// Ed25519 public key: embedded CR/LF/CRLF, whitespace, padding, a wrong
// decoded length, malformed text, or alternate text that decodes to different
// bytes. Nothing is trimmed or canonicalized before acceptance.
func TestPublicKeyRejectsNoncanonical(t *testing.T) {
	priv := newKey(t)
	good := accesslease.PublicString(priv.Public().(ed25519.PublicKey))
	bad := map[string]string{
		"empty":         "",
		"leading-CR":    "\r" + good,
		"leading-LF":    "\n" + good,
		"mid-CRLF":      good[:len(good)/2] + "\r\n" + good[len(good)/2:],
		"mid-space":     good[:10] + " " + good[10:],
		"leading-tab":   "\t" + good,
		"padded-eq":     good + "=",
		"padded-double": good + "==",
		"too-short":     good[:8],
		"too-long":      good + "aaaa",
		"illegal-char":  "!" + good[1:],
	}
	for name, s := range bad {
		t.Run(name, func(t *testing.T) {
			pk, err := accesslease.PublicFromString(s)
			if err == nil && len(pk) == ed25519.PublicKeySize {
				t.Fatalf("accepted non-canonical public key %q", s)
			}
		})
	}
	// Deterministic case-sensitivity: a fixed all-uppercase canonical public-key
	// text (32 zero bytes) is changed by lowercasing, so the lowercased form is a
	// non-canonical alternate spelling and must be rejected regardless of the
	// random key content in the loop above.
	allUpper := base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.PublicKeySize))
	if lower, err := accesslease.PublicFromString(strings.ToLower(allUpper)); err == nil && len(lower) == ed25519.PublicKeySize {
		t.Fatal("lowercased alternate spelling accepted")
	}

	if _, err := accesslease.PublicFromString(good); err != nil {
		t.Fatalf("canonical public key rejected: %v", err)
	}
}

// TestLeaseOuterRejectsNoncanonical proves the outer AccessLease base64url body
// must be exactly canonical: CR/LF/CRLF/padding injected into the outer text is
// rejected even though Go's base64url decoder alone would silently skip newlines.
func TestLeaseOuterRejectsNoncanonical(t *testing.T) {
	priv := newKey(t)
	subject := pubStr(priv)
	lease, err := accesslease.NewAccessLease(priv, subject, "pip_exact_1", fixNow(), time.Minute)
	mustVerifyOK(t, err)
	enc, err := accesslease.Encode(lease)
	mustVerifyOK(t, err)
	if _, err := accesslease.Decode(enc); err != nil {
		t.Fatalf("canonical lease must decode: %v", err)
	}
	bad := map[string]string{
		"leading-LF":  "\n" + enc,
		"mid-CRLF":    enc[:len(enc)/2] + "\r\n" + enc[len(enc)/2:],
		"trailing-CR": enc + "\r",
		"padded":      enc + "=",
	}
	for name, s := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := accesslease.Decode(s); err == nil {
				t.Fatalf("accepted non-canonical outer lease: %q", s)
			}
		})
	}
}

// TestLeaseSignatureFieldRejectsNoncanonical proves a non-canonical signature
// field value (embedded CR, which survives as \r in the JSON string) is
// rejected at Verify before crypto, even though the outer body is canonical.
func TestLeaseSignatureFieldRejectsNoncanonical(t *testing.T) {
	priv := newKey(t)
	subject := pubStr(priv)
	lease, err := accesslease.NewAccessLease(priv, subject, "pip_exact_1", fixNow(), time.Minute)
	mustVerifyOK(t, err)
	enc, _ := accesslease.Encode(lease)
	tampered := mutateDoc(t, enc, func(d map[string]any) { d["signature"] = lease.Signature + "\r" })
	dec, err := accesslease.Decode(tampered)
	mustVerifyOK(t, err)
	err = dec.Verify(priv.Public().(ed25519.PublicKey), subject, "pip_exact_1", fixNow())
	mustVerifyDeny(t, err)
}

// TestInviteNestedBindingRejectsNoncanonical proves the embedded OwnerBinding
// reference must itself be canonical base64url before it is decoded (i.e.
// before any crypto), within invite Verify.
func TestInviteNestedBindingRejectsNoncanonical(t *testing.T) {
	_, _, pid, invite := makeInvite(t)
	enc, err := accesslease.EncodeInvite(invite)
	mustVerifyOK(t, err)
	if err := invite.Verify(fixNow(), pid); err != nil {
		t.Fatalf("initial invite must verify: %v", err)
	}
	if _, err := accesslease.DecodeInvite("\n" + enc); err == nil {
		t.Fatal("non-canonical invite outer accepted")
	}
	// embedded binding field with a non-canonical base64url reference survives
	// the outer scan and is rejected only when the binding is strictly decoded.
	tampered := mutateDoc(t, enc, func(d map[string]any) { d["binding"] = invite.Binding + "\n" })
	dec, err := accesslease.DecodeInvite(tampered)
	mustVerifyOK(t, err)
	if err := dec.Verify(fixNow(), pid); err == nil {
		t.Fatal("invite with non-canonical embedded binding accepted")
	}
}
