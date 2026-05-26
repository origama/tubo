package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	bridgeapp "github.com/origama/tubo/internal/app/bridge"
	serviceapp "github.com/origama/tubo/internal/app/service"
	capability "github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/ssh"
)

func TestShareInviteRedeemIsOneTimeAcrossClientsAndServiceRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upstream := httptest.NewServer(nil)
	defer upstream.Close()

	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	authorityKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	serviceSeed := "share-redeem-once-service-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	ownerPath := filepath.Join(work, "service.owner.key")
	if err := serviceidentity.Save(ownerPath, owner.PrivateKey); err != nil {
		t.Fatal(err)
	}
	serviceCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	serviceCapBytes, _ := json.MarshalIndent(serviceCap, "", "  ")
	serviceCapPath := filepath.Join(work, "service.membership.cap.json")
	if err := os.WriteFile(serviceCapPath, append(serviceCapBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: owner.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey), PublisherPeerID: servicePeerID.String(), RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "share-redeem-once"}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "myapi", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaseBytes, _ := json.MarshalIndent(leaseArtifacts.Lease, "", "  ")
	leasePath := filepath.Join(work, "service.publish-lease.json")
	if err := os.WriteFile(leasePath, append(leaseBytes, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	invite, err := grantspkg.BuildShareInviteArtifactsFromLease(authorityPriv, "home", leaseArtifacts.Lease, "myapi", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	startService := func(parent context.Context) (*serviceapp.App, context.CancelFunc, <-chan error) {
		serviceCtx, serviceCancel := context.WithCancel(parent)
		serviceP2P := freePort(t)
		serviceHealth := freePort(t)
		topic := discovery.NamespaceTopic("cluster-123", "default")
		app, err := serviceapp.New(serviceCtx, serviceapp.Config{Listen: fmt.Sprintf("/ip4/127.0.0.1/tcp/%d", serviceP2P), Seed: serviceSeed, ServiceName: "myapi", ServiceID: owner.ServiceID, ServiceOwnerKeyFile: ownerPath, Target: upstream.URL, HealthListen: fmt.Sprintf("127.0.0.1:%d", serviceHealth), HeartbeatInterval: 500 * time.Millisecond, BootstrapRetryInterval: 500 * time.Millisecond, DiscoveryEnabled: true, DiscoveryTopic: topic, DiscoveryMode: discovery.ModeNamespaceV2.String(), DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", AuthorityPublicKey: authorityKey, ConnectPolicy: string("invite_only"), MembershipCapabilityFile: serviceCapPath, ServicePublishLeaseFile: leasePath, ClusterName: "home"})
		if err != nil {
			t.Fatal(err)
		}
		errCh := make(chan error, 1)
		go func() { errCh <- app.Start(serviceCtx) }()
		waitUntil(t, 15*time.Second, func() bool { return httpOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", serviceHealth)) }, "share redeem service health")
		return app, serviceCancel, errCh
	}

	serviceApp, serviceCancel, serviceErr := startService(ctx)
	defer func() {
		serviceCancel()
		<-serviceErr
	}()

	serviceInfo := serviceApp.Host().Peerstore().PeerInfo(serviceApp.Host().ID())
	servicePeer := p2p.PeerAddrs(serviceApp.Host())[0]
	bobHost, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "bob-redeemer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bobHost.Close()
	artifacts, err := grantspkg.RedeemShareInvite(ctx, bobHost, serviceInfo, invite.Token, connectClientPublicKeyForTest(t, bobHost))
	if err != nil {
		t.Fatal(err)
	}
	if artifacts.RefreshLease.SessionID == "" {
		t.Fatalf("expected session id in redeemed lease: %#v", artifacts)
	}

	carolHost, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "carol-redeemer", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer carolHost.Close()
	if _, err := grantspkg.RedeemShareInvite(ctx, carolHost, serviceInfo, invite.Token, connectClientPublicKeyForTest(t, carolHost)); err == nil || !strings.Contains(err.Error(), "already redeemed") {
		t.Fatalf("expected second client redemption denial, got %v", err)
	}

	bob2Host, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "bob-redeemer-fresh-config", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bob2Host.Close()
	if _, err := grantspkg.RedeemShareInvite(ctx, bob2Host, serviceInfo, invite.Token, connectClientPublicKeyForTest(t, bob2Host)); err == nil || !strings.Contains(err.Error(), "already redeemed") {
		t.Fatalf("expected fresh-client redemption denial, got %v", err)
	}
	if _, err := bridgeapp.New(ctx, bridgeapp.Config{
		Listen:             "127.0.0.1:0",
		Seed:               "share-redeem-once-bridge-fresh-client",
		P2PListen:          "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:        servicePeer,
		ConnectInviteToken: invite.Token,
		ConnectGrantPeers:  []string{servicePeer},
	}); err == nil || !strings.Contains(err.Error(), "already redeemed") {
		t.Fatalf("expected fresh bridge connect denial, got %v", err)
	}

	serviceCancel()
	<-serviceErr
	serviceApp, serviceCancel, serviceErr = startService(ctx)
	serviceInfo = serviceApp.Host().Peerstore().PeerInfo(serviceApp.Host().ID())
	servicePeer = p2p.PeerAddrs(serviceApp.Host())[0]
	if _, err := grantspkg.RedeemShareInvite(ctx, carolHost, serviceInfo, invite.Token, connectClientPublicKeyForTest(t, carolHost)); err == nil || !strings.Contains(err.Error(), "already redeemed") {
		t.Fatalf("expected post-restart redemption denial, got %v", err)
	}
	if _, err := bridgeapp.New(ctx, bridgeapp.Config{
		Listen:             "127.0.0.1:0",
		Seed:               "share-redeem-once-bridge-post-restart",
		P2PListen:          "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:        servicePeer,
		ConnectInviteToken: invite.Token,
		ConnectGrantPeers:  []string{servicePeer},
	}); err == nil || !strings.Contains(err.Error(), "already redeemed") {
		t.Fatalf("expected post-restart bridge denial, got %v", err)
	}
}

func connectClientPublicKeyForTest(t *testing.T, h host.Host) string {
	t.Helper()
	pub := h.Peerstore().PubKey(h.ID())
	if pub == nil {
		t.Fatal("missing public key")
	}
	raw, err := pub.Raw()
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(ed25519.PublicKey(raw))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
}
