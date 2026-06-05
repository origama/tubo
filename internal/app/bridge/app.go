package bridge

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/protocol"
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
	ServiceKind                                                                                        string
}
type RuntimeStatus struct {
	Status                  string
	Reason                  string
	ServiceKind             string
	Path                    string
	ConnectAccessExpiresAt  *time.Time
	ConnectAccessExpiresIn  string
	ConnectRefreshExpiresAt *time.Time
	ConnectRefreshExpiresIn string
	LastTunnelError         string
	LastTunnelErrorAt       *time.Time
	LastTunnelHealthyAt     *time.Time
	LastRefreshError        string
	NextRefreshRetryAt      *time.Time
}

type App struct {
	cfg                  Config
	host                 host.Host
	service              peer.AddrInfo
	server               *http.Server
	listener             net.Listener
	listenAddr           string
	stateMu              sync.RWMutex
	connectMu            sync.Mutex
	connectLease         *grantspkg.ConnectAccessLease
	refreshingLease      bool
	refreshDone          chan struct{}
	lastRefreshError     string
	nextRefreshRetryAt   time.Time
	healthMu             sync.RWMutex
	lastTunnelError      string
	lastTunnelErrorAt    time.Time
	lastTunnelHealthyAt  time.Time
	statusReporter       func(RuntimeStatus)
	openTunnelStream     func(context.Context) (network.Stream, error)
	startClientTCPTunnel func(network.Stream, string, *protocol.ConnectProof) error
	reconnectServiceFn   func(context.Context) error
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
		if len(cfg.ConnectGrantPeers) == 0 {
			_ = h.Close()
			return nil, fmt.Errorf("share invite is missing grant service metadata; ask the service owner to reissue the invite")
		}
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
	app := &App{cfg: cfg, host: h, service: si, connectLease: connectLease}
	app.reportStatus()
	return app, nil
}

