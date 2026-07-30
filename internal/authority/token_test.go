package authority

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

func TestTokenSignatureAndBindings(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	token, err := NewToken("connect.access", scope, "client-key", "pip_exact", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := token.Sign(private); err != nil {
		t.Fatal(err)
	}
	encoded, err := Encode(token)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Verify(private.Public().(ed25519.PublicKey), time.Now()); err != nil {
		t.Fatal(err)
	}
	decoded.Audience = "pip_other"
	if err := decoded.Verify(private.Public().(ed25519.PublicKey), time.Now()); err == nil {
		t.Fatal("tampered audience verified")
	}
}

func TestShareInviteAndRefreshBindings(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	share, err := NewToken("share.invite", scope, "issuer-client", "pip_exact", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	share.OneTime = true
	share.PipeEndpoint = &PipeEndpoint{PeerID: "12D3peer", Addresses: []string{"/ip4/127.0.0.1/tcp/4001"}, Protocol: "/tubo/tunnel/2.0"}
	if err := share.Sign(private); err != nil {
		t.Fatal(err)
	}
	if err := share.Verify(public, time.Now()); err != nil {
		t.Fatal(err)
	}
	share.PipeEndpoint = nil
	if err := share.Verify(public, time.Now()); err == nil {
		t.Fatal("invite without endpoint verified")
	}
	refresh, err := NewToken("connect.refresh", scope, "client", "pip_exact", RefreshLeaseTTL)
	if err != nil {
		t.Fatal(err)
	}
	if refresh.ExpiresAt.Sub(refresh.IssuedAt) != RefreshLeaseTTL {
		t.Fatal("wrong refresh ttl")
	}
}

func TestProofRequiresPrivateKey(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	proof := SignProof(private, "tubo-open-v2", "challenge", "pip_exact")
	if !VerifyProof(public, proof, "tubo-open-v2", "challenge", "pip_exact") {
		t.Fatal("valid proof rejected")
	}
	if VerifyProof(public, proof, "tubo-open-v2", "other", "pip_exact") {
		t.Fatal("proof replayed for another challenge")
	}
}
