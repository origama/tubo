package edge

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	"p2p-api-tunnel/internal/discovery"
	"p2p-api-tunnel/internal/p2p"
	"p2p-api-tunnel/internal/routing"
)

// Config captures the runtime configuration of the edge gateway.
type Config struct {
	HTTPListen             string
	P2PListen              string
	Seed                   string
	AdminListen            string
	BootstrapPeers         []string
	RelayPeers             []string
	BootstrapRetryInterval time.Duration
	DirectStreamTimeout    time.Duration
	PrivateKeyFile         string
	PrivateKeyB64          string
}

// LoadConfigFromEnv loads edge configuration from environment variables.
func LoadConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPListen:     firstNonEmpty(getenv("EDGE_LISTEN"), ":8443"),
		P2PListen:      firstNonEmpty(getenv("EDGE_P2P_LISTEN"), "/ip4/0.0.0.0/tcp/4001"),
		Seed:           getenv("EDGE_SEED"),
		AdminListen:    firstNonEmpty(getenv("EDGE_ADMIN_LISTEN"), "127.0.0.1:8444"),
		BootstrapPeers: splitCSV(getenv("BOOTSTRAP_PEERS")),
		RelayPeers:     splitCSV(getenv("RELAY_PEERS")),
		PrivateKeyFile: getenv("LIBP2P_PRIVATE_NETWORK_KEY"),
		PrivateKeyB64:  getenv("LIBP2P_PRIVATE_NETWORK_KEY_B64"),
	}

	bootstrapRetryIntervalRaw := firstNonEmpty(getenv("BOOTSTRAP_RETRY_INTERVAL"), "5s")
	bootstrapRetryInterval, err := time.ParseDuration(bootstrapRetryIntervalRaw)
	if err != nil {
		return Config{}, fmt.Errorf("invalid BOOTSTRAP_RETRY_INTERVAL %q: %w", bootstrapRetryIntervalRaw, err)
	}
	cfg.BootstrapRetryInterval = bootstrapRetryInterval

	directStreamTimeoutRaw := firstNonEmpty(getenv("EDGE_DIRECT_STREAM_TIMEOUT"), "750ms")
	directStreamTimeout, err := time.ParseDuration(directStreamTimeoutRaw)
	if err != nil {
		return Config{}, fmt.Errorf("invalid EDGE_DIRECT_STREAM_TIMEOUT %q: %w", directStreamTimeoutRaw, err)
	}
	cfg.DirectStreamTimeout = directStreamTimeout

	return cfg, nil
}

// App owns the lifecycle of the edge gateway runtime.
type App struct {
	cfg            Config
	gateway        *Gateway
	stopSubscriber chan struct{}
	httpServer     *http.Server
	adminServer    *http.Server
}

// Gateway holds all the components of the edge gateway.
type Gateway struct {
	host                host.Host
	pubsub              *pubsub.PubSub
	cache               *discovery.Cache
	subscriber          *discovery.PubSubSubscriber
	routes              *routing.RouteTable
	relayPeers          []peer.AddrInfo
	directStreamTimeout time.Duration
}

// New constructs a new edge runtime.
func New(ctx context.Context, cfg Config) (*App, error) {
	gw, stopSubscriber, err := newGateway(ctx, cfg.P2PListen, cfg.Seed, cfg.RelayPeers, cfg.DirectStreamTimeout, cfg.PrivateKeyFile, cfg.PrivateKeyB64)
	if err != nil {
		return nil, err
	}

	app := &App{
		cfg:            cfg,
		gateway:        gw,
		stopSubscriber: stopSubscriber,
		httpServer: &http.Server{
			Addr:    cfg.HTTPListen,
			Handler: ingressMux(gw),
		},
		adminServer: &http.Server{
			Addr:    cfg.AdminListen,
			Handler: adminMux(gw),
		},
	}

	go app.gateway.syncDiscoveryRoutes(ctx)
	return app, nil
}

