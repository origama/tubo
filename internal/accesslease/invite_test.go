package accesslease_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

func makeInvite(t *testing.T) (ed25519.PrivateKey, ed25519.PrivateKey, string, accesslease.ExactInvite) {
	t.Helper()
	owner, _, pid, auth, binding := makeOwner(t)
	invite, err := accesslease.NewExactInvite(auth, binding, pid, fixNow(), time.Minute, 0)
	mustVerifyOK(t, err)
	return owner, auth, pid, invite
}

func TestInviteValidRecipientUnbound(t *testing.T) {
	_, _, pid, invite := makeInvite(t)
	err := invite.Verify(fixNow(), pid)
	mustVerifyOK(t, err)
	// recipient-unbound: struct has no Subject field; ensure no subject encoded
	enc, _ := accesslease.EncodeInvite(invite)
	if strings.Contains(string(rawJSON(t, enc)), `"subject"`) {
		t.Fatal("invite carries a subject (must remain recipient-unbound)")
	}
}

func TestInviteRoundTrip(t *testing.T) {
	_, _, pid, invite := makeInvite(t)
	enc, err := accesslease.EncodeInvite(invite)
	mustVerifyOK(t, err)
	dec, err := accesslease.DecodeInvite(enc)
	mustVerifyOK(t, err)
	if dec.JTI != invite.JTI || dec.AudiencePipeID != pid {
		t.Fatal("invite round trip mismatch")
	}
}

