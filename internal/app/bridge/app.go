package bridge

import (
	"bufio"
	"context"
	"crypto/ed25519"
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
type App struct {
	cfg          Config
	host         host.Host
	service      peer.AddrInfo
	server       *http.Server
	listener     net.Listener
	listenAddr   string
	stateMu      sync.RWMutex
	connectMu    sync.Mutex
	connectLease *grantspkg.ConnectAccessLease
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
	if cfg.ConnectGrant != nil {
		log.Printf("bridge legacy connect grants enabled cluster=%s namespace=%s service=%s", cfg.ConnectGrant.ClusterID, cfg.ConnectGrant.NamespaceID, cfg.ConnectGrant.ServiceID)
	}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.Connect(c, si); err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("connect service peer: %w", err)
	}
	return &App{cfg: cfg, host: h, service: si, connectLease: connectLease}, nil
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
	streamCtx := network.WithAllowLimitedConn(context.Background(), "bridge tcp tunnel stream")
	s, err := a.host.NewStream(streamCtx, a.service.ID, p2p.ProtocolID)
	if err != nil {
		log.Printf("bridge tcp open stream local=%s err=%v", conn.RemoteAddr(), err)
		return
	}
	defer s.Close()
	proof, err := a.connectProof()
	if err != nil {
		log.Printf("bridge tcp connect proof local=%s err=%v", conn.RemoteAddr(), err)
		return
	}
	if err := p2p.StartClientTCPTunnel(s, "bridge", proof); err != nil {
		log.Printf("bridge tcp start tunnel local=%s err=%v", conn.RemoteAddr(), err)
		return
	}
	sent, received, err := p2p.ProxyTCPStream(conn, s)
	if err != nil {
		log.Printf("bridge tcp proxy closed local=%s bytes_in=%d bytes_out=%d err=%v duration=%s", conn.RemoteAddr(), received, sent, err, time.Since(start))
		return
	}
	log.Printf("bridge tcp proxy completed local=%s bytes_in=%d bytes_out=%d duration=%s", conn.RemoteAddr(), received, sent, time.Since(start))
}

func (a *App) mux() *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) })
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		streamCtx := network.WithAllowLimitedConn(context.Background(), "bridge tunnel stream")
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
	a.connectMu.Lock()
	defer a.connectMu.Unlock()
	if a.connectLease != nil && !connectAccessLeaseNeedsRefresh(*a.connectLease, time.Now().UTC()) {
		return *a.connectLease, nil
	}
	if a.cfg.ConnectRefreshLease == nil {
		if a.connectLease == nil || !time.Now().UTC().Before(a.connectLease.ExpiresAt.UTC()) {
			return grantspkg.ConnectAccessLease{}, fmt.Errorf("connect access lease expired; ask the service owner for a fresh token/invite")
		}
		return *a.connectLease, nil
	}
	if !time.Now().UTC().Before(a.cfg.ConnectRefreshLease.ExpiresAt.UTC()) {
		return grantspkg.ConnectAccessLease{}, fmt.Errorf("connect refresh lease expired; ask the service owner for a fresh token/invite")
	}
	refreshCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	access, err := a.refreshConnectAccessLease(refreshCtx, *a.cfg.ConnectRefreshLease)
	if err != nil {
		return grantspkg.ConnectAccessLease{}, fmt.Errorf("refresh connect access lease: %w", err)
	}
	a.connectLease = &access
	a.cfg.ConnectAccessLease = &access
	log.Printf("bridge connect access lease refreshed service=%s expires_at=%s", access.ServiceID, access.ExpiresAt.UTC().Format(time.RFC3339))
	return access, nil
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
	ttl := lease.ExpiresAt.UTC().Sub(lease.IssuedAt.UTC())
	renewBefore := ttl / 3
	if renewBefore < time.Second {
		renewBefore = time.Second
	}
	if renewBefore > 5*time.Minute {
		renewBefore = 5 * time.Minute
	}
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
