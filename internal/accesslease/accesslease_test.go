package accesslease_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/accesslease"
)

// TestLeaseSubjectMustBeCanonicalPubKey proves the AccessLease Subject is
// constrained to a canonical Ed25519 public key at construction and at
// verification; arbitrary strings are denied.
func TestLeaseSubjectMustBeCanonicalPubKey(t *testing.T) {
	auth := newKey(t)
	if _, err := accesslease.NewAccessLease(auth, "not-a-public-key", "pip_example_1", fixNow(), time.Minute, 0); err == nil {
		t.Fatal("construction accepted a non-public-key subject")
	}
	// a base64 string of the wrong decoded length is also rejected
	if _, err := accesslease.NewAccessLease(auth, strings.Repeat("A", 20), "pip_example_1", fixNow(), time.Minute, 0); err == nil {
		t.Fatal("construction accepted a wrong-length subject")
	}
	// Verify likewise rejects a non-canonical subject even when the signature
	// would otherwise be valid.
	lease, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	mustVerifyOK(t, err)
	enc, _ := accesslease.Encode(lease)
	// replace the subject with an arbitrary (short) string that survives the
	// structural scan but fails the canonical-key check at Verify.
	tampered := mutateDoc(t, enc, func(d map[string]any) { d["subject"] = "definitely-not-a-key" })
	dec, err := accesslease.Decode(tampered)
	mustVerifyOK(t, err)
	err = dec.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", fixNow())
	mustVerifyDeny(t, err)
}

// TestLeaseStrictDecodePerFieldBounds proves overlong string fields are
// rejected during strict decode (before typed unmarshal), not only at Verify.
func TestLeaseStrictDecodePerFieldBounds(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	enc, _ := accesslease.Encode(lease)
	// audience_pipe_id beyond MaxPipeIDLen
	tampered := mutateDoc(t, enc, func(d map[string]any) {
		d["audience_pipe_id"] = strings.Repeat("p", accesslease.MaxPipeIDLen+1)
	})
	mustReject(t, accesslease.Decode, tampered)
	// subject beyond MaxFieldLen
	tol2 := mutateDoc(t, enc, func(d map[string]any) {
		d["subject"] = strings.Repeat("s", accesslease.MaxFieldLen+1)
	})
	mustReject(t, accesslease.Decode, tol2)
}

func TestLeaseGoldenCanonicalBytesStable(t *testing.T) {
	auth := newKey(t)
	lease, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_exact_1", fixNow(), time.Minute, 0)
	mustVerifyOK(t, err)
	enc, err := accesslease.Encode(lease)
	mustVerifyOK(t, err)
	dec, err := accesslease.Decode(enc)
	mustVerifyOK(t, err)
	reEnc, err := accesslease.Encode(dec)
	mustVerifyOK(t, err)
	if reEnc != enc {
		t.Fatalf("canonical round-trip not stable")
	}
	if !strings.Contains(string(rawJSON(t, enc)), `"audience_pipe_id":`) {
		t.Fatal("canonical bytes missing audience_pipe_id")
	}
}

func TestLeaseRoundTrip(t *testing.T) {
	auth := newKey(t)
	lease, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	mustVerifyOK(t, err)
	enc, err := accesslease.Encode(lease)
	mustVerifyOK(t, err)
	dec, err := accesslease.Decode(enc)
	mustVerifyOK(t, err)
	if dec.JTI != lease.JTI || dec.Subject != lease.Subject || dec.AudiencePipeID != "pip_example_1" {
		t.Fatalf("round trip mismatch")
	}
}

func TestLeaseVerifyOK(t *testing.T) {
	auth := newKey(t)
	lease, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	mustVerifyOK(t, err)
	err = lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", fixNow())
	mustVerifyOK(t, err)
}

func TestLeaseWrongSubject(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(newKey(t)), "pip_example_1", fixNow())
	mustVerifyDeny(t, err)
}

func TestLeaseWrongPipeAudience(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_other", fixNow())
	mustVerifyDeny(t, err)
}

func TestLeaseWrongIssuer(t *testing.T) {
	auth := newKey(t)
	impostor := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	err := lease.Verify(impostor.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", fixNow())
	mustVerifyDeny(t, err)
}

func TestLeaseTamperedSignature(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	enc, _ := accesslease.Encode(lease)
	tampered := mutateDoc(t, enc, func(d map[string]any) { d["signature"] = strings.Repeat("A", 86) })
	dec, err := accesslease.Decode(tampered)
	mustVerifyOK(t, err)
	err = dec.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", fixNow())
	mustVerifyDeny(t, err)
}

func TestLeaseExpired(t *testing.T) {
	auth := newKey(t)
	now := fixNow()
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", now.Add(-time.Hour), time.Minute, 0)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", now)
	mustVerifyDeny(t, err)
}

func TestLeaseFutureIssued(t *testing.T) {
	auth := newKey(t)
	now := fixNow()
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", now.Add(24*time.Hour), time.Minute, 0)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", now)
	mustVerifyDeny(t, err)
}

