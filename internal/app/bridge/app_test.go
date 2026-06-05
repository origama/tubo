package bridge

import (
	"context"
	"crypto/ed25519"
	crand "crypto/rand"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	hostpkg "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	coreprotocol "github.com/libp2p/go-libp2p/core/protocol"
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
	if err == nil || !strings.Contains(err.Error(), "missing grant service metadata") {
		t.Fatalf("expected missing grant-service metadata error, got %v", err)
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

func TestServiceStreamContextForcesDirectForDirectAddress(t *testing.T) {
	ctx := serviceStreamContext("/ip4/10.0.0.2/tcp/4001/p2p/12D3KooWService", "test")
	if force, _ := network.GetForceDirectDial(ctx); !force {
		t.Fatal("expected direct candidate stream context to force direct dial")
	}
	if allowLimited, _ := network.GetAllowLimitedConn(ctx); allowLimited {
		t.Fatal("direct candidate stream context must not allow limited relay connections")
	}
}

func TestServiceStreamContextAllowsLimitedForRelayAddress(t *testing.T) {
	ctx := serviceStreamContext("/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWService", "test")
	if allowLimited, _ := network.GetAllowLimitedConn(ctx); !allowLimited {
		t.Fatal("expected relayed candidate stream context to allow limited relay connections")
	}
	if force, _ := network.GetForceDirectDial(ctx); force {
		t.Fatal("relayed candidate stream context must not force direct dial")
	}
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
