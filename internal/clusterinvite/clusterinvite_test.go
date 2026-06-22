package clusterinvite

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cfgpkg "github.com/origama/tubo/internal/config"
	"golang.org/x/crypto/ssh"
)

func testInviteAuthority(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func testInviteAuthorityPub(t *testing.T, priv ed25519.PrivateKey) string {
	t.Helper()
	pubKey, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pubKey)))
}

func testInvitePayload(t *testing.T, priv ed25519.PrivateKey) Payload {
	t.Helper()
	secret, err := cfgpkg.GenerateSecretBytes(cfgpkg.NamespaceDiscoverySecretLength)
	if err != nil {
		t.Fatal(err)
	}
	return Payload{
		Version:            Version,
		Kind:               Kind,
		JTI:                "ci_test_123",
		ClusterName:        "home",
		ClusterID:          "cluster-123",
		AuthorityPublicKey: testInviteAuthorityPub(t, priv),
		Namespace:          "default",
		Discovery: &NamespaceDiscoveryEntry{
			Version:   "v1",
			Type:      cfgpkg.SecretTypeNamespaceDiscovery,
			KeyID:     "nsdk_20260602_abcd1234",
			Secret:    base64.RawURLEncoding.EncodeToString(secret),
			CreatedAt: time.Date(2026, 6, 2, 10, 0, 0, 0, time.UTC),
		},
		Grant:     Grant{Role: RoleMember, Permissions: []string{"subscribe", "list", "publish", "connect"}},
		IssuedAt:  time.Now().Add(-time.Minute).UTC(),
		ExpiresAt: time.Now().Add(time.Hour).UTC(),
	}
}

func TestParseAndVerifyTokenKeepsDiscoveryEntry(t *testing.T) {
	priv := testInviteAuthority(t)
	payload := testInvitePayload(t, priv)
	token, err := SignToken(payload, priv)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseAndVerifyToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if got.Discovery == nil || got.Discovery.KeyID != payload.Discovery.KeyID || got.Discovery.Secret != payload.Discovery.Secret {
		t.Fatalf("unexpected discovery entry after parse: %#v", got.Discovery)
	}
}

func TestParseAndVerifyTokenRejectsTamperedDiscoveryEntry(t *testing.T) {
	priv := testInviteAuthority(t)
	payload := testInvitePayload(t, priv)
	token, err := SignToken(payload, priv)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(strings.TrimPrefix(token, TokenPrefix), ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected token format: %q", token)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payloadBytes, &decoded); err != nil {
		t.Fatal(err)
	}
	discovery := decoded["discovery"].(map[string]any)
	discovery["key_id"] = "nsdk_tampered"
	payloadBytes, err = json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	tampered := TokenPrefix + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + parts[1]
	if _, err := ParseAndVerifyToken(tampered); err == nil || !strings.Contains(err.Error(), "invalid cluster invite signature") {
		t.Fatalf("expected tampered discovery entry signature error, got %v", err)
	}
}

func TestValidatePayloadRejectsMissingDiscoveryEntry(t *testing.T) {
	priv := testInviteAuthority(t)
	payload := testInvitePayload(t, priv)
	payload.Discovery = nil
	if err := ValidatePayload(payload); err == nil || !strings.Contains(err.Error(), "missing namespace discovery entry") {
		t.Fatalf("expected missing discovery entry error, got %v", err)
	}
}

func TestMembershipGrantPayloadFromInviteDropsDiscovery(t *testing.T) {
	priv := testInviteAuthority(t)
	invite := testInvitePayload(t, priv)
	membership, err := MembershipGrantPayloadFromInvite(invite)
	if err != nil {
		t.Fatal(err)
	}
	if membership.Kind != MembershipGrantKind {
		t.Fatalf("membership kind = %q", membership.Kind)
	}
	if membership.Discovery != nil {
		t.Fatalf("membership payload leaked discovery entry: %#v", membership.Discovery)
	}
	token, err := SignToken(membership, priv)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseAndVerifyMembershipGrantToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Discovery != nil {
		t.Fatalf("parsed membership payload leaked discovery entry: %#v", parsed.Discovery)
	}
}

func TestValidateMembershipGrantRejectsDiscoveryEntry(t *testing.T) {
	priv := testInviteAuthority(t)
	invite := testInvitePayload(t, priv)
	membership, err := MembershipGrantPayloadFromInvite(invite)
	if err != nil {
		t.Fatal(err)
	}
	membership.Discovery = invite.Discovery
	if err := ValidateMembershipGrantPayload(membership); err == nil || !strings.Contains(err.Error(), "must not contain namespace discovery entry") {
		t.Fatalf("expected membership discovery rejection, got %v", err)
	}
}

func TestVerifyMembershipGrantTokenForScopeAcceptsPermissionSuperset(t *testing.T) {
	priv := testInviteAuthority(t)
	payload := testInvitePayload(t, priv)
	payload.Grant = Grant{Role: RoleMember, Permissions: []string{"connect", "extra", "subscribe", "publish", "list"}}
	payload.MembershipToken = ""
	payload.Discovery = nil
	payload.Kind = MembershipGrantKind
	token, err := SignToken(payload, priv)
	if err != nil {
		t.Fatal(err)
	}
	verified, err := VerifyMembershipGrantTokenForScope(token, payload.ClusterID, payload.Namespace)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified.Grant.Permissions) != len(payload.Grant.Permissions) {
		t.Fatalf("unexpected permission count: %#v", verified.Grant.Permissions)
	}
}
