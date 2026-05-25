package grants

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/serviceidentity"
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

func TestBuildShareInviteArtifactsFromLeaseRequiresShareMint(t *testing.T) {
	_, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3-peer",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "nonce-share-mint",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, req, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := BuildShareInviteArtifactsFromLease(authorityPriv, "home", leaseArtifacts.Lease, "myapi", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if invite.Payload.TargetServiceID != leaseArtifacts.Lease.ServiceID || invite.Payload.DisplayNameHint != "myapi" || invite.Payload.JTI == "" {
		t.Fatalf("unexpected invite payload: %#v", invite.Payload)
	}
	parsed, err := ParseAndVerifyServiceShareToken(invite.Token)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.TargetServiceID != leaseArtifacts.Lease.ServiceID || parsed.ServiceName != "myapi" {
		t.Fatalf("parsed invite mismatch: %#v", parsed)
	}
}

func TestBuildShareInviteArtifactsRejectsLeaseWithoutShareMint(t *testing.T) {
	_, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3-peer",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		Nonce:                 "nonce-no-share-mint",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, req, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := BuildShareInviteArtifactsFromLease(authorityPriv, "home", leaseArtifacts.Lease, "myapi", time.Hour); err == nil || !strings.Contains(err.Error(), "share invite minting") {
		t.Fatalf("expected share mint rejection, got %v", err)
	}
}

