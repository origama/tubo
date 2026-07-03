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

func TestConnectLeaseArtifactsBoundToShareInviteExpiry(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := BuildServiceShareArtifacts(priv, "home", "cluster-123", "default", "myapi", "svc-123", 150*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := BuildConnectLeaseArtifacts(priv, invite.Payload, testAuthorizedClientKey(t), time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.RefreshLease.ExpiresAt.After(invite.Payload.ExpiresAt.Add(100 * time.Millisecond)) {
		t.Fatalf("refresh lease expiry = %s, want bounded by share invite expiry %s", artifacts.RefreshLease.ExpiresAt, invite.Payload.ExpiresAt)
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

func TestVerifyConnectMembershipCapabilityPeerIDMismatch(t *testing.T) {
	// This test reproduces the three-machine failure where oripi's connect
	// process has a different peer ID than the membership capability's SubjectPeerID.
	//
	// Root cause: connect process uses ephemeral peer ID (no node.seed configured)
	// but membership capability is bound to a specific SubjectPeerID.
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	// Membership capability signed for one peer ID
	capSubjectPeerID := "12D3KooWCapabilitySubjectPeerID"
	// But the connect process uses a different (ephemeral) peer ID
	requesterPeerID := "12D3KooWDifferentConnectProcessPeerID"
	clusterID := "cluster-test"
	namespaceID := "default"

	membership := capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   namespaceID,
		SubjectPeerID: capSubjectPeerID,
		Permissions:   []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	signed, err := capability.SignMembershipCapability(membership, priv)
	if err != nil {
		t.Fatal(err)
	}

	// Verification should fail because requesterPeerID != SubjectPeerID
	_, err = VerifyConnectMembershipCapability(signed, pub, clusterID, namespaceID, requesterPeerID, time.Time{})
	if err == nil {
		t.Fatal("expected verification to fail due to peer ID mismatch")
	}

	// The error message must clearly show all diagnostic information:
	// 1. capability.SubjectPeerID - what peer ID the capability is bound to
	// 2. requesterPeerID - what peer ID made the request
	// 3. clusterID - cluster-scoped subject that was also tried
	// 4. cluster/namespace context
	errMsg := err.Error()

	// Must contain capability's SubjectPeerID
	if !strings.Contains(errMsg, capSubjectPeerID) {
		t.Errorf("error should contain capability.SubjectPeerID %q, got: %s", capSubjectPeerID, errMsg)
	}
	// Must contain requester peer ID
	if !strings.Contains(errMsg, requesterPeerID) {
		t.Errorf("error should contain requester peer ID %q, got: %s", requesterPeerID, errMsg)
	}
	// Must contain cluster ID (tried as fallback subject)
	if !strings.Contains(errMsg, clusterID) {
		t.Errorf("error should contain cluster ID %q (tried as cluster-scoped subject), got: %s", clusterID, errMsg)
	}
	// Must NOT only show "want cluster-xxx" which was the misleading old error
	if strings.Contains(errMsg, "want \""+clusterID+"\"") && !strings.Contains(errMsg, "want \""+requesterPeerID+"\"") {
		t.Errorf("error should not only show 'want cluster-id' without showing requester peer ID, got: %s", errMsg)
	}

	// Verification should succeed when requesterPeerID matches SubjectPeerID
	_, err = VerifyConnectMembershipCapability(signed, pub, "cluster-test", "default", capSubjectPeerID, time.Time{})
	if err != nil {
		t.Fatalf("expected verification to succeed with matching peer ID, got: %v", err)
	}
}

func TestVerifyConnectMembershipCapabilityClusterScoped(t *testing.T) {
	// Cluster-scoped membership (SubjectPeerID == clusterID) should also work
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	clusterID := "cluster-test"
	membership := capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   "default",
		SubjectPeerID: clusterID, // cluster-scoped
		Permissions:   []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	signed, err := capability.SignMembershipCapability(membership, priv)
	if err != nil {
		t.Fatal(err)
	}

	// Any requesterPeerID should work with cluster-scoped membership
	requesterPeerID := "12D3KooWAnyPeerID"
	_, err = VerifyConnectMembershipCapability(signed, pub, clusterID, "default", requesterPeerID, time.Time{})
	if err != nil {
		t.Fatalf("cluster-scoped membership should authorize any peer, got: %v", err)
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