// Start runs the edge gateway until ctx is cancelled or a server exits with an error.
func (a *App) Start(ctx context.Context) error {
	defer a.close()

	log.Printf("edge gateway config listen=%s admin_listen=%s p2p_listen=%s seed_configured=%t bootstrap_peers=%d relay_peers=%d bootstrap_retry_interval=%s direct_stream_timeout=%s", a.cfg.HTTPListen, a.cfg.AdminListen, a.cfg.P2PListen, a.cfg.Seed != "", len(a.cfg.BootstrapPeers), len(a.gateway.relayPeers), a.cfg.BootstrapRetryInterval, a.cfg.DirectStreamTimeout)
	log.Printf("edge gateway peer_id=%s", a.gateway.host.ID())
	log.Printf("edge gateway addrs: %v", p2p.PeerAddrs(a.gateway.host))

	if len(a.cfg.BootstrapPeers) > 0 {
		dialBootstrapPeers(a.gateway.host, a.cfg.BootstrapPeers)
		go func() {
			ticker := time.NewTicker(a.cfg.BootstrapRetryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					dialBootstrapPeers(a.gateway.host, a.cfg.BootstrapPeers)
				}
			}
		}()
	}

	errCh := make(chan error, 2)
	go func() {
		log.Printf("edge gateway HTTP listening on %s", a.cfg.HTTPListen)
		if err := a.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("HTTP server: %w", err)
		}
	}()
	go func() {
		log.Printf("edge gateway admin API listening on %s", a.cfg.AdminListen)
		if err := a.adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("Admin server: %w", err)
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.httpServer.Shutdown(shutdownCtx)
		_ = a.adminServer.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		return err
	}
}

func (a *App) close() {
	if a.stopSubscriber != nil {
		close(a.stopSubscriber)
		a.stopSubscriber = nil
	}
	if a.gateway != nil {
		a.gateway.cache.Stop()
		_ = a.gateway.host.Close()
	}
}

func ingressMux(gw *Gateway) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", gw.handleProxy)
	return mux
}

func adminMux(gw *Gateway) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/services", gw.handleListServices)
	mux.HandleFunc("/routes", gw.handleListRoutes)
	mux.HandleFunc("/add_route", gw.handleAddRoute)
	return mux
}

func newGateway(ctx context.Context, p2pListen, seed string, relayPeers []string, directStreamTimeout time.Duration, privateKeyFile, privateKeyB64 string) (*Gateway, chan struct{}, error) {
	psk, usingPrivateNetwork, err := p2p.LoadPrivateNetworkPSK(privateKeyFile, privateKeyB64)
	if err != nil {
		return nil, nil, fmt.Errorf("load private network key: %w", err)
	}

	h, err := p2p.NewHostWithSeedAndPSK(p2pListen, seed, psk)
	if err != nil {
		return nil, nil, fmt.Errorf("create host: %w", err)
	}
	if usingPrivateNetwork {
		log.Printf("libp2p private network enabled")
	}
	p2p.LogNetworkEvents(h, "edge")

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		_ = h.Close()
		return nil, nil, fmt.Errorf("create gossipsub: %w", err)
	}

	topic, err := ps.Join(discovery.DiscoveryTopic)
	if err != nil {
		_ = h.Close()
		return nil, nil, fmt.Errorf("join discovery topic: %w", err)
	}

	cache := discovery.NewCache(30*time.Second, 15*time.Second)
	sub := discovery.NewPubSubSubscriber(topic, cache)

	pubKey := h.Peerstore().PubKey(h.ID())
	if pubKey != nil {
		sub.AddPublicKey(h.ID(), pubKey)
	}

	stopCh := sub.Start(ctx)
	parsedRelayPeers, err := parseAddrInfos(relayPeers)
	if err != nil {
		close(stopCh)
		cache.Stop()
		_ = h.Close()
		return nil, nil, err
	}

	gw := &Gateway{
		host:                h,
		pubsub:              ps,
		cache:               cache,
		subscriber:          sub,
		routes:              routing.NewRouteTable(),
		relayPeers:          parsedRelayPeers,
		directStreamTimeout: directStreamTimeout,
	}
	if len(gw.relayPeers) > 0 {
		log.Printf("configured %d relay peer(s)", len(gw.relayPeers))
	}

	return gw, stopCh, nil
}

func (gw *Gateway) syncDiscoveryRoutes(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event := <-gw.subscriber.OnEvents():
			gw.handleDiscoveryEvent(event)
		}
	}
}

func (gw *Gateway) handleDiscoveryEvent(event discovery.DiscoveryEvent) {
	switch event.Type {
	case "added":
		rt := routing.Route{
			Hostname:    event.ServiceName,
			PathPrefix:  "/",
			ServiceName: event.ServiceName,
			PeerID:      event.PeerID.String(),
		}
		if err := gw.routes.Add(rt); err != nil {
			log.Printf("auto-route add failed for %q: %v", event.ServiceName, err)
		} else {
			log.Printf("auto-discovery route added: %s → service=%q peer=%s", rt.Hostname, rt.ServiceName, rt.PeerID)
		}
	case "removed":
		if gw.routes.Remove(event.ServiceName, "/") {
			log.Printf("auto-discovery route removed: %s (service expired)", event.ServiceName)
		}
	default:
		log.Printf("unknown discovery event type: %q", event.Type)
	}
}