func TestBuildShareInviteArtifactsRejectsExpiredOrMismatchedLease(t *testing.T) {
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = authorityPub
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3-peer",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "nonce-lease-checks",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, req, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	expired := leaseArtifacts.Lease
	expired.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	expired.ServiceClaim.ExpiresAt = expired.ExpiresAt
	expired.ServiceClaim, err = capability.SignServiceClaim(expired.ServiceClaim, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := canonicalPublishLease(expired)
	if err != nil {
		t.Fatal(err)
	}
	expired.Signature = ed25519.Sign(authorityPriv, payload)
	if _, err := BuildShareInviteArtifactsFromLease(authorityPriv, "home", expired, "myapi", time.Hour); err == nil || !strings.Contains(err.Error(), "publish lease expired") {
		t.Fatalf("expected expired lease rejection, got %v", err)
	}

	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mismatched := leaseArtifacts.Lease
	mismatched.ServiceID = serviceidentity.ServiceIDFromPublicKey(otherPub)
	mismatched.ServiceClaim.ServiceID = mismatched.ServiceID
	mismatched.ServiceClaim, err = capability.SignServiceClaim(mismatched.ServiceClaim, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	payload, err = canonicalPublishLease(mismatched)
	if err != nil {
		t.Fatal(err)
	}
	mismatched.Signature = ed25519.Sign(authorityPriv, payload)
	if _, err := BuildShareInviteArtifactsFromLease(authorityPriv, "home", mismatched, "myapi", time.Hour); err == nil || !strings.Contains(err.Error(), "service id mismatch") {
		t.Fatalf("expected mismatched service id rejection, got %v", err)
	}
}

func TestSignServiceShareTokenOmitsEmptyGrantServiceMetadata(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := BuildServiceShareArtifacts(priv, "home", "cluster-123", "default", "myapi", "service-myapi", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	raw := decodeServiceShareTokenPayloadJSON(t, artifacts.Token)
	if _, ok := raw["grant_service"]; ok {
		t.Fatalf("expected empty grant_service metadata to be omitted, got %#v", raw["grant_service"])
	}
}

func TestBuildServiceShareArtifactsWithEndpointsIncludesMetadata(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	grantPeers := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant"}
	serviceAddrs := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService"}
	artifacts, err := BuildServiceShareArtifactsWithEndpoints(priv, "home", "cluster-123", "default", "myapi", "service-myapi", time.Hour, grantPeers, "12D3KooWService", serviceAddrs)
	if err != nil {
		t.Fatal(err)
	}
	raw := decodeServiceShareTokenPayloadJSON(t, artifacts.Token)
	if endpointValue, ok := raw["service_endpoint"]; !ok {
		t.Fatal("expected service_endpoint metadata to be present")
	} else if endpoint, ok := endpointValue.(map[string]any); !ok {
		t.Fatalf("service_endpoint payload has unexpected type %T", endpointValue)
	} else if endpoint["peer_id"] != "12D3KooWService" {
		t.Fatalf("service_endpoint peer_id = %#v", endpoint["peer_id"])
	}
	if grantValue, ok := raw["grant_service"]; !ok {
		t.Fatal("expected grant_service metadata to be present")
	} else if grantService, ok := grantValue.(map[string]any); !ok {
		t.Fatalf("grant_service payload has unexpected type %T", grantValue)
	} else if grantService["protocol"] != ProtocolID {
		t.Fatalf("grant_service protocol = %#v, want %q", grantService["protocol"], ProtocolID)
	}
	parsed, err := ParseAndVerifyServiceShareToken(artifacts.Token)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ServiceEndpoint.PeerID != "12D3KooWService" || len(parsed.ServiceEndpoint.Addresses) != 1 || parsed.ServiceEndpoint.Addresses[0] != serviceAddrs[0] {
		t.Fatalf("parsed service endpoint = %#v", parsed.ServiceEndpoint)
	}
	if parsed.GrantService.Protocol != ProtocolID || len(parsed.GrantService.Peers) != 1 || parsed.GrantService.Peers[0] != grantPeers[0] {
		t.Fatalf("parsed grant service = %#v", parsed.GrantService)
	}
}

func TestBuildShareInviteArtifactsWithGrantServiceIncludesMetadata(t *testing.T) {
	_, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	req, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3-peer",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "nonce-share-metadata",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, req, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	grantPeers := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant"}
	serviceAddrs := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService"}
	invite, err := BuildShareInviteArtifactsFromLeaseWithEndpoints(authorityPriv, "home", leaseArtifacts.Lease, "myapi", time.Hour, grantPeers, "12D3KooWService", serviceAddrs)
	if err != nil {
		t.Fatal(err)
	}
	raw := decodeServiceShareTokenPayloadJSON(t, invite.Token)
	value, ok := raw["grant_service"]
	if !ok {
		t.Fatal("expected grant_service metadata to be present")
	}
	grantService, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("grant_service payload has unexpected type %T", value)
	}
	if grantService["protocol"] != ProtocolID {
		t.Fatalf("grant_service protocol = %#v, want %q", grantService["protocol"], ProtocolID)
	}
	peers, ok := grantService["peers"].([]any)
	if !ok || len(peers) != 1 || peers[0] != grantPeers[0] {
		t.Fatalf("grant_service peers = %#v, want %#v", grantService["peers"], grantPeers)
	}
	endpointValue, ok := raw["service_endpoint"]
	if !ok {
		t.Fatal("expected service_endpoint metadata to be present")
	}
	endpoint, ok := endpointValue.(map[string]any)
	if !ok {
		t.Fatalf("service_endpoint payload has unexpected type %T", endpointValue)
	}
	if endpoint["peer_id"] != "12D3KooWService" {
		t.Fatalf("service_endpoint peer_id = %#v", endpoint["peer_id"])
	}
	addresses, ok := endpoint["addresses"].([]any)
	if !ok || len(addresses) != 1 || addresses[0] != serviceAddrs[0] {
		t.Fatalf("service_endpoint addresses = %#v, want %#v", endpoint["addresses"], serviceAddrs)
	}
	parsed, err := ParseAndVerifyServiceShareToken(invite.Token)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ServiceEndpoint.PeerID != "12D3KooWService" || len(parsed.ServiceEndpoint.Addresses) != 1 || parsed.ServiceEndpoint.Addresses[0] != serviceAddrs[0] {
		t.Fatalf("parsed service endpoint = %#v", parsed.ServiceEndpoint)
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

func decodeServiceShareTokenPayloadJSON(t *testing.T, token string) map[string]any {
	t.Helper()
	trimmed := strings.TrimPrefix(token, ServiceShareTokenPrefix)
	trimmed = strings.TrimPrefix(trimmed, LegacyServiceShareTokenPrefix)
	parts := strings.Split(trimmed, ".")
	if len(parts) != 2 {
		t.Fatalf("invalid service share token format: %q", token)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payloadBytes, &raw); err != nil {
		t.Fatal(err)
	}
	return raw
}
