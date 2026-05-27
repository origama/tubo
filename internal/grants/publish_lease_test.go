package grants

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/serviceidentity"
)

func TestPublishLeaseRequestSignsAndVerifies(t *testing.T) {
	priv, pub := testOwnerKey("lease-request-a")
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceidentity.ServiceIDFromPublicKey(pub),
		ServicePublicKey:      serviceidentity.EncodePublicKey(pub),
		PublisherPeerID:       "12D3-service-a",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		Nonce:                 "nonce-a",
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPublishLeaseRequest(req); err != nil {
		t.Fatal(err)
	}
}

func TestPublishLeaseRequestRejectsMissingProofAndBadSignature(t *testing.T) {
	priv, pub := testOwnerKey("lease-request-b")
	good, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceidentity.ServiceIDFromPublicKey(pub),
		ServicePublicKey:      serviceidentity.EncodePublicKey(pub),
		PublisherPeerID:       "12D3-service-b",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		Nonce:                 "nonce-b",
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.ServiceOwnerSignature = nil
	if err := VerifyPublishLeaseRequest(bad); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature error, got %v", err)
	}
	bad = good
	_, otherPub := testOwnerKey("lease-request-other")
	bad.ServiceID = serviceidentity.ServiceIDFromPublicKey(otherPub)
	if err := VerifyPublishLeaseRequest(bad); err == nil || !strings.Contains(err.Error(), "service id mismatch") {
		t.Fatalf("expected service id mismatch, got %v", err)
	}
}

func TestBuildPublishLeaseArtifactsIssuesDistinctLeasesForDistinctServiceIDs(t *testing.T) {
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = authorityPub

	privA, pubA := testOwnerKey("lease-a")
	privB, pubB := testOwnerKey("lease-b")
	reqA, err := SignPublishLeaseRequest(PublishLeaseRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: serviceidentity.ServiceIDFromPublicKey(pubA), ServicePublicKey: serviceidentity.EncodePublicKey(pubA), PublisherPeerID: "12D3-peer-a", RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce}, Nonce: "nonce-a"}, privA)
	if err != nil {
		t.Fatal(err)
	}
	reqB, err := SignPublishLeaseRequest(PublishLeaseRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: serviceidentity.ServiceIDFromPublicKey(pubB), ServicePublicKey: serviceidentity.EncodePublicKey(pubB), PublisherPeerID: "12D3-peer-b", RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce}, Nonce: "nonce-b"}, privB)
	if err != nil {
		t.Fatal(err)
	}

	leaseA, err := BuildPublishLeaseArtifacts(authorityPriv, reqA, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaseB, err := BuildPublishLeaseArtifacts(authorityPriv, reqB, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if leaseA.Lease.ServiceID == leaseB.Lease.ServiceID {
		t.Fatalf("service ids should differ: %#v %#v", leaseA.Lease.ServiceID, leaseB.Lease.ServiceID)
	}
	if leaseA.Lease.ServiceID != leaseA.ServiceClaim.ServiceID || leaseB.Lease.ServiceID != leaseB.ServiceClaim.ServiceID {
		t.Fatal("lease and claim service ids must match")
	}
}

func TestPublishLeaseRejectsWrongPeer(t *testing.T) {
	priv, pub := testOwnerKey("lease-peer")
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: serviceidentity.ServiceIDFromPublicKey(pub), ServicePublicKey: serviceidentity.EncodePublicKey(pub), PublisherPeerID: "12D3-peer-a", RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce}, Nonce: "nonce-peer"}, priv)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := BuildPublishLeaseArtifacts(authorityPriv, req, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPublishLease(lease.Lease, authorityPub, "cluster-123", "default", lease.Lease.ServiceID, "12D3-peer-b"); err == nil || !strings.Contains(err.Error(), "publisher peer id mismatch") {
		t.Fatalf("expected peer mismatch, got %v", err)
	}
}
