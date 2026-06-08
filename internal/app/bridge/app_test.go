package bridge

import (
	"bytes"
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hostpkg "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
	capability "github.com/origama/tubo/internal/capability"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	iprotocol "github.com/origama/tubo/internal/protocol"
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

func TestBridgeMintsConnectLeaseLocallyFromShareInvite(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes, err := x509.MarshalPKCS8PrivateKey(authPriv)
	if err != nil {
		t.Fatal(err)
	}
	authFile := filepath.Join(t.TempDir(), "authority.key")
	if err := os.WriteFile(authFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pemBytes}), 0600); err != nil {
		t.Fatal(err)
	}
	host, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-local-mint-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer host.Close()
	invite, err := grantspkg.BuildServiceShareArtifacts(authPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	app, err := New(context.Background(), Config{Seed: "bridge-local-mint-seed", ServiceSeed: "bridge-local-mint-service", ServiceAddr: p2p.PeerAddrs(host)[0], P2PListen: "/ip4/127.0.0.1/tcp/0", ConnectInviteToken: invite.Token, ConnectAuthorityPrivateKeyFile: authFile})
	if err != nil {
		t.Fatalf("bridge new: %v", err)
	}
	if app.cfg.ConnectAccessLease == nil || app.cfg.ConnectRefreshLease == nil {
		t.Fatalf("expected minted leases, got %#v", app.cfg)
	}
}

