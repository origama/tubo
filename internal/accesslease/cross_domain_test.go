package accesslease

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func cdFixNow() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }

func cdNewKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

// resignCanonical re-signs a credential's canonical signature-less signing
// body under a foreign domain prefix and returns the base64url signature. The
// canonical signing bytes are produced by the same signingBody path the
// verifier recomputes, so any acceptance would prove the domain prefix is not
// actually pinning the signature.
func resignCanonical(t *testing.T, signer ed25519.PrivateKey, foreignDomain string, body []byte) string {
	t.Helper()
	canonical := append([]byte(foreignDomain), body...)
	return PublicString(ed25519.Sign(signer, canonical))
}

// TestCrossDomainSigning enforces the distinct NUL-delimited Ed25519 signing// TestCrossDomainSigning enforces the distinct NUL-delimited Ed25519 signing
// domains: a signature produced under another credential's domain prefix must
// never validate as this credential. Each Verify path pins its own domain;
// re-signing the EXACT canonical signature-less bytes under a foreign domain
// cannot be accepted.
func TestCrossDomainSigning(t *testing.T) {
	owner := cdNewKey(t)
	auth := cdNewKey(t)
	authPub := auth.Public().(ed25519.PublicKey)
	subject := PublicString(authPub)
	pid := PipeIDFromOwner(owner.Public().(ed25519.PublicKey))
	binding, err := NewOwnerBinding(owner, authPub, pid, 7, "", "")
	if err != nil {
		t.Fatal(err)
	}
	lease, err := NewAccessLease(auth, subject, pid, cdFixNow(), time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := NewExactInvite(auth, binding, pid, cdFixNow(), time.Minute, 0)
	if err != nil {
		t.Fatal(err)
	}

	for _, falseDomain := range []string{OwnerBindingSigningDomain, ExactInviteSigningDomain} {
		l := lease
		body, lerr := l.signingBody()
		if lerr != nil {
			t.Fatal(lerr)
		}
		l.Signature = resignCanonical(t, auth, falseDomain, body)
		if err := l.Verify(authPub, subject, pid, cdFixNow()); err == nil {
			t.Fatalf("lease accepted under foreign domain %q", falseDomain)
		}
	}

	// invite signed under the lease domain must fail invite.Verify
	iv := invite
	ib, ierr := iv.signingBody()
	if ierr != nil {
		t.Fatal(ierr)
	}
	iv.Signature = resignCanonical(t, auth, AccessLeaseSigningDomain, ib)
	if err := iv.Verify(cdFixNow(), pid); err == nil {
		t.Fatal("invite accepted under lease domain")
	}

	// binding signed under the lease domain fails the owner-signature check
	bb := binding
	bbody, berr := bb.signingBody()
	if berr != nil {
		t.Fatal(berr)
	}
	bb.Signature = resignCanonical(t, owner, AccessLeaseSigningDomain, bbody)
	if _, err := bb.VerifyOwnerAuthority(""); err == nil {
		t.Fatal("binding accepted under foreign lease domain")
	}
}
