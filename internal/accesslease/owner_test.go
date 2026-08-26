package accesslease_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

func mutateOwner(t *testing.T, enc string, fn func(map[string]any)) string {
	t.Helper()
	return mutateDoc(t, enc, fn)
}

// TestVerifyPipeOwnerSelfCertifying proves the PipeID is the owner's
// self-deriving identity and a non-owner (or wrong pipe) fails closed.
func TestVerifyPipeOwner(t *testing.T) {
	owner := newKey(t)
	ownerPub := owner.Public().(ed25519.PublicKey)
	pipeID := accesslease.PipeIDFromOwner(ownerPub)
	if !accesslease.VerifyPipeOwner(pipeID, ownerPub) {
		t.Fatal("owner must verify its self-derived pipe")
	}
	if accesslease.VerifyPipeOwner(pipeID, newKey(t).Public().(ed25519.PublicKey)) {
		t.Fatal("non-owner verified as pipe owner")
	}
	if accesslease.VerifyPipeOwner("pip_"+"zzzz", ownerPub) {
		t.Fatal("wrong pipe id verified")
	}
	if accesslease.VerifyPipeOwner("", ownerPub) {
		t.Fatal("empty pipe id verified")
	}
	if accesslease.VerifyPipeOwner(pipeID, ed25519.PublicKey{1, 2, 3}) {
		t.Fatal("malformed owner key verified")
	}
	if accesslease.PipeIDFromOwner(ownerPub) != pipeID {
		t.Fatal("pipe id not deterministic")
	}
}

func TestOwnerBindingValid(t *testing.T) {
	owner, _, pid, _, binding := makeOwner(t)
	ownerPub, err := binding.VerifyOwnerBinding()
	mustVerifyOK(t, err)
	if accesslease.PublicString(ownerPub) != accesslease.PublicString(owner.Public().(ed25519.PublicKey)) {
		t.Fatal("binding owner wrong")
	}
	oa, err := binding.VerifyOwnerAuthority(binding.AuthorityKey)
	mustVerifyOK(t, err)
	if oa.PipeID != pid || oa.Epoch != 7 {
		t.Fatalf("owner authority mismatch: %+v", oa)
	}
}

func TestOwnerBindingWrongSignerForeignPipe(t *testing.T) {
	owner := newKey(t)
	auth := newKey(t)
	otherOwnerPub := newKey(t).Public().(ed25519.PublicKey)
	foreignPipe := accesslease.PipeIDFromOwner(otherOwnerPub)
	if _, err := accesslease.NewOwnerBinding(owner, auth.Public().(ed25519.PublicKey), foreignPipe, 1, "", ""); err == nil {
		t.Fatal("owner forged a binding for a foreign pipe")
	}
}

// TestOwnerBindingTamperedPartProof covers tampered authority key, peers,
// protocol, pipe, and epoch after signing: the owner signature must no longer
// validate.
func TestOwnerBindingTamperedPartProof(t *testing.T) {
	_, _, _, auth, binding := makeOwner(t)
	enc, err := accesslease.EncodeOwnerBinding(binding)
	mustVerifyOK(t, err)
	authStr := accesslease.PublicString(auth.Public().(ed25519.PublicKey))

	cases := []struct {
		name string
		fn   func(d map[string]any)
	}{
		{"authority_key", func(d map[string]any) { d["authority_key"] = "AAAAAA" }},
		{"redemption_peers", func(d map[string]any) { d["redemption_peers"] = "evil.example.com:4444" }},
		{"redemption_protocol", func(d map[string]any) { d["redemption_protocol"] = "/malicious" }},
		{"pipe_id", func(d map[string]any) { d["pipe_id"] = "pip_other" }},
		{"epoch", func(d map[string]any) { d["epoch"] = 999 }},
		{"owner", func(d map[string]any) { d["owner"] = accesslease.PublicString(newKey(t).Public().(ed25519.PublicKey)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tampered := mutateOwner(t, enc, c.fn)
			dec, err := accesslease.DecodeOwnerBinding(tampered)
			mustVerifyOK(t, err)
			_, err = dec.VerifyOwnerAuthority(authStr)
			mustVerifyDeny(t, err)
			_, err = dec.VerifyOwnerAuthority("")
			mustVerifyDeny(t, err)
		})
	}
}

func TestOwnerBindingAuthorityMismatch(t *testing.T) {
	_, _, _, _, binding := makeOwner(t)
	unbound := newKey(t)
	_, err := binding.VerifyOwnerAuthority(accesslease.PublicString(unbound.Public().(ed25519.PublicKey)))
	mustVerifyDeny(t, err)
}

func TestOwnerBindingStrictDecode(t *testing.T) {
	_, _, _, _, binding := makeOwner(t)
	enc, err := accesslease.EncodeOwnerBinding(binding)
	mustVerifyOK(t, err)
	mustReject(t, accesslease.DecodeOwnerBinding, encodeJSON(map[string]any{"version": 1, "type": "exact.owner.binding", "nope": true}))
	mustReject(t, accesslease.DecodeOwnerBinding, mutateOwner(t, enc, func(d map[string]any) { d["service_target"] = map[string]any{"target": "x"} }))
	mustReject(t, accesslease.DecodeOwnerBinding, base64.RawURLEncoding.EncodeToString([]byte(`{"version":1,"version":2}`)))

	// over-length redemption_peers is rejected during strict decode (bounded
	// before typed unmarshal), not only at Verify.
	tampered := mutateOwner(t, enc, func(d map[string]any) {
		d["redemption_peers"] = strings.Repeat("a", accesslease.MaxRedemptionAddrLen+1)
	})
	mustReject(t, accesslease.DecodeOwnerBinding, tampered)
}

func TestOwnerBindingNoScopeNoMetadata(t *testing.T) {
	_, _, _, _, binding := makeOwner(t)
	enc, _ := accesslease.EncodeOwnerBinding(binding)
	raw := string(rawJSON(t, enc))
	for _, forbidden := range []string{`"scope"`, `"metadata"`, `"channel"`, `"role"`, `"bootstrap"`} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("binding carries forbidden %q", forbidden)
		}
	}
}

func TestOwnerBindingRedemptionBounds(t *testing.T) {
	owner := newKey(t)
	auth := newKey(t)
	pipe := accesslease.PipeIDFromOwner(owner.Public().(ed25519.PublicKey))
	tooMany := strings.Repeat("h:1,", 9)
	if _, err := accesslease.NewOwnerBinding(owner, auth.Public().(ed25519.PublicKey), pipe, 1, tooMany, ""); err == nil {
		t.Fatal("too many redemption peers accepted")
	}
}
