package capability

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"
)

func mustKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestMembershipCapabilitySignAndVerify(t *testing.T) {
	pub, priv := mustKeyPair(t)
	cap, err := SignMembershipCapability(MembershipCapability{
		ClusterID:     "cluster-a",
		NamespaceID:   "default",
		SubjectPeerID: "12D3KooWsubject",
		Permissions:   []string{PermissionConnect, PermissionPublish, PermissionList, PermissionSubscribe},
		ExpiresAt:     time.Now().Add(time.Hour),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyMembershipCapability(cap, pub, "cluster-a", "default", "12D3KooWsubject"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cap.Permissions, ","); got != "connect,list,publish,subscribe" {
		t.Fatalf("permissions = %q", got)
	}
}

func TestServiceClaimSignAndVerify(t *testing.T) {
	pub, priv := mustKeyPair(t)
	cap, err := SignServiceClaim(ServiceClaim{
		ClusterID:     "cluster-a",
		NamespaceID:   "default",
		ServiceID:     "svc-a",
		SubjectPeerID: "12D3KooWsubject",
		Permissions:   []string{PermissionAnnounce, PermissionAttach},
		ExpiresAt:     time.Now().Add(time.Hour),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyServiceClaim(cap, pub, "cluster-a", "default", "svc-a", "12D3KooWsubject"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cap.Permissions, ","); got != "announce,attach" {
		t.Fatalf("permissions = %q", got)
	}
}

func TestConnectCapabilitySignAndVerify(t *testing.T) {
	pub, priv := mustKeyPair(t)
	cap, err := SignConnectCapability(ConnectCapability{
		ClusterID:     "cluster-a",
		NamespaceID:   "default",
		ServiceID:     "svc-a",
		SubjectPeerID: "12D3KooWsubject",
		Permissions:   []string{PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyConnectCapability(cap, pub, "cluster-a", "default", "svc-a", "12D3KooWsubject"); err != nil {
		t.Fatal(err)
	}
}

func TestCapabilityFailures(t *testing.T) {
	pub, priv := mustKeyPair(t)
	otherPub, _ := mustKeyPair(t)

	membership := MembershipCapability{
		ClusterID:     "cluster-a",
		NamespaceID:   "default",
		SubjectPeerID: "12D3KooWsubject",
		Permissions:   []string{PermissionSubscribe, PermissionList, PermissionPublish, PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	serviceClaim := ServiceClaim{
		ClusterID:     "cluster-a",
		NamespaceID:   "default",
		ServiceID:     "svc-a",
		SubjectPeerID: "12D3KooWsubject",
		Permissions:   []string{PermissionAttach, PermissionAnnounce},
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	connectCap := ConnectCapability{
		ClusterID:     "cluster-a",
		NamespaceID:   "default",
		ServiceID:     "svc-a",
		SubjectPeerID: "12D3KooWsubject",
		Permissions:   []string{PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	}

	signedMembership, err := SignMembershipCapability(membership, priv)
	if err != nil {
		t.Fatal(err)
	}
	signedServiceClaim, err := SignServiceClaim(serviceClaim, priv)
	if err != nil {
		t.Fatal(err)
	}
	signedConnectCap, err := SignConnectCapability(connectCap, priv)
	if err != nil {
		t.Fatal(err)
	}

	if err := VerifyMembershipCapability(signedMembership, otherPub, "cluster-a", "default", "12D3KooWsubject"); err == nil || !strings.Contains(err.Error(), "invalid signature") {
		t.Fatalf("expected bad issuer error, got %v", err)
	}

	expiredMembership := signedMembership
	expiredMembership.ExpiresAt = time.Now().Add(-time.Minute)
	expiredMembership, err = SignMembershipCapability(expiredMembership, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyMembershipCapability(expiredMembership, pub, "cluster-a", "default", "12D3KooWsubject"); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired error, got %v", err)
	}

	wrongSubject := signedMembership
	if err := VerifyMembershipCapability(wrongSubject, pub, "cluster-a", "default", "other-subject"); err == nil || !strings.Contains(err.Error(), "subject peer id mismatch") {
		t.Fatalf("expected subject mismatch, got %v", err)
	}

	if err := VerifyServiceClaim(signedServiceClaim, pub, "cluster-x", "default", "svc-a", "12D3KooWsubject"); err == nil || !strings.Contains(err.Error(), "cluster id mismatch") {
		t.Fatalf("expected cluster mismatch, got %v", err)
	}
	if err := VerifyServiceClaim(signedServiceClaim, pub, "cluster-a", "default", "svc-x", "12D3KooWsubject"); err == nil || !strings.Contains(err.Error(), "service id mismatch") {
		t.Fatalf("expected service mismatch, got %v", err)
	}
	if err := VerifyConnectCapability(signedConnectCap, pub, "cluster-a", "default", "svc-a", "wrong-subject"); err == nil || !strings.Contains(err.Error(), "subject peer id mismatch") {
		t.Fatalf("expected connect subject mismatch, got %v", err)
	}

	missingPerms := signedConnectCap
	missingPerms.Permissions = nil
	missingPerms, err = SignConnectCapability(missingPerms, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyConnectCapability(missingPerms, pub, "cluster-a", "default", "svc-a", "12D3KooWsubject"); err == nil || !strings.Contains(err.Error(), "missing required permissions") {
		t.Fatalf("expected missing permission error, got %v", err)
	}
}
