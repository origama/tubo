package bridge

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	p2pping "github.com/libp2p/go-libp2p/p2p/protocol/ping"
	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/reachability"
	statspkg "github.com/origama/tubo/internal/runtime/stats"
	"golang.org/x/crypto/ssh"
)

type ConnectLeaseRefresher func(context.Context, grantspkg.ConnectRefreshLease) (grantspkg.ConnectAccessLease, error)

type Config struct {
	Listen, Seed, P2PListen, ServiceAddr, ServiceSeed, ServiceP2PListen, PrivateKeyFile, PrivateKeyB64 string
	RelayPeers                                                                                         []string
	Autorelay, HolePunching                                                                            bool
	ConnectGrant                                                                                       *capability.ConnectCapability // legacy/manual low-level connect capability support
	ConnectInviteToken                                                                                 string
	ConnectGrantPeers                                                                                  []string
	ConnectClusterID                                                                                   string
	ConnectNamespaceID                                                                                 string
	ConnectServiceID                                                                                   string
	ConnectMembershipCapability                                                                        *capability.MembershipCapability
	ConnectMembershipGrantToken                                                                        string
	ConnectAccessLease                                                                                 *grantspkg.ConnectAccessLease
	ConnectRefreshLease                                                                                *grantspkg.ConnectRefreshLease
	ConnectLeaseRefresher                                                                              ConnectLeaseRefresher
	ConnectRebindResolver                                                                              func(context.Context) (peer.AddrInfo, string, string, error)
	ConnectAuthorityPrivateKey                                                                         ed25519.PrivateKey
	ConnectAuthorityPrivateKeyFile                                                                     string
	SelectedAddr                                                                                       string
	SelectedPath                                                                                       string
	ServiceKind                                                                                        string
}
type RuntimeStatus struct {
	Status                  string
	Reason                  string
	ServiceKind             string
	Path                    string
	SelectedAddr            string
	SelectedPath            string
	SelectedPeerID          string
	ConnectAccessExpiresAt  *time.Time
	ConnectAccessExpiresIn  string
	ConnectRefreshExpiresAt *time.Time
	ConnectRefreshExpiresIn string
	LastTunnelError         string
	LastTunnelErrorAt       *time.Time
	LastTunnelHealthyAt     *time.Time
	PeerLivenessState       string
	PeerLivenessReason      string
	LastPingRTT             string
	LastPingAt              *time.Time
	LastPingError           string
	LastPingErrorAt         *time.Time
	ConsecutivePingFailures int
	NetworkState            string
	NetworkReason           string
	NetworkSince            *time.Time
	LastNetworkError        string
	LastNetworkErrorAt      *time.Time
	LastNetworkRecoveredAt  *time.Time
	LastRefreshError        string
	NextRefreshRetryAt      *time.Time
}

type peerPinger interface {
	Ping(context.Context, peer.ID) <-chan p2pping.Result
}

type countingReadCloser struct {
	rc     io.ReadCloser
	onRead func(int64)
}

func (c *countingReadCloser) Read(p []byte) (int, error) {
	n, err := c.rc.Read(p)
	if n > 0 && c.onRead != nil {
		c.onRead(int64(n))
	}
	return n, err
}

func (c *countingReadCloser) Close() error { return c.rc.Close() }

type countingWriter struct {
	w       io.Writer
	onWrite func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 && c.onWrite != nil {
		c.onWrite(int64(n))
	}
	return n, err
}

type App struct {
	cfg                      Config
	host                     host.Host
	service                  peer.AddrInfo
	server                   *http.Server
	listener                 net.Listener
	listenAddr               string
	stateMu                  sync.RWMutex
	connectMu                sync.Mutex
	connectLease             *grantspkg.ConnectAccessLease
	refreshingLease          bool
	refreshDone              chan struct{}
	lastRefreshError         string
	lastRefreshErrorAt       time.Time
	lastRefreshHealthyAt     time.Time
	lastRefreshErrorClass    reachability.ErrorClass
	nextRefreshRetryAt       time.Time
	consecutiveRefreshFails  int
	lastPeerPingRTT          time.Duration
	lastPeerPingAt           time.Time
	lastPeerPingError        string
	lastPeerPingErrorAt      time.Time
	lastPeerPingHealthyAt    time.Time
	peerPingConsecutiveFails int
	healthMu                 sync.RWMutex
	lastTunnelError          string
	lastTunnelErrorAt        time.Time
	lastTunnelHealthyAt      time.Time
	statusReporter           func(RuntimeStatus)
	stats                    *statspkg.Collector
	renewalReachability      *reachability.Manager
	peerPing                 peerPinger
	peerPingInterval         time.Duration
	peerPingTimeout          time.Duration
	peerPingFailureThreshold int
	openTunnelStream         func(context.Context) (network.Stream, error)
	startClientTCPTunnel     func(network.Stream, string, *protocol.ConnectProof) error
	rebindServiceFn          func(context.Context) (peer.AddrInfo, string, string, error)
	reconnectServiceFn       func(context.Context) error
	selectedAddr             string
	selectedPath             string
}