func TestLeaseNotYetValid(t *testing.T) {
	auth := newKey(t)
	now := fixNow()
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", now, time.Minute, 0)
	lease.NotBefore = now.Add(5 * time.Minute)
	lease.Sign(auth)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", now)
	mustVerifyDeny(t, err)
}

func TestLeaseMaxTTLExceeded(t *testing.T) {
	auth := newKey(t)
	_, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Hour, 0)
	mustVerifyDeny(t, err)
}

func TestLeaseStrictDecodeRejections(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	enc, _ := accesslease.Encode(lease)

	// unknown field
	mustReject(t, accesslease.Decode, encodeJSON(map[string]any{"version": 1, "type": "pipe.open.access", "unknown": true}))
	// duplicate key — JSON with repeated "version" rejected by scan
	mustReject(t, accesslease.Decode, base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"version":2}`)))
	// nested object (forbidden Scope/metadata)
	mustReject(t, accesslease.Decode, mutateDoc(t, enc, func(d map[string]any) { d["scope"] = map[string]any{"cluster": "x"} }))
	// nested array (e.g. pipe_endpoint addresses)
	mustReject(t, accesslease.Decode, mutateDoc(t, enc, func(d map[string]any) { d["metadata"] = []string{"a"} }))
	// trailing payload
	mustReject(t, accesslease.Decode, base64.RawURLEncoding.EncodeToString(append(rawJSON(t, enc), []byte(" } ")...)))
	// malformed base64
	mustReject(t, accesslease.Decode, "!!!not-base64!!!")
	// oversized encoded field
	mustReject(t, accesslease.Decode, strings.Repeat("A", accesslease.MaxBytes+1))
}

// TestLeaseNoScopeNoMetadata asserts the canonical lease carries no Scope,
// endpoint, route, target, discovery, namespace, or metadata map.
func TestLeaseNoScopeNoMetadata(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute, 0)
	enc, _ := accesslease.Encode(lease)
	for _, forbidden := range []string{`"scope"`, `"pipe_endpoint"`, `"role"`, `"namespace_key"`, `"authority_address"`, `"display_name_hint"`, `"metadata"`} {
		if strings.Contains(string(rawJSON(t, enc)), forbidden) {
			t.Fatalf("lease carries forbidden field %q", forbidden)
		}
	}
}

// TestLeaseNotBeforeSkewTolerance proves the bounded clock-skew semantic:
// a lease minted by an issuer whose clock is up to MaxClockSkew ahead verifies
// locally, but strict expiry (ExpiresAt) is never extended by skew.
func TestLeaseNotBeforeSkewTolerance(t *testing.T) {
	auth := newKey(t)
	subject := pubStr(auth)
	issuerNow := fixNow() // issuer clock; client clock is skewed behind

	// Client clock 25s behind issuer: NotBefore = issuerNow is 25s in the
	// client's future, inside MaxClockSkew (30s) -> valid.
	clientBack := issuerNow
	lease, _ := accesslease.NewAccessLease(auth, subject, "pip_example_1", issuerNow.Add(25*time.Second), time.Minute, 0)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), subject, "pip_example_1", clientBack)
	mustVerifyOK(t, err)

	// Boundary sanity at the exact skew edges:
	// +5s and +29s inside tolerance verify; +31s (outside MaxClockSkew) rejects
	// with "not yet valid".
	lease5, _ := accesslease.NewAccessLease(auth, subject, "pip_example_1", fixNow(), time.Minute, 0)
	lease5.NotBefore = fixNow().Add(5 * time.Second)
	lease5.Sign(auth)
	err = lease5.Verify(auth.Public().(ed25519.PublicKey), subject, "pip_example_1", fixNow())
	mustVerifyOK(t, err)

	lease29, _ := accesslease.NewAccessLease(auth, subject, "pip_example_1", fixNow(), time.Minute, 0)
	lease29.NotBefore = fixNow().Add(29 * time.Second)
	lease29.Sign(auth)
	err = lease29.Verify(auth.Public().(ed25519.PublicKey), subject, "pip_example_1", fixNow())
	mustVerifyOK(t, err)

	lease31, _ := accesslease.NewAccessLease(auth, subject, "pip_example_1", fixNow(), time.Minute, 0)
	lease31.NotBefore = fixNow().Add(31 * time.Second)
	lease31.Sign(auth)
	err = lease31.Verify(auth.Public().(ed25519.PublicKey), subject, "pip_example_1", fixNow())
	mustVerifyDeny(t, err)

	// Expiry stays STRICT even inside the skew window: 5s past expiry fails.
	leaseExp, _ := accesslease.NewAccessLease(auth, subject, "pip_example_1", fixNow(), time.Minute, 0)
	leaseExp.ExpiresAt = fixNow().Add(-5 * time.Second)
	leaseExp.Sign(auth)
	err = leaseExp.Verify(auth.Public().(ed25519.PublicKey), subject, "pip_example_1", fixNow())
	mustVerifyDeny(t, err)
}
