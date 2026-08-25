package accesslease_test

import (
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

// resignPayload re-signs the canonical payload (signature omitted) under the
// given domain prefix.
func resignPayload(t *testing.T, signer ed25519.PrivateKey, domain string, canonical []byte) string {
	t.Helper()
	body := append([]byte(domain), canonical...)
	return accesslease.PublicString(ed25519.Sign(signer, body))
}

func siglessLease(lease accesslease.AccessLease) []byte {
	l := lease
	l.Signature = ""
	b, _ := json.Marshal(l)
	return b
}

func siglessInvite(i accesslease.ExactInvite) []byte {
	ii := i
	ii.Signature = ""
	b, _ := json.Marshal(ii)
	return b
}

// TestCrossDomainSigning enforces the distinct Ed25519 signing domains: a
// signature produced under another credential's domain prefix must never
// validate as this credential. Each Verify path pins its own domain, so
// re-signing under a foreign domain cannot be accepted.
func TestCrossDomainSigning(t *testing.T) {
	_, _, pid, auth, binding := makeOwner(t)
	subject := accesslease.PublicString(auth.Public().(ed25519.PublicKey))
	lease, err := accesslease.NewAccessLease(auth, subject, pid, fixNow(), time.Minute)
	mustVerifyOK(t, err)

	wrongDomains := []string{accesslease.OwnerBindingSigningDomain, accesslease.ExactInviteSigningDomain}
	for _, d := range wrongDomains {
		// lease re-signed under a foreign domain must fail lease.Verify
		l := lease
		l.Signature = resignPayload(t, auth, d, siglessLease(lease))
		enc, _ := accesslease.Encode(l)
		dec, de := accesslease.Decode(enc)
		mustVerifyOK(t, de)
		if err := dec.Verify(auth.Public().(ed25519.PublicKey), subject, pid, fixNow()); err == nil {
			t.Fatalf("lease accepted under foreign domain %q", d)
		}
	}

	// invite re-signed under the lease domain must fail invite.Verify
	inv, err := accesslease.NewExactInvite(auth, binding, pid, fixNow(), time.Minute)
	mustVerifyOK(t, err)
	enc2, _ := accesslease.EncodeInvite(inv)
	dec2, de2 := accesslease.DecodeInvite(enc2)
	mustVerifyOK(t, de2)
	inv2 := dec2
	inv2.Signature = resignPayload(t, auth, accesslease.AccessLeaseSigningDomain, siglessInvite(dec2))
	if err := inv2.Verify(fixNow(), pid); err == nil {
		t.Fatal("invite accepted under lease domain")
	}

	// binding signed under the lease domain fails the owner-signature check
	bind2 := binding
	bind2.Signature = resignPayload(t, auth, accesslease.AccessLeaseSigningDomain, func() []byte {
		bb := binding
		bb.Signature = ""
		b, _ := json.Marshal(bb)
		return b
	}())
	if _, err := bind2.VerifyOwnerAuthority(""); err == nil {
		t.Fatal("binding accepted under foreign lease domain")
	}
}