// tryRelayFallback attempts to connect to targetPeer through each relay peer in sequence.
// It returns the first successful stream or the last error encountered.
func tryRelayFallback(ctx context.Context, h host.Host, targetPeer peer.ID, relayPeers []peer.AddrInfo) (network.Stream, error) {
	var lastErr error

	if hasLimitedConnToPeer(h, targetPeer) {
		stream, err := h.NewStream(network.WithAllowLimitedConn(ctx, "reuse existing relayed tunnel stream"), targetPeer, p2p.ProtocolID)
		if err == nil {
			log.Printf("relay fallback reusing existing limited connection target=%s", targetPeer)
			return stream, nil
		}
		lastErr = err
	}

	for _, relayAddrInfo := range relayPeers {
		log.Printf("relay fallback attempting relay=%s target=%s addrs=%v", relayAddrInfo.ID, targetPeer, relayAddrInfo.Addrs)
		if len(relayAddrInfo.Addrs) == 0 {
			log.Printf("relay %s has no addresses, skipping", relayAddrInfo.ID)
			continue
		}

		if err := h.Connect(ctx, relayAddrInfo); err != nil {
			log.Printf("connect to relay %s failed: %v", relayAddrInfo.ID, err)
			lastErr = err
			continue
		}

		relayPeerMaddr, err := multiaddr.NewMultiaddr("/p2p/" + relayAddrInfo.ID.String())
		if err != nil {
			log.Printf("failed to build relay peer multiaddr: %v", err)
			lastErr = err
			continue
		}
		relayMaddr := relayAddrInfo.Addrs[0].Encapsulate(relayPeerMaddr)
		circuitMaddr := relayMaddr.Encapsulate(multiaddr.StringCast("/p2p-circuit"))
		targetMaddr, err := multiaddr.NewMultiaddr("/p2p/" + targetPeer.String())
		if err != nil {
			log.Printf("failed to build target multiaddr: %v", err)
			lastErr = err
			continue
		}
		fullMaddr := circuitMaddr.Encapsulate(targetMaddr)
		log.Printf("relay fallback dialing circuit=%s", fullMaddr)

		circuitInfo, err := peer.AddrInfoFromP2pAddr(fullMaddr)
		if err != nil {
			log.Printf("failed to parse circuit multiaddr: %v", err)
			lastErr = err
			continue
		}

		if !hasLimitedConnToPeer(h, targetPeer) {
			if err := h.Connect(ctx, *circuitInfo); err != nil {
				log.Printf("relay circuit dial failed via relay %s: %v", relayAddrInfo.ID, err)
				lastErr = err
				continue
			}
		}

		stream, err := h.NewStream(network.WithAllowLimitedConn(ctx, "relay fallback tunnel stream"), targetPeer, p2p.ProtocolID)
		if err != nil {
			log.Printf("relay stream failed via relay %s: %v", relayAddrInfo.ID, err)
			lastErr = err
			continue
		}

		log.Printf("relay fallback succeeded via relay %s -> target %s", relayAddrInfo.ID, targetPeer)
		return stream, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all relays failed: %w", lastErr)
	}
	return nil, fmt.Errorf("no relay peers configured")
}

func hasLimitedConnToPeer(h host.Host, targetPeer peer.ID) bool {
	for _, conn := range h.Network().ConnsToPeer(targetPeer) {
		if conn.Stat().Limited {
			return true
		}
	}
	return false
}

