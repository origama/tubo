package bridge

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hostpkg "github.com/libp2p/go-libp2p/core/host"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"golang.org/x/crypto/ssh"
)

func TestBridgeRefreshesConnectAccessLeaseBeforeExpiry(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-refresh-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	clientKey := bridgeHostAuthorizedKey(t, h)
	invite, err := grantspkg.BuildServiceShareArtifacts(authPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := grantspkg.BuildConnectLeaseArtifacts(authPriv, invite.Payload, clientKey, time.Second, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var refreshes int32
	app := &App{host: h, cfg: Config{ConnectRefreshLease: &leases.RefreshLease, ConnectLeaseRefresher: func(ctx context.Context, refresh grantspkg.ConnectRefreshLease) (grantspkg.ConnectAccessLease, error) {
		atomic.AddInt32(&refreshes, 1)
		return grantspkg.RefreshConnectAccessLease(authPriv, refresh, time.Minute)
	}}, connectLease: &leases.AccessLease}
	if _, err := app.connectProof(); err != nil {
		t.Fatalf("connect proof after refresh: %v", err)
	}
	if atomic.LoadInt32(&refreshes) == 0 {
		t.Fatal("expected access lease refresh")
	}
}

func TestBridgeReportsExpiredRefreshLeaseClearly(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-refresh-expired-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	clientKey := bridgeHostAuthorizedKey(t, h)
	refresh, err := grantspkg.SignConnectRefreshLease(grantspkg.ConnectRefreshLease{
		JTI:             "cr_expired",
		SessionID:       "cs_expired",
		ClusterID:       "cluster-123",
		NamespaceID:     "default",
		ServiceID:       "svc-123",
		ClientPublicKey: clientKey,
		Permissions:     []string{"connect"},
		IssuedAt:        time.Now().Add(-2 * time.Hour),
		ExpiresAt:       time.Now().Add(-time.Hour),
	}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{host: h, cfg: Config{ConnectRefreshLease: &refresh}}
	_, err = app.connectProof()
	if err == nil || !strings.Contains(err.Error(), "fresh token/invite") {
		t.Fatalf("expected fresh token hint, got %v", err)
	}
}

func bridgeHostAuthorizedKey(t *testing.T, h hostpkg.Host) string {
	t.Helper()
	pub := h.Peerstore().PubKey(h.ID())
	if pub == nil {
		t.Fatal("missing host public key")
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