func LoadConfigFromEnv(g func(string) string) (Config, error) {
	return Config{Listen: first(g("BRIDGE_LISTEN"), "127.0.0.1:18081"), Seed: first(g("BRIDGE_SEED"), "bridge-demo-seed"), P2PListen: first(g("BRIDGE_P2P_LISTEN"), "/ip4/127.0.0.1/tcp/0"), ServiceAddr: g("SERVICE_ADDR"), ServiceSeed: g("SERVICE_SEED"), ServiceP2PListen: first(g("SERVICE_P2P_LISTEN"), "/ip4/127.0.0.1/tcp/40123"), PrivateKeyFile: g("LIBP2P_PRIVATE_NETWORK_KEY"), PrivateKeyB64: g("LIBP2P_PRIVATE_NETWORK_KEY_B64")}, nil
}
func New(ctx context.Context, cfg Config) (*App, error) {
	cfg.ServiceKind = string(cfgpkg.NormalizeServiceKind(cfgpkg.ServiceKind(cfg.ServiceKind), ""))
	var si peer.AddrInfo
	var err error
	if cfg.ServiceAddr != "" {
		si, err = p2p.AddrInfoFromString(cfg.ServiceAddr)
	} else if cfg.ServiceSeed != "" {
		si, err = p2p.AddrInfoFromListenAndSeed(cfg.ServiceP2PListen, cfg.ServiceSeed)
	} else {
		return nil, fmt.Errorf("set service_addr or service_seed")
	}
	if err != nil {
		return nil, err
	}
	psk, using, err := p2p.LoadPrivateNetworkPSK(cfg.PrivateKeyFile, cfg.PrivateKeyB64)
	if err != nil {
		return nil, err
	}
	relays := parseAddrInfos(cfg.RelayPeers)
	var opts []libp2p.Option
	if allowed, configured, err := p2p.LoadAllowedPeersFromEnv(); err != nil {
		return nil, err
	} else if configured {
		opts = append(opts, libp2p.ConnectionGater(p2p.NewPeerAllowlistConnectionGater(allowed)))
		log.Printf("peer allowlist enabled peers=%d", len(allowed))
	}
	if len(relays) > 0 && cfg.Autorelay {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(relays))
	}
	if cfg.HolePunching {
		opts = append(opts, libp2p.EnableHolePunching())
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions(cfg.P2PListen, cfg.Seed, psk, opts...)
	if err != nil {
		return nil, err
	}
	p2p.LogNetworkEvents(h, "bridge")
	if using {
		log.Printf("libp2p private network enabled")
	}
	if cfg.HolePunching {
		log.Printf("bridge hole punching enabled relay_peers=%d autorelay=%t", len(relays), cfg.Autorelay)
	}
	var connectLease *grantspkg.ConnectAccessLease
	if cfg.ConnectAccessLease != nil {
		lease := *cfg.ConnectAccessLease
		connectLease = &lease
		log.Printf("bridge connect access lease enabled cluster=%s namespace=%s service=%s expires_at=%s", lease.ClusterID, lease.NamespaceID, lease.ServiceID, lease.ExpiresAt.UTC().Format(time.RFC3339))
	}
	if cfg.ConnectGrant != nil {
		log.Printf("bridge legacy connect grants enabled cluster=%s namespace=%s service=%s", cfg.ConnectGrant.ClusterID, cfg.ConnectGrant.NamespaceID, cfg.ConnectGrant.ServiceID)
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.Connect(c, si); err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("connect service peer: %w", err)
	}
	if cfg.ConnectInviteToken != "" && cfg.ConnectRefreshLease == nil {
		authorityPriv := cfg.ConnectAuthorityPrivateKey
		if len(authorityPriv) == 0 && strings.TrimSpace(cfg.ConnectAuthorityPrivateKeyFile) != "" {
			log.Printf("bridge share invite local mint using authority key file %q", cfg.ConnectAuthorityPrivateKeyFile)
			var err error
			authorityPriv, err = loadConnectAuthorityPrivateKey(cfg.ConnectAuthorityPrivateKeyFile)
			if err != nil {
				log.Printf("bridge share invite local mint unavailable: %v", err)
			}
		}
		if len(authorityPriv) > 0 {
			log.Printf("bridge share invite using self-contained service endpoint addr=%s path=%s", cfg.SelectedAddr, cfg.SelectedPath)
			payload, parseErr := grantspkg.ParseAndVerifyServiceShareToken(cfg.ConnectInviteToken)
			if parseErr != nil {
				_ = h.Close()
				return nil, fmt.Errorf("share invite local mint parse failed: %w", parseErr)
			}
			clientPublicKey, pubErr := connectClientPublicKey(h)
			if pubErr != nil {
				_ = h.Close()
				return nil, fmt.Errorf("share invite local mint missing client public key: %w", pubErr)
			}
			artifacts, mintErr := grantspkg.BuildConnectLeaseArtifacts(authorityPriv, payload, clientPublicKey, grantspkg.DefaultConnectAccessLeaseTTL, grantspkg.DefaultConnectRefreshLeaseTTL)
			if mintErr != nil {
				_ = h.Close()
				return nil, fmt.Errorf("share invite local mint failed: %w", mintErr)
			}
			cfg.ConnectAccessLease = &artifacts.AccessLease
			cfg.ConnectRefreshLease = &artifacts.RefreshLease
			connectLease = &artifacts.AccessLease
			log.Printf("bridge share invite minted locally service=%s access_expires_at=%s refresh_expires_at=%s", artifacts.AccessLease.ServiceID, artifacts.AccessLease.ExpiresAt.UTC().Format(time.RFC3339), artifacts.RefreshLease.ExpiresAt.UTC().Format(time.RFC3339))
		}
		if cfg.ConnectAccessLease == nil && cfg.ConnectRefreshLease == nil {
			if len(cfg.ConnectGrantPeers) == 0 {
				_ = h.Close()
				return nil, fmt.Errorf("share invite does not contain a valid authorization path; ask the service owner to reissue the invite")
			}
			log.Printf("bridge share invite remote redeem via grant service peers=%d", len(cfg.ConnectGrantPeers))
			artifacts, err := redeemConnectInvite(ctx, h, cfg.ConnectGrantPeers, cfg.ConnectInviteToken)
			if err != nil {
				_ = h.Close()
				return nil, err
			}
			cfg.ConnectAccessLease = &artifacts.AccessLease
			cfg.ConnectRefreshLease = &artifacts.RefreshLease
			connectLease = &artifacts.AccessLease
			log.Printf("bridge share invite redeemed service=%s access_expires_at=%s refresh_expires_at=%s", artifacts.AccessLease.ServiceID, artifacts.AccessLease.ExpiresAt.UTC().Format(time.RFC3339), artifacts.RefreshLease.ExpiresAt.UTC().Format(time.RFC3339))
		}
	}
	if cfg.ConnectAccessLease == nil && cfg.ConnectRefreshLease == nil && cfg.ConnectGrant == nil && cfg.ConnectInviteToken == "" && len(cfg.ConnectGrantPeers) > 0 && cfg.ConnectClusterID != "" && cfg.ConnectNamespaceID != "" && cfg.ConnectServiceID != "" {
		artifacts, err := requestDirectConnectLease(ctx, h, cfg.ConnectGrantPeers, cfg.ConnectClusterID, cfg.ConnectNamespaceID, cfg.ConnectServiceID, cfg.ConnectMembershipCapability, cfg.ConnectMembershipGrantToken)
		if err != nil {
			_ = h.Close()
			return nil, err
		}
		cfg.ConnectAccessLease = &artifacts.AccessLease
		cfg.ConnectRefreshLease = &artifacts.RefreshLease
		connectLease = &artifacts.AccessLease
		log.Printf("bridge discovery connect lease acquired cluster=%s namespace=%s service=%s access_expires_at=%s refresh_expires_at=%s", artifacts.AccessLease.ClusterID, artifacts.AccessLease.NamespaceID, artifacts.AccessLease.ServiceID, artifacts.AccessLease.ExpiresAt.UTC().Format(time.RFC3339), artifacts.RefreshLease.ExpiresAt.UTC().Format(time.RFC3339))
	}
	pingService := p2pping.NewPingService(h)
	app := &App{cfg: cfg, host: h, service: si, connectLease: connectLease, rebindServiceFn: cfg.ConnectRebindResolver, selectedAddr: cfg.SelectedAddr, selectedPath: cfg.SelectedPath, stats: statspkg.New(statspkg.Snapshot{Role: "connect", Kind: cfg.ServiceKind, Service: cfg.ConnectServiceID, ServiceID: si.ID.String(), Path: cfg.SelectedPath, Status: "running"}), renewalReachability: reachability.NewManager(reachability.ManagerConfig{Buffer: 4}), peerPing: pingService, peerPingInterval: 20 * time.Second, peerPingTimeout: 5 * time.Second, peerPingFailureThreshold: 3}
	app.reportStatus()
	return app, nil
}

func serviceAddrUsesRelay(addr string) bool {
	return strings.Contains(addr, "/p2p-circuit")
}

func tcpConnectAdminListenAddr(listenAddr string) (string, bool) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", false
	}
	p, err := strconv.Atoi(port)
	if err != nil || p >= 65535 {
		return "", false
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, strconv.Itoa(p+1)), true
}

func serviceStreamContext(serviceAddr, reason string) context.Context {
	ctx := context.Background()
	if serviceAddrUsesRelay(serviceAddr) {
		return network.WithAllowLimitedConn(ctx, reason)
	}
	return network.WithForceDirectDial(ctx, reason)
}
func (a *App) Start(ctx context.Context) error {
	defer a.host.Close()
	log.Printf("bridge peer_id=%s service_kind=%s", a.host.ID(), a.cfg.ServiceKind)
	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen bridge: %w", err)
	}
	listenAddr := ln.Addr().String()
	a.stateMu.Lock()
	a.listener = ln
	a.listenAddr = listenAddr
	a.stateMu.Unlock()
	if a.cfg.ConnectRefreshLease != nil {
		go a.startConnectLeaseRenewal(ctx)
	}
	if a.peerPing != nil {
		go a.startPeerPingLoop(ctx)
	}
	if a.connectPathTransitionMonitoringEnabled() {
		go a.watchConnectPathTransitions(ctx)
	}
	if a.cfg.ServiceKind == string(cfgpkg.ServiceKindTCP) {
		adminAddr, ok := tcpConnectAdminListenAddr(listenAddr)
		if !ok {
			adminAddr = "127.0.0.1:0"
		}
		adminLn, err := net.Listen("tcp", adminAddr)
		if err != nil {
			return fmt.Errorf("listen bridge admin: %w", err)
		}
		adminServer := &http.Server{Handler: a.mux()}
		go func() {
			log.Printf("client bridge admin listening on %s", adminLn.Addr().String())
			if err := adminServer.Serve(adminLn); err != nil && err != http.ErrServerClosed {
				log.Printf("bridge admin server: %v", err)
			}
		}()
		go a.serveTCP(ctx, ln)
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = adminServer.Shutdown(shutdownCtx)
		_ = adminLn.Close()
		return ln.Close()
	}
	server := &http.Server{Addr: a.cfg.Listen, Handler: a.mux()}
	a.stateMu.Lock()
	a.server = server
	a.stateMu.Unlock()
	go func() {
		log.Printf("client bridge listening on %s", listenAddr)
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("bridge server: %v", err)
		}
	}()
	<-ctx.Done()
	sd, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	a.stateMu.RLock()
	server = a.server
	a.stateMu.RUnlock()
	return server.Shutdown(sd)
}

func (a *App) ListenAddr() string {
	a.stateMu.RLock()
	listenAddr := a.listenAddr
	a.stateMu.RUnlock()
	if listenAddr != "" {
		return listenAddr
	}
	return a.cfg.Listen
}

func (a *App) serveTCP(ctx context.Context, ln net.Listener) {
	log.Printf("client tcp bridge listening on %s", ln.Addr().String())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("bridge tcp accept: %v", err)
			continue
		}
		go a.handleTCPConn(conn)
	}
}

func (a *App) handleTCPConn(conn net.Conn) {
	defer conn.Close()
	start := time.Now()
	var opErr error
	if a.stats != nil {
		a.stats.Begin()
		defer func() { a.stats.Finish(opErr) }()
	}
	s, err := a.establishTCPTunnel(conn.RemoteAddr().String())
	if err != nil {
		opErr = err
		a.markTunnelDegraded(err)
		log.Printf("bridge tcp establish tunnel local=%s err=%v", conn.RemoteAddr(), err)
		return
	}
	defer s.Close()
	sent, received, err := p2p.ProxyTCPStream(conn, s, a.stats)
	if a.stats != nil {
		a.stats.Observe(0, time.Since(start))
	}
	if err != nil {
		opErr = err
		a.markTunnelDegraded(err)
		log.Printf("bridge tcp proxy closed local=%s bytes_in=%d bytes_out=%d err=%v duration=%s", conn.RemoteAddr(), received, sent, err, time.Since(start))
		return
	}
	log.Printf("bridge tcp proxy completed local=%s bytes_in=%d bytes_out=%d duration=%s", conn.RemoteAddr(), received, sent, time.Since(start))
}

