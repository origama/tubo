package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	bridgeapp "github.com/origama/tubo/internal/app/bridge"
	serviceapp "github.com/origama/tubo/internal/app/service"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
)

func TestConnectLeaseRedeemAndRefreshKeepsBridgeAlive(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"path": r.URL.Path, "query": r.URL.RawQuery})
	}))
	defer upstream.Close()

	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authAuthorized, err := ssh.NewPublicKey(authPub)
	if err != nil {
		t.Fatal(err)
	}
	authKeyText := string(ssh.MarshalAuthorizedKey(authAuthorized))

	clusterID := "cluster-a"
	namespaceID := "default"
	serviceID := "svc-refresh"

	grantHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "grant-refresh-seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer grantHost.Close()
	grantServer, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: clusterID, NamespaceID: namespaceID, AutoApprove: true, AuthorityPrivateKey: authPriv, ConnectAccessTTL: 2 * time.Second, ConnectRefreshTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	grantServer.Register(grantHost)
	grantPeer := p2p.PeerAddrs(grantHost)[0]

	serviceApp, err := serviceapp.New(ctx, serviceapp.Config{
		Listen:               "/ip4/127.0.0.1/tcp/0",
		Seed:                 "service-refresh-seed",
		ServiceName:          "myapi",
		ServiceID:            serviceID,
		Target:               upstream.URL,
		DiscoveryMode:        discovery.ModeNamespaceV2.String(),
		DiscoveryClusterID:   clusterID,
		DiscoveryNamespaceID: namespaceID,
		AuthorityPublicKey:   authKeyText,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer serviceApp.Host().Close()
	serviceAddr := p2p.PeerAddrs(serviceApp.Host())[0]

	invite, err := grantspkg.BuildServiceShareArtifacts(authPriv, "home", clusterID, namespaceID, "myapi", serviceID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	invite.Payload.GrantService = grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{grantPeer}}
	shareToken, err := grantspkg.SignServiceShareToken(invite.Payload, authPriv)
	if err != nil {
		t.Fatal(err)
	}

	bridge := startBridgeApp(t, ctx, bridgeapp.Config{
		Listen:             "127.0.0.1:0",
		Seed:               "bridge-refresh-seed",
		P2PListen:          "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:        serviceAddr,
		ConnectInviteToken: shareToken,
		ConnectGrantPeers:  []string{grantPeer},
	})
	defer bridge.cancel()

	deadline := time.Now().Add(8 * time.Second)
	for i := 0; time.Now().Before(deadline); i++ {
		status, body := httpRequest(t, bridge.url, "/v1/dummy?from=refresh")
		if status != http.StatusOK {
			t.Fatalf("request %d failed after lease refresh window: status=%d body=%s", i, status, body)
		}
		time.Sleep(1500 * time.Millisecond)
	}
}
