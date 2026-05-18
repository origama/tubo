package grants

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/serviceidentity"
)

func TestGrantMessagesRoundTrip(t *testing.T) {
	claim := &capability.ServiceClaim{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: "service-myapi", SubjectPeerID: "12D3-service", Permissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, ExpiresAt: time.Now().Add(time.Hour), Signature: []byte("sig")}
	_, leasePub := testOwnerKey("lease-roundtrip")
	lease := &PublishLease{Version: PublishLeaseVersion, Kind: PublishLeaseKind, ClusterID: "cluster-123", NamespaceID: "default", ServiceID: "service-myapi", ServicePublicKey: serviceidentity.EncodePublicKey(leasePub), PublisherPeerID: "12D3-service", RequestedCapabilities: []string{capability.PermissionPublish, capability.PermissionAttach, capability.PermissionAnnounce}, Nonce: "nonce", IssuedAt: time.Now().Add(-time.Minute), ExpiresAt: time.Now().Add(time.Hour), ServiceClaim: *claim, Signature: []byte("sig")}
	for _, msg := range []Message{
		validSubmit(),
		{Type: TypePoll, Version: VersionV1, RequestID: "gr_123"},
		{Type: TypePending, Version: VersionV1, RequestID: "gr_123", ExpiresAt: time.Now().Add(time.Hour)},
		{Type: TypeApproved, Version: VersionV1, RequestID: "gr_123", ServiceClaim: claim, PublishLease: lease, ServiceShareToken: "tubo-service-share-v1.token"},
		{Type: TypeDenied, Version: VersionV1, RequestID: "gr_123", Reason: "no"},
		{Type: TypeExpired, Version: VersionV1, RequestID: "gr_123", Reason: "expired"},
	} {
		t.Run(msg.Type, func(t *testing.T) {
			var buf bytes.Buffer
			if err := EncodeMessage(&buf, msg); err != nil {
				t.Fatal(err)
			}
			got, err := DecodeMessage(&buf)
			if err != nil {
				t.Fatal(err)
			}
			if got.Type != msg.Type || got.Version != msg.Version {
				t.Fatalf("roundtrip = %#v want %#v", got, msg)
			}
			if msg.Type == TypeApproved && got.ServiceShareToken != msg.ServiceShareToken {
				t.Fatalf("service share token roundtrip mismatch: got %q want %q", got.ServiceShareToken, msg.ServiceShareToken)
			}
		})
	}
}

func TestGrantMessageValidationRejectsInvalid(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{"bad version", func() Message { m := validSubmit(); m.Version = "v2"; return m }(), "version"},
		{"bad type", func() Message { m := validSubmit(); m.Type = "bad"; return m }(), "type"},
		{"missing service", func() Message { m := validSubmit(); m.ServiceName = ""; return m }(), "missing"},
		{"bad name", func() Message { m := validSubmit(); m.ServiceName = "Bad_Name"; return m }(), "invalid service name"},
		{"bad permission", func() Message { m := validSubmit(); m.RequestedPermissions = []string{"root"}; return m }(), "permissions"},
		{"ttl too large", func() Message {
			m := validSubmit()
			m.RequestedTTLSeconds = int64((MaxTTL + time.Second).Seconds())
			return m
		}(), "ttl"},
		{"poll missing id", Message{Type: TypePoll, Version: VersionV1}, "request_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateMessage(tc.msg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q error, got %v", tc.want, err)
			}
		})
	}
}

func TestDecodeRejectsOversizedPayload(t *testing.T) {
	_, err := DecodeMessage(strings.NewReader(strings.Repeat("x", MaxMessageBytes+1)))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("expected too large error, got %v", err)
	}
}

func validSubmit() Message { return signedSubmit("default", "myapi", "12D3-service") }

func signedSubmit(label, serviceName, servicePeerID string) Message {
	ownerPriv, ownerPub := testOwnerKey(label)
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       servicePeerID,
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		Nonce:                 label + "-nonce",
	}, ownerPriv)
	if err != nil {
		panic(err)
	}
	return Message{Type: TypeSubmit, Version: VersionV1, Token: "tubo-invite-v1.token", ClusterID: "cluster-123", NamespaceID: "default", ServiceName: serviceName, ServiceID: serviceID, ServicePublicKey: req.ServicePublicKey, ServiceOwnerSignature: req.ServiceOwnerSignature, ServicePeerID: servicePeerID, RequestNonce: req.Nonce, RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, RequestedTTLSeconds: int64((7 * 24 * time.Hour).Seconds())}
}

func testOwnerKey(label string) (ed25519.PrivateKey, ed25519.PublicKey) {
	seed := sha256.Sum256([]byte(label))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pub := priv.Public().(ed25519.PublicKey)
	return priv, pub
}