const bridgeSelfHealTimeout = 2 * time.Second
const connectRefreshMinUsefulLifetime = 5 * time.Second
const connectRefreshFailureCooldown = 5 * time.Second
const connectRefreshMinExtension = time.Second

// connectRefreshBackoff returns an exponential backoff duration for consecutive
// refresh/rollover failures. The backoff starts at connectRefreshFailureCooldown
// (5 s), doubles on each attempt, and is capped at 120 s. A ±20 % jitter is
// added to avoid thundering-herd when multiple connects refresh simultaneously.
// The 120 s cap ensures the client recovers well before a server-side deny-cache
// TTL (default 30 s) can be perpetually renewed by rapid retries.
func connectRefreshBackoff(consecutiveFailures int) time.Duration {
	const base = connectRefreshFailureCooldown
	const maxBackoff = 120 * time.Second
	if consecutiveFailures <= 0 {
		return base
	}
	mult := math.Pow(2, float64(consecutiveFailures-1))
	d := time.Duration(float64(base) * mult)
	if d > maxBackoff {
		d = maxBackoff
	}
	// ±20% jitter
	jitter := time.Duration(float64(d) * (0.8 + 0.4*rand.Float64()))
	if jitter < base {
		jitter = base
	}
	return jitter
}

func (a *App) establishTCPTunnel(localAddr string) (network.Stream, error) {
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		streamCtx := network.WithAllowLimitedConn(context.Background(), "bridge tcp tunnel stream")
		s, err := a.openServiceTunnelStream(streamCtx)
		if err == nil {
			proof, proofErr := a.connectProof()
			if proofErr != nil {
				a.markTunnelDegraded(proofErr)
				_ = s.Close()
				return nil, fmt.Errorf("connect proof: %w", proofErr)
			}
			if startErr := a.startTCPTunnelStream(s, proof); startErr == nil {
				a.markTunnelHealthy()
				if attempt > 1 {
					log.Printf("bridge tcp self-heal recovered local=%s", localAddr)
				}
				return s, nil
			} else {
				_ = s.Close()
				lastErr = fmt.Errorf("start tunnel: %w", startErr)
			}
		} else {
			lastErr = fmt.Errorf("open stream: %w", err)
		}
		if attempt == 1 {
			if healErr := a.selfHealServicePath(localAddr, lastErr); healErr != nil {
				return nil, fmt.Errorf("%v; self-heal failed: %w", lastErr, healErr)
			}
		}
	}
	a.markTunnelDegraded(lastErr)
	return nil, lastErr
}

func (a *App) markTunnelHealthy() {
	now := time.Now()
	a.healthMu.Lock()
	previousError := a.lastTunnelError
	previousErrorAt := a.lastTunnelErrorAt
	previousHealthyAt := a.lastTunnelHealthyAt
	a.lastTunnelHealthyAt = now
	a.lastTunnelError = ""
	a.lastTunnelErrorAt = time.Time{}
	a.healthMu.Unlock()
	a.connectMu.Lock()
	a.lastRefreshHealthyAt = now
	a.clearPeerPingStateLocked(now)
	a.connectMu.Unlock()
	if previousError != "" && (previousHealthyAt.IsZero() || previousHealthyAt.Before(previousErrorAt)) {
		log.Printf("bridge network recovered reason=network_recovered message=%q", "network reachability recovered")
		a.recordRenewalReachabilitySuccess()
	}
	a.reportStatus()
}

func (a *App) clearPeerPingStateLocked(now time.Time) {
	a.lastPeerPingHealthyAt = now
	a.peerPingConsecutiveFails = 0
}

func (a *App) markTunnelDegraded(err error) {
	if err == nil {
		return
	}
	now := time.Now()
	classification := reachability.Classify(err)
	a.healthMu.Lock()
	previousError := a.lastTunnelError
	previousHealthyAt := a.lastTunnelHealthyAt
	previousErrorAt := a.lastTunnelErrorAt
	a.lastTunnelError = err.Error()
	a.lastTunnelErrorAt = now
	a.healthMu.Unlock()
	if previousError == "" || previousError != err.Error() || (!previousHealthyAt.IsZero() && previousHealthyAt.After(previousErrorAt)) {
		log.Printf("bridge network degraded reason=%s message=%q", classification.Reason, err.Error())
		a.recordRenewalReachabilityFailure(err)
	}
	a.reportStatus()
}

func (a *App) startPeerPingLoop(ctx context.Context) {
	if a.peerPing == nil || a.peerPingInterval <= 0 {
		return
	}
	interval := a.peerPingInterval
	if interval < time.Second {
		interval = time.Second
	}
	for {
		if ctx.Err() != nil {
			return
		}
		lastActivity := a.lastPeerActivityAt()
		wait := interval
		if !lastActivity.IsZero() {
			next := lastActivity.Add(interval)
			if d := time.Until(next); d > 0 {
				wait = d
			} else {
				wait = 0
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		peerID, ok := a.selectedPeerID()
		if !ok {
			continue
		}
		a.runPeerPingOnce(ctx, peerID)
	}
}

func (a *App) selectedPeerID() (peer.ID, bool) {
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	if a.service.ID == "" {
		return "", false
	}
	return a.service.ID, true
}

func (a *App) lastPeerActivityAt() time.Time {
	a.healthMu.RLock()
	lastTunnelHealthyAt := a.lastTunnelHealthyAt
	a.healthMu.RUnlock()
	a.connectMu.Lock()
	lastRefreshHealthyAt := a.lastRefreshHealthyAt
	lastPeerPingHealthyAt := a.lastPeerPingHealthyAt
	a.connectMu.Unlock()
	latest := lastRefreshHealthyAt
	if lastTunnelHealthyAt.After(latest) {
		latest = lastTunnelHealthyAt
	}
	if lastPeerPingHealthyAt.After(latest) {
		latest = lastPeerPingHealthyAt
	}
	return latest
}

func (a *App) runPeerPingOnce(ctx context.Context, peerID peer.ID) {
	timeout := a.peerPingTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := a.peerPing.Ping(pingCtx, peerID)
	select {
	case res, ok := <-results:
		if !ok {
			a.recordPeerPingFailure(peerID, errors.New("peer ping stream closed without result"))
			return
		}
		if res.Error != nil {
			a.recordPeerPingFailure(peerID, res.Error)
			return
		}
		a.recordPeerPingSuccess(peerID, res.RTT)
	case <-pingCtx.Done():
		a.recordPeerPingFailure(peerID, pingCtx.Err())
	}
}

func (a *App) recordPeerPingSuccess(peerID peer.ID, rtt time.Duration) {
	now := time.Now().UTC()
	a.connectMu.Lock()
	previousFails := a.peerPingConsecutiveFails
	a.lastPeerPingAt = now
	a.lastPeerPingRTT = rtt
	a.lastPeerPingHealthyAt = now
	a.peerPingConsecutiveFails = 0
	a.connectMu.Unlock()
	if previousFails > 0 {
		log.Printf("bridge peer ping recovered peer=%s rtt=%s", peerID, rtt.Round(time.Millisecond))
	}
	a.reportStatus()
}

func (a *App) recordPeerPingFailure(peerID peer.ID, err error) {
	if err == nil {
		return
	}
	now := time.Now().UTC()
	a.connectMu.Lock()
	previousFails := a.peerPingConsecutiveFails
	a.lastPeerPingAt = now
	a.lastPeerPingError = err.Error()
	a.lastPeerPingErrorAt = now
	a.peerPingConsecutiveFails++
	count := a.peerPingConsecutiveFails
	threshold := a.peerPingFailureThreshold
	a.connectMu.Unlock()
	if threshold <= 0 {
		threshold = 3
	}
	if count >= threshold && previousFails < threshold {
		log.Printf("bridge peer ping degraded peer=%s reason=peer_unreachable message=%q", peerID, err.Error())
	}
	a.reportStatus()
}

func (a *App) recordRenewalReachabilityFailure(err error) {
	if a.renewalReachability == nil || err == nil {
		return
	}
	classification := reachability.Classify(err)
	switch classification.Class {
	case reachability.ErrorTransient, reachability.ErrorUnknown:
		a.renewalReachability.RecordFailure(reachability.FailureKindNetwork, err)
	}
}

func (a *App) recordRenewalReachabilitySuccess() {
	if a.renewalReachability == nil {
		return
	}
	a.renewalReachability.RecordSuccess(reachability.SuccessKindNetwork)
}

func (a *App) renewalRecoveryEvents() <-chan reachability.Event {
	if a == nil || a.renewalReachability == nil {
		return nil
	}
	return a.renewalReachability.Events()
}

func (a *App) tunnelHealth() (bool, string) {
	a.healthMu.RLock()
	defer a.healthMu.RUnlock()
	if a.lastTunnelError == "" {
		return true, "ok"
	}
	if !a.lastTunnelHealthyAt.IsZero() && a.lastTunnelHealthyAt.After(a.lastTunnelErrorAt) {
		return true, "ok"
	}
	return false, fmt.Sprintf("degraded: %s", a.lastTunnelError)
}

type networkStatusSnapshot struct {
	State           string
	Reason          string
	Since           *time.Time
	LastError       string
	LastErrorAt     *time.Time
	LastRecoveredAt *time.Time
}

func (a *App) networkStatus() networkStatusSnapshot {
	a.healthMu.RLock()
	lastTunnelError := a.lastTunnelError
	lastTunnelErrorAt := a.lastTunnelErrorAt
	lastTunnelHealthyAt := a.lastTunnelHealthyAt
	a.healthMu.RUnlock()
	if lastTunnelError == "" || (!lastTunnelHealthyAt.IsZero() && lastTunnelHealthyAt.After(lastTunnelErrorAt)) {
		snap := networkStatusSnapshot{State: string(reachability.StateHealthy), Reason: string(reachability.StateHealthy)}
		if !lastTunnelHealthyAt.IsZero() {
			t := lastTunnelHealthyAt.UTC()
			snap.Since = &t
			snap.LastRecoveredAt = &t
		}
		return snap
	}
	classification := reachability.Classify(errors.New(lastTunnelError))
	snap := networkStatusSnapshot{State: string(classification.State), Reason: classification.Reason, LastError: lastTunnelError}
	if snap.Reason == "" {
		snap.Reason = lastTunnelError
	}
	if !lastTunnelErrorAt.IsZero() {
		t := lastTunnelErrorAt.UTC()
		snap.Since = &t
		snap.LastErrorAt = &t
	}
	return snap
}

func (a *App) openServiceTunnelStream(ctx context.Context) (network.Stream, error) {
	if a.openTunnelStream != nil {
		return a.openTunnelStream(ctx)
	}
	peerID, ok := a.selectedPeerID()
	if !ok {
		return nil, fmt.Errorf("bridge service peer unavailable")
	}
	return a.host.NewStream(ctx, peerID, p2p.ProtocolID)
}

func (a *App) startTCPTunnelStream(s network.Stream, proof *protocol.ConnectProof) error {
	if a.startClientTCPTunnel != nil {
		return a.startClientTCPTunnel(s, "bridge", proof)
	}
	return p2p.StartClientTCPTunnel(s, "bridge", proof)
}

func (a *App) selfHealServicePath(localAddr string, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), bridgeSelfHealTimeout)
	defer cancel()
	log.Printf("bridge tcp self-heal local=%s cause=%v", localAddr, cause)
	if a.reconnectServiceFn != nil {
		return a.reconnectServiceFn(ctx)
	}
	return a.reconnectService(ctx)
}

