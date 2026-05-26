package grants

import (
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/serviceidentity"
)

func TestSignAndVerifyShareMintRequest(t *testing.T) {
	authorityPriv, _ := testOwnerKey("share-mint-authority")
	ownerPriv, ownerPub := testOwnerKey("share-mint-owner")
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	leaseReq, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3KooWService",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "share-mint-request",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	req, err := SignShareMintRequest(ShareMintRequest{
		ClusterID:           "cluster-123",
		NamespaceID:         "default",
		ServiceID:           serviceID,
		PublishLease:        leaseArtifacts.Lease,
		ServicePeerID:       "12D3KooWService",
		ServiceAddresses:    []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService"},
		RequestedTTLSeconds: int64(time.Hour.Seconds()),
		RequestNonce:        "share-mint-fresh",
		RequestIssuedAt:     time.Now().UTC(),
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyShareMintRequest(req); err != nil {
		t.Fatalf("VerifyShareMintRequest() error = %v", err)
	}
}

func TestVerifyShareMintRequestRejectsWrongOwnerKeyAndServiceID(t *testing.T) {
	authorityPriv, _ := testOwnerKey("share-mint-authority-reject")
	ownerPriv, ownerPub := testOwnerKey("share-mint-owner-reject")
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	leaseReq, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3KooWService",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "share-mint-request-reject",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	wrongPriv, _ := testOwnerKey("share-mint-wrong-owner")
	req, err := SignShareMintRequest(ShareMintRequest{
		ClusterID:           "cluster-123",
		NamespaceID:         "default",
		ServiceID:           serviceID,
		PublishLease:        leaseArtifacts.Lease,
		ServicePeerID:       "12D3KooWService",
		ServiceAddresses:    []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService"},
		RequestedTTLSeconds: int64(time.Hour.Seconds()),
		RequestNonce:        "share-mint-wrong-owner",
		RequestIssuedAt:     time.Now().UTC(),
	}, wrongPriv)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyShareMintRequest(req); err == nil {
		t.Fatal("expected wrong owner key rejection")
	}
	req, err = SignShareMintRequest(ShareMintRequest{
		ClusterID:           "cluster-123",
		NamespaceID:         "default",
		ServiceID:           serviceID,
		PublishLease:        leaseArtifacts.Lease,
		ServicePeerID:       "12D3KooWService",
		ServiceAddresses:    []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService"},
		RequestedTTLSeconds: int64(time.Hour.Seconds()),
		RequestNonce:        "share-mint-service-id",
		RequestIssuedAt:     time.Now().UTC(),
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	req.ServiceID = "service-other"
	if err := VerifyShareMintRequest(req); err == nil {
		t.Fatal("expected service id mismatch rejection")
	}
}

func TestShareMintRequestFreshness(t *testing.T) {
	req := ShareMintRequest{RequestIssuedAt: time.Now().UTC().Add(-ShareMintMaxAge - time.Minute)}
	if err := ShareMintRequestMatchesFreshness(req, time.Now().UTC()); err == nil {
		t.Fatal("expected stale request rejection")
	}
	future := ShareMintRequest{RequestIssuedAt: time.Now().UTC().Add(ShareMintMaxClockSkew + time.Second)}
	if err := ShareMintRequestMatchesFreshness(future, time.Now().UTC()); err == nil {
		t.Fatal("expected future request rejection")
	}
}

func TestValidateShareMintServiceEndpointRequiresMatchingEmbeddedServicePeer(t *testing.T) {
	cleaned, err := validateShareMintServiceEndpoint("12D3KooWService", []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService"})
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 1 {
		t.Fatalf("cleaned addrs = %#v", cleaned)
	}
	if _, err := validateShareMintServiceEndpoint("12D3KooWService", []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWOther"}); err == nil || !strings.Contains(err.Error(), "embeds peer") {
		t.Fatalf("expected embedded peer mismatch rejection, got %v", err)
	}
	if _, err := validateShareMintServiceEndpoint("12D3KooWService", []string{"/dns4/relay.tubo.click/tcp/4001"}); err == nil || !strings.Contains(err.Error(), "must embed /p2p/") {
		t.Fatalf("expected missing embedded peer rejection, got %v", err)
	}
}

func TestValidateShareMintServiceEndpointRejectsLocalOnly(t *testing.T) {
	for _, addr := range []string{
		"/ip4/127.0.0.1/tcp/1234/p2p/12D3KooWService",
		"/ip4/0.0.0.0/tcp/1234/p2p/12D3KooWService",
		"/ip6/::1/tcp/1234/p2p/12D3KooWService",
		"/ip6/::/tcp/1234/p2p/12D3KooWService",
		"/dns4/localhost/tcp/1234/p2p/12D3KooWService",
	} {
		if _, err := validateShareMintServiceEndpoint("12D3KooWService", []string{addr}); err == nil {
			t.Fatalf("expected local-only endpoint rejection for %q", addr)
		}
	}
}

func TestShareMintLeaseHashStable(t *testing.T) {
	authorityPriv, _ := testOwnerKey("share-mint-hash-authority")
	ownerPriv, ownerPub := testOwnerKey("share-mint-hash-owner")
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	leaseReq, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3KooWService",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "share-mint-hash",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a, err := shareMintLeaseHash(leaseArtifacts.Lease)
	if err != nil {
		t.Fatal(err)
	}
	b, err := shareMintLeaseHash(leaseArtifacts.Lease)
	if err != nil {
		t.Fatal(err)
	}
	if a == "" || a != b {
		t.Fatalf("unstable lease hash: %q vs %q", a, b)
	}
}
