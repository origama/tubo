package bridge

import (
	"bufio"
	"context"
	"fmt"
	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/origama/tubo/internal/p2p"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"
)

type Config struct {
	Listen, Seed, P2PListen, ServiceAddr, ServiceSeed, ServiceP2PListen, PrivateKeyFile, PrivateKeyB64 string
	RelayPeers                                                                                         []string
	Autorelay, HolePunching                                                                            bool
}
type App struct {
	cfg        Config
	host       host.Host
	service    peer.AddrInfo
	server     *http.Server
	listener   net.Listener
	listenAddr string
}

func LoadConfigFromEnv(g func(string) string) (Config, error) {
	return Config{Listen: first(g("BRIDGE_LISTEN"), "127.0.0.1:18081"), Seed: first(g("BRIDGE_SEED"), "bridge-demo-seed"), P2PListen: first(g("BRIDGE_P2P_LISTEN"), "/ip4/127.0.0.1/tcp/0"), ServiceAddr: g("SERVICE_ADDR"), ServiceSeed: g("SERVICE_SEED"), ServiceP2PListen: first(g("SERVICE_P2P_LISTEN"), "/ip4/127.0.0.1/tcp/40123"), PrivateKeyFile: g("LIBP2P_PRIVATE_NETWORK_KEY"), PrivateKeyB64: g("LIBP2P_PRIVATE_NETWORK_KEY_B64")}, nil
}
func New(ctx context.Context, cfg Config) (*App, error) {
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
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := h.Connect(c, si); err != nil {
		_ = h.Close()
		return nil, fmt.Errorf("connect service peer: %w", err)
	}
	return &App{cfg: cfg, host: h, service: si}, nil
}
func (a *App) Start(ctx context.Context) error {
	defer a.host.Close()
	log.Printf("bridge peer_id=%s", a.host.ID())
	ln, err := net.Listen("tcp", a.cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen bridge: %w", err)
	}
	a.listener = ln
	a.listenAddr = ln.Addr().String()
	a.server = &http.Server{Addr: a.cfg.Listen, Handler: a.mux()}
	go func() {
		log.Printf("client bridge listening on %s", a.listenAddr)
		if err := a.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("bridge server: %v", err)
		}
	}()
	<-ctx.Done()
	sd, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	return a.server.Shutdown(sd)
}

func (a *App) ListenAddr() string {
	if a.listenAddr != "" {
		return a.listenAddr
	}
	return a.cfg.Listen
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
		resp, err := p2p.HandleClientRequest(s, "bridge", r.Method, r.URL.Path, r.URL.RawQuery, headers, r.Body)
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
	respHeader, err := p2p.StartClientWebSocketUpgrade(s, "bridge", r.Method, r.URL.Path, r.URL.RawQuery, headers)
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
