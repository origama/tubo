package integration_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	bridgeapp "github.com/origama/tubo/internal/app/bridge"
	serviceapp "github.com/origama/tubo/internal/app/service"
	capability "github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/protocol"
)

func TestConnectProofAuthorizesBridgeAndRejectsReplayAndScope(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var upstreamHits int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&upstreamHits, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"method": r.Method,
			"path":   r.URL.Path,
			"query":  r.URL.RawQuery,
		})
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
	serviceID := "svc-a"

	serviceApp, err := serviceapp.New(ctx, serviceapp.Config{
		Listen:               "/ip4/127.0.0.1/tcp/0",
		Seed:                 "service-proof-seed",
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
	serviceInfo, err := p2p.AddrInfoFromString(serviceAddr)
	if err != nil {
		t.Fatal(err)
	}

	makeGrant := func(service string, expiresAt time.Time) capability.ConnectCapability {
		grant, err := capability.SignConnectCapability(capability.ConnectCapability{
			ClusterID:   clusterID,
			NamespaceID: namespaceID,
			ServiceID:   service,
			Permissions: []string{capability.PermissionConnect},
			ExpiresAt:   expiresAt,
		}, authPriv)
		if err != nil {
			t.Fatal(err)
		}
		return grant
	}

	goodGrant := makeGrant(serviceID, time.Now().Add(time.Hour))
	goodBridge := startBridgeApp(t, ctx, bridgeapp.Config{
		Listen:       "127.0.0.1:0",
		Seed:         "bridge-good-seed",
		P2PListen:    "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:  serviceAddr,
		Autorelay:    false,
		HolePunching: false,
		ConnectGrant: &goodGrant,
	})
	defer goodBridge.cancel()
	goodStatus, goodBody := httpRequest(t, goodBridge.url, "/v1/dummy?from=proof")
	if goodStatus != http.StatusOK {
		t.Fatalf("authorized bridge status=%d body=%s", goodStatus, goodBody)
	}
	if atomic.LoadInt32(&upstreamHits) != 1 {
		t.Fatalf("upstream hits after authorized request = %d, want 1", upstreamHits)
	}

	missingBridge := startBridgeApp(t, ctx, bridgeapp.Config{
		Listen:       "127.0.0.1:0",
		Seed:         "bridge-missing-seed",
		P2PListen:    "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:  serviceAddr,
		Autorelay:    false,
		HolePunching: false,
	})
	defer missingBridge.cancel()
	missingStatus, missingBody := httpRequest(t, missingBridge.url, "/v1/dummy?from=missing")
	if missingStatus != http.StatusBadGateway {
		t.Fatalf("missing proof status=%d body=%s", missingStatus, missingBody)
	}
	if atomic.LoadInt32(&upstreamHits) != 1 {
		t.Fatalf("upstream should not be hit on missing proof, got %d", upstreamHits)
	}

	expiredGrant := makeGrant(serviceID, time.Now().Add(-time.Minute))
	expiredBridge := startBridgeApp(t, ctx, bridgeapp.Config{
		Listen:       "127.0.0.1:0",
		Seed:         "bridge-expired-seed",
		P2PListen:    "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:  serviceAddr,
		Autorelay:    false,
		HolePunching: false,
		ConnectGrant: &expiredGrant,
	})
	defer expiredBridge.cancel()
	expiredStatus, expiredBody := httpRequest(t, expiredBridge.url, "/v1/dummy?from=expired")
	if expiredStatus != http.StatusBadGateway {
		t.Fatalf("expired proof status=%d body=%s", expiredStatus, expiredBody)
	}
	if atomic.LoadInt32(&upstreamHits) != 1 {
		t.Fatalf("upstream should not be hit on expired proof, got %d", upstreamHits)
	}

	wrongGrant := makeGrant("svc-other", time.Now().Add(time.Hour))
	wrongBridge := startBridgeApp(t, ctx, bridgeapp.Config{
		Listen:       "127.0.0.1:0",
		Seed:         "bridge-wrong-seed",
		P2PListen:    "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:  serviceAddr,
		Autorelay:    false,
		HolePunching: false,
		ConnectGrant: &wrongGrant,
	})
	defer wrongBridge.cancel()
	wrongStatus, wrongBody := httpRequest(t, wrongBridge.url, "/v1/dummy?from=wrong")
	if wrongStatus != http.StatusBadGateway {
		t.Fatalf("wrong service proof status=%d body=%s", wrongStatus, wrongBody)
	}
	if atomic.LoadInt32(&upstreamHits) != 1 {
		t.Fatalf("upstream should not be hit on wrong-service proof, got %d", upstreamHits)
	}

	replayClient, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "replay-client-seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer replayClient.Close()
	clientPriv := replayClient.Peerstore().PrivKey(replayClient.ID())
	if clientPriv == nil {
		t.Fatal("missing replay client private key")
	}
	rawPriv, err := clientPriv.Raw()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := protocol.NewConnectProof(goodGrant, replayClient.ID().String(), ed25519.PrivateKey(rawPriv))
	if err != nil {
		t.Fatal(err)
	}
	if err := replayClient.Connect(ctx, serviceInfo); err != nil {
		t.Fatal(err)
	}
	status, body, err := rawBridgeRequest(ctx, replayClient, serviceInfo.ID, &proof)
	if err != nil || status != http.StatusOK {
		t.Fatalf("first replay request failed: status=%d err=%v body=%s", status, err, body)
	}
	if atomic.LoadInt32(&upstreamHits) != 2 {
		t.Fatalf("upstream hits after replay warmup = %d, want 2", upstreamHits)
	}
	status, body, err = rawBridgeRequest(ctx, replayClient, serviceInfo.ID, &proof)
	if err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("expected replay rejection, got status=%d err=%v body=%s", status, err, body)
	}
	if atomic.LoadInt32(&upstreamHits) != 2 {
		t.Fatalf("upstream should not be hit on replay, got %d", upstreamHits)
	}
}

type bridgeTestApp struct {
	cancel context.CancelFunc
	url    string
}

func startBridgeApp(t *testing.T, parent context.Context, cfg bridgeapp.Config) bridgeTestApp {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	app, err := bridgeapp.New(ctx, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() { _ = app.Start(ctx) }()
	url := waitForBridgeURL(t, app)
	return bridgeTestApp{cancel: cancel, url: url}
}

func waitForBridgeURL(t *testing.T, app interface{ ListenAddr() string }) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if addr := app.ListenAddr(); addr != "" && !strings.HasSuffix(addr, ":0") {
			return "http://" + addr
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("bridge app did not start listening")
	return ""
}

func httpRequest(t *testing.T, baseURL, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(buf)
}

func rawBridgeRequest(ctx context.Context, client host.Host, serviceID peer.ID, proof *protocol.ConnectProof) (int, string, error) {
	stream, err := client.NewStream(ctx, serviceID, p2p.SupportedProtocolIDs()...)
	if err != nil {
		return 0, "", err
	}
	defer stream.Close()
	resp, err := p2p.HandleClientRequest(stream, "bridge", http.MethodGet, "/v1/dummy", "from=replay", nil, nil, proof)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	buf, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(buf), nil
}