func serviceAddrUsesRelay(addr string) bool {
	return strings.Contains(addr, "/p2p-circuit")
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
	if a.cfg.ServiceKind == string(cfgpkg.ServiceKindTCP) {
		go a.serveTCP(ctx, ln)
		<-ctx.Done()
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
	s, err := a.establishTCPTunnel(conn.RemoteAddr().String())
	if err != nil {
		a.markTunnelDegraded(err)
		log.Printf("bridge tcp establish tunnel local=%s err=%v", conn.RemoteAddr(), err)
		return
	}
	defer s.Close()
	sent, received, err := p2p.ProxyTCPStream(conn, s)
	if err != nil {
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
			}
			_ = s.Close()
			lastErr = fmt.Errorf("start tunnel: %w", startErr)
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
	a.healthMu.Lock()
	a.lastTunnelHealthyAt = time.Now()
	a.lastTunnelError = ""
	a.lastTunnelErrorAt = time.Time{}
	a.healthMu.Unlock()
	a.reportStatus()
}

func (a *App) markTunnelDegraded(err error) {
	if err == nil {
		return
	}
	a.healthMu.Lock()
	a.lastTunnelError = err.Error()
	a.lastTunnelErrorAt = time.Now()
	a.healthMu.Unlock()
	a.reportStatus()
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

func (a *App) openServiceTunnelStream(ctx context.Context) (network.Stream, error) {
	if a.openTunnelStream != nil {
		return a.openTunnelStream(ctx)
	}
	return a.host.NewStream(ctx, a.service.ID, p2p.ProtocolID)
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
	_ = a.host.Network().ClosePeer(a.service.ID)
	connectCtx := network.WithAllowLimitedConn(context.Background(), "bridge tcp self-heal reconnect")
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		connectCtx, cancel = context.WithDeadline(connectCtx, deadline)
		defer cancel()
	}
	if err := a.host.Connect(connectCtx, a.service); err != nil {
		return fmt.Errorf("reconnect service peer: %w", err)
	}
	return nil
}

type statusSnapshot struct {
	Status                  string     `json:"status"`
	Reason                  string     `json:"reason,omitempty"`
	ServiceKind             string     `json:"service_kind,omitempty"`
	Path                    string     `json:"path,omitempty"`
	ConnectAccessExpiresAt  *time.Time `json:"connect_access_expires_at,omitempty"`
	ConnectAccessExpiresIn  string     `json:"connect_access_expires_in,omitempty"`
	ConnectRefreshExpiresAt *time.Time `json:"connect_refresh_expires_at,omitempty"`
	ConnectRefreshExpiresIn string     `json:"connect_refresh_expires_in,omitempty"`
	LastTunnelError         string     `json:"last_tunnel_error,omitempty"`
	LastTunnelErrorAt       *time.Time `json:"last_tunnel_error_at,omitempty"`
	LastTunnelHealthyAt     *time.Time `json:"last_tunnel_healthy_at,omitempty"`
}

func (a *App) currentPath() string {
	if a.host == nil {
		return ""
	}
	conns := a.host.Network().ConnsToPeer(a.service.ID)
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
	if strings.Contains(a.cfg.ServiceAddr, "/p2p-circuit") {
		return "relayed"
	}
	if a.cfg.ServiceAddr != "" {
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
	snap := statusSnapshot{Status: "running", ServiceKind: a.cfg.ServiceKind, Path: a.currentPath()}
	if !ok {
		snap.Status = "degraded"
		snap.Reason = msg
	}
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
		if !now.Before(t) {
			snap.Status = "degraded"
			snap.Reason = "connect refresh lease expired; ask the service owner for a fresh token/invite"
		} else if time.Until(t) <= connectRefreshMinUsefulLifetime && snap.Reason == "" {
			snap.Status = "degraded"
			snap.Reason = "connect refresh lease is near expiry; ask the service owner for a fresh token/invite"
		}
	} else if a.connectLease != nil {
		if !now.Before(a.connectLease.ExpiresAt.UTC()) {
			snap.Status = "degraded"
			snap.Reason = "connect access lease expired; ask the service owner for a fresh token/invite"
		}
	}
	if a.lastRefreshError != "" && snap.Reason == "" {
		snap.Status = "degraded"
		snap.Reason = a.lastRefreshError
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

func (a *App) startConnectLeaseRenewal(ctx context.Context) {
	for {
		a.connectMu.Lock()
		refresh := a.cfg.ConnectRefreshLease
		lease := a.connectLease
		nextRetry := a.nextRefreshRetryAt
		a.connectMu.Unlock()
		if refresh == nil {
			return
		}
		now := time.Now().UTC()
		if !now.Before(refresh.ExpiresAt.UTC()) {
			err := fmt.Errorf("connect refresh lease expired; ask the service owner for a fresh token/invite")
			a.recordRefreshFailure(err, refresh.ExpiresAt.UTC())
			a.markTunnelDegraded(err)
			return
		}
		if nextRetry.After(now) {
			wait := time.Until(nextRetry)
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			continue
		}
		var wait time.Duration
		if lease == nil || connectAccessLeaseNeedsRefresh(*lease, now) {
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
	return RuntimeStatus{
		Status:                  snap.Status,
		Reason:                  snap.Reason,
		ServiceKind:             snap.ServiceKind,
		Path:                    snap.Path,
		ConnectAccessExpiresAt:  snap.ConnectAccessExpiresAt,
		ConnectAccessExpiresIn:  snap.ConnectAccessExpiresIn,
		ConnectRefreshExpiresAt: snap.ConnectRefreshExpiresAt,
		ConnectRefreshExpiresIn: snap.ConnectRefreshExpiresIn,
		LastTunnelError:         snap.LastTunnelError,
		LastTunnelErrorAt:       snap.LastTunnelErrorAt,
		LastTunnelHealthyAt:     snap.LastTunnelHealthyAt,
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
	if a.statusReporter != nil {
		a.statusReporter(a.CurrentRuntimeStatus())
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
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		streamCtx := serviceStreamContext(a.cfg.ServiceAddr, "bridge tunnel stream")
		s, err := a.host.NewStream(streamCtx, a.service.ID, p2p.SupportedProtocolIDs()...)
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
		resp, err := p2p.HandleClientRequest(s, "bridge", r.Method, r.URL.Path, r.URL.RawQuery, headers, r.Body, proof)
		if err != nil {
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
		_, _ = io.Copy(w, resp.Body)
	})
	return m
}
func (a *App) serveWebSocket(w http.ResponseWriter, r *http.Request, s network.Stream, headers map[string][]string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "websocket hijack unsupported", http.StatusInternalServerError)
		return
	}
	proof, err := a.connectProof()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	respHeader, err := p2p.StartClientWebSocketUpgrade(s, "bridge", r.Method, r.URL.Path, r.URL.RawQuery, headers, proof)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	conn, rw, err := hj.Hijack()
	if err != nil {
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
		return
	}
	if err := bw.Flush(); err != nil {
		return
	}
	if respHeader.StatusCode != http.StatusSwitchingProtocols {
		return
	}
	log.Printf("bridge websocket upgraded path=%s", r.URL.Path)
	proxyRawClient(s, conn, rw.Reader)
}

func proxyRawClient(s network.Stream, conn net.Conn, clientReader io.Reader) {
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(s, clientReader); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, s); done <- struct{}{} }()
	<-done
	_ = s.Close()
	_ = conn.Close()
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
		if a.connectLease != nil && !connectAccessLeaseNeedsRefresh(*a.connectLease, now) {
			lease := *a.connectLease
			a.connectMu.Unlock()
			return lease, nil
		}
		if a.cfg.ConnectRefreshLease == nil {
			if a.connectLease == nil || !now.Before(a.connectLease.ExpiresAt.UTC()) {
				a.connectMu.Unlock()
				return grantspkg.ConnectAccessLease{}, fmt.Errorf("connect access lease expired; ask the service owner for a fresh token/invite")
			}
			lease := *a.connectLease
			a.connectMu.Unlock()
			return lease, nil
		}
		refresh := *a.cfg.ConnectRefreshLease
		if !now.Before(refresh.ExpiresAt.UTC()) {
			err := fmt.Errorf("connect refresh lease expired; ask the service owner for a fresh token/invite")
			a.connectMu.Unlock()
			a.recordRefreshFailure(err, refresh.ExpiresAt.UTC())
			return grantspkg.ConnectAccessLease{}, err
		}
		if remaining := time.Until(refresh.ExpiresAt.UTC()); remaining <= connectRefreshMinUsefulLifetime {
			err := fmt.Errorf("connect refresh lease is near expiry; ask the service owner for a fresh token/invite")
			a.connectMu.Unlock()
			a.recordRefreshFailure(err, refresh.ExpiresAt.UTC())
			return grantspkg.ConnectAccessLease{}, err
		}
		if !a.nextRefreshRetryAt.IsZero() && now.Before(a.nextRefreshRetryAt) {
			errText := strings.TrimSpace(a.lastRefreshError)
			if errText == "" {
				errText = "connect lease refresh cooling down; retry later"
			}
			a.connectMu.Unlock()
			return grantspkg.ConnectAccessLease{}, fmt.Errorf("%s", errText)
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
		if a.connectLease != nil {
			prevExpiry = a.connectLease.ExpiresAt.UTC()
		}
		refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		a.refreshingLease = true
		a.refreshDone = make(chan struct{})
		a.connectMu.Unlock()
		access, err := a.refreshConnectAccessLease(refreshCtx, refresh)
		cancel()
		a.connectMu.Lock()
		done := a.refreshDone
		a.refreshingLease = false
		a.refreshDone = nil
		if done != nil {
			close(done)
		}
		if err != nil {
			a.connectMu.Unlock()
			wrapped := fmt.Errorf("refresh connect access lease: %w", err)
			a.recordRefreshFailure(wrapped, time.Now().UTC().Add(connectRefreshFailureCooldown))
			return grantspkg.ConnectAccessLease{}, wrapped
		}
		if !connectRefreshResultUseful(prevExpiry, access.ExpiresAt.UTC(), time.Now().UTC()) {
			a.connectMu.Unlock()
			err := fmt.Errorf("connect refresh lease is near expiry; ask the service owner for a fresh token/invite")
			a.recordRefreshFailure(err, refresh.ExpiresAt.UTC())
			return grantspkg.ConnectAccessLease{}, err
		}
		a.connectLease = &access
		a.cfg.ConnectAccessLease = &access
		a.lastRefreshError = ""
		a.nextRefreshRetryAt = time.Time{}
		a.connectMu.Unlock()
		log.Printf("bridge connect access lease refreshed service=%s expires_at=%s", access.ServiceID, access.ExpiresAt.UTC().Format(time.RFC3339))
		a.reportStatus()
		return access, nil
	}
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
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	a.lastRefreshError = err.Error()
	a.nextRefreshRetryAt = retryAt.UTC()
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
		return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("redeem share invite: %w", lastErr)
	}
	return grantspkg.ConnectLeaseArtifacts{}, fmt.Errorf("redeem share invite: no grant service peers configured")
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
