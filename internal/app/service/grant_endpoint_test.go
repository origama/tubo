package service

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	capability "github.com/origama/tubo/internal/capability"
	clusterinvite "github.com/origama/tubo/internal/clusterinvite"
	cfgpkg "github.com/origama/tubo/internal/config"
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
	viewerGrant, err := clusterinvite.GrantForRole(clusterinvite.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	discoverySecret, err := cfgpkg.GenerateSecretBytes(cfgpkg.NamespaceDiscoverySecretLength)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, err := clusterinvite.SignToken(clusterinvite.Payload{Version: clusterinvite.Version, Kind: clusterinvite.Kind, JTI: "viewer-jti", ClusterName: "home", ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(mustSSHKey(t, authPub)))), Namespace: "default", Discovery: &clusterinvite.NamespaceDiscoveryEntry{Version: "v1", Type: cfgpkg.SecretTypeNamespaceDiscovery, KeyID: "nsdk_viewer", Secret: base64.RawURLEncoding.EncodeToString(discoverySecret), CreatedAt: time.Now().UTC()}, Grant: viewerGrant, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp = endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-4", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t), MembershipGrantToken: viewerToken}, requester)
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "missing connect permission") {
		t.Fatalf("expected viewer grant denial, got %#v", resp)
	}
	memberGrant, err := clusterinvite.GrantForRole(clusterinvite.RoleMember)
	if err != nil {
		t.Fatal(err)
	}
	memberToken, err := clusterinvite.SignToken(clusterinvite.Payload{Version: clusterinvite.Version, Kind: clusterinvite.Kind, JTI: "member-jti", ClusterName: "home", ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(mustSSHKey(t, authPub)))), Namespace: "default", Discovery: &clusterinvite.NamespaceDiscoveryEntry{Version: "v1", Type: cfgpkg.SecretTypeNamespaceDiscovery, KeyID: "nsdk_member", Secret: base64.RawURLEncoding.EncodeToString(discoverySecret), CreatedAt: time.Now().UTC()}, Grant: memberGrant, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp = endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-5", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t), MembershipGrantToken: memberToken}, requester)
	if resp.Type != grantspkg.TypeConnectGranted {
		t.Fatalf("expected member grant approval, got %#v", resp)
	}
	membershipOnlyToken, err := clusterinvite.SignToken(clusterinvite.Payload{Version: clusterinvite.Version, Kind: clusterinvite.MembershipGrantKind, JTI: "member-jti-file", ClusterName: "home", ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(mustSSHKey(t, authPub)))), Namespace: "default", Grant: memberGrant, IssuedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp = endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-5b", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t), MembershipGrantToken: membershipOnlyToken}, requester)
	if resp.Type != grantspkg.TypeConnectGranted {
		t.Fatalf("expected membership-only grant approval, got %#v", resp)
	}
	if _, err := endpoint.revocations.RevokeInvite("member-jti", "test"); err != nil {
		t.Fatal(err)
	}
	resp = endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-6", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t), MembershipGrantToken: memberToken}, requester)
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "revoked") {
		t.Fatalf("expected revoked member grant denial, got %#v", resp)
	}
}

func TestServiceGrantEndpointConnectRequestBoundsLeasesByMembershipExpiry(t *testing.T) {
	endpoint, owner, _, servicePeerID, authPriv := newGrantEndpointWithAuthorityForTest(t, "namespace_members")
	requester := peer.ID("12D3KooWMembershipExpiryRequester")
	capShort, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: requester.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(200 * time.Millisecond)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp := endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "req-short", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t), MembershipCapability: &capShort}, requester)
	if resp.Type != grantspkg.TypeConnectGranted || resp.ConnectRefreshLease == nil {
		t.Fatalf("expected granted connect response, got %#v", resp)
	}
	if resp.ConnectRefreshLease.ExpiresAt.After(capShort.ExpiresAt.Add(100 * time.Millisecond)) {
		t.Fatalf("refresh lease expiry = %s, want bound by membership expiry %s", resp.ConnectRefreshLease.ExpiresAt, capShort.ExpiresAt)
	}
	time.Sleep(300 * time.Millisecond)
	resp = endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeConnectRefresh, Version: grantspkg.VersionV1, ConnectRefreshLease: resp.ConnectRefreshLease}, requester)
	if resp.Type != grantspkg.TypeDenied {
		t.Fatalf("expected expired refresh denial, got %#v", resp)
	}
	_ = servicePeerID
}