func (a *App) reconnectService(ctx context.Context) error {
	if a.host == nil {
		return fmt.Errorf("bridge host unavailable")
	}
	a.connectMu.Lock()
	oldPeer := a.service.ID.String()
	oldAddr := a.cfg.ServiceAddr
	service := a.service
	if a.rebindServiceFn != nil {
		newService, newAddr, newPath, err := a.rebindServiceFn(ctx)
		if err != nil {
			a.connectMu.Unlock()
			return fmt.Errorf("rebind service peer: %w", err)
		}
		if newService.ID != "" {
			a.service = newService
			service = newService
		}
		if strings.TrimSpace(newAddr) != "" {
			a.cfg.ServiceAddr = newAddr
			a.selectedAddr = newAddr
		}
		if strings.TrimSpace(newPath) != "" {
			a.selectedPath = newPath
		}
	}
	newPeer := a.service.ID.String()
	newAddr := a.cfg.ServiceAddr
	a.connectMu.Unlock()
	if a.rebindServiceFn != nil {
		log.Printf("bridge tcp rebind service_id=%s old_peer=%s new_peer=%s old_addr=%s new_addr=%s reason=stream_failed", a.cfg.ConnectServiceID, oldPeer, newPeer, oldAddr, newAddr)
	}
	_ = a.host.Network().ClosePeer(service.ID)
	connectCtx := network.WithAllowLimitedConn(context.Background(), "bridge tcp self-heal reconnect")
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		connectCtx, cancel = context.WithDeadline(connectCtx, deadline)
		defer cancel()
	}
	if err := a.host.Connect(connectCtx, a.service); err != nil {
		return fmt.Errorf("reconnect service peer: %w", err)
	}
	a.reportStatus()
	return nil
}

type statusSnapshot struct {
	Status                  string     `json:"status"`
	Reason                  string     `json:"reason,omitempty"`
	ServiceKind             string     `json:"service_kind,omitempty"`
	Path                    string     `json:"path,omitempty"`
	SelectedAddr            string     `json:"selected_addr,omitempty"`
	SelectedPath            string     `json:"selected_path,omitempty"`
	SelectedPeerID          string     `json:"selected_peer_id,omitempty"`
	ConnectAccessExpiresAt  *time.Time `json:"connect_access_expires_at,omitempty"`
	ConnectAccessExpiresIn  string     `json:"connect_access_expires_in,omitempty"`
	ConnectRefreshExpiresAt *time.Time `json:"connect_refresh_expires_at,omitempty"`
	ConnectRefreshExpiresIn string     `json:"connect_refresh_expires_in,omitempty"`
	LastTunnelError         string     `json:"last_tunnel_error,omitempty"`
	LastTunnelErrorAt       *time.Time `json:"last_tunnel_error_at,omitempty"`
	LastTunnelHealthyAt     *time.Time `json:"last_tunnel_healthy_at,omitempty"`
	PeerLivenessState       string     `json:"peer_liveness_state,omitempty"`
	PeerLivenessReason      string     `json:"peer_liveness_reason,omitempty"`
	LastPingRTT             string     `json:"last_ping_rtt,omitempty"`
	LastPingAt              *time.Time `json:"last_ping_at,omitempty"`
	LastPingError           string     `json:"last_ping_error,omitempty"`
	LastPingErrorAt         *time.Time `json:"last_ping_error_at,omitempty"`
	ConsecutivePingFailures int        `json:"consecutive_ping_failures,omitempty"`
	NetworkState            string     `json:"network_state,omitempty"`
	NetworkReason           string     `json:"network_reason,omitempty"`
	NetworkSince            *time.Time `json:"network_since,omitempty"`
	LastNetworkError        string     `json:"last_network_error,omitempty"`
	LastNetworkErrorAt      *time.Time `json:"last_network_error_at,omitempty"`
	LastNetworkRecoveredAt  *time.Time `json:"last_network_recovered_at,omitempty"`
}

func (a *App) currentPath() string {
	if a.host == nil {
		return ""
	}
	a.connectMu.Lock()
	serviceID := a.service.ID
	selectedPath := a.selectedPath
	serviceAddr := a.cfg.ServiceAddr
	a.connectMu.Unlock()
	conns := a.host.Network().ConnsToPeer(serviceID)
	hasRelay := false
	for _, conn := range conns {
		if strings.Contains(conn.RemoteMultiaddr().String(), "/p2p-circuit") {
			hasRelay = true
			continue
		}
		return "direct"
	}
	if hasRelay {
		return "relayed"
	}
	if selectedPath != "" {
		return selectedPath
	}
	if strings.Contains(serviceAddr, "/p2p-circuit") {
		return "relayed"
	}
	if serviceAddr != "" {
		return "direct"
	}
	return ""
}

func formatRemaining(until time.Time, now time.Time) string {
	if until.IsZero() {
		return ""
	}
	d := until.Sub(now)
	if d <= 0 {
		return "expired"
	}
	return d.Round(time.Second).String()
}

