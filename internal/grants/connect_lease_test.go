package grants

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/serviceidentity"
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

func TestDelegatedConnectLeaseVerifyAndRefresh(t *testing.T) {
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	servicePeerID := "12D3KooWDelegatedServicePeer"
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             owner.ServiceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:       servicePeerID,
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "delegated-connect-lease",
	}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := BuildPublishLeaseArtifacts(authPriv, req, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := BuildDelegatedConnectLeaseArtifacts(authPub, owner.PrivateKey, artifacts.Lease, "", testAuthorizedClientKey(t), 0, 2*time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyDelegatedConnectAccessLease(leases.AccessLease, authPub, "cluster-123", "default", owner.ServiceID, servicePeerID); err != nil {
		t.Fatalf("verify delegated access lease: %v", err)
	}
	if err := VerifyDelegatedConnectRefreshLease(leases.RefreshLease, authPub, "cluster-123", "default", owner.ServiceID, servicePeerID); err != nil {
		t.Fatalf("verify delegated refresh lease: %v", err)
	}
	refreshed, err := RefreshDelegatedConnectAccessLease(authPub, owner.PrivateKey, leases.RefreshLease, 2*time.Second, servicePeerID)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.JTI == leases.AccessLease.JTI {
		t.Fatal("delegated refresh should rotate access lease jti")
	}
}

func TestDelegatedConnectLeaseRejectsMissingDelegationAndScopeMismatch(t *testing.T) {
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	servicePeerID := "12D3KooWDelegatedServicePeer"
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             owner.ServiceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:       servicePeerID,
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "delegated-connect-lease-reject",
	}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := BuildPublishLeaseArtifacts(authPriv, req, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := BuildDelegatedConnectLeaseArtifacts(authPub, owner.PrivateKey, artifacts.Lease, "", testAuthorizedClientKey(t), 0, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	missingDelegation := leases.AccessLease
	missingDelegation.DelegationPublishLease = nil
	if err := VerifyDelegatedConnectAccessLease(missingDelegation, authPub, "cluster-123", "default", owner.ServiceID, servicePeerID); err == nil || (!strings.Contains(err.Error(), "delegation") && !strings.Contains(err.Error(), "signature")) {
		t.Fatalf("expected missing delegation rejection, got %v", err)
	}
	if err := VerifyDelegatedConnectAccessLease(leases.AccessLease, authPub, "cluster-123", "other", owner.ServiceID, servicePeerID); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("expected namespace mismatch, got %v", err)
	}
	if err := VerifyDelegatedConnectAccessLease(leases.AccessLease, authPub, "cluster-123", "default", "service-wrong", servicePeerID); err == nil || !strings.Contains(err.Error(), "service") {
		t.Fatalf("expected service mismatch, got %v", err)
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