func (gw *Gateway) handleProxy(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	hostname := r.Host
	if idx := strings.Index(hostname, ":"); idx >= 0 {
		hostname = hostname[:idx]
	}
	log.Printf("proxy request method=%s host=%s path=%s query=%q remote=%s", r.Method, hostname, r.URL.Path, r.URL.RawQuery, r.RemoteAddr)

	route, ok := gw.routes.Match(hostname, r.URL.Path)
	if !ok {
		log.Printf("proxy route missing host=%s path=%s duration=%s", hostname, r.URL.Path, time.Since(start))
		http.Error(w, "no route for "+hostname+r.URL.Path, http.StatusNotFound)
		return
	}

	log.Printf("route matched host=%s path=%s service=%q route_peer=%s", hostname, r.URL.Path, route.ServiceName, route.PeerID)

	entry, ok := gw.cache.Resolve(route.ServiceName)
	if !ok {
		log.Printf("discovery missing service=%q host=%s path=%s duration=%s", route.ServiceName, hostname, r.URL.Path, time.Since(start))
		http.Error(w, "service "+route.ServiceName+" not found in discovery", http.StatusBadGateway)
		return
	}

	log.Printf("resolved service=%q peer=%s addrs=%v", route.ServiceName, entry.PeerID, entry.Addresses)

	directCtx, cancelDirect := context.WithTimeout(r.Context(), gw.directStreamTimeout)
	stream, err := gw.host.NewStream(directCtx, entry.PeerID, p2p.ProtocolID)
	cancelDirect()
	connectionPath := "direct"
	if err != nil {
		log.Printf("direct stream failed to %s: %v", entry.PeerID, err)

		if len(gw.relayPeers) == 0 {
			log.Printf("proxy failed reason=peer_not_connected peer=%s relay_peers=0 duration=%s", entry.PeerID, time.Since(start))
			http.Error(w, "cannot reach "+entry.PeerID.String(), http.StatusBadGateway)
			return
		}

		stream, err = tryRelayFallback(r.Context(), gw.host, entry.PeerID, gw.relayPeers)
		if err != nil {
			log.Printf("proxy failed reason=relay_unavailable peer=%s err=%v duration=%s", entry.PeerID, err, time.Since(start))
			http.Error(w, "cannot reach "+entry.PeerID.String()+" (direct and relay): "+err.Error(), http.StatusBadGateway)
			return
		}
		connectionPath = "relayed"
	}
	defer stream.Close()

	headers := make(map[string][]string, len(r.Header))
	for k, vals := range r.Header {
		switch k {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		default:
			headers[k] = vals
		}
	}

	resp, err := p2p.HandleClientRequest(stream, r.Method, r.URL.Path, r.URL.RawQuery, headers, r.Body)
	if err != nil {
		log.Printf("proxy failed reason=stream_forward_failed service=%q peer=%s connection_path=%s err=%v duration=%s", route.ServiceName, entry.PeerID, connectionPath, err, time.Since(start))
		stream.Reset()
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	for k, vals := range resp.Header {
		switch k {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		default:
			for _, v := range vals {
				w.Header().Add(k, v)
			}
		}
	}

	w.WriteHeader(resp.StatusCode)

	bytesWritten, err := io.Copy(w, resp.Body)
	if err != nil {
		log.Printf("streaming response: %v", err)
	}
	log.Printf("proxy completed service=%q peer=%s connection_path=%s status=%d bytes=%d duration=%s", route.ServiceName, entry.PeerID, connectionPath, resp.StatusCode, bytesWritten, time.Since(start))
}

func (gw *Gateway) handleListServices(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "{\"count\":%d}\n", gw.cache.Count())
}

func (gw *Gateway) handleListRoutes(w http.ResponseWriter, r *http.Request) {
	routes := gw.routes.List()
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, "[")
	for i, rt := range routes {
		if i > 0 {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, `{"hostname":"%s","path_prefix":"%s","service":"%s","peer_id":"%s"}`,
			rt.Hostname, rt.PathPrefix, rt.ServiceName, rt.PeerID)
	}
	fmt.Fprint(w, "]\n")
}

func (gw *Gateway) handleAddRoute(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "parse form", http.StatusBadRequest)
		return
	}

	host := r.FormValue("hostname")
	pathPrefix := r.FormValue("path_prefix")
	serviceName := r.FormValue("service")
	peerIDStr := r.FormValue("peer_id")

	if host == "" || serviceName == "" {
		http.Error(w, "hostname and service are required", http.StatusBadRequest)
		return
	}

	var peerID string
	if peerIDStr != "" {
		peerID = peerIDStr
	}

	rt := routing.Route{
		Hostname:    host,
		PathPrefix:  pathPrefix,
		ServiceName: serviceName,
		PeerID:      peerID,
	}

	if err := gw.routes.Add(rt); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusCreated)
	fmt.Fprintf(w, "route added: %s%s → %s\n", host, pathPrefix, serviceName)
}

func dialBootstrapPeers(h host.Host, bootstrapPeers []string) {
	log.Println("dialing bootstrap peers")
	for _, raw := range bootstrapPeers {
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			log.Printf("invalid bootstrap peer %q: %v", raw, err)
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := h.Connect(ctx, info); err != nil {
			log.Printf("failed to dial bootstrap peer %s: %v", info.ID, err)
		} else {
			log.Printf("connected to bootstrap peer %s", info.ID)
		}
		cancel()
	}
}

func parseAddrInfos(raw []string) ([]peer.AddrInfo, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	infos := make([]peer.AddrInfo, 0, len(raw))
	for _, addr := range raw {
		info, err := p2p.AddrInfoFromString(addr)
		if err != nil {
			return nil, fmt.Errorf("invalid relay peer address %q: %w", addr, err)
		}
		infos = append(infos, info)
	}
	return infos, nil
}

func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	items := strings.Split(raw, ",")
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" {
			out = append(out, item)
		}
	}
	return out
}

func firstNonEmpty(v, def string) string {
	if v != "" {
		return v
	}
	return def
}
