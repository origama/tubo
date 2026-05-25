package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	capability "github.com/origama/tubo/internal/capability"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/ssh"
)

func TestServiceGrantEndpointInviteOnlyDeniesDirectConnectRequest(t *testing.T) {
	endpoint, owner, authPub, servicePeerID := newGrantEndpointForTest(t, "invite_only")
	resp := endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-1", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID(servicePeerID))
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "connect --token") {
		t.Fatalf("unexpected response: %#v (authority pub len=%d)", resp, len(authPub))
	}
}

func TestServiceGrantEndpointNamespaceMembersPolicy(t *testing.T) {
	endpoint, owner, authPub, servicePeerID, authPriv := newGrantEndpointWithAuthorityForTest(t, "namespace_members")
	requester := peer.ID("12D3KooWNamespaceMemberRequester")
	capNoConnect, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: "cluster-123", Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}, ExpiresAt: time.Now().Add(time.Hour)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp := endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-2", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t), MembershipCapability: &capNoConnect}, requester)
	if resp.Type != grantspkg.TypeDenied || (!strings.Contains(resp.Reason, "connect permission") && !strings.Contains(resp.Reason, "required permissions")) {
		t.Fatalf("expected connect permission denial, got %#v", resp)
	}
	capWithConnect, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: "cluster-123", Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp = endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-3", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t), MembershipCapability: &capWithConnect}, requester)
	if resp.Type != grantspkg.TypeConnectGranted || resp.ConnectAccessLease == nil || resp.ConnectRefreshLease == nil {
		t.Fatalf("expected granted response, got %#v", resp)
	}
	if err := grantspkg.VerifyDelegatedConnectAccessLease(*resp.ConnectAccessLease, authPub, "cluster-123", "default", owner.ServiceID, servicePeerID); err != nil {
		t.Fatalf("verify delegated access lease: %v", err)
	}
}

func TestServiceGrantEndpointPublicPolicyRateLimits(t *testing.T) {
	endpoint, owner, _, _ := newGrantEndpointForTest(t, "public")
	requester := peer.ID("12D3KooWPublicRequester")
	var denied bool
	for i := 0; i < publicConnectRateLimitBurst+1; i++ {
		resp := endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-public", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t)}, requester)
		if i < publicConnectRateLimitBurst && resp.Type != grantspkg.TypeConnectGranted {
			t.Fatalf("expected granted response before rate limit, got %#v", resp)
		}
		if i == publicConnectRateLimitBurst {
			denied = resp.Type == grantspkg.TypeDenied && strings.Contains(resp.Reason, "rate limit")
		}
	}
	if !denied {
		t.Fatal("expected rate limit denial")
	}
}

func newGrantEndpointForTest(t *testing.T, policy string) (*serviceGrantEndpoint, serviceidentity.Identity, ed25519.PublicKey, string) {
	endpoint, owner, authPub, servicePeerID, _ := newGrantEndpointWithAuthorityForTest(t, policy)
	return endpoint, owner, authPub, servicePeerID
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

func newGrantEndpointWithAuthorityForTest(t *testing.T, policy string) (*serviceGrantEndpoint, serviceidentity.Identity, ed25519.PublicKey, string, ed25519.PrivateKey) {
	t.Helper()
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSSH, err := ssh.NewPublicKey(authPub)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ownerPath := filepath.Join(dir, "service.owner.key")
	if err := serviceidentity.Save(ownerPath, owner.PrivateKey); err != nil {
		t.Fatal(err)
	}
	servicePeerID := "12D3KooWGrantEndpointServicePeer"
	req, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey), PublisherPeerID: servicePeerID, RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "grant-endpoint-test"}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authPriv, req, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaseBytes, err := json.MarshalIndent(leaseArtifacts.Lease, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(dir, "service.publish-lease.json")
	if err := os.WriteFile(leasePath, append(leaseBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	endpoint, err := newServiceGrantEndpoint(Config{AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authSSH))), ClusterName: "home", DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", ServiceOwnerKeyFile: ownerPath, ServicePublishLeaseFile: leasePath, ConnectPolicy: policy}, owner.ServiceID, servicePeerID)
	if err != nil {
		t.Fatal(err)
	}
	return endpoint, owner, authPub, servicePeerID, authPriv
}
