package accesslease

import (
	"bytes"
	"crypto/ed25519"
	"strings"
	"testing"
	"time"
)

func genFixedKeys(t *testing.T) (ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	auth := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x5A}, 32))
	owner := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0xC3}, 32))
	return auth, owner
}

func goldNow() time.Time { return time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) }

func TestLeaseCanonicalBytesGolden(t *testing.T) {
	auth, owner := genFixedKeys(t)
	authPub := auth.Public().(ed25519.PublicKey)
	ownerPub := owner.Public().(ed25519.PublicKey)
	want := "tubo://access-lease/v1\x00{\"version\":1,\"type\":\"pipe.open.access\",\"issuer\":\"DXVQdU4IAKXSN-71gmA1dmubPloVhoqUCrKJlYeI47A\",\"subject\":\"1AS8RFZa7buJkVDlsLOzK5RBvwy3iEwzEw2o28J90s8\",\"audience_pipe_id\":\"pip_exact_golden\",\"jti\":\"jti_golden_fixed\",\"issued_at\":\"2030-01-02T03:04:05Z\",\"not_before\":\"2030-01-02T03:04:05Z\",\"expires_at\":\"2030-01-02T03:09:05Z\",\"access_epoch\":11}"
	lease := AccessLease{Version: Version, Type: AccessLeaseType, Issuer: PublicString(authPub), Subject: PublicString(ownerPub), AudiencePipeID: "pip_exact_golden", JTI: "jti_golden_fixed", IssuedAt: goldNow(), NotBefore: goldNow(), ExpiresAt: goldNow().Add(5 * time.Minute), AccessEpoch: 11}
	got, err := lease.signingBody()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("lease canonical mismatch:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(string(got), `"signature"`) {
		t.Fatal("signature field leaked into lease signing bytes")
	}
}

func TestOwnerBindingCanonicalBytesGolden(t *testing.T) {
	auth, owner := genFixedKeys(t)
	authPub := auth.Public().(ed25519.PublicKey)
	ownerPub := owner.Public().(ed25519.PublicKey)
	want := "tubo://exact-owner-binding/v1\x00{\"version\":1,\"type\":\"exact.owner.binding\",\"owner\":\"1AS8RFZa7buJkVDlsLOzK5RBvwy3iEwzEw2o28J90s8\",\"authority_key\":\"DXVQdU4IAKXSN-71gmA1dmubPloVhoqUCrKJlYeI47A\",\"pipe_id\":\"pip_exact_golden\",\"redemption_peers\":\"relay-a.example:4001,relay-b.example:4001\",\"redemption_protocol\":\"/tubo/red/1.0\",\"epoch\":9}"
	bind := OwnerBinding{Version: Version, Type: OwnerBindingType, Owner: PublicString(ownerPub), AuthorityKey: PublicString(authPub), PipeID: "pip_exact_golden", RedemptionPeers: "relay-a.example:4001,relay-b.example:4001", RedemptionProtocol: "/tubo/red/1.0", Epoch: 9}
	got, err := bind.signingBody()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("binding canonical mismatch:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(string(got), `"signature"`) {
		t.Fatal("signature field leaked into binding signing bytes")
	}
}

func TestExactInviteCanonicalBytesGolden(t *testing.T) {
	auth, _ := genFixedKeys(t)
	authPub := auth.Public().(ed25519.PublicKey)
	want := "tubo://exact-invite/v1\x00{\"version\":1,\"type\":\"pipe.open.invite\",\"issuer\":\"DXVQdU4IAKXSN-71gmA1dmubPloVhoqUCrKJlYeI47A\",\"audience_pipe_id\":\"pip_exact_golden\",\"binding\":\"b64-binding-ref-placeholder\",\"jti\":\"jti_golden_inv\",\"issued_at\":\"2030-01-02T03:04:05Z\",\"not_before\":\"2030-01-02T03:04:05Z\",\"expires_at\":\"2030-01-02T03:09:05Z\",\"access_epoch\":11,\"one_time\":true}"
	inv := ExactInvite{Version: Version, Type: InviteType, Issuer: PublicString(authPub), AudiencePipeID: "pip_exact_golden", Binding: "b64-binding-ref-placeholder", JTI: "jti_golden_inv", IssuedAt: goldNow(), NotBefore: goldNow(), ExpiresAt: goldNow().Add(5 * time.Minute), AccessEpoch: 11, OneTime: true}
	got, err := inv.signingBody()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("invite canonical mismatch:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(string(got), `"signature"`) {
		t.Fatal("signature field leaked into invite signing bytes")
	}
}
