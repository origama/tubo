package grants

import (
	"crypto/ed25519"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestGrantServerRevokedInviteCannotRedeem(t *testing.T) {
	authorityPriv, _ := testOwnerKey("revoked-invite-authority")
	revocations := NewRevocationStore(filepath.Join(t.TempDir(), "revocations.json"))
	server := newRevocationTestServer(t, authorityPriv, revocations)
	invite, err := BuildServiceShareArtifacts(authorityPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := revocations.RevokeInvite(invite.Payload.JTI, "test"); err != nil {
		t.Fatal(err)
	}
	resp := server.HandleMessage(Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: invite.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3-requester"))
	if resp.Type != TypeDenied || !strings.Contains(resp.Reason, "revoked") {
		t.Fatalf("expected revoked invite denial, got %#v", resp)
	}
}

func TestGrantServerRevokedSessionAndServiceAccessCannotRefresh(t *testing.T) {
	authorityPriv, _ := testOwnerKey("revoked-session-authority")
	revocations := NewRevocationStore(filepath.Join(t.TempDir(), "revocations.json"))
	server := newRevocationTestServer(t, authorityPriv, revocations)
	invite, err := BuildServiceShareArtifacts(authorityPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	redeemed := server.HandleMessage(Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: invite.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3-requester"))
	if redeemed.Type != TypeShareRedeem || redeemed.ConnectRefreshLease == nil {
		t.Fatalf("redeem failed: %#v", redeemed)
	}
	if _, err := revocations.RevokeSession(redeemed.ConnectRefreshLease.SessionID, "test"); err != nil {
		t.Fatal(err)
	}
	refreshResp := server.HandleMessage(Message{Type: TypeConnectRefresh, Version: VersionV1, ConnectRefreshLease: redeemed.ConnectRefreshLease}, peer.ID("12D3-requester"))
	if refreshResp.Type != TypeDenied || !strings.Contains(refreshResp.Reason, "session revoked") {
		t.Fatalf("expected revoked session denial, got %#v", refreshResp)
	}

	invite2, err := BuildServiceShareArtifacts(authorityPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	redeemed2 := server.HandleMessage(Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: invite2.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3-requester"))
	if redeemed2.Type != TypeShareRedeem || redeemed2.ConnectRefreshLease == nil {
		t.Fatalf("redeem2 failed: %#v", redeemed2)
	}
	if _, err := revocations.RevokeServiceAccess("svc-123", "test"); err != nil {
		t.Fatal(err)
	}
	refreshResp = server.HandleMessage(Message{Type: TypeConnectRefresh, Version: VersionV1, ConnectRefreshLease: redeemed2.ConnectRefreshLease}, peer.ID("12D3-requester"))
	if refreshResp.Type != TypeDenied || !strings.Contains(refreshResp.Reason, "service access revoked") {
		t.Fatalf("expected service access epoch denial, got %#v", refreshResp)
	}
	redeemResp := server.HandleMessage(Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: invite2.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3-requester"))
	if redeemResp.Type != TypeDenied || !strings.Contains(redeemResp.Reason, "service access revoked") {
		t.Fatalf("expected stale invite epoch denial, got %#v", redeemResp)
	}
}

func TestGrantServerPublishRevokeBlocksSubmit(t *testing.T) {
	authorityPriv, _ := testOwnerKey("publish-revoke-authority")
	revocations := NewRevocationStore(filepath.Join(t.TempDir(), "revocations.json"))
	server := newRevocationTestServer(t, authorityPriv, revocations)
	submit := signedSubmit("publish-revoked", "myapi", "12D3-service")
	if _, err := revocations.RevokePublish(submit.ServiceID, "test"); err != nil {
		t.Fatal(err)
	}
	resp := server.HandleMessage(submit, peer.ID("12D3-requester"))
	if resp.Type != TypeDenied || !strings.Contains(resp.Reason, "publish revoked") {
		t.Fatalf("expected publish revoked denial, got %#v", resp)
	}
}

func newRevocationTestServer(t *testing.T, authorityPriv ed25519.PrivateKey, revocations *RevocationStore) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: NewStore(filepath.Join(t.TempDir(), "requests.json")), AutoApprove: true, AuthorityPrivateKey: authorityPriv, ClaimTTL: time.Hour, ServiceShareTTL: time.Hour, ConnectAccessTTL: time.Minute, ConnectRefreshTTL: time.Hour, Revocations: revocations})
	if err != nil {
		t.Fatal(err)
	}
	return server
}