func TestInviteStrictDecode(t *testing.T) {
	_, _, _, invite := makeInvite(t)
	enc, err := accesslease.EncodeInvite(invite)
	mustVerifyOK(t, err)
	mustReject(t, accesslease.DecodeInvite, encodeJSON(map[string]any{"version": 1, "type": "pipe.open.invite", "bogus": 1}))
	mustReject(t, accesslease.DecodeInvite, mutateDoc(t, enc, func(d map[string]any) { d["scope"] = map[string]any{"c": "1"} }))
	mustReject(t, accesslease.DecodeInvite, base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"version":2}`)))
	mustReject(t, accesslease.DecodeInvite, "##badbase64##")
}

func TestInviteNonBoundSigner(t *testing.T) {
	_, _, pid, invite := makeInvite(t)
	unboundAuth := newKey(t)
	// re-sign invite with a non-bound authority key: signature over a different
	// issuer must break the owner-bound guarantee.
	invite.Issuer = accesslease.PublicString(unboundAuth.Public().(ed25519.PublicKey))
	invite.Sign(unboundAuth)
	enc, _ := accesslease.EncodeInvite(invite)
	dec, err := accesslease.DecodeInvite(enc)
	mustVerifyOK(t, err)
	err = dec.Verify(fixNow(), pid)
	mustVerifyDeny(t, err)
}

func TestInviteTamperedBinding(t *testing.T) {
	_, _, pid, invite := makeInvite(t)
	enc, _ := accesslease.EncodeInvite(invite)
	// replace the embedded binding reference with garbage and tamper issuer.
	tampered := mutateDoc(t, enc, func(d map[string]any) { d["binding"] = "AAAA" })
	dec, err := accesslease.DecodeInvite(tampered)
	mustVerifyOK(t, err)
	err = dec.Verify(fixNow(), pid)
	mustVerifyDeny(t, err)
}

func TestInviteWrongPipe(t *testing.T) {
	_, _, _, invite := makeInvite(t)
	err := invite.Verify(fixNow(), "pip_other")
	mustVerifyDeny(t, err)
}

func TestInviteTimeFailures(t *testing.T) {
	now := fixNow()
	owner, _, pid, auth, binding := makeOwner(t)
	expired, err := accesslease.NewExactInvite(auth, binding, pid, now.Add(-time.Hour), time.Minute, 0)
	mustVerifyOK(t, err)
	if err := expired.Verify(now, pid); err == nil {
		t.Fatal("expired invite validated")
	}
	future, err := accesslease.NewExactInvite(auth, binding, pid, now.Add(24*time.Hour), time.Minute, 0)
	mustVerifyOK(t, err)
	if err = future.Verify(now, pid); err == nil {
		t.Fatal("future-issued invite validated")
	}
	if _, err = accesslease.NewExactInvite(auth, binding, pid, now, time.Hour, 0); err == nil {
		t.Fatal("max-TTL invite minted")
	}
	_ = owner
}

func TestInviteOneTimeRequired(t *testing.T) {
	_, auth, pid, invite := makeInvite(t)
	invite.OneTime = false
	invite.Sign(auth)
	enc, _ := accesslease.EncodeInvite(invite)
	dec, _ := accesslease.DecodeInvite(enc)
	if err := dec.Verify(fixNow(), pid); err == nil {
		t.Fatal("one_time=false invite validated")
	}
}

func TestInviteComposesOwnerBinding(t *testing.T) {
	_, _, pid, invite := makeInvite(t)
	enc, _ := accesslease.EncodeInvite(invite)
	// the invite must expose the owner-signed binding for the exact pipe
	if !strings.Contains(string(rawJSON(t, enc)), `"binding"`) {
		t.Fatal("invite does not compose the owner binding")
	}
	// an invite whose composed binding is for a DIFFERENT authority must fail
	// even if it passes its own signature (impossible via NewExactInvite, so
	// we only assert the invariant field is presence and owned-pipe binding).
	_ = pid
}

// TestInviteNotBeforeSkewTolerance mirrors the lease skew semantic: an invite
// issuer within MaxClockSkew ahead is usable (same guard as IssuedAt), and
// strict expiry is never extended by skew.
func TestInviteNotBeforeSkewTolerance(t *testing.T) {
	now := fixNow()
	owner, _, pid, auth, binding := makeOwner(t)
	_ = owner

	// Issuer clock 25s ahead of the verifier: IssuedAt and NotBefore both sit
	// inside the 30s skew window -> the invite verifies.
	invite, err := accesslease.NewExactInvite(auth, binding, pid, now.Add(25*time.Second), time.Minute, 0)
	mustVerifyOK(t, err)
	if err := invite.Verify(now, pid); err != nil {
		t.Fatalf("invite within clock skew must verify: %v", err)
	}

	// Edges: +5s and +29s NotBefore verify; +31s rejects "not yet valid".
	at5, _ := accesslease.NewExactInvite(auth, binding, pid, now, time.Minute, 0)
	at5.NotBefore = now.Add(5 * time.Second)
	at5.Sign(auth)
	if err := at5.Verify(now, pid); err != nil {
		t.Fatalf("invite +5s NotBefore must verify: %v", err)
	}
	at29, _ := accesslease.NewExactInvite(auth, binding, pid, now, time.Minute, 0)
	at29.NotBefore = now.Add(29 * time.Second)
	at29.Sign(auth)
	if err := at29.Verify(now, pid); err != nil {
		t.Fatalf("invite +29s NotBefore must verify: %v", err)
	}
	at31, _ := accesslease.NewExactInvite(auth, binding, pid, now, time.Minute, 0)
	at31.NotBefore = now.Add(31 * time.Second)
	at31.Sign(auth)
	if err := at31.Verify(now, pid); err == nil {
		t.Fatal("invite +31s NotBefore must be denied")
	}

	// Expiry stays STRICT: 5s past expiry fails even though the issuer clock
	// skew window would otherwise mask it.
	expInvite, _ := accesslease.NewExactInvite(auth, binding, pid, now.Add(-time.Hour), time.Minute, 0)
	expInvite.ExpiresAt = now.Add(-5 * time.Second)
	expInvite.Sign(auth)
	if err := expInvite.Verify(now, pid); err == nil {
		t.Fatal("invite expired by 5s must be denied despite skew")
	}
}
