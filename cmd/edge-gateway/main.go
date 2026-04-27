package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
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

// Gateway holds all the components of the edge gateway.
type Gateway struct {
	host       host.Host
	pubsub     *pubsub.PubSub
	cache      *discovery.Cache
	subscriber *discovery.PubSubSubscriber
	routes     *routing.RouteTable
	relayPeers []peer.AddrInfo
}

func main() {
	listen := getenv("EDGE_LISTEN", ":8443")
	p2pListen := getenv("EDGE_P2P_LISTEN", "/ip4/0.0.0.0/tcp/4001")
	seed := getenv("EDGE_SEED", "")
	adminListen := getenv("EDGE_ADMIN_LISTEN", "127.0.0.1:8444")
	bootstrapPeers := getenv("BOOTSTRAP_PEERS", "")
	bootstrapRetryInterval, err := time.ParseDuration(getenv("BOOTSTRAP_RETRY_INTERVAL", "5s"))
	if err != nil {
		log.Fatalf("invalid BOOTSTRAP_RETRY_INTERVAL %q: %v", getenv("BOOTSTRAP_RETRY_INTERVAL", ""), err)
	}

	gw, err := newGateway(context.Background(), p2pListen, seed)
	if err != nil {
		log.Fatalf("create gateway: %v", err)
	}

	log.Printf("edge gateway peer_id=%s", gw.host.ID())
	log.Printf("edge gateway addrs: %v", p2p.PeerAddrs(gw.host))

	if bootstrapPeers != "" {
		dialBootstrapPeers(gw.host, bootstrapPeers)
		go func() {
			ticker := time.NewTicker(bootstrapRetryInterval)
			defer ticker.Stop()
			for range ticker.C {
				dialBootstrapPeers(gw.host, bootstrapPeers)
			}
		}()
	}

	// Start HTTP server (ingress)
	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})

		// Main proxy handler — match route → resolve → forward over P2P
		mux.HandleFunc("/", gw.handleProxy)

		log.Printf("edge gateway HTTP listening on %s", listen)
		if err := http.ListenAndServe(listen, mux); err != nil {
			log.Fatalf("HTTP server: %v", err)
		}
	}()

	// Start admin API server
	go func() {
		adminMux := http.NewServeMux()
		adminMux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		adminMux.HandleFunc("/services", gw.handleListServices)
		adminMux.HandleFunc("/routes", gw.handleListRoutes)
		adminMux.HandleFunc("/add_route", gw.handleAddRoute)

		log.Printf("edge gateway admin API listening on %s", adminListen)
		if err := http.ListenAndServe(adminListen, adminMux); err != nil {
			log.Fatalf("Admin server: %v", err)
		}
	}()

	// Wait forever (servers run in goroutines)
	select {}
}

func newGateway(ctx context.Context, p2pListen, seed string) (*Gateway, error) {
	psk, usingPrivateNetwork, err := p2p.LoadPrivateNetworkPSKFromEnv()
	if err != nil {
		return nil, fmt.Errorf("load private network key: %w", err)
	}

	h, err := p2p.NewHostWithSeedAndPSK(p2pListen, seed, psk)
	if err != nil {
		return nil, fmt.Errorf("create host: %w", err)
	}
	if usingPrivateNetwork {
		log.Printf("libp2p private network enabled")
	}

	ps, err := pubsub.NewGossipSub(ctx, h)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("create gossipsub: %w", err)
	}

	topic, err := ps.Join(discovery.DiscoveryTopic)
	if err != nil {
		h.Close()
		return nil, fmt.Errorf("join discovery topic: %w", err)
	}

	cache := discovery.NewCache(30*time.Second, 15*time.Second)
	sub := discovery.NewPubSubSubscriber(topic, cache)

	// Register our own public key so we can verify our announcements
	pubKey := h.Peerstore().PubKey(h.ID())
	if pubKey != nil {
		sub.AddPublicKey(h.ID(), pubKey)
	}

	// Start subscriber — it will populate the cache from pubsub messages
	stopCh := sub.Start(context.Background())

	gw := &Gateway{
		host:       h,
		pubsub:     ps,
		cache:      cache,
		subscriber: sub,
		routes:     routing.NewRouteTable(),
	}

	// Auto-discovery-to-route integration: listen for discovery events and
	// automatically create/remove routes so services are reachable without manual intervention.
	go func() {
		for event := range sub.OnEvents() {
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
	}()

	// On shutdown, stop subscriber and close host.
	// Important: do not close immediately, or the P2P listener disappears at startup.
	go func() {
		<-ctx.Done()
		close(stopCh)
		_ = h.Close()
	}()

	// Parse RELAY_PEERS env var (comma-separated multiaddrs of known relay nodes)
	if relayPeersStr := os.Getenv("RELAY_PEERS"); relayPeersStr != "" {
		for _, addrStr := range strings.Split(relayPeersStr, ",") {
			addrStr = strings.TrimSpace(addrStr)
			if addrStr == "" {
				continue
			}
			info, err := p2p.AddrInfoFromString(addrStr)
			if err != nil {
				log.Printf("invalid relay peer address %q: %v", addrStr, err)
				continue
			}
			gw.relayPeers = append(gw.relayPeers, info)
		}
		if len(gw.relayPeers) > 0 {
			log.Printf("configured %d relay peer(s)", len(gw.relayPeers))
		}
	}

	return gw, nil
}

