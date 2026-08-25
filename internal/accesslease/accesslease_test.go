package accesslease_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

func TestLeaseGoldenCanonicalBytesStable(t *testing.T) {
	auth := newKey(t)
	lease, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_exact_1", fixNow(), time.Minute)
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
	lease, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute)
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
	lease, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute)
	mustVerifyOK(t, err)
	err = lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", fixNow())
	mustVerifyOK(t, err)
}

func TestLeaseWrongSubject(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(newKey(t)), "pip_example_1", fixNow())
	mustVerifyDeny(t, err)
}

func TestLeaseWrongPipeAudience(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_other", fixNow())
	mustVerifyDeny(t, err)
}

func TestLeaseWrongIssuer(t *testing.T) {
	auth := newKey(t)
	impostor := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute)
	err := lease.Verify(impostor.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", fixNow())
	mustVerifyDeny(t, err)
}

func TestLeaseTamperedSignature(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute)
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
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", now.Add(-time.Hour), time.Minute)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", now)
	mustVerifyDeny(t, err)
}

func TestLeaseFutureIssued(t *testing.T) {
	auth := newKey(t)
	now := fixNow()
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", now.Add(24*time.Hour), time.Minute)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", now)
	mustVerifyDeny(t, err)
}

func TestLeaseNotYetValid(t *testing.T) {
	auth := newKey(t)
	now := fixNow()
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", now, time.Minute)
	lease.NotBefore = now.Add(5 * time.Minute)
	lease.Sign(auth)
	err := lease.Verify(auth.Public().(ed25519.PublicKey), pubStr(auth), "pip_example_1", now)
	mustVerifyDeny(t, err)
}

func TestLeaseMaxTTLExceeded(t *testing.T) {
	auth := newKey(t)
	_, err := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Hour)
	mustVerifyDeny(t, err)
}

func TestLeaseStrictDecodeRejections(t *testing.T) {
	auth := newKey(t)
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute)
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
	lease, _ := accesslease.NewAccessLease(auth, pubStr(auth), "pip_example_1", fixNow(), time.Minute)
	enc, _ := accesslease.Encode(lease)
	for _, forbidden := range []string{`"scope"`, `"pipe_endpoint"`, `"role"`, `"namespace_key"`, `"authority_address"`, `"display_name_hint"`, `"metadata"`} {
		if strings.Contains(string(rawJSON(t, enc)), forbidden) {
			t.Fatalf("lease carries forbidden field %q", forbidden)
		}
	}
}