func TestServiceGrantEndpointShareRedeemIsOneTime(t *testing.T) {
	endpoint, _, _, _, authPriv := newGrantEndpointWithAuthorityForTest(t, "namespace_members")
	requester := peer.ID("12D3KooWShareRedeemRequester")
	rawLease, err := os.ReadFile(endpoint.publishLeaseFile)
	if err != nil {
		t.Fatal(err)
	}
	parsedLease, err := grantspkg.ParseAndVerifyPublishLeaseBytes(rawLease, endpoint.authorityPub, endpoint.clusterID, endpoint.namespaceID, endpoint.serviceID, endpoint.servicePeerID)
	if err != nil {
		t.Fatal(err)
	}
	invite, err := grantspkg.BuildShareInviteArtifactsFromLease(authPriv, "home", parsedLease, "myapi", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	resp := endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeShareRedeem, Version: grantspkg.VersionV1, ShareInviteToken: invite.Token, ClientPublicKey: testAuthorizedClientKey(t)}, requester)
	if resp.Type != grantspkg.TypeShareRedeem || resp.ConnectAccessLease == nil || resp.ConnectRefreshLease == nil {
		t.Fatalf("expected first share redeem success, got %#v", resp)
	}
	resp = endpoint.handleMessage(grantspkg.Message{Type: grantspkg.TypeShareRedeem, Version: grantspkg.VersionV1, ShareInviteToken: invite.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3KooWAnotherRequester"))
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "already redeemed") {
		t.Fatalf("expected share invite reuse denial, got %#v", resp)
	}
}

func TestServiceGrantEndpointAbuseControlsDenyCacheAndServiceBurst(t *testing.T) {
	endpoint, owner, _, _, authPriv := newGrantEndpointWithAuthorityForTest(t, "namespace_members")
	now := time.Unix(1700001000, 0).UTC()
	endpoint.now = func() time.Time { return now }
	endpoint.abuse = newGrantEndpointAbuseController(grantEndpointAbuseConfig{now: endpoint.now, perPeerBurst: 8, perServiceBurst: 3, invalidBurst: 2, denyTTL: time.Minute, window: time.Minute})
	requester := peer.ID("12D3KooWAbuseRequester")
	invalid := grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "deny-cache", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t)}
	resp := endpoint.handleMessage(invalid, requester)
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "requires a membership") {
		t.Fatalf("expected first invalid denial, got %#v", resp)
	}
	resp = endpoint.handleMessage(invalid, requester)
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "requires a membership") {
		t.Fatalf("expected second invalid denial, got %#v", resp)
	}
	resp = endpoint.handleMessage(invalid, requester)
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "deny cache") {
		t.Fatalf("expected deny cache denial, got %#v", resp)
	}
	now = now.Add(2 * time.Minute)
	capWithConnect, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: "cluster-123", Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(24 * time.Hour)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	valid := grantspkg.Message{Type: grantspkg.TypeConnectRequest, Version: grantspkg.VersionV1, RequestID: "service-burst", ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ClientPublicKey: testAuthorizedClientKey(t), MembershipCapability: &capWithConnect}
	for i := 0; i < 3; i++ {
		resp = endpoint.handleMessage(valid, peer.ID(fmt.Sprintf("12D3KooWServiceBurst%d", i)))
		if resp.Type != grantspkg.TypeConnectGranted {
			t.Fatalf("expected sporadic success before service burst, got %#v", resp)
		}
	}
	resp = endpoint.handleMessage(valid, peer.ID("12D3KooWServiceBurstOverflow"))
	if resp.Type != grantspkg.TypeDenied || !strings.Contains(resp.Reason, "rate limit exceeded for service") {
		t.Fatalf("expected service burst denial, got %#v", resp)
	}
}

func TestServiceGrantEndpointPublicPolicyRateLimits(t *testing.T) {
	endpoint, owner, _, _ := newGrantEndpointForTest(t, "public")
	endpoint.now = func() time.Time { return time.Unix(1700000000, 0).UTC() }
	endpoint.abuse = newGrantEndpointAbuseController(grantEndpointAbuseConfig{now: endpoint.now, perPeerBurst: 64, perServiceBurst: 64})
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

func TestServiceGrantStateDirPrefersWritableLeaseDir(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{ServicePublishLeaseFile: filepath.Join(dir, "service.publish-lease.json")}
	got := serviceGrantStateDirWithCheck(cfg, "svc-123", func(path string) bool { return path == dir })
	if got != dir {
		t.Fatalf("state dir = %q, want %q", got, dir)
	}
}

func TestServiceGrantStateDirFallsBackToDataHomeWhenLeaseDirIsNotWritable(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "xdg"))
	cfg := Config{ServicePublishLeaseFile: filepath.Join(t.TempDir(), "readonly", "service.publish-lease.json")}
	got := serviceGrantStateDirWithCheck(cfg, "svc-123", func(string) bool { return false })
	want := filepath.Join(os.Getenv("XDG_DATA_HOME"), "tubo", "services", "svc-123")
	if got != want {
		t.Fatalf("state dir = %q, want %q", got, want)
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
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "xdg"))
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

func mustSSHKey(t *testing.T, pub ed25519.PublicKey) ssh.PublicKey {
	t.Helper()
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return sshPub
}
