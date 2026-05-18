package grants

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestConnectLeaseRedeemAndRefresh(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := BuildServiceShareArtifacts(priv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := testAuthorizedClientKey(t)
	artifacts, err := BuildConnectLeaseArtifacts(priv, invite.Payload, clientKey, 2*time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.AccessLease.ClientKeyThumbprint == "" || artifacts.RefreshLease.ClientKeyThumbprint == "" {
		t.Fatalf("missing key binding: %#v %#v", artifacts.AccessLease, artifacts.RefreshLease)
	}
	if err := VerifyConnectAccessLease(artifacts.AccessLease, pub, "cluster-123", "default", "svc-123"); err != nil {
		t.Fatalf("verify access lease: %v", err)
	}
	if err := VerifyConnectRefreshLease(artifacts.RefreshLease, pub, "cluster-123", "default", "svc-123"); err != nil {
		t.Fatalf("verify refresh lease: %v", err)
	}
	refreshed, err := RefreshConnectAccessLease(priv, artifacts.RefreshLease, 2*time.Second)
	if err != nil {
		t.Fatalf("refresh access lease: %v", err)
	}
	if refreshed.JTI == artifacts.AccessLease.JTI {
		t.Fatal("refresh should rotate access lease jti")
	}
	if refreshed.ExpiresAt.After(artifacts.RefreshLease.ExpiresAt) {
		t.Fatal("access lease exceeded refresh lease hard expiry")
	}
}

func TestConnectLeaseRejectsWrongClientKeyAndExpiredRefresh(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := BuildServiceShareArtifacts(priv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := BuildConnectLeaseArtifacts(priv, invite.Payload, testAuthorizedClientKey(t), time.Second, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	wrong := artifacts.AccessLease
	wrong.ClientPublicKey = testAuthorizedClientKey(t)
	if err := VerifyConnectAccessLease(wrong, pub, "cluster-123", "default", "svc-123"); err == nil || !strings.Contains(err.Error(), "thumbprint mismatch") {
		t.Fatalf("expected client thumbprint mismatch, got %v", err)
	}
	expiredRefresh, err := SignConnectRefreshLease(ConnectRefreshLease{
		JTI:             "cr_expired",
		SessionID:       "cs_expired",
		ShareInviteJTI:  invite.Payload.JTI,
		ClusterID:       invite.Payload.ClusterID,
		NamespaceID:     invite.Payload.NamespaceID,
		ServiceID:       invite.Payload.TargetServiceID,
		ClientPublicKey: testAuthorizedClientKey(t),
		Permissions:     []string{"connect"},
		IssuedAt:        time.Now().Add(-2 * time.Hour),
		ExpiresAt:       time.Now().Add(-time.Hour),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshConnectAccessLease(priv, expiredRefresh, time.Second); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired refresh rejection, got %v", err)
	}
}

func testAuthorizedClientKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}
