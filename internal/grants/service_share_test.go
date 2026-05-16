package grants

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	"golang.org/x/crypto/ssh"
)

func TestBuildServiceShareArtifactsSignsConnectOnlyToken(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := BuildServiceShareArtifacts(priv, "home", "cluster-123", "default", "myapi", "service-myapi", 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.Token == "" {
		t.Fatal("expected service share token")
	}
	if artifacts.Payload.ClusterName != "home" || artifacts.Payload.Namespace != "default" || artifacts.Payload.ServiceName != "myapi" {
		t.Fatalf("unexpected payload: %#v", artifacts.Payload)
	}
	if len(artifacts.Payload.Grant.Permissions) != 1 || artifacts.Payload.Grant.Permissions[0] != "connect" {
		t.Fatalf("grant is not connect-only: %#v", artifacts.Payload.Grant.Permissions)
	}
	parsed, err := ParseAndVerifyServiceShareToken(artifacts.Token)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ServiceID != artifacts.Payload.ServiceID || parsed.ClusterID != artifacts.Payload.ClusterID {
		t.Fatalf("parsed payload mismatch: %#v vs %#v", parsed, artifacts.Payload)
	}
}

func TestBuildServiceShareArtifactsClampsTTL(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := BuildServiceShareArtifacts(priv, "home", "cluster-123", "default", "myapi", "service-myapi", 72*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.Payload.ExpiresAt.Sub(artifacts.Payload.IssuedAt) > MaxServiceShareTTL+time.Second {
		t.Fatalf("ttl = %s, want <= %s", artifacts.Payload.ExpiresAt.Sub(artifacts.Payload.IssuedAt), MaxServiceShareTTL)
	}
}

func TestParseAndVerifyServiceShareTokenRejectsExpiredAndScopeMismatch(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	authKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	expired := ServiceSharePayload{
		ClusterName:        "home",
		ClusterID:          "cluster-123",
		AuthorityPublicKey: authKey,
		Namespace:          "default",
		NamespaceID:        "default",
		ServiceName:        "myapi",
		ServiceID:          "service-myapi",
		Grant: capability.ConnectCapability{
			ClusterID:     "cluster-123",
			NamespaceID:   "default",
			ServiceID:     "service-myapi",
			SubjectPeerID: "",
			Permissions:   []string{capability.PermissionConnect},
			ExpiresAt:     time.Now().UTC().Add(-time.Minute),
		},
		IssuedAt:  time.Now().UTC().Add(-2 * time.Minute),
		ExpiresAt: time.Now().UTC().Add(-time.Minute),
	}
	expiredToken, err := SignServiceShareToken(expired, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAndVerifyServiceShareToken(expiredToken); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired token error, got %v", err)
	}

	mismatch := expired
	mismatch.ExpiresAt = time.Now().UTC().Add(time.Hour)
	mismatch.Grant = capability.ConnectCapability{
		ClusterID:     "cluster-other",
		NamespaceID:   "default",
		ServiceID:     "service-myapi",
		SubjectPeerID: "",
		Permissions:   []string{capability.PermissionConnect},
		ExpiresAt:     mismatch.ExpiresAt,
	}
	signedGrant, err := capability.SignConnectCapability(mismatch.Grant, priv)
	if err != nil {
		t.Fatal(err)
	}
	mismatch.Grant = signedGrant
	mismatchToken, err := SignServiceShareToken(mismatch, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseAndVerifyServiceShareToken(mismatchToken); err == nil || !strings.Contains(err.Error(), "cluster id mismatch") {
		t.Fatalf("expected scope mismatch error, got %v", err)
	}
}