func (a *App) statusSnapshot(now time.Time) statusSnapshot {
	ok, msg := a.tunnelHealth()
	a.connectMu.Lock()
	selAddr := a.selectedAddr
	selPath := a.selectedPath
	selPeer := a.service.ID.String()
	a.connectMu.Unlock()
	snap := statusSnapshot{Status: "running", ServiceKind: a.cfg.ServiceKind, Path: a.currentPath(), SelectedAddr: selAddr, SelectedPath: selPath, SelectedPeerID: selPeer}
	if !ok {
		snap.Status = "degraded"
		snap.Reason = msg
	}
	network := a.networkStatus()
	snap.NetworkState = network.State
	snap.NetworkReason = network.Reason
	snap.NetworkSince = network.Since
	snap.LastNetworkError = network.LastError
	snap.LastNetworkErrorAt = network.LastErrorAt
	snap.LastNetworkRecoveredAt = network.LastRecoveredAt
	a.healthMu.RLock()
	if a.lastTunnelError != "" {
		snap.LastTunnelError = a.lastTunnelError
	}
	if !a.lastTunnelErrorAt.IsZero() {
		t := a.lastTunnelErrorAt
		snap.LastTunnelErrorAt = &t
	}
	if !a.lastTunnelHealthyAt.IsZero() {
		t := a.lastTunnelHealthyAt
		snap.LastTunnelHealthyAt = &t
	}
	a.healthMu.RUnlock()
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	if a.connectLease != nil {
		t := a.connectLease.ExpiresAt.UTC()
		snap.ConnectAccessExpiresAt = &t
		snap.ConnectAccessExpiresIn = formatRemaining(t, now)
	}
	if a.cfg.ConnectRefreshLease != nil {
		t := a.cfg.ConnectRefreshLease.ExpiresAt.UTC()
		snap.ConnectRefreshExpiresAt = &t
		snap.ConnectRefreshExpiresIn = formatRemaining(t, now)
		if rolloverDue := connectRefreshLeaseNeedsRollover(*a.cfg.ConnectRefreshLease, now); rolloverDue && !a.connectCanRolloverLocked() {
			snap.Status = "degraded"
			snap.Reason = connectRefreshLeaseFreshTokenReason(*a.cfg.ConnectRefreshLease, now)
		}
	} else if a.connectLease != nil {
		if !now.Before(a.connectLease.ExpiresAt.UTC()) {
			snap.Status = "degraded"
			snap.Reason = "connect access lease expired; ask the service owner for a fresh token/invite"
		}
	}
	if a.lastRefreshError != "" && snap.Reason == "" {
		if a.lastRefreshHealthyAt.IsZero() || a.lastRefreshHealthyAt.Before(a.lastRefreshErrorAt) {
			snap.Status = "degraded"
			snap.Reason = a.lastRefreshError
		}
	}
	if !a.lastPeerPingAt.IsZero() {
		t := a.lastPeerPingAt
		snap.LastPingAt = &t
	}
	if !a.lastPeerPingErrorAt.IsZero() {
		t := a.lastPeerPingErrorAt
		snap.LastPingErrorAt = &t
	}
	snap.LastPingError = a.lastPeerPingError
	snap.ConsecutivePingFailures = a.peerPingConsecutiveFails
	if a.lastPeerPingRTT > 0 {
		snap.LastPingRTT = a.lastPeerPingRTT.Round(time.Millisecond).String()
	}
	threshold := a.peerPingFailureThreshold
	if threshold <= 0 {
		threshold = 3
	}
	if a.peerPingConsecutiveFails >= threshold && a.lastPeerPingError != "" && (a.lastPeerPingHealthyAt.IsZero() || a.lastPeerPingHealthyAt.Before(a.lastPeerPingErrorAt)) {
		snap.PeerLivenessState = "degraded"
		snap.PeerLivenessReason = fmt.Sprintf("peer_unreachable: %s after %d consecutive failures", a.lastPeerPingError, a.peerPingConsecutiveFails)
	} else if a.peerPingConsecutiveFails > 0 && (a.lastPeerPingHealthyAt.IsZero() || a.lastPeerPingHealthyAt.Before(a.lastPeerPingErrorAt)) {
		snap.PeerLivenessState = "suspect"
		snap.PeerLivenessReason = fmt.Sprintf("peer_unreachable: %s after %d consecutive failures", a.lastPeerPingError, a.peerPingConsecutiveFails)
	} else if !a.lastPeerPingAt.IsZero() {
		snap.PeerLivenessState = "healthy"
		if snap.LastPingRTT != "" {
			snap.PeerLivenessReason = fmt.Sprintf("last ping RTT %s", snap.LastPingRTT)
		}
	} else {
		snap.PeerLivenessState = "unknown"
	}
	if snap.Reason == "" && snap.PeerLivenessState == "degraded" {
		snap.Status = "degraded"
		snap.Reason = snap.PeerLivenessReason
	}
	return snap
}

func connectLeaseRenewBefore(lease grantspkg.ConnectAccessLease) time.Duration {
	if lease.ExpiresAt.IsZero() {
		return time.Second
	}
	ttl := lease.ExpiresAt.UTC().Sub(lease.IssuedAt.UTC())
	renewBefore := ttl / 3
	if renewBefore < time.Second {
		renewBefore = time.Second
	}
	if renewBefore > 5*time.Minute {
		renewBefore = 5 * time.Minute
	}
	return renewBefore
}

func connectRefreshLeaseNeedsRollover(refresh grantspkg.ConnectRefreshLease, now time.Time) bool {
	if refresh.ExpiresAt.IsZero() {
		return false
	}
	return !now.Before(refresh.ExpiresAt.UTC()) || time.Until(refresh.ExpiresAt.UTC()) <= connectRefreshMinUsefulLifetime
}

func connectRefreshLeaseFreshTokenReason(refresh grantspkg.ConnectRefreshLease, now time.Time) string {
	if refresh.ExpiresAt.IsZero() || !now.Before(refresh.ExpiresAt.UTC()) {
		return "connect refresh lease expired; ask the service owner for a fresh token/invite"
	}
	return "connect refresh lease is near expiry; ask the service owner for a fresh token/invite"
}

func (a *App) connectCanRolloverLocked() bool {
	return len(a.cfg.ConnectGrantPeers) > 0 && strings.TrimSpace(a.cfg.ConnectClusterID) != "" && strings.TrimSpace(a.cfg.ConnectNamespaceID) != "" && strings.TrimSpace(a.cfg.ConnectServiceID) != "" && (a.cfg.ConnectMembershipCapability != nil || strings.TrimSpace(a.cfg.ConnectMembershipGrantToken) != "")
}

func (a *App) applyConnectLeaseArtifactsLocked(artifacts grantspkg.ConnectLeaseArtifacts) {
	access := artifacts.AccessLease
	refresh := artifacts.RefreshLease
	a.connectLease = &access
	a.cfg.ConnectAccessLease = &access
	a.cfg.ConnectRefreshLease = &refresh
	now := time.Now().UTC()
	a.lastRefreshHealthyAt = now
	a.clearPeerPingStateLocked(now)
	a.lastRefreshErrorClass = reachability.ErrorNone
	a.nextRefreshRetryAt = time.Time{}
	a.consecutiveRefreshFails = 0
}

func ConnectPathTransitionMessage(previous, current string) (string, bool) {
	prev := strings.TrimSpace(previous)
	curr := strings.TrimSpace(current)
	if prev == "" || curr == "" || prev == curr {
		return "", false
	}
	switch {
	case prev == "relayed" && curr == "direct":
		return "connect path upgraded to direct", true
	case prev == "direct" && curr == "relayed":
		return "connect path downgraded to relayed", true
	default:
		return fmt.Sprintf("connect path changed from %s to %s", prev, curr), true
	}
}

func connectLeaseFailureIsTerminal(err error) bool {
	switch reachability.Classify(err).Class {
	case reachability.ErrorAuth, reachability.ErrorConfig:
		return true
	default:
		return false
	}
}

func connectLeaseFailureRetryAt(err error, current *grantspkg.ConnectAccessLease, refresh grantspkg.ConnectRefreshLease, now time.Time) time.Time {
	retryAt := now.Add(connectRefreshFailureCooldown)
	switch reachability.Classify(err).Class {
	case reachability.ErrorAuth, reachability.ErrorConfig:
		if current != nil && now.Before(current.ExpiresAt.UTC()) {
			retryAt = current.ExpiresAt.UTC()
		} else if !refresh.ExpiresAt.IsZero() {
			retryAt = refresh.ExpiresAt.UTC()
		}
	}
	return retryAt
}

func (a *App) refreshRetryCanWakeOnRecovery() bool {
	a.connectMu.Lock()
	class := a.lastRefreshErrorClass
	a.connectMu.Unlock()
	switch class {
	case reachability.ErrorTransient, reachability.ErrorUnknown:
		return true
	default:
		return false
	}
}

func (a *App) clearRefreshRetryAfterRecovery() bool {
	if !a.refreshRetryCanWakeOnRecovery() {
		return false
	}
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	if a.lastRefreshErrorClass != reachability.ErrorTransient && a.lastRefreshErrorClass != reachability.ErrorUnknown {
		return false
	}
	a.nextRefreshRetryAt = time.Time{}
	return true
}

