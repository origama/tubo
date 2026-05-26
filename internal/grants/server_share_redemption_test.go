package grants

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestServerShareRedeemIsOneTime(t *testing.T) {
	authorityPriv, _ := testOwnerKey("share-redeem-once-authority")
	invite, err := BuildServiceShareArtifacts(authorityPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "requests.json")
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: NewStore(storePath), AutoApprove: true, AuthorityPrivateKey: authorityPriv, ClaimTTL: time.Hour, ServiceShareTTL: time.Hour, ConnectAccessTTL: time.Minute, ConnectRefreshTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	first := server.HandleMessage(Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: invite.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3-first"))
	if first.Type != TypeShareRedeem || first.ConnectAccessLease == nil || first.ConnectRefreshLease == nil {
		t.Fatalf("first redeem failed: %#v", first)
	}
	second := server.HandleMessage(Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: invite.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3-second"))
	if second.Type != TypeDenied || !strings.Contains(second.Reason, "already redeemed") {
		t.Fatalf("expected one-time denial, got %#v", second)
	}
}

func TestServerShareRedeemPersistsAcrossReload(t *testing.T) {
	authorityPriv, _ := testOwnerKey("share-redeem-persist-authority")
	invite, err := BuildServiceShareArtifacts(authorityPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(t.TempDir(), "requests.json")
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: NewStore(storePath), AutoApprove: true, AuthorityPrivateKey: authorityPriv, ClaimTTL: time.Hour, ServiceShareTTL: time.Hour, ConnectAccessTTL: time.Minute, ConnectRefreshTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	resp := server.HandleMessage(Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: invite.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3-first"))
	if resp.Type != TypeShareRedeem {
		t.Fatalf("first redeem failed: %#v", resp)
	}
	reloaded, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: NewStore(storePath), AutoApprove: true, AuthorityPrivateKey: authorityPriv, ClaimTTL: time.Hour, ServiceShareTTL: time.Hour, ConnectAccessTTL: time.Minute, ConnectRefreshTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	resp = reloaded.HandleMessage(Message{Type: TypeShareRedeem, Version: VersionV1, ShareInviteToken: invite.Token, ClientPublicKey: testAuthorizedClientKey(t)}, peer.ID("12D3-second"))
	if resp.Type != TypeDenied || !strings.Contains(resp.Reason, "already redeemed") {
		t.Fatalf("expected persisted one-time denial, got %#v", resp)
	}
}