func TestBridgeDoesNotRefreshWhenRefreshLeaseNearExpiry(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-refresh-near-expiry-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	clientKey := bridgeHostAuthorizedKey(t, h)
	refresh, err := grantspkg.SignConnectRefreshLease(grantspkg.ConnectRefreshLease{
		JTI:             "cr_near_expiry",
		SessionID:       "cs_near_expiry",
		ClusterID:       "cluster-123",
		NamespaceID:     "default",
		ServiceID:       "svc-123",
		ClientPublicKey: clientKey,
		Permissions:     []string{"connect"},
		IssuedAt:        time.Now().UTC().Add(-time.Minute),
		ExpiresAt:       time.Now().UTC().Add(connectRefreshMinUsefulLifetime / 2),
	}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	access, err := grantspkg.RefreshConnectAccessLease(authPriv, refresh, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var refreshes int32
	app := &App{host: h, cfg: Config{ConnectRefreshLease: &refresh, ConnectLeaseRefresher: func(context.Context, grantspkg.ConnectRefreshLease) (grantspkg.ConnectAccessLease, error) {
		atomic.AddInt32(&refreshes, 1)
		return grantspkg.ConnectAccessLease{}, nil
	}}, connectLease: &access}
	_, err = app.ensureConnectAccessLease(context.Background())
	if err == nil || !strings.Contains(err.Error(), "fresh token/invite") {
		t.Fatalf("expected fresh-token hint, got %v", err)
	}
	if got := atomic.LoadInt32(&refreshes); got != 0 {
		t.Fatalf("refresh calls = %d, want 0", got)
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

func TestBridgeDiscoveryConnectLeaseErrorsMentionAttemptedGrantPeers(t *testing.T) {
	serviceHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-discovery-service", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serviceHost.Close()
	_, err = New(context.Background(), Config{
		Listen:             "127.0.0.1:0",
		Seed:               "bridge-discovery-client",
		P2PListen:          "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:        p2p.PeerAddrs(serviceHost)[0],
		ConnectClusterID:   "cluster-123",
		ConnectNamespaceID: "default",
		ConnectServiceID:   "svc-123",
		ConnectGrantPeers:  []string{"/ip4/127.0.0.1/tcp/1/p2p/12D3KooWAttemptedGrantPeer"},
	})
	if err == nil || !strings.Contains(err.Error(), "12D3KooWAttemptedGrantPeer") {
		t.Fatalf("expected attempted grant peer in error, got %v", err)
	}
}

func TestBridgeDiscoveryConnectAuthorizationFailureIsReturned(t *testing.T) {
	serviceHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-discovery-service-authz", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serviceHost.Close()
	grantHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-discovery-grant-authz", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer grantHost.Close()
	grantHost.SetStreamHandler(grantspkg.ProtocolID, func(stream network.Stream) {
		defer stream.Close()
		_, _ = grantspkg.DecodeMessage(stream)
		_ = grantspkg.EncodeMessage(stream, grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "authz", Reason: "namespace_members policy denied connect"})
	})
	_, err = New(context.Background(), Config{
		Listen:             "127.0.0.1:0",
		Seed:               "bridge-discovery-client-authz",
		P2PListen:          "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:        p2p.PeerAddrs(serviceHost)[0],
		ConnectClusterID:   "cluster-123",
		ConnectNamespaceID: "default",
		ConnectServiceID:   "svc-123",
		ConnectGrantPeers:  []string{p2p.PeerAddrs(grantHost)[0]},
	})
	if err == nil || !strings.Contains(err.Error(), "denied connect") {
		t.Fatalf("expected authorization failure, got %v", err)
	}
}

func TestBridgeInviteConnectFailsWhenGrantServicePeersAreUnreachable(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-fallback-service", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serviceHost.Close()
	invite, err := grantspkg.BuildServiceShareArtifacts(authPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload := invite.Payload
	payload.GrantService = grantspkg.GrantServiceEndpoint{
		Protocol: grantspkg.ProtocolID,
		Peers:    []string{"/ip4/127.0.0.1/tcp/1/p2p/12D3KooWFallbackGrantPeer"},
	}
	token, err := grantspkg.SignServiceShareToken(payload, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), Config{
		Listen:             "127.0.0.1:0",
		Seed:               "bridge-fallback-client",
		P2PListen:          "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:        p2p.PeerAddrs(serviceHost)[0],
		ConnectInviteToken: token,
		ConnectGrantPeers:  append([]string(nil), payload.GrantService.Peers...),
	})
	if err == nil || !strings.Contains(err.Error(), "redeem share invite") {
		t.Fatalf("expected redemption failure, got %v", err)
	}
}

func TestBridgeInviteConnectReportsUnsupportedGrantEndpointClearly(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-unsupported-grant-service", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serviceHost.Close()
	grantHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-unsupported-grant-endpoint", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer grantHost.Close()
	invite, err := grantspkg.BuildServiceShareArtifacts(authPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload := invite.Payload
	payload.GrantService = grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{p2p.PeerAddrs(grantHost)[0]}}
	token, err := grantspkg.SignServiceShareToken(payload, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), Config{
		Listen:             "127.0.0.1:0",
		Seed:               "bridge-unsupported-grant-client",
		P2PListen:          "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:        p2p.PeerAddrs(serviceHost)[0],
		ConnectInviteToken: token,
		ConnectGrantPeers:  append([]string(nil), payload.GrantService.Peers...),
	})
	if err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("expected unsupported grant endpoint error, got %v", err)
	}
}

func TestBridgeInviteConnectRequiresGrantServiceMetadata(t *testing.T) {
	serviceHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-no-grant-service-service", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serviceHost.Close()
	_, err = New(context.Background(), Config{
		Listen:             "127.0.0.1:0",
		Seed:               "bridge-no-grant-service-client",
		P2PListen:          "/ip4/127.0.0.1/tcp/0",
		ServiceAddr:        p2p.PeerAddrs(serviceHost)[0],
		ConnectInviteToken: "tubo-share-invite-v1.test",
	})
	if err == nil || !strings.Contains(err.Error(), "valid authorization path") {
		t.Fatalf("expected missing authorization-path error, got %v", err)
	}
}

func TestBridgeEstablishTCPTunnelSelfHealsOnOpenStreamFailure(t *testing.T) {
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-self-heal-open", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var opens int32
	var heals int32
	app := &App{
		host: h,
		openTunnelStream: func(context.Context) (network.Stream, error) {
			if atomic.AddInt32(&opens, 1) == 1 {
				return nil, io.EOF
			}
			return stubNetworkStream{}, nil
		},
		startClientTCPTunnel: func(network.Stream, string, *iprotocol.ConnectProof) error { return nil },
		reconnectServiceFn:   func(context.Context) error { atomic.AddInt32(&heals, 1); return nil },
	}
	stream, err := app.establishTCPTunnel("127.0.0.1:1234")
	if err != nil {
		t.Fatalf("establish tunnel: %v", err)
	}
	if stream == nil {
		t.Fatal("expected stream")
	}
	_ = stream.Close()
	if got := atomic.LoadInt32(&opens); got != 2 {
		t.Fatalf("open attempts = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&heals); got != 1 {
		t.Fatalf("self-heal attempts = %d, want 1", got)
	}
}

func TestBridgeEstablishTCPTunnelSelfHealsOnStartFailure(t *testing.T) {
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-self-heal-start", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	var starts int32
	var heals int32
	app := &App{
		host:             h,
		openTunnelStream: func(context.Context) (network.Stream, error) { return stubNetworkStream{}, nil },
		startClientTCPTunnel: func(network.Stream, string, *iprotocol.ConnectProof) error {
			if atomic.AddInt32(&starts, 1) == 1 {
				return io.ErrUnexpectedEOF
			}
			return nil
		},
		reconnectServiceFn: func(context.Context) error { atomic.AddInt32(&heals, 1); return nil },
	}
	stream, err := app.establishTCPTunnel("127.0.0.1:1234")
	if err != nil {
		t.Fatalf("establish tunnel: %v", err)
	}
	if stream == nil {
		t.Fatal("expected stream")
	}
	_ = stream.Close()
	if got := atomic.LoadInt32(&starts); got != 2 {
		t.Fatalf("start attempts = %d, want 2", got)
	}
	if got := atomic.LoadInt32(&heals); got != 1 {
		t.Fatalf("self-heal attempts = %d, want 1", got)
	}
}

func TestBridgeTCPFailureLogsAreActionable(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	defer func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	}()
	client, server := net.Pipe()
	defer client.Close()
	app := &App{
		openTunnelStream:   func(context.Context) (network.Stream, error) { return nil, io.EOF },
		reconnectServiceFn: func(context.Context) error { return nil },
	}
	done := make(chan struct{})
	go func() {
		app.handleTCPConn(server)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handleTCPConn timed out")
	}
	logs := buf.String()
	for _, want := range []string{"bridge tcp self-heal local=", "cause=open stream: EOF", "bridge tcp establish tunnel local="} {
		if !strings.Contains(logs, want) {
			t.Fatalf("log output missing %q:\n%s", want, logs)
		}
	}
}

func TestBridgeTunnelHealthTracksDegradedAndHealthy(t *testing.T) {
	app := &App{}
	if ok, _ := app.tunnelHealth(); !ok {
		t.Fatal("expected initial health to be ok")
	}
	app.markTunnelDegraded(io.EOF)
	if ok, msg := app.tunnelHealth(); ok || !strings.Contains(msg, "EOF") {
		t.Fatalf("expected degraded health, got ok=%t msg=%q", ok, msg)
	}
	app.markTunnelHealthy()
	if ok, _ := app.tunnelHealth(); !ok {
		t.Fatal("expected health to recover after success")
	}
}

func TestBridgeStatusSnapshotDegradesWhenRefreshLeaseExpires(t *testing.T) {
	app := &App{cfg: Config{ServiceKind: "tcp", ConnectRefreshLease: &grantspkg.ConnectRefreshLease{ExpiresAt: time.Now().UTC().Add(-time.Minute)}}}
	snap := app.statusSnapshot(time.Now().UTC())
	if snap.Status != "degraded" {
		t.Fatalf("status = %q", snap.Status)
	}
	if !strings.Contains(snap.Reason, "refresh lease expired") {
		t.Fatalf("reason = %q", snap.Reason)
	}
}

func TestBridgeStatusSnapshotDegradesWhenRefreshLeaseNearExpiry(t *testing.T) {
	app := &App{cfg: Config{ServiceKind: "tcp", ConnectRefreshLease: &grantspkg.ConnectRefreshLease{ExpiresAt: time.Now().UTC().Add(connectRefreshMinUsefulLifetime / 2)}}}
	snap := app.statusSnapshot(time.Now().UTC())
	if snap.Status != "degraded" {
		t.Fatalf("status = %q", snap.Status)
	}
	if !strings.Contains(snap.Reason, "fresh token/invite") {
		t.Fatalf("reason = %q", snap.Reason)
	}
}

func TestBridgeConnectPathTransitionMessage(t *testing.T) {
	if msg, ok := ConnectPathTransitionMessage("relayed", "direct"); !ok || msg != "connect path upgraded to direct" {
		t.Fatalf("relayed->direct = %q, %v", msg, ok)
	}
	if msg, ok := ConnectPathTransitionMessage("direct", "relayed"); !ok || msg != "connect path downgraded to relayed" {
		t.Fatalf("direct->relayed = %q, %v", msg, ok)
	}
	if msg, ok := ConnectPathTransitionMessage("direct", "direct"); ok || msg != "" {
		t.Fatalf("direct->direct = %q, %v", msg, ok)
	}
}

func TestBridgeCurrentRuntimeStatusIncludesSelectedBinding(t *testing.T) {
	app := &App{cfg: Config{ServiceKind: "tcp"}, selectedAddr: "/ip4/1.2.3.4/tcp/4001/p2p/peer", selectedPath: "relayed"}
	snap := app.CurrentRuntimeStatus()
	if snap.SelectedAddr != "/ip4/1.2.3.4/tcp/4001/p2p/peer" || snap.SelectedPath != "relayed" {
		t.Fatalf("unexpected runtime selected binding: %#v", snap)
	}
}

func TestBridgeNoUsefulRefreshResultDoesNotImmediatelyRetry(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-refresh-noop-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	clientKey := bridgeHostAuthorizedKey(t, h)
	refresh, err := grantspkg.SignConnectRefreshLease(grantspkg.ConnectRefreshLease{
		JTI:             "cr_noop",
		SessionID:       "cs_noop",
		ClusterID:       "cluster-123",
		NamespaceID:     "default",
		ServiceID:       "svc-123",
		ClientPublicKey: clientKey,
		Permissions:     []string{"connect"},
		IssuedAt:        time.Now().UTC().Add(-time.Minute),
		ExpiresAt:       time.Now().UTC().Add(time.Minute),
	}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	access, err := grantspkg.RefreshConnectAccessLease(authPriv, refresh, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	access.ExpiresAt = time.Now().UTC().Add(200 * time.Millisecond)
	access.IssuedAt = time.Now().UTC().Add(-800 * time.Millisecond)
	var refreshes int32
	app := &App{host: h, cfg: Config{ConnectRefreshLease: &refresh, ConnectLeaseRefresher: func(context.Context, grantspkg.ConnectRefreshLease) (grantspkg.ConnectAccessLease, error) {
		atomic.AddInt32(&refreshes, 1)
		return grantspkg.ConnectAccessLease{ExpiresAt: time.Now().UTC().Add(connectRefreshMinExtension / 2), IssuedAt: time.Now().UTC()}, nil
	}}, connectLease: &access}
	lease, err := app.ensureConnectAccessLease(context.Background())
	if err != nil {
		t.Fatalf("expected current lease to remain usable, got %v", err)
	}
	if lease.JTI != access.JTI {
		t.Fatalf("expected current lease to be reused, got %#v", lease)
	}
	if got := atomic.LoadInt32(&refreshes); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	snap := app.statusSnapshot(time.Now().UTC())
	if snap.Status != "degraded" || !strings.Contains(snap.Reason, "fresh token/invite") {
		t.Fatalf("expected degraded token hint, got %#v", snap)
	}
}

func TestBridgeConnectLeaseRolloverRenewsAccessAndRefresh(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bridgeHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-member-rollover-client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeHost.Close()
	grantHost := newConnectLeaseGrantHost(t, authPriv, 10*time.Second, 20*time.Second, func(msg grantspkg.Message, requester string) error {
		if msg.MembershipCapability == nil {
			return errors.New("missing membership capability")
		}
		if err := capability.VerifyMembershipCapability(*msg.MembershipCapability, authPriv.Public().(ed25519.PublicKey), "cluster-123", "default", requester); err != nil {
			return err
		}
		if !strings.Contains(strings.Join(msg.MembershipCapability.Permissions, ","), capability.PermissionConnect) {
			return errors.New("missing connect permission")
		}
		return nil
	})
	defer grantHost.Close()
	memberCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: bridgeHost.ID().String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := bridgeHostAuthorizedKey(t, bridgeHost)
	initial, err := grantspkg.BuildConnectLeaseArtifacts(authPriv, grantspkg.ServiceSharePayload{JTI: "bridge-member-rollover-initial", ClusterID: "cluster-123", NamespaceID: "default", TargetServiceID: "svc-123", ExpiresAt: time.Now().Add(time.Hour)}, clientKey, 150*time.Millisecond, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{host: bridgeHost, connectLease: &initial.AccessLease, cfg: Config{ConnectRefreshLease: &initial.RefreshLease, ConnectGrantPeers: []string{p2p.PeerAddrs(grantHost)[0]}, ConnectClusterID: "cluster-123", ConnectNamespaceID: "default", ConnectServiceID: "svc-123", ConnectMembershipCapability: &memberCap}}
	snap := app.statusSnapshot(time.Now().UTC())
	if snap.Status == "degraded" || strings.Contains(strings.ToLower(snap.Reason), "fresh token/invite") {
		t.Fatalf("expected rollover-capable session to stay non-alarmist before rollover, got %#v", snap)
	}
	rolled, err := app.ensureConnectAccessLease(context.Background())
	if err != nil {
		t.Fatalf("rollover lease: %v", err)
	}
	if rolled.JTI == initial.AccessLease.JTI {
		t.Fatalf("expected rollover to replace access lease: %#v", rolled)
	}
	if app.cfg.ConnectRefreshLease == nil || app.cfg.ConnectRefreshLease.JTI == initial.RefreshLease.JTI {
		t.Fatalf("expected rollover to replace refresh lease: %#v", app.cfg.ConnectRefreshLease)
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := app.connectProof(); err != nil {
		t.Fatalf("connect proof after rollover and old refresh expiry: %v", err)
	}
}

func TestBridgeConnectLeaseRolloverSkipsAlarmistRefreshHintWhenMembershipCanRollOver(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bridgeHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-member-rollover-skip-refresh-hint-client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeHost.Close()
	var rolloverRequests int32
	grantHost := newConnectLeaseGrantHost(t, authPriv, 10*time.Second, 20*time.Second, func(msg grantspkg.Message, requester string) error {
		atomic.AddInt32(&rolloverRequests, 1)
		if msg.MembershipCapability == nil {
			return errors.New("missing membership capability")
		}
		if err := capability.VerifyMembershipCapability(*msg.MembershipCapability, authPriv.Public().(ed25519.PublicKey), "cluster-123", "default", requester); err != nil {
			return err
		}
		if !strings.Contains(strings.Join(msg.MembershipCapability.Permissions, ","), capability.PermissionConnect) {
			return errors.New("missing connect permission")
		}
		return nil
	})
	defer grantHost.Close()
	memberCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: bridgeHost.ID().String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := bridgeHostAuthorizedKey(t, bridgeHost)
	initial, err := grantspkg.BuildConnectLeaseArtifacts(authPriv, grantspkg.ServiceSharePayload{JTI: "bridge-member-rollover-skip-refresh-hint-initial", ClusterID: "cluster-123", NamespaceID: "default", TargetServiceID: "svc-123", ExpiresAt: time.Now().Add(time.Hour)}, clientKey, 150*time.Millisecond, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	refresh, err := grantspkg.SignConnectRefreshLease(grantspkg.ConnectRefreshLease{JTI: initial.RefreshLease.JTI, SessionID: initial.RefreshLease.SessionID, ShareInviteJTI: initial.RefreshLease.ShareInviteJTI, ClusterID: initial.RefreshLease.ClusterID, NamespaceID: initial.RefreshLease.NamespaceID, ServiceID: initial.RefreshLease.ServiceID, ClientPublicKey: initial.RefreshLease.ClientPublicKey, Permissions: []string{capability.PermissionConnect}, IssuedAt: time.Now().UTC().Add(-time.Second), ExpiresAt: time.Now().UTC().Add(8 * time.Second)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	var refreshCalls int32
	var logBuf bytes.Buffer
	oldLogOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(oldLogOutput)
	app := &App{host: bridgeHost, connectLease: &initial.AccessLease, cfg: Config{ConnectRefreshLease: &refresh, ConnectGrantPeers: []string{p2p.PeerAddrs(grantHost)[0]}, ConnectClusterID: "cluster-123", ConnectNamespaceID: "default", ConnectServiceID: "svc-123", ConnectMembershipCapability: &memberCap, ConnectLeaseRefresher: func(_ context.Context, got grantspkg.ConnectRefreshLease) (grantspkg.ConnectAccessLease, error) {
		atomic.AddInt32(&refreshCalls, 1)
		return grantspkg.RefreshConnectAccessLease(authPriv, got, 300*time.Millisecond)
	}}}
	rolled, err := app.ensureConnectAccessLease(context.Background())
	if err != nil {
		t.Fatalf("expected rollover through membership after useless refresh result, got %v", err)
	}
	if got := atomic.LoadInt32(&refreshCalls); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&rolloverRequests); got != 1 {
		t.Fatalf("rollover requests = %d, want 1", got)
	}
	if rolled.JTI == initial.AccessLease.JTI {
		t.Fatalf("expected rollover to replace access lease: %#v", rolled)
	}
	if app.cfg.ConnectRefreshLease == nil || app.cfg.ConnectRefreshLease.JTI == initial.RefreshLease.JTI {
		t.Fatalf("expected rollover to replace refresh lease: %#v", app.cfg.ConnectRefreshLease)
	}
	snap := app.CurrentRuntimeStatus()
	if snap.Status == "degraded" || strings.Contains(strings.ToLower(snap.Reason), "fresh token/invite") || strings.Contains(strings.ToLower(snap.LastRefreshError), "fresh token/invite") {
		t.Fatalf("expected non-alarmist rollover-capable status, got %#v", snap)
	}
	if strings.Contains(strings.ToLower(logBuf.String()), "fresh token/invite") {
		t.Fatalf("expected no fresh-token/invite log for rollover-capable session, got logs: %s", logBuf.String())
	}
	if !strings.Contains(logBuf.String(), "bridge connect access lease refresh skipped; rolling over through membership") {
		t.Fatalf("expected membership rollover log, got logs: %s", logBuf.String())
	}
}

func TestBridgeConnectLeaseRolloverDeniedMarksDegraded(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bridgeHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-member-rollover-denied-client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeHost.Close()
	grantHost := newConnectLeaseGrantHost(t, authPriv, 10*time.Second, 20*time.Second, func(msg grantspkg.Message, requester string) error {
		if msg.MembershipCapability == nil {
			return errors.New("missing membership capability")
		}
		if err := capability.VerifyMembershipCapability(*msg.MembershipCapability, authPriv.Public().(ed25519.PublicKey), "cluster-123", "default", requester); err != nil {
			return err
		}
		if !strings.Contains(strings.Join(msg.MembershipCapability.Permissions, ","), capability.PermissionConnect) {
			return errors.New("missing connect permission")
		}
		return nil
	})
	defer grantHost.Close()
	memberCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: bridgeHost.ID().String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}, ExpiresAt: time.Now().Add(time.Hour)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := bridgeHostAuthorizedKey(t, bridgeHost)
	initial, err := grantspkg.BuildConnectLeaseArtifacts(authPriv, grantspkg.ServiceSharePayload{JTI: "bridge-member-rollover-denied-initial", ClusterID: "cluster-123", NamespaceID: "default", TargetServiceID: "svc-123", ExpiresAt: time.Now().Add(time.Hour)}, clientKey, 150*time.Millisecond, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{host: bridgeHost, connectLease: &initial.AccessLease, cfg: Config{ConnectRefreshLease: &initial.RefreshLease, ConnectGrantPeers: []string{p2p.PeerAddrs(grantHost)[0]}, ConnectClusterID: "cluster-123", ConnectNamespaceID: "default", ConnectServiceID: "svc-123", ConnectMembershipCapability: &memberCap}}
	lease, err := app.ensureConnectAccessLease(context.Background())
	if err != nil {
		t.Fatalf("expected current lease to remain usable after denial, got %v", err)
	}
	if lease.JTI != initial.AccessLease.JTI {
		t.Fatalf("expected current lease to remain in use, got %#v", lease)
	}
	snap := app.CurrentRuntimeStatus()
	if snap.Status != "degraded" || !strings.Contains(strings.ToLower(snap.Reason), "connect permission") {
		t.Fatalf("expected degraded membership denial, got %#v", snap)
	}
}

func TestBridgeConnectLeaseRolloverTemporaryFailureBacksOff(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bridgeHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-member-rollover-backoff-client", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer bridgeHost.Close()
	var requests int32
	grantHost := newConnectLeaseGrantHost(t, authPriv, 10*time.Second, 20*time.Second, func(msg grantspkg.Message, requester string) error {
		if atomic.AddInt32(&requests, 1) == 1 {
			return errors.New("temporary grant endpoint failure")
		}
		if msg.MembershipCapability == nil {
			return errors.New("missing membership capability")
		}
		if err := capability.VerifyMembershipCapability(*msg.MembershipCapability, authPriv.Public().(ed25519.PublicKey), "cluster-123", "default", requester); err != nil {
			return err
		}
		if !strings.Contains(strings.Join(msg.MembershipCapability.Permissions, ","), capability.PermissionConnect) {
			return errors.New("missing connect permission")
		}
		return nil
	})
	defer grantHost.Close()
	memberCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: bridgeHost.ID().String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}, ExpiresAt: time.Now().Add(time.Hour)}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := bridgeHostAuthorizedKey(t, bridgeHost)
	initial, err := grantspkg.BuildConnectLeaseArtifacts(authPriv, grantspkg.ServiceSharePayload{JTI: "bridge-member-rollover-backoff-initial", ClusterID: "cluster-123", NamespaceID: "default", TargetServiceID: "svc-123", ExpiresAt: time.Now().Add(time.Hour)}, clientKey, 150*time.Millisecond, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{host: bridgeHost, connectLease: &initial.AccessLease, cfg: Config{ConnectRefreshLease: &initial.RefreshLease, ConnectGrantPeers: []string{p2p.PeerAddrs(grantHost)[0]}, ConnectClusterID: "cluster-123", ConnectNamespaceID: "default", ConnectServiceID: "svc-123", ConnectMembershipCapability: &memberCap}}
	lease, err := app.ensureConnectAccessLease(context.Background())
	if err != nil {
		t.Fatalf("expected current lease to remain usable after temporary failure, got %v", err)
	}
	if lease.JTI != initial.AccessLease.JTI {
		t.Fatalf("expected current lease to remain in use, got %#v", lease)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected one rollover request, got %d", got)
	}
	snap := app.CurrentRuntimeStatus()
	if snap.NextRefreshRetryAt == nil {
		t.Fatal("expected retry backoff to be scheduled")
	}
	if snap.Status != "degraded" || !strings.Contains(strings.ToLower(snap.Reason), "temporary grant endpoint failure") {
		t.Fatalf("expected degraded temporary failure, got %#v", snap)
	}
	lease, err = app.ensureConnectAccessLease(context.Background())
	if err != nil {
		t.Fatalf("expected backoff path to keep current lease usable, got %v", err)
	}
	if lease.JTI != initial.AccessLease.JTI {
		t.Fatalf("expected current lease to remain in use during backoff, got %#v", lease)
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Fatalf("expected no retry before backoff, got %d requests", got)
	}
}

func TestBridgeLeaseRenewalRefreshesAccessLeaseProactively(t *testing.T) {
	_, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-renew-proactive-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	clientKey := bridgeHostAuthorizedKey(t, h)
	refresh, err := grantspkg.SignConnectRefreshLease(grantspkg.ConnectRefreshLease{
		JTI:             "cr-proactive",
		SessionID:       "cs-proactive",
		ClusterID:       "cluster-123",
		NamespaceID:     "default",
		ServiceID:       "svc-123",
		ClientPublicKey: clientKey,
		Permissions:     []string{"connect"},
		IssuedAt:        time.Now().UTC().Add(-time.Second),
		ExpiresAt:       time.Now().UTC().Add(time.Minute),
	}, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	access, err := grantspkg.RefreshConnectAccessLease(authPriv, refresh, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	access.ExpiresAt = time.Now().UTC().Add(1200 * time.Millisecond)
	access.IssuedAt = time.Now().UTC().Add(-1200 * time.Millisecond)
	var refreshes int32
	app := &App{host: h, cfg: Config{ConnectRefreshLease: &refresh, ConnectLeaseRefresher: func(ctx context.Context, got grantspkg.ConnectRefreshLease) (grantspkg.ConnectAccessLease, error) {
		atomic.AddInt32(&refreshes, 1)
		return grantspkg.RefreshConnectAccessLease(authPriv, got, time.Minute)
	}}, connectLease: &access}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go app.startConnectLeaseRenewal(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&refreshes) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected proactive access lease refresh")
}

type stubNetworkStream struct{}

func (stubNetworkStream) Read([]byte) (int, error)                     { return 0, io.EOF }
func (stubNetworkStream) Write(p []byte) (int, error)                  { return len(p), nil }
func (stubNetworkStream) Close() error                                 { return nil }
func (stubNetworkStream) CloseWrite() error                            { return nil }
func (stubNetworkStream) CloseRead() error                             { return nil }
func (stubNetworkStream) Reset() error                                 { return nil }
func (stubNetworkStream) ResetWithError(network.StreamErrorCode) error { return nil }
func (stubNetworkStream) SetDeadline(time.Time) error                  { return nil }
func (stubNetworkStream) SetReadDeadline(time.Time) error              { return nil }
func (stubNetworkStream) SetWriteDeadline(time.Time) error             { return nil }
func (stubNetworkStream) ID() string                                   { return "stub" }
func (stubNetworkStream) Protocol() coreprotocol.ID                    { return p2p.ProtocolID }
func (stubNetworkStream) SetProtocol(coreprotocol.ID) error            { return nil }
func (stubNetworkStream) Stat() network.Stats                          { return network.Stats{} }
func (stubNetworkStream) Conn() network.Conn                           { return nil }
func (stubNetworkStream) Scope() network.StreamScope                   { return nil }
func newConnectLeaseGrantHost(t *testing.T, authPriv ed25519.PrivateKey, accessTTL, refreshTTL time.Duration, authorize func(grantspkg.Message, string) error) hostpkg.Host {
	t.Helper()
	grantHost, err := p2p.NewHostWithSeedAndPSKAndOptions("/ip4/127.0.0.1/tcp/0", "bridge-connect-grant-"+testNameSlug(t.Name()), nil)
	if err != nil {
		t.Fatal(err)
	}
	grantHost.SetStreamHandler(grantspkg.ProtocolID, func(stream network.Stream) {
		defer stream.Close()
		msg, err := grantspkg.DecodeMessage(stream)
		if err != nil {
			_ = grantspkg.EncodeMessage(stream, grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "invalid", Reason: err.Error()})
			return
		}
		requester := stream.Conn().RemotePeer().String()
		if authorize != nil {
			if err := authorize(msg, requester); err != nil {
				_ = grantspkg.EncodeMessage(stream, grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: "connect-request", Reason: err.Error()})
				return
			}
		}
		invite := grantspkg.ServiceSharePayload{JTI: "bridge-connect-grant-" + testNameSlug(t.Name()), ClusterID: msg.ClusterID, NamespaceID: msg.NamespaceID, TargetServiceID: msg.ServiceID, ExpiresAt: time.Now().UTC().Add(time.Hour)}
		artifacts, err := grantspkg.BuildConnectLeaseArtifacts(authPriv, invite, msg.ClientPublicKey, accessTTL, refreshTTL)
		if err != nil {
			_ = grantspkg.EncodeMessage(stream, grantspkg.Message{Type: grantspkg.TypeDenied, Version: grantspkg.VersionV1, RequestID: msg.RequestID, Reason: err.Error()})
			return
		}
		_ = grantspkg.EncodeMessage(stream, grantspkg.Message{Type: grantspkg.TypeConnectGranted, Version: grantspkg.VersionV1, RequestID: msg.RequestID, ConnectAccessLease: &artifacts.AccessLease, ConnectRefreshLease: &artifacts.RefreshLease})
	})
	return grantHost
}

func testNameSlug(s string) string {
	mapped := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		case r >= '0' && r <= '9':
			return r
		case r == '-', r == '_', r == '.':
			return '-'
		default:
			return '-'
		}
	}, s)
	mapped = strings.Trim(mapped, "-")
	for strings.Contains(mapped, "--") {
		mapped = strings.ReplaceAll(mapped, "--", "-")
	}
	if mapped == "" {
		return "default"
	}
	return mapped
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