func (a *App) waitForRenewalRetry(ctx context.Context, nextRetry time.Time) bool {
	wait := time.Until(nextRetry)
	if wait <= 0 {
		return false
	}
	if a.refreshRetryCanWakeOnRecovery() {
		return reachability.WaitForRecovered(ctx, a.renewalRecoveryEvents(), wait)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (a *App) startConnectLeaseRenewal(ctx context.Context) {
	for {
		a.connectMu.Lock()
		refresh := a.cfg.ConnectRefreshLease
		lease := a.connectLease
		nextRetry := a.nextRefreshRetryAt
		canRollover := a.connectCanRolloverLocked()
		a.connectMu.Unlock()
		if refresh == nil {
			return
		}
		now := time.Now().UTC()
		if nextRetry.After(now) {
			if a.waitForRenewalRetry(ctx, nextRetry) {
				a.clearRefreshRetryAfterRecovery()
			}
			if ctx.Err() != nil {
				return
			}
			now = time.Now().UTC()
		}
		rolloverDue := connectRefreshLeaseNeedsRollover(*refresh, now)
		if rolloverDue && !canRollover {
			err := errors.New(connectRefreshLeaseFreshTokenReason(*refresh, now))
			a.recordRefreshFailure(err, refresh.ExpiresAt.UTC())
			a.markTunnelDegraded(err)
			return
		}
		var wait time.Duration
		if rolloverDue || lease == nil || connectAccessLeaseNeedsRefresh(*lease, now) {
			wait = 0
		} else {
			renewBefore := connectLeaseRenewBefore(*lease)
			wait = time.Until(lease.ExpiresAt.UTC().Add(-renewBefore))
		}
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := a.ensureConnectAccessLease(refreshCtx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			a.markTunnelDegraded(err)
			log.Printf("bridge connect lease renewal failed: %v", err)
			continue
		}
	}
}

func (a *App) CurrentRuntimeStatus() RuntimeStatus {
	snap := a.statusSnapshot(time.Now().UTC())
	a.connectMu.Lock()
	lastRefreshError := a.lastRefreshError
	nextRefreshRetryAt := a.nextRefreshRetryAt
	a.connectMu.Unlock()
	selectedPeerID, _ := a.selectedPeerID()
	return RuntimeStatus{
		Status:                  snap.Status,
		Reason:                  snap.Reason,
		ServiceKind:             snap.ServiceKind,
		Path:                    snap.Path,
		SelectedAddr:            snap.SelectedAddr,
		SelectedPath:            snap.SelectedPath,
		SelectedPeerID:          selectedPeerID.String(),
		ConnectAccessExpiresAt:  snap.ConnectAccessExpiresAt,
		ConnectAccessExpiresIn:  snap.ConnectAccessExpiresIn,
		ConnectRefreshExpiresAt: snap.ConnectRefreshExpiresAt,
		ConnectRefreshExpiresIn: snap.ConnectRefreshExpiresIn,
		LastTunnelError:         snap.LastTunnelError,
		LastTunnelErrorAt:       snap.LastTunnelErrorAt,
		LastTunnelHealthyAt:     snap.LastTunnelHealthyAt,
		PeerLivenessState:       snap.PeerLivenessState,
		PeerLivenessReason:      snap.PeerLivenessReason,
		LastPingRTT:             snap.LastPingRTT,
		LastPingAt:              snap.LastPingAt,
		LastPingError:           snap.LastPingError,
		LastPingErrorAt:         snap.LastPingErrorAt,
		ConsecutivePingFailures: snap.ConsecutivePingFailures,
		NetworkState:            snap.NetworkState,
		NetworkReason:           snap.NetworkReason,
		NetworkSince:            snap.NetworkSince,
		LastNetworkError:        snap.LastNetworkError,
		LastNetworkErrorAt:      snap.LastNetworkErrorAt,
		LastNetworkRecoveredAt:  snap.LastNetworkRecoveredAt,
		LastRefreshError:        lastRefreshError,
		NextRefreshRetryAt: func() *time.Time {
			if nextRefreshRetryAt.IsZero() {
				return nil
			}
			t := nextRefreshRetryAt.UTC()
			return &t
		}(),
	}
}

func (a *App) SetStatusReporter(report func(RuntimeStatus)) {
	a.statusReporter = report
	a.reportStatus()
}

func (a *App) reportStatus() {
	snap := a.CurrentRuntimeStatus()
	if a.stats != nil {
		a.stats.SetMeta(statspkg.Snapshot{Role: "connect", Kind: snap.ServiceKind, Service: a.cfg.ConnectServiceID, ServiceID: snap.SelectedPeerID, Path: snap.Path, Status: snap.Status, Reason: snap.Reason})
	}
	if a.statusReporter != nil {
		a.statusReporter(snap)
	}
}

func (a *App) connectPathTransitionMonitoringEnabled() bool {
	return a.cfg.ConnectAccessLease != nil || a.cfg.ConnectRefreshLease != nil || a.cfg.ConnectGrant != nil || a.cfg.ConnectInviteToken != "" || len(a.cfg.ConnectGrantPeers) > 0 || a.cfg.ConnectMembershipCapability != nil || a.cfg.ConnectMembershipGrantToken != ""
}

func (a *App) watchConnectPathTransitions(ctx context.Context) {
	prev := strings.TrimSpace(a.currentPath())
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		curr := strings.TrimSpace(a.currentPath())
		if msg, ok := ConnectPathTransitionMessage(prev, curr); ok {
			runtime := a.CurrentRuntimeStatus()
			if os.Getenv("TUBO_DETACHED_CHILD") != "1" {
				selectedPath := runtime.SelectedPath
				if selectedPath == "" {
					selectedPath = "-"
				}
				selectedAddr := runtime.SelectedAddr
				if selectedAddr == "" {
					selectedAddr = "-"
				}
				log.Printf("bridge %s selected_path=%s selected_addr=%s", msg, selectedPath, selectedAddr)
			}
			a.reportStatus()
		}
		if curr != "" {
			prev = curr
		}
	}
}

func (a *App) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		snap := a.statusSnapshot(time.Now().UTC())
		if snap.Status == "running" {
			w.WriteHeader(200)
			_, _ = w.Write([]byte("ok"))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(snap.Reason))
	})
	m.HandleFunc("/statusz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(a.statusSnapshot(time.Now().UTC()))
	})
	m.HandleFunc("/statsz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if a.stats == nil {
			_ = json.NewEncoder(w).Encode(statspkg.Snapshot{CollectedAt: time.Now().UTC(), Role: "connect", Kind: a.cfg.ServiceKind, Service: a.cfg.ConnectServiceID, Path: a.currentPath(), Status: "unknown", Reason: "stats unavailable"})
			return
		}
		snap := a.stats.Snapshot()
		snap.Path = a.currentPath()
		if snap.Status == "" {
			snap.Status = a.CurrentRuntimeStatus().Status
		}
		if snap.Reason == "" {
			snap.Reason = a.CurrentRuntimeStatus().Reason
		}
		_ = json.NewEncoder(w).Encode(snap)
	})
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		streamCtx := serviceStreamContext(a.cfg.ServiceAddr, "bridge tunnel stream")
		peerID, ok := a.selectedPeerID()
		if !ok {
			http.Error(w, "bridge service peer unavailable", 502)
			return
		}
		s, err := a.host.NewStream(streamCtx, peerID, p2p.SupportedProtocolIDs()...)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer s.Close()
		headers := map[string][]string{}
		for k, v := range r.Header {
			headers[k] = v
		}
		if isWebSocketRequest(r) {
			a.serveWebSocket(w, r, s, headers)
			return
		}
		proof, err := a.connectProof()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		start := time.Now()
		reqBody := io.Reader(r.Body)
		if r.Body != nil && a.stats != nil {
			reqBody = &countingReadCloser{rc: r.Body, onRead: a.stats.AddTx}
		}
		var opErr error
		if a.stats != nil {
			a.stats.Begin()
			defer func() { a.stats.Finish(opErr) }()
		}
		resp, err := p2p.HandleClientRequest(s, "bridge", r.Method, r.URL.Path, r.URL.RawQuery, headers, reqBody, proof)
		if err != nil {
			opErr = err
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		for k, vs := range resp.Header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		respWriter := io.Writer(w)
		if a.stats != nil {
			respWriter = &countingWriter{w: w, onWrite: a.stats.AddRx}
		}
		wrote, copyErr := io.Copy(respWriter, resp.Body)
		if a.stats != nil {
			a.stats.Observe(resp.StatusCode, time.Since(start))
		}
		if copyErr == nil {
			a.markTunnelHealthy()
		} else {
			opErr = copyErr
			log.Printf("bridge http response copy failed bytes=%d err=%v", wrote, copyErr)
		}
	})
	return m
}
func (a *App) serveWebSocket(w http.ResponseWriter, r *http.Request, s network.Stream, headers map[string][]string) {
	start := time.Now()
	var opErr error
	if a.stats != nil {
		a.stats.Begin()
		defer func() { a.stats.Finish(opErr) }()
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		opErr = fmt.Errorf("websocket hijack unsupported")
		http.Error(w, "websocket hijack unsupported", http.StatusInternalServerError)
		return
	}
	proof, err := a.connectProof()
	if err != nil {
		opErr = err
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	respHeader, err := p2p.StartClientWebSocketUpgrade(s, "bridge", r.Method, r.URL.Path, r.URL.RawQuery, headers, proof)
	if err != nil {
		opErr = err
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
		opErr = err
		return
	}
	defer conn.Close()
	if rw.Reader.Buffered() > 0 {
		log.Printf("bridge websocket hijack had %d buffered client bytes", rw.Reader.Buffered())
	}
	resp := &http.Response{
		StatusCode: respHeader.StatusCode,
		Status:     fmt.Sprintf("%d %s", respHeader.StatusCode, http.StatusText(respHeader.StatusCode)),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     http.Header(respHeader.Headers),
	}
	bw := bufio.NewWriter(conn)
	if err := resp.Write(bw); err != nil {
		opErr = err
		return
	}
	if err := bw.Flush(); err != nil {
		opErr = err
		return
	}
	if respHeader.StatusCode != http.StatusSwitchingProtocols {
		if a.stats != nil {
			a.stats.Observe(respHeader.StatusCode, time.Since(start))
		}
		return
	}
	log.Printf("bridge websocket upgraded path=%s", r.URL.Path)
	a.markTunnelHealthy()
	proxyRawClient(s, conn, rw.Reader, a.stats, start)
}

func proxyRawClient(s network.Stream, conn net.Conn, clientReader io.Reader, recorder *statspkg.Collector, start time.Time) {
	done := make(chan struct{}, 2)
	go func() {
		dst := io.Writer(s)
		if recorder != nil {
			dst = &countingWriter{w: s, onWrite: recorder.AddTx}
		}
		_, _ = io.Copy(dst, clientReader)
		done <- struct{}{}
	}()
	go func() {
		dst := io.Writer(conn)
		if recorder != nil {
			dst = &countingWriter{w: conn, onWrite: recorder.AddRx}
		}
		_, _ = io.Copy(dst, s)
		done <- struct{}{}
	}()
	<-done
	_ = s.Close()
	_ = conn.Close()
	<-done
	if recorder != nil {
		recorder.Observe(http.StatusSwitchingProtocols, time.Since(start))
	}
}

func (a *App) connectProof() (*protocol.ConnectProof, error) {
	priv := a.host.Peerstore().PrivKey(a.host.ID())
	if priv == nil {
		return nil, fmt.Errorf("no private key for peer")
	}
	raw, err := priv.Raw()
	if err != nil {
		return nil, fmt.Errorf("raw private key: %w", err)
	}
	if a.cfg.ConnectRefreshLease != nil || a.connectLease != nil {
		lease, err := a.ensureConnectAccessLease(context.Background())
		if err != nil {
			return nil, err
		}
		leaseBytes, err := grantspkg.MarshalConnectAccessLease(lease)
		if err != nil {
			return nil, fmt.Errorf("marshal connect access lease: %w", err)
		}
		proof, err := protocol.NewConnectProofWithPayload(lease.ClusterID, lease.NamespaceID, lease.ServiceID, lease.ExpiresAt, leaseBytes, grantspkg.ConnectAccessLeaseHashBytes(leaseBytes), a.host.ID().String(), ed25519.PrivateKey(raw))
		if err != nil {
			return nil, fmt.Errorf("build connect proof: %w", err)
		}
		return &proof, nil
	}
	if a.cfg.ConnectGrant == nil {
		return nil, nil
	}
	proof, err := protocol.NewConnectProof(*a.cfg.ConnectGrant, a.host.ID().String(), ed25519.PrivateKey(raw))
	if err != nil {
		return nil, fmt.Errorf("build connect proof: %w", err)
	}
	return &proof, nil
}

func (a *App) ensureConnectAccessLease(ctx context.Context) (grantspkg.ConnectAccessLease, error) {
	for {
		now := time.Now().UTC()
		a.connectMu.Lock()
		current := a.connectLease
		refresh := a.cfg.ConnectRefreshLease
		if current != nil && !connectAccessLeaseNeedsRefresh(*current, now) {
			if refresh == nil {
				lease := *current
				a.connectMu.Unlock()
				return lease, nil
			}
			if !connectRefreshLeaseNeedsRollover(*refresh, now) {
				lease := *current
				a.connectMu.Unlock()
				return lease, nil
			}
			if !a.connectCanRolloverLocked() {
				err := errors.New(connectRefreshLeaseFreshTokenReason(*refresh, now))
				a.connectMu.Unlock()
				a.recordRefreshFailureLocked(err, refresh.ExpiresAt.UTC())
				a.reportStatus()
				return grantspkg.ConnectAccessLease{}, err
			}
		}
		if refresh == nil {
			if current == nil || !now.Before(current.ExpiresAt.UTC()) {
				a.connectMu.Unlock()
				a.reportStatus()
				return grantspkg.ConnectAccessLease{}, fmt.Errorf("connect access lease expired; ask the service owner for a fresh token/invite")
			}
			lease := *current
			a.connectMu.Unlock()
			return lease, nil
		}
		rolloverDue := connectRefreshLeaseNeedsRollover(*refresh, now)
		canRollover := a.connectCanRolloverLocked()
		if rolloverDue && !canRollover {
			err := errors.New(connectRefreshLeaseFreshTokenReason(*refresh, now))
			a.connectMu.Unlock()
			a.recordRefreshFailureLocked(err, refresh.ExpiresAt.UTC())
			a.reportStatus()
			return grantspkg.ConnectAccessLease{}, err
		}
		if !a.nextRefreshRetryAt.IsZero() && now.Before(a.nextRefreshRetryAt) {
			if current != nil && now.Before(current.ExpiresAt.UTC()) {
				lease := *current
				a.connectMu.Unlock()
				a.reportStatus()
				return lease, nil
			}
			errText := strings.TrimSpace(a.lastRefreshError)
			if errText == "" {
				errText = "connect lease refresh cooling down; retry later"
			}
			a.connectMu.Unlock()
			return grantspkg.ConnectAccessLease{}, errors.New(errText)
		}
		if a.refreshingLease {
			done := a.refreshDone
			a.connectMu.Unlock()
			select {
			case <-ctx.Done():
				return grantspkg.ConnectAccessLease{}, ctx.Err()
			case <-done:
			}
			continue
		}
		prevExpiry := time.Time{}
		if current != nil {
			prevExpiry = current.ExpiresAt.UTC()
		}
		if rolloverDue {
			return a.rolloverConnectLeaseLocked(ctx, current, now, false)
		}
		log.Printf("bridge connect access lease refresh requested service=%s expires_at=%s", refresh.ServiceID, refresh.ExpiresAt.UTC().Format(time.RFC3339))
		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		a.refreshingLease = true
		a.refreshDone = make(chan struct{})
		a.connectMu.Unlock()
		access, err := a.refreshConnectAccessLease(refreshCtx, *refresh)
		cancel()
		a.connectMu.Lock()
		done := a.refreshDone
		a.refreshingLease = false
		a.refreshDone = nil
		if done != nil {
			close(done)
		}
		if err != nil {
			wrapped := fmt.Errorf("refresh connect access lease: %w", err)
			retryAt := connectLeaseFailureRetryAt(err, current, *refresh, now)
			if current != nil && now.Before(current.ExpiresAt.UTC()) {
				a.recordRefreshFailureLocked(wrapped, retryAt)
				log.Printf("bridge connect access lease refresh failed: %v", err)
				a.connectMu.Unlock()
				a.reportStatus()
				return *current, nil
			}
			a.recordRefreshFailureLocked(wrapped, retryAt)
			log.Printf("bridge connect access lease refresh failed: %v", err)
			a.connectMu.Unlock()
			return grantspkg.ConnectAccessLease{}, wrapped
		}
		if !connectRefreshResultUseful(prevExpiry, access.ExpiresAt.UTC(), time.Now().UTC()) {
			if a.connectCanRolloverLocked() {
				return a.rolloverConnectLeaseLocked(ctx, current, now, true)
			}
			wrapped := errors.New("connect refresh lease is near expiry; ask the service owner for a fresh token/invite")
			if current != nil && now.Before(current.ExpiresAt.UTC()) {
				a.recordRefreshFailureLocked(wrapped, current.ExpiresAt.UTC())
				log.Printf("bridge connect access lease refresh failed: %v", wrapped)
				a.connectMu.Unlock()
				a.reportStatus()
				return *current, nil
			}
			a.recordRefreshFailureLocked(wrapped, refresh.ExpiresAt.UTC())
			log.Printf("bridge connect access lease refresh failed: %v", wrapped)
			a.connectMu.Unlock()
			return grantspkg.ConnectAccessLease{}, wrapped
		}
		a.connectLease = &access
		a.cfg.ConnectAccessLease = &access
		a.lastRefreshError = ""
		a.lastRefreshErrorClass = reachability.ErrorNone
		a.nextRefreshRetryAt = time.Time{}
		a.consecutiveRefreshFails = 0
		a.connectMu.Unlock()
		log.Printf("bridge connect access lease refreshed service=%s expires_at=%s", access.ServiceID, access.ExpiresAt.UTC().Format(time.RFC3339))
		a.recordRenewalReachabilitySuccess()
		a.reportStatus()
		return access, nil
	}
}

func (a *App) rolloverConnectLeaseLocked(ctx context.Context, current *grantspkg.ConnectAccessLease, now time.Time, skippedRefresh bool) (grantspkg.ConnectAccessLease, error) {
	grantPeers := append([]string(nil), a.cfg.ConnectGrantPeers...)
	clusterID := a.cfg.ConnectClusterID
	namespaceID := a.cfg.ConnectNamespaceID
	serviceID := a.cfg.ConnectServiceID
	membership := a.cfg.ConnectMembershipCapability
	membershipGrantToken := a.cfg.ConnectMembershipGrantToken
	if skippedRefresh {
		log.Printf("bridge connect access lease refresh skipped; rolling over through membership service=%s", serviceID)
	} else {
		log.Printf("bridge member connect lease rollover requested service=%s cluster=%s namespace=%s", serviceID, clusterID, namespaceID)
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	a.refreshingLease = true
	a.refreshDone = make(chan struct{})
	a.connectMu.Unlock()
	artifacts, err := requestDirectConnectLease(refreshCtx, a.host, grantPeers, clusterID, namespaceID, serviceID, membership, membershipGrantToken)
	cancel()
	a.connectMu.Lock()
	done := a.refreshDone
	a.refreshingLease = false
	a.refreshDone = nil
	if done != nil {
		close(done)
	}
	if err != nil {
		wrapped := fmt.Errorf("rollover connect lease: %w", err)
		retryAt := time.Now().UTC().Add(connectRefreshFailureCooldown)
		if connectLeaseFailureIsTerminal(err) && current != nil && now.Before(current.ExpiresAt.UTC()) {
			retryAt = current.ExpiresAt.UTC()
		}
		if current != nil && now.Before(current.ExpiresAt.UTC()) {
			a.recordRefreshFailureLocked(wrapped, retryAt)
			log.Printf("bridge member connect lease rollover failed: %v", err)
			a.connectMu.Unlock()
			a.reportStatus()
			return *current, nil
		}
		a.recordRefreshFailureLocked(wrapped, retryAt)
		log.Printf("bridge member connect lease rollover failed: %v", err)
		a.connectMu.Unlock()
		return grantspkg.ConnectAccessLease{}, wrapped
	}
	a.applyConnectLeaseArtifactsLocked(artifacts)
	a.consecutiveRefreshFails = 0
	a.connectMu.Unlock()
	log.Printf("bridge member connect lease rolled over service=%s access_expires_at=%s refresh_expires_at=%s", artifacts.AccessLease.ServiceID, artifacts.AccessLease.ExpiresAt.UTC().Format(time.RFC3339), artifacts.RefreshLease.ExpiresAt.UTC().Format(time.RFC3339))
	a.reportStatus()
	return artifacts.AccessLease, nil
}

func connectRefreshResultUseful(previousExpiry, newExpiry, now time.Time) bool {
	if newExpiry.IsZero() || !now.Before(newExpiry) {
		return false
	}
	if previousExpiry.IsZero() {
		return true
	}
	return newExpiry.After(previousExpiry.Add(connectRefreshMinExtension))
}

func (a *App) recordRefreshFailure(err error, retryAt time.Time) {
	if err == nil {
		return
	}
	a.recordRenewalReachabilityFailure(err)
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	a.lastRefreshError = err.Error()
	a.lastRefreshErrorAt = time.Now().UTC()
	a.lastRefreshErrorClass = reachability.Classify(err).Class
	a.nextRefreshRetryAt = retryAt.UTC()
}

func (a *App) recordRefreshFailureLocked(err error, retryAt time.Time) {
	if err == nil {
		return
	}
	a.recordRenewalReachabilityFailure(err)
	a.consecutiveRefreshFails++
	a.lastRefreshError = err.Error()
	a.lastRefreshErrorAt = time.Now().UTC()
	a.lastRefreshErrorClass = reachability.Classify(err).Class
	// Apply exponential backoff so rapid retries cannot perpetually renew the
	// server-side deny-cache. If the caller's retryAt is further in the future
	// (e.g. a terminal lease-expiry deadline), honour that instead.
	backoffAt := time.Now().UTC().Add(connectRefreshBackoff(a.consecutiveRefreshFails))
	if retryAt.UTC().After(backoffAt) {
		a.nextRefreshRetryAt = retryAt.UTC()
	} else {
		a.nextRefreshRetryAt = backoffAt
	}
}

func (a *App) refreshConnectAccessLease(ctx context.Context, refresh grantspkg.ConnectRefreshLease) (grantspkg.ConnectAccessLease, error) {
	if a.cfg.ConnectLeaseRefresher != nil {
		return a.cfg.ConnectLeaseRefresher(ctx, refresh)
	}
	if len(a.cfg.ConnectGrantPeers) == 0 {
		return grantspkg.ConnectAccessLease{}, fmt.Errorf("connect grant service unavailable; ask the service owner for a fresh token/invite")
	}
	var lastErr error
	for _, rawPeer := range a.cfg.ConnectGrantPeers {
		info, err := p2p.AddrInfoFromString(rawPeer)
		if err != nil {
			lastErr = err
			continue
		}
		access, err := grantspkg.RefreshConnectLease(ctx, a.host, info, refresh)
		if err == nil {
			return access, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return grantspkg.ConnectAccessLease{}, lastErr
	}
	return grantspkg.ConnectAccessLease{}, fmt.Errorf("no connect grant service peers configured")
}

func requestDirectConnectLease(ctx context.Context, h host.Host, grantPeers []string, clusterID, namespaceID, serviceID string, membership *capability.MembershipCapability, membershipGrantToken string) (grantspkg.ConnectLeaseArtifacts, error) {
	clientPublicKey, err := connectClientPublicKey(h)
	if err != nil {
		return grantspkg.ConnectLeaseArtifacts{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var attempted []string
	var lastErr error
	for _, rawPeer := range grantPeers {
		attempted = append(attempted, rawPeer)
		info, err := p2p.AddrInfoFromString(rawPeer)
		if err != nil {
			lastErr = err
			continue
		}
		artifacts, err := grantspkg.RequestConnectLease(requestCtx, h, info, clusterID, namespaceID, serviceID, clientPublicKey, membership, membershipGrantToken)
		if err == nil {
			return artifacts, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("request connect lease from advertised grant endpoint(s) %s: %w", strings.Join(attempted, ", "), lastErr)
	}
	return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("request connect lease: no advertised grant endpoint peers configured")
}

func redeemConnectInvite(ctx context.Context, h host.Host, grantPeers []string, token string) (grantspkg.ConnectLeaseArtifacts, error) {
	clientPublicKey, err := connectClientPublicKey(h)
	if err != nil {
		return grantspkg.ConnectLeaseArtifacts{}, err
	}
	redeemCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var lastErr error
	for _, rawPeer := range grantPeers {
		info, err := p2p.AddrInfoFromString(rawPeer)
		if err != nil {
			lastErr = err
			continue
		}
		artifacts, err := grantspkg.RedeemShareInvite(redeemCtx, h, info, token, clientPublicKey)
		if err == nil {
			return artifacts, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		msg := lastErr.Error()
		if strings.Contains(strings.ToLower(msg), "protocols not supported") {
			return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("redeem share invite: grant service endpoint does not support %s", grantspkg.ProtocolID)
		}
		return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("redeem share invite: %w", lastErr)
	}
	return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("redeem share invite: no grant service peers configured")
}

func loadConnectAuthorityPrivateKey(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(strings.TrimSpace(path))
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("cluster authority private key is not PEM encoded")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return k, nil
	case *ed25519.PrivateKey:
		return *k, nil
	default:
		return nil, fmt.Errorf("unsupported cluster authority private key type %T", key)
	}
}

func connectClientPublicKey(h host.Host) (string, error) {
	pub := h.Peerstore().PubKey(h.ID())
	if pub == nil {
		return "", fmt.Errorf("no public key for peer")
	}
	raw, err := pub.Raw()
	if err != nil {
		return "", fmt.Errorf("raw public key: %w", err)
	}
	sshPub, err := ssh.NewPublicKey(ed25519.PublicKey(raw))
	if err != nil {
		return "", fmt.Errorf("encode connect client public key: %w", err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))), nil
}

func connectAccessLeaseNeedsRefresh(lease grantspkg.ConnectAccessLease, now time.Time) bool {
	if lease.ExpiresAt.IsZero() || !now.Before(lease.ExpiresAt.UTC()) {
		return true
	}
	renewBefore := connectLeaseRenewBefore(lease)
	return !now.Add(renewBefore).Before(lease.ExpiresAt.UTC())
}

func isWebSocketRequest(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, part := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(part), "upgrade") {
			return true
		}
	}
	return false
}

func parseAddrInfos(peers []string) []peer.AddrInfo {
	out := make([]peer.AddrInfo, 0, len(peers))
	for _, raw := range peers {
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			continue
		}
		out = append(out, info)
	}
	return out
}

func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