// tryRelayFallback attempts to connect to targetPeer through each relay peer in sequence.
// It returns the first successful stream or the last error encountered.
func tryRelayFallback(ctx context.Context, h host.Host, targetPeer peer.ID, relayPeers []peer.AddrInfo) (network.Stream, error) {
	var lastErr error

	for _, relayAddrInfo := range relayPeers {
		if len(relayAddrInfo.Addrs) == 0 {
			log.Printf("relay %s has no addresses, skipping", relayAddrInfo.ID)
			continue
		}

		// Ensure connected to the relay peer (idempotent if already connected)
		if err := h.Connect(ctx, relayAddrInfo); err != nil {
			log.Printf("connect to relay %s failed: %v", relayAddrInfo.ID, err)
			lastErr = err
			continue
		}

		// Construct circuit multiaddr:
		// relay address + /p2p/<RELAY_ID> + /p2p-circuit + /p2p/<TARGET_ID>
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

		// Create AddrInfo from the full circuit multiaddr and dial through relay
		circuitInfo, err := peer.AddrInfoFromP2pAddr(fullMaddr)
		if err != nil {
			log.Printf("failed to parse circuit multiaddr: %v", err)
			lastErr = err
			continue
		}

		if err := h.Connect(ctx, *circuitInfo); err != nil {
			log.Printf("relay circuit dial failed via relay %s: %v", relayAddrInfo.ID, err)
			lastErr = err
			continue
		}

		// Open stream to target peer through the established relay circuit
		stream, err := h.NewStream(ctx, targetPeer, p2p.ProtocolID)
		if err != nil {
			log.Printf("relay stream failed via relay %s: %v", relayAddrInfo.ID, err)
			lastErr = err
			continue
		}

		log.Printf("relay fallback succeeded via relay %s → target %s", relayAddrInfo.ID, targetPeer)
		return stream, nil
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all relays failed: %w", lastErr)
	}
	return nil, fmt.Errorf("no relay peers configured")
}

func (gw *Gateway) handleProxy(w http.ResponseWriter, r *http.Request) {
	hostname := r.Host
	if idx := strings.Index(hostname, ":"); idx >= 0 {
		hostname = hostname[:idx] // strip port
	}

	// Match route by hostname + path
	route, ok := gw.routes.Match(hostname, r.URL.Path)
	if !ok {
		http.Error(w, "no route for "+hostname+r.URL.Path, http.StatusNotFound)
		return
	}

	log.Printf("route matched: %s%s → service=%q peer=%s", hostname, r.URL.Path, route.ServiceName, route.PeerID)

	// Resolve service via discovery cache
	entry, ok := gw.cache.Resolve(route.ServiceName)
	if !ok {
		http.Error(w, "service "+route.ServiceName+" not found in discovery", http.StatusBadGateway)
		return
	}

	log.Printf("resolved %s → peer %s (addrs: %v)", route.ServiceName, entry.PeerID, entry.Addresses)

	// Open stream to resolved peer (direct P2P connection)
	stream, err := gw.host.NewStream(r.Context(), entry.PeerID, p2p.ProtocolID)
	if err != nil {
		log.Printf("direct stream failed to %s: %v", entry.PeerID, err)

		if len(gw.relayPeers) == 0 {
			http.Error(w, "cannot reach "+entry.PeerID.String(), http.StatusBadGateway)
			return
		}

		stream, err = tryRelayFallback(r.Context(), gw.host, entry.PeerID, gw.relayPeers)
		if err != nil {
			log.Printf("relay fallback failed for %s: %v", entry.PeerID, err)
			http.Error(w, "cannot reach "+entry.PeerID.String()+" (direct and relay): "+err.Error(), http.StatusBadGateway)
			return
		}
	}
	defer stream.Close()

	// Build headers map (filter hop-by-hop)
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

	// Forward request over P2P stream using HandleClientRequest
	resp, err := p2p.HandleClientRequest(stream, r.Method, r.URL.Path, r.URL.RawQuery, headers, r.Body)
	if err != nil {
		log.Printf("forward failed: %v", err)
		stream.Reset()
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// Copy response headers (filter hop-by-hop)
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

	// Stream response body to client
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("streaming response: %v", err)
	}
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

func dialBootstrapPeers(h host.Host, bootstrapPeers string) {
	log.Println("dialing bootstrap peers")
	for _, raw := range strings.Split(bootstrapPeers, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
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

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
