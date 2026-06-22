package service

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	circuitclient "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	p2pping "github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/multiformats/go-multiaddr"

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/reachability"
	statspkg "github.com/origama/tubo/internal/runtime/stats"
)

type Config struct {
	Listen, Seed, ServiceName, ServiceKind, Target, HealthListen, PrivateKeyFile, PrivateKeyB64, ForceReachability string
	BootstrapPeers, RelayPeers                                                                                     []string
	Autorelay, HolePunching                                                                                        bool
	HeartbeatInterval, BootstrapRetryInterval                                                                      time.Duration
	DiscoveryTopic                                                                                                 string
	DiscoveryPreviousTopic                                                                                         string
	DiscoveryMode                                                                                                  string
	DiscoveryClusterID                                                                                             string
	DiscoveryNamespaceID                                                                                           string
	DiscoveryContext                                                                                               *discovery.NamespaceDiscoveryContext
	DiscoveryPreviousContext                                                                                       *discovery.NamespaceDiscoveryContext
	AuthorityPublicKey                                                                                             string
	AuthorityPrivateKeyFile                                                                                        string
	ClusterName                                                                                                    string
	ServiceID                                                                                                      string
	ServiceOwnerKeyFile                                                                                            string
	ConnectPolicy                                                                                                  string
	GrantService                                                                                                   *grantspkg.GrantServiceEndpoint
	MembershipCapabilityFile                                                                                       string
	ServiceClaimFile                                                                                               string
	ServicePublishLeaseFile                                                                                        string
	PublishAuthorizationHandler                                                                                    PublishAuthorizationHandler
	StatusReporter                                                                                                 func(RuntimeStatus)
	DiscoveryEnabled                                                                                               bool
	Visibility                                                                                                     string
}
type PublishAuthorizationOutcome string

const (
	PublishAuthorizationOutcomeReady       PublishAuthorizationOutcome = "ready"
	PublishAuthorizationOutcomePending     PublishAuthorizationOutcome = "pending"
	PublishAuthorizationOutcomeDenied      PublishAuthorizationOutcome = "denied"
	PublishAuthorizationOutcomeUnreachable PublishAuthorizationOutcome = "unreachable"
	PublishAuthorizationOutcomeRetryable   PublishAuthorizationOutcome = "retryable"
	PublishAuthorizationOutcomeSkipped     PublishAuthorizationOutcome = "skipped"
)

type PublishAuthorizationRequest struct {
	Reason AnnouncementBlockReason
}

type PublishAuthorizationResult struct {
	Outcome    PublishAuthorizationOutcome `json:"outcome"`
	Message    string                      `json:"message,omitempty"`
	RetryAfter *time.Time                  `json:"retry_after,omitempty"`
}

type PublishAuthorizationHandler func(context.Context, PublishAuthorizationRequest) PublishAuthorizationResult

type RuntimeStatus struct {
	Status             string
	Reason             string
	LastRefreshError   string
	NextRefreshRetryAt *time.Time
}

type announcementPublisher interface {
	PublishV3(context.Context, discovery.AnnouncementV3) error
}

type App struct {
	cfg                      Config
	host                     host.Host
	publisher                announcementPublisher
	hb                       *discovery.HeartbeatLoop
	discoveryMode            discovery.Mode
	serviceID                string
	serviceCapabilityFile    string
	serviceClaimFile         string
	servicePublishLeaseFile  string
	grantEndpointEnabled     bool
	health                   *http.Server
	cache                    *discovery.Cache
	subscriber               *discovery.PubSubSubscriber
	stopSubscriber           chan struct{}
	relayInfos               []peer.AddrInfo
	announcementTTL          time.Duration
	requireRelayReadyAnn     bool
	reservationMu            sync.RWMutex
	reservationReadyUntil    time.Time
	relayConnMu              sync.RWMutex
	relayConnected           map[peer.ID]bool
	announcementReachability *reachability.Manager
	announcementRecoveryBus  *reachability.Broadcaster
	announcementLogMu        sync.Mutex
	lastAnnouncementLogAt    time.Time
	statusReporter           func(RuntimeStatus)
	stats                    *statspkg.Collector
}

func LoadConfigFromEnv(getenv func(string) string) (Config, error) {
	cfg := Config{Listen: first(getenv("SERVICE_P2P_LISTEN"), "/ip4/127.0.0.1/tcp/40123"), Seed: first(getenv("NODE_SEED"), "service-demo-seed"), ServiceName: first(getenv("SERVICE_NAME"), "demo-service"), ServiceKind: first(getenv("SERVICE_KIND"), "http"), Target: first(getenv("SERVICE_TARGET"), "http://127.0.0.1:8000"), HealthListen: first(getenv("SERVICE_HEALTH_LISTEN"), "127.0.0.1:8091"), PrivateKeyFile: getenv("LIBP2P_PRIVATE_NETWORK_KEY"), PrivateKeyB64: getenv("LIBP2P_PRIVATE_NETWORK_KEY_B64"), BootstrapPeers: csv(getenv("BOOTSTRAP_PEERS")), RelayPeers: csv(getenv("RELAY_PEERS")), Autorelay: parseBool(getenv("ENABLE_AUTORELAY"), true), HolePunching: parseBool(getenv("ENABLE_HOLE_PUNCHING"), true), BootstrapRetryInterval: 5 * time.Second}
	if parseBool(getenv("FORCE_REACHABILITY_PRIVATE"), false) {
		cfg.ForceReachability = "private"
	}
	d, err := time.ParseDuration(first(getenv("HEARTBEAT_INTERVAL"), "15s"))
	if err != nil {
		return cfg, err
	}
	cfg.HeartbeatInterval = d
	return cfg, nil
}
func New(ctx context.Context, cfg Config) (*App, error) {
	cfg.ServiceKind = string(cfgpkg.NormalizeServiceKind(cfgpkg.ServiceKind(cfg.ServiceKind), cfg.Target))
	psk, using, err := p2p.LoadPrivateNetworkPSK(cfg.PrivateKeyFile, cfg.PrivateKeyB64)
	if err != nil {
		return nil, err
	}
	var opts []libp2p.Option
	if allowed, configured, err := p2p.LoadAllowedPeersFromEnv(); err != nil {
		return nil, err
	} else if configured {
		opts = append(opts, libp2p.ConnectionGater(p2p.NewPeerAllowlistConnectionGater(allowed)))
		log.Printf("peer allowlist enabled peers=%d", len(allowed))
	}
	relays := parseAddrInfos(cfg.RelayPeers)
	if len(relays) > 0 && cfg.Autorelay {
		opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(relays))
	}
	if cfg.HolePunching {
		opts = append(opts, libp2p.EnableHolePunching())
	}
	if cfg.ForceReachability == "private" {
		opts = append(opts, libp2p.ForceReachabilityPrivate())
	}
	h, err := p2p.NewHostWithSeedAndPSKAndOptions(cfg.Listen, cfg.Seed, psk, opts...)
	if err != nil {
		return nil, err
	}
	p2p.LogNetworkEvents(h, "service")
	if using {
		log.Printf("libp2p private network enabled")
	}
	mode := discovery.Mode(cfg.DiscoveryMode)
	if cfg.DiscoveryEnabled && mode != discovery.ModeNamespaceV3 {
		_ = h.Close()
		return nil, fmt.Errorf("discovery-enabled namespaces require discovery mode %q", discovery.ModeNamespaceV3)
	}
	var authorityPub ed25519.PublicKey
	if strings.TrimSpace(cfg.AuthorityPublicKey) == "" {
		if cfg.DiscoveryEnabled {
			_ = h.Close()
			return nil, fmt.Errorf("authority public key is required for discovery-enabled namespace runtime")
		}
	} else {
		parsedAuthority, err := discovery.ParseAuthorityPublicKey(cfg.AuthorityPublicKey)
		if err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("parse authority public key: %w", err)
		}
		authorityPub = parsedAuthority
	}
	var connectAuth *p2p.ConnectProofValidation
	if len(authorityPub) > 0 && strings.TrimSpace(cfg.DiscoveryClusterID) != "" && strings.TrimSpace(cfg.DiscoveryNamespaceID) != "" {
		connectAuth = &p2p.ConnectProofValidation{Require: true, AuthorityPublicKey: authorityPub, ClusterID: cfg.DiscoveryClusterID, NamespaceID: cfg.DiscoveryNamespaceID, ServiceID: resolveServiceID(cfg.DiscoveryClusterID, cfg.DiscoveryNamespaceID, cfg.ServiceID, cfg.ServiceName), ServicePeerID: h.ID().String(), Replay: p2p.NewConnectProofReplayCache(1024)}
	}
	p2pping.NewPingService(h)
	stats := statspkg.New(statspkg.Snapshot{Role: "service", Kind: cfg.ServiceKind, Service: cfg.ServiceName, ServiceID: h.ID().String(), Status: "running"})
	if cfg.ServiceKind == string(cfgpkg.ServiceKindTCP) {
		h.SetStreamHandler(p2p.ProtocolID, p2p.HandleServiceTCPStream(cfg.Target, connectAuth, stats))
	} else {
		h.SetStreamHandler(p2p.ProtocolID, p2p.HandleServiceStream(cfg.Target, connectAuth, stats))
	}
	grantEndpointEnabled := false
	if len(authorityPub) > 0 && strings.TrimSpace(cfg.DiscoveryClusterID) != "" && strings.TrimSpace(cfg.DiscoveryNamespaceID) != "" {
		grantEndpoint, err := newServiceGrantEndpoint(cfg, resolveServiceID(cfg.DiscoveryClusterID, cfg.DiscoveryNamespaceID, cfg.ServiceID, cfg.ServiceName), h.ID().String())
		if err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("configure service grant endpoint: %w", err)
		}
		h.SetStreamHandler(grantspkg.ProtocolID, grantEndpoint.HandleStream)
		grantEndpointEnabled = true
	}
	var pub *discovery.Publisher
	var cache *discovery.Cache
	var subscriber *discovery.PubSubSubscriber
	var stopSubscriber chan struct{}
	if cfg.DiscoveryEnabled {
		gs, err := pubsub.NewGossipSub(ctx, h, pubsub.WithFloodPublish(true))
		if err != nil {
			_ = h.Close()
			return nil, err
		}
		if cfg.DiscoveryContext == nil {
			_ = h.Close()
			return nil, fmt.Errorf("missing discovery context for namespace %s/%s", cfg.DiscoveryClusterID, cfg.DiscoveryNamespaceID)
		}
		topic, err := gs.Join(cfg.DiscoveryTopic)
		if err != nil {
			_ = h.Close()
			return nil, err
		}
		cache = discovery.NewCache(30*time.Second, time.Second)
		topics := []*pubsub.Topic{topic}
		contexts := []discovery.NamespaceDiscoveryContext{*cfg.DiscoveryContext}
		if cfg.DiscoveryPreviousTopic != "" && cfg.DiscoveryPreviousContext != nil {
			previousTopic, err := gs.Join(cfg.DiscoveryPreviousTopic)
			if err != nil {
				_ = h.Close()
				return nil, err
			}
			topics = append(topics, previousTopic)
			contexts = append(contexts, *cfg.DiscoveryPreviousContext)
		}
		subscriber = discovery.NewPubSubSubscriberV3(topics, cache, contexts)
		subscriber.SetAuthorityPublicKey(authorityPub)
		if pubKey := h.Peerstore().PubKey(h.ID()); pubKey != nil {
			subscriber.AddPublicKey(h.ID(), pubKey)
		}
		stopSubscriber = subscriber.Start(ctx)

		pk := h.Peerstore().PrivKey(h.ID())
		if pk == nil {
			close(stopSubscriber)
			cache.Stop()
			_ = h.Close()
			return nil, fmt.Errorf("no private key for peer")
		}
		pub = discovery.NewPublisher(topic, pk)
		h.SetStreamHandler(discoveryquery.ProtocolID, discoveryquery.HandleStream(h, "attach", cache))
	}
	app := &App{
		cfg:                      cfg,
		host:                     h,
		cache:                    cache,
		subscriber:               subscriber,
		stats:                    stats,
		stopSubscriber:           stopSubscriber,
		relayInfos:               relays,
		announcementTTL:          computeAnnouncementTTL(cfg.HeartbeatInterval),
		requireRelayReadyAnn:     len(relays) > 0 && (cfg.Autorelay || cfg.ForceReachability == "private"),
		relayConnected:           make(map[peer.ID]bool),
		announcementReachability: reachability.NewManager(reachability.ManagerConfig{Buffer: 4}),
		announcementRecoveryBus:  reachability.NewBroadcaster(),
		discoveryMode:            mode,
		serviceID:                resolveServiceID(cfg.DiscoveryClusterID, cfg.DiscoveryNamespaceID, cfg.ServiceID, cfg.ServiceName),
		serviceCapabilityFile:    cfg.MembershipCapabilityFile,
		serviceClaimFile:         cfg.ServiceClaimFile,
		servicePublishLeaseFile:  cfg.ServicePublishLeaseFile,
		grantEndpointEnabled:     grantEndpointEnabled,
	}
	if pub != nil {
		app.publisher = pub
	}
	app.registerRelayNotifiee()
	if cfg.StatusReporter != nil {
		app.SetStatusReporter(cfg.StatusReporter)
	}
	return app, nil
}
func (a *App) Host() host.Host { return a.host }

func (a *App) SetStatusReporter(report func(RuntimeStatus)) {
	a.statusReporter = report
	a.reportStatus(RuntimeStatus{Status: "running"})
}

func (a *App) reportStatus(status RuntimeStatus) {
	if a.stats != nil {
		a.stats.SetMeta(statspkg.Snapshot{Role: "service", Kind: a.cfg.ServiceKind, Service: a.cfg.ServiceName, ServiceID: a.host.ID().String(), Status: status.Status, Reason: status.Reason})
	}
	if a.statusReporter != nil {
		a.statusReporter(status)
	}
}

func (a *App) Start(ctx context.Context) error {
	defer a.host.Close()
	log.Printf("service agent config service=%q kind=%s target=%s p2p_listen=%s health_listen=%s", a.cfg.ServiceName, a.cfg.ServiceKind, a.cfg.Target, a.cfg.Listen, a.cfg.HealthListen)
	log.Printf("peer_id=%s", a.host.ID())
	dialBootstrapPeers(a.host, a.cfg.BootstrapPeers)
	if len(a.cfg.BootstrapPeers) > 0 && a.cfg.BootstrapRetryInterval > 0 {
		go func() {
			ticker := time.NewTicker(a.cfg.BootstrapRetryInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					dialBootstrapPeers(a.host, a.cfg.BootstrapPeers)
				}
			}
		}()
	}
	if a.cfg.HealthListen != "" {
		a.health = &http.Server{Addr: a.cfg.HealthListen, Handler: healthMux(a.host, a.stats)}
		go func() {
			log.Printf("service health listening on %s", a.cfg.HealthListen)
			if err := a.health.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("health: %v", err)
			}
		}()
	}
	if a.announcementReachability != nil && a.announcementRecoveryBus != nil {
		go a.announcementRecoveryBus.Run(ctx, a.announcementReachability.Events())
	}
	go a.maintainRelayReservations(ctx, a.announcementRecoveryEvents())
	if a.cfg.DiscoveryEnabled && a.discoveryMode == discovery.ModeNamespaceV3 {
		if reason, ok := a.publishCurrentAnnouncementV3(ctx); !ok && reason != AnnouncementReady {
			log.Printf("initial announcement deferred: %s", announcementBlockLogDetails(reason))
		}
		go a.runAnnouncementLoopV3(ctx, a.announcementRecoveryEvents())
	} else if a.cfg.DiscoveryEnabled {
		if !a.hb.PublishNow(ctx) {
			log.Printf("initial announcement deferred: reason=relay_not_ready message=%q", "relay reservation not ready yet")
		}
		a.hb.Start(ctx)
	}
	<-ctx.Done()
	if a.hb != nil {
		a.hb.Stop()
	}
	if a.health != nil {
		sd, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		_ = a.health.Shutdown(sd)
	}
	if a.stopSubscriber != nil {
		close(a.stopSubscriber)
	}
	if a.cache != nil {
		a.cache.Stop()
	}
	return nil
}
func computeAnnouncementTTL(interval time.Duration) time.Duration {
	ttl := interval * 2
	if ttl < 10*time.Second {
		ttl = 10 * time.Second
	}
	if ttl > 30*time.Second {
		ttl = 30 * time.Second
	}
	return ttl
}

func (a *App) hasConnectedRelay() bool {
	a.relayConnMu.RLock()
	defer a.relayConnMu.RUnlock()
	for _, connected := range a.relayConnected {
		if connected {
			return true
		}
	}
	return false
}

func (a *App) hasRelayReservation() bool {
	if len(a.relayInfos) > 0 && !a.hasConnectedRelay() {
		return false
	}
	if a.host != nil {
		for _, addr := range p2p.PeerAddrs(a.host) {
			if strings.Contains(addr, "/p2p-circuit") {
				return true
			}
		}
	}
	a.reservationMu.RLock()
	readyUntil := a.reservationReadyUntil
	a.reservationMu.RUnlock()
	return !readyUntil.IsZero() && time.Now().Before(readyUntil)
}

// relayReservationRenewMargin is how far ahead of the reservation expiry the
// maintenance loop proactively renews. It must be shorter than the relay's
// reservation_ttl (typically 1h) so that renewals happen well before expiry.
const relayReservationRenewMargin = 10 * time.Minute

// needsRelayReservation reports whether the maintenance loop should (re)acquire
// a relay reservation right now. Unlike hasRelayReservation, it does NOT treat
// a lingering /p2p-circuit address in Host.Addrs() as proof of a live
// reservation. It renews proactively based on the tracked expiry so that
// always-connected nodes (e.g. a grants-serve authority with a stable relay
// link) do not silently lose their reservation when the 1-hour TTL lapses.
func (a *App) needsRelayReservation() bool {
	if len(a.relayInfos) > 0 && !a.hasConnectedRelay() {
		return true
	}
	a.reservationMu.RLock()
	readyUntil := a.reservationReadyUntil
	a.reservationMu.RUnlock()
	if readyUntil.IsZero() {
		return true
	}
	return time.Now().After(readyUntil.Add(-relayReservationRenewMargin))
}

func mergeRelayCircuitAddrs(base []string, relayInfos []peer.AddrInfo, self peer.ID) []string {
	seen := make(map[string]struct{}, len(base)+len(relayInfos))
	out := make([]string, 0, len(base)+len(relayInfos))
	for _, addr := range base {
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	for _, relayInfo := range relayInfos {
		for _, addr := range relayInfo.Addrs {
			relayCircuit := fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s", addr.String(), relayInfo.ID, self)
			if _, ok := seen[relayCircuit]; ok {
				continue
			}
			seen[relayCircuit] = struct{}{}
			out = append(out, relayCircuit)
		}
	}
	return out
}

type AnnouncementBlockReason string

const (
	AnnouncementReady                       AnnouncementBlockReason = ""
	AnnouncementBlockedPublisherUnavailable AnnouncementBlockReason = "publisher_unavailable"
	AnnouncementBlockedRelayNotReady        AnnouncementBlockReason = "relay_not_ready"
	AnnouncementBlockedPublishLeaseMissing  AnnouncementBlockReason = "publish_lease_missing"
	AnnouncementBlockedPublishLeaseExpired  AnnouncementBlockReason = "publish_lease_expired"
	AnnouncementBlockedPublishLeaseInvalid  AnnouncementBlockReason = "publish_lease_invalid"
)

func announcementBlockDescription(reason AnnouncementBlockReason) string {
	switch reason {
	case AnnouncementBlockedPublisherUnavailable:
		return "discovery publisher unavailable"
	case AnnouncementBlockedRelayNotReady:
		return "relay reservation not ready yet"
	case AnnouncementBlockedPublishLeaseMissing:
		return "publish lease missing"
	case AnnouncementBlockedPublishLeaseExpired:
		return "publish lease expired"
	case AnnouncementBlockedPublishLeaseInvalid:
		return "publish lease invalid or unverifiable"
	default:
		return "announcement not ready"
	}
}

func announcementBlockLogDetails(reason AnnouncementBlockReason) string {
	return fmt.Sprintf("reason=%s message=%q", reason, announcementBlockDescription(reason))
}

func classifyPublishLeaseBlockReason(err error, leaseBytes []byte) AnnouncementBlockReason {
	if err == nil {
		if len(leaseBytes) == 0 {
			return AnnouncementBlockedPublishLeaseMissing
		}
		return AnnouncementReady
	}
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, os.ErrNotExist):
		return AnnouncementBlockedPublishLeaseMissing
	case strings.Contains(msg, "publish lease expired"):
		return AnnouncementBlockedPublishLeaseExpired
	default:
		return AnnouncementBlockedPublishLeaseInvalid
	}
}

func (a *App) currentAnnouncementV3() (discovery.AnnouncementV3, discovery.AnnouncementV3Payload, AnnouncementBlockReason, bool) {
	if a.discoveryMode != discovery.ModeNamespaceV3 || a.cfg.DiscoveryContext == nil {
		return discovery.AnnouncementV3{}, discovery.AnnouncementV3Payload{}, AnnouncementReady, false
	}
	addrs := expandUnspecifiedListenAddrs(p2p.PeerAddrs(a.host), a.cfg.Listen, a.host.ID())
	if a.requireRelayReadyAnn && !hasCircuitAddr(addrs) && !a.hasRelayReservation() {
		return discovery.AnnouncementV3{}, discovery.AnnouncementV3Payload{}, AnnouncementBlockedRelayNotReady, false
	}
	if a.requireRelayReadyAnn {
		addrs = mergeRelayCircuitAddrs(addrs, a.relayInfos, a.host.ID())
	}
	grantService := grantspkg.SanitizeGrantServiceEndpoint(a.cfg.GrantService)
	if a.grantEndpointEnabled {
		grantService = advertisedGrantServiceEndpoint(addrs)
	}
	payload := discovery.AnnouncementV3Payload{ClusterID: a.discoveryClusterID(), NamespaceID: a.discoveryNamespaceID(), Kind: discovery.ResourceKindService, ServiceName: a.cfg.ServiceName, ServiceKind: a.cfg.ServiceKind, ServiceID: a.serviceID, ConnectPolicy: strings.TrimSpace(a.cfg.ConnectPolicy), GrantService: grantService, Addresses: addrs, Capabilities: protocol.SupportedCapabilities(), RegisteredAt: time.Now().UTC()}
	if capBytes, err := a.loadMembershipCapabilityBytes(); err == nil && len(capBytes) > 0 {
		payload.MembershipCapability = capBytes
	}
	leaseBytes, lease, err := a.loadPublishLeaseBytes()
	if blockReason := classifyPublishLeaseBlockReason(err, leaseBytes); blockReason != AnnouncementReady {
		return discovery.AnnouncementV3{}, discovery.AnnouncementV3Payload{}, blockReason, false
	}
	payload.PublishLease = leaseBytes
	payload.ServicePublicKey = lease.ServicePublicKey
	if claimBytes, err := json.Marshal(lease.ServiceClaim); err == nil {
		payload.ServiceClaim = claimBytes
	}
	ann, err := discovery.NewAnnouncementV3(*a.cfg.DiscoveryContext, a.host.ID(), a.announcementTTL, payload)
	if err != nil {
		return discovery.AnnouncementV3{}, discovery.AnnouncementV3Payload{}, AnnouncementBlockedPublishLeaseInvalid, false
	}
	return ann, payload, AnnouncementReady, true
}

func isPublishAuthorizationBlock(reason AnnouncementBlockReason) bool {
	return reason == AnnouncementBlockedPublishLeaseMissing || reason == AnnouncementBlockedPublishLeaseExpired || reason == AnnouncementBlockedPublishLeaseInvalid
}

func (a *App) reconcilePublishAuthorization(ctx context.Context, reason AnnouncementBlockReason) PublishAuthorizationResult {
	if a == nil || a.cfg.PublishAuthorizationHandler == nil {
		return PublishAuthorizationResult{Outcome: PublishAuthorizationOutcomeSkipped}
	}
	result := a.cfg.PublishAuthorizationHandler(ctx, PublishAuthorizationRequest{Reason: reason})
	if result.Outcome != PublishAuthorizationOutcomeReady {
		return result
	}
	return result
}

func (a *App) publishCurrentAnnouncementV3(ctx context.Context) (AnnouncementBlockReason, bool) {
	if a.publisher == nil {
		return AnnouncementBlockedPublisherUnavailable, false
	}
	ann, payload, reason, ok := a.currentAnnouncementV3()
	recoveredAuth := false
	if !ok {
		blockedReason := reason
		a.recordAnnouncementReachabilityFailure(blockedReason)
		if isPublishAuthorizationBlock(blockedReason) {
			result := a.reconcilePublishAuthorization(ctx, blockedReason)
			switch result.Outcome {
			case PublishAuthorizationOutcomeReady:
				if ann, payload, reason, ok = a.currentAnnouncementV3(); !ok {
					a.recordAnnouncementReachabilityFailure(reason)
					a.reportStatus(RuntimeStatus{Status: "degraded", Reason: announcementBlockDescription(reason), LastRefreshError: result.Message})
					log.Printf("publish authorization refreshed but announcement still blocked reason=%s", announcementBlockLogDetails(reason))
					return reason, false
				}
				recoveredAuth = true
				log.Printf("publish authorization refreshed reason=%s message=%q", announcementBlockDescription(blockedReason), result.Message)
			case PublishAuthorizationOutcomePending:
				log.Printf("publish authorization pending reason=%s message=%q", announcementBlockDescription(blockedReason), result.Message)
			case PublishAuthorizationOutcomeDenied:
				log.Printf("publish authorization denied reason=%s message=%q", announcementBlockDescription(blockedReason), result.Message)
			case PublishAuthorizationOutcomeUnreachable:
				log.Printf("publish authorization unreachable reason=%s message=%q", announcementBlockDescription(blockedReason), result.Message)
			case PublishAuthorizationOutcomeRetryable:
				log.Printf("publish authorization retryable reason=%s message=%q", announcementBlockDescription(blockedReason), result.Message)
			case PublishAuthorizationOutcomeSkipped:
				if strings.TrimSpace(result.Message) != "" {
					log.Printf("publish authorization skipped reason=%s message=%q", announcementBlockDescription(blockedReason), result.Message)
				}
			}
			a.reportPublishAuthorizationStatus(blockedReason, result)
		}
		if !ok {
			return reason, false
		}
	}
	if err := a.publisher.PublishV3(ctx, ann); err != nil {
		log.Printf("heartbeat immediate publish failed: %v", err)
		a.recordAnnouncementReachabilityFailure(AnnouncementBlockedRelayNotReady)
		a.reportStatus(RuntimeStatus{Status: "degraded", Reason: announcementBlockDescription(AnnouncementBlockedRelayNotReady), LastRefreshError: err.Error()})
		return AnnouncementReady, false
	}
	if recoveredAuth {
		a.recordAnnouncementReachabilitySuccess(reachability.SuccessKindGrant)
	} else {
		a.recordAnnouncementReachabilitySuccess(reachability.SuccessKindRelay)
	}
	a.reportStatus(RuntimeStatus{Status: "running"})
	a.cacheCurrentAnnouncementV3(payload, ann.TTL)
	if err := a.syncAnnouncementToPeers(ctx, payload); err != nil {
		log.Printf("heartbeat relay sync failed: %v", err)
	}
	if a.shouldLogAnnouncementPublish(time.Now().UTC()) {
		log.Printf("heartbeat published discovery v3 service %q (peer=%s)", a.cfg.ServiceName, ann.PeerID)
	}
	return AnnouncementReady, true
}

func (a *App) runAnnouncementLoopV3(ctx context.Context, recovery <-chan reachability.Event) {
	log.Printf("heartbeat loop started (interval=%s)", a.cfg.HeartbeatInterval)
	for {
		if err := ctx.Err(); err != nil {
			log.Println("heartbeat loop: context cancelled, stopping")
			return
		}
		if reason, ok := a.publishCurrentAnnouncementV3(ctx); !ok {
			if reason != AnnouncementReady {
				log.Printf("heartbeat skipped: %s; service remains running but is not advertised", announcementBlockLogDetails(reason))
			}
		}
		if recovered := reachability.WaitForRecovered(ctx, recovery, a.cfg.HeartbeatInterval); !recovered && ctx.Err() != nil {
			log.Println("heartbeat loop: context cancelled, stopping")
			return
		}
	}
}

func (a *App) recordAnnouncementReachabilityFailure(reason AnnouncementBlockReason) {
	if a.announcementReachability == nil {
		return
	}
	switch reason {
	case AnnouncementBlockedRelayNotReady:
		a.announcementReachability.RecordFailure(reachability.FailureKindRelay, errors.New(announcementBlockDescription(reason)))
	case AnnouncementBlockedPublishLeaseMissing, AnnouncementBlockedPublishLeaseExpired, AnnouncementBlockedPublishLeaseInvalid:
		a.announcementReachability.RecordFailure(reachability.FailureKindGrant, errors.New(announcementBlockDescription(reason)))
	case AnnouncementBlockedPublisherUnavailable:
		a.announcementReachability.RecordFailure(reachability.FailureKindNetwork, errors.New(announcementBlockDescription(reason)))
	}
}

func (a *App) recordAnnouncementReachabilitySuccess(kind reachability.SuccessKind) {
	if a.announcementReachability == nil {
		return
	}
	a.announcementReachability.RecordSuccess(kind)
}

func (a *App) reportPublishAuthorizationStatus(reason AnnouncementBlockReason, result PublishAuthorizationResult) {
	if a.statusReporter == nil {
		return
	}
	status := RuntimeStatus{Status: "degraded", Reason: announcementBlockDescription(reason), LastRefreshError: result.Message, NextRefreshRetryAt: result.RetryAfter}
	if result.Outcome == PublishAuthorizationOutcomeReady {
		status = RuntimeStatus{Status: "running"}
	}
	a.reportStatus(status)
}

func (a *App) shouldLogAnnouncementPublish(now time.Time) bool {
	if a == nil {
		return false
	}
	a.announcementLogMu.Lock()
	defer a.announcementLogMu.Unlock()
	if !a.lastAnnouncementLogAt.IsZero() && now.Sub(a.lastAnnouncementLogAt) < time.Second {
		return false
	}
	a.lastAnnouncementLogAt = now
	return true
}

func (a *App) announcementRecoveryEvents() <-chan reachability.Event {
	if a == nil || a.announcementReachability == nil {
		return nil
	}
	if a.announcementRecoveryBus == nil {
		return a.announcementReachability.Events()
	}
	return a.announcementRecoveryBus.Subscribe(4)
}

func (a *App) cacheCurrentAnnouncementV3(payload discovery.AnnouncementV3Payload, ttl time.Duration) {
	if a.cache == nil {
		return
	}
	if err := a.cache.AddV2(a.host.ID(), payload.ClusterID, payload.NamespaceID, payload.ServiceID, payload.ServiceName, payload.Kind, payload.ServiceKind, payload.ServicePublicKey, payload.ConnectPolicy, grantspkg.SanitizeGrantServiceEndpoint(payload.GrantService), append([]string(nil), payload.Addresses...), append([]string(nil), payload.Capabilities...), ttl); err != nil {
		log.Printf("heartbeat local cache update failed: %v", err)
	}
}

func (a *App) syncAnnouncementToPeers(ctx context.Context, payload discovery.AnnouncementV3Payload) error {
	if strings.TrimSpace(payload.ClusterID) != "" || strings.TrimSpace(payload.NamespaceID) != "" {
		return nil
	}
	peers := append([]string(nil), a.cfg.BootstrapPeers...)
	peers = append(peers, a.cfg.RelayPeers...)
	seen := make(map[string]struct{}, len(peers))
	service := discoveryquery.Service{Kind: payload.Kind, ClusterID: payload.ClusterID, NamespaceID: payload.NamespaceID, ServiceKind: payload.ServiceKind, Name: payload.ServiceName, ServiceID: payload.ServiceID, ServicePublicKey: payload.ServicePublicKey, ConnectPolicy: payload.ConnectPolicy, GrantService: grantspkg.CloneGrantServiceEndpoint(payload.GrantService), PeerID: a.host.ID().String(), Addresses: append([]string(nil), payload.Addresses...), Status: "online", TTLSeconds: int64(a.announcementTTL.Seconds()), Capabilities: append([]string(nil), payload.Capabilities...), RegisteredAt: payload.RegisteredAt.Format(time.RFC3339)}
	for _, raw := range peers {
		if raw == "" {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		info, err := p2p.AddrInfoFromString(raw)
		if err != nil {
			continue
		}
		if _, err := discoveryquery.AnnounceService(ctx, a.host, info, service); err != nil {
			log.Printf("discovery sync announce failed peer=%s: %v", info.ID, err)
		}
	}
	return nil
}

func (a *App) loadMembershipCapabilityBytes() ([]byte, error) {
	if a.serviceCapabilityFile == "" {
		return nil, nil
	}
	return os.ReadFile(a.serviceCapabilityFile)
}

func (a *App) loadPublishLeaseBytes() ([]byte, grantspkg.PublishLease, error) {
	if a.servicePublishLeaseFile == "" {
		return nil, grantspkg.PublishLease{}, nil
	}
	b, err := os.ReadFile(a.servicePublishLeaseFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, grantspkg.PublishLease{}, nil
		}
		return nil, grantspkg.PublishLease{}, err
	}
	if len(b) == 0 {
		return nil, grantspkg.PublishLease{}, nil
	}
	authorityPub, err := discovery.ParseAuthorityPublicKey(a.cfg.AuthorityPublicKey)
	if err != nil {
		return nil, grantspkg.PublishLease{}, err
	}
	lease, err := grantspkg.ParseAndVerifyPublishLeaseBytes(b, authorityPub, a.discoveryClusterIDValue(), a.discoveryNamespaceIDValue(), a.serviceID, a.host.ID().String())
	if err != nil {
		return nil, grantspkg.PublishLease{}, err
	}
	return b, lease, nil
}

func (a *App) discoveryClusterID() string {
	return a.discoveryClusterIDValue()
}

func (a *App) discoveryNamespaceID() string {
	return a.discoveryNamespaceIDValue()
}

func (a *App) discoveryClusterIDValue() string {
	return a.cfg.DiscoveryClusterID
}

func (a *App) discoveryNamespaceIDValue() string {
	return a.cfg.DiscoveryNamespaceID
}

func resolveServiceID(clusterID, namespaceID, explicitID, serviceName string) string {
	explicitID = strings.TrimSpace(explicitID)
	if explicitID != "" {
		return explicitID
	}
	if serviceName == "" {
		return ""
	}
	if clusterID == "" || namespaceID == "" {
		return serviceName
	}
	sum := sha256.Sum256([]byte(clusterID + "\x00" + namespaceID + "\x00" + serviceName))
	return "service-" + fmt.Sprintf("%x", sum[:8])
}

func hasCircuitAddr(addrs []string) bool {
	for _, addr := range addrs {
		if strings.Contains(addr, "/p2p-circuit") {
			return true
		}
	}
	return false
}

func expandUnspecifiedListenAddrs(addrs []string, listen string, self peer.ID) []string {
	if !strings.Contains(listen, "/ip4/0.0.0.0/") && !strings.Contains(listen, "/ip6/::/") {
		return addrs
	}
	seen := make(map[string]struct{}, len(addrs))
	out := make([]string, 0, len(addrs)+4)
	for _, addr := range addrs {
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	for _, addr := range addrs {
		if strings.Contains(addr, "/p2p-circuit") {
			continue
		}
		port := tcpPortFromMultiaddr(addr)
		if port == "" {
			continue
		}
		for _, ip := range interfaceIPs() {
			candidate := fmt.Sprintf("/ip4/%s/tcp/%s/p2p/%s", ip, port, self)
			if _, ok := seen[candidate]; ok {
				continue
			}
			seen[candidate] = struct{}{}
			out = append(out, candidate)
		}
	}
	return out
}

func tcpPortFromMultiaddr(addr string) string {
	parts := strings.Split(addr, "/")
	for i := 0; i < len(parts)-1; i++ {
		if parts[i] == "tcp" {
			return parts[i+1]
		}
	}
	return ""
}

func interfaceIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, raw := range addrs {
			var ip net.IP
			switch v := raw.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			ip4 := ip.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsUnspecified() {
				continue
			}
			out = append(out, ip4.String())
		}
	}
	return out
}

func (a *App) registerRelayNotifiee() {
	if a.host == nil || len(a.relayInfos) == 0 {
		return
	}
	relaySet := make(map[peer.ID]struct{}, len(a.relayInfos))
	for _, relayInfo := range a.relayInfos {
		relaySet[relayInfo.ID] = struct{}{}
	}
	a.host.Network().Notify(&network.NotifyBundle{
		ConnectedF: func(_ network.Network, conn network.Conn) {
			if _, ok := relaySet[conn.RemotePeer()]; !ok {
				return
			}
			a.relayConnMu.Lock()
			a.relayConnected[conn.RemotePeer()] = true
			a.relayConnMu.Unlock()
		},
		DisconnectedF: func(_ network.Network, conn network.Conn) {
			if _, ok := relaySet[conn.RemotePeer()]; !ok {
				return
			}
			a.relayConnMu.Lock()
			delete(a.relayConnected, conn.RemotePeer())
			a.relayConnMu.Unlock()
			a.reservationMu.Lock()
			a.reservationReadyUntil = time.Time{}
			a.reservationMu.Unlock()
			log.Printf("relay peer disconnected relay=%s; forcing reservation refresh", conn.RemotePeer())
		},
	})
}

func (a *App) maintainRelayReservations(ctx context.Context, recovery <-chan reachability.Event) {
	if !a.requireRelayReadyAnn || len(a.relayInfos) == 0 {
		return
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	lastReady := false

	publish := func() {
		if !a.cfg.DiscoveryEnabled {
			return
		}
		if a.discoveryMode == discovery.ModeNamespaceV3 {
			if reason, ok := a.publishCurrentAnnouncementV3(ctx); !ok && reason != AnnouncementReady {
				log.Printf("relay-ready publish skipped: %s", announcementBlockLogDetails(reason))
			}
			return
		}
		if a.hb != nil {
			a.hb.PublishNow(ctx)
		}
	}

	for {
		ready := a.hasRelayReservation()
		if ready && !lastReady {
			log.Printf("relay reservation observed in host addrs; publishing refreshed announcement")
			publish()
		}
		lastReady = ready

		if a.needsRelayReservation() {
			for _, relayInfo := range a.relayInfos {
				reserveCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := a.host.Connect(reserveCtx, relayInfo); err != nil {
					cancel()
					log.Printf("relay reservation connect failed relay=%s err=%v", relayInfo.ID, err)
					continue
				}
				reservation, err := circuitclient.Reserve(reserveCtx, a.host, relayInfo)
				cancel()
				if err != nil {
					log.Printf("relay reservation failed relay=%s err=%v", relayInfo.ID, err)
					continue
				}
				a.reservationMu.Lock()
				a.reservationReadyUntil = reservation.Expiration
				a.reservationMu.Unlock()
				log.Printf("relay reservation ready relay=%s expires=%s addrs=%d", relayInfo.ID, reservation.Expiration.Format(time.RFC3339), len(reservation.Addrs))
				if !lastReady {
					log.Printf("relay reservation refreshed; publishing announcement using reserved relay path")
					publish()
				}
				break
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-recovery:
		}
	}
}

func healthMux(h host.Host, stats *statspkg.Collector) *http.ServeMux {
	m := http.NewServeMux()
	m.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200); _, _ = w.Write([]byte("ok")) })
	m.HandleFunc("/statsz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if stats == nil {
			_ = json.NewEncoder(w).Encode(statspkg.Snapshot{CollectedAt: time.Now().UTC(), Role: "service", ServiceID: h.ID().String(), Status: "unknown", Reason: "stats unavailable"})
			return
		}
		snap := stats.Snapshot()
		if snap.Role == "" {
			snap.Role = "service"
		}
		if snap.ServiceID == "" {
			snap.ServiceID = h.ID().String()
		}
		if snap.Status == "" {
			snap.Status = "running"
		}
		_ = json.NewEncoder(w).Encode(snap)
	})
	m.HandleFunc("/debug/peer", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("peer_id=" + h.ID().String() + "\n"))
		for _, a := range p2p.PeerAddrs(h) {
			_, _ = w.Write([]byte("addr=" + a + "\n"))
		}
	})
	m.HandleFunc("/debug/protocol", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		events := p2p.RecentNegotiations()
		fmt.Fprintf(w, `{"preferred_stream_protocol_id":%q,"protocol_version":%q,"protocol_major":%d,"protocol_minor":%d,"supported_capabilities":[`,
			p2p.ProtocolID, p2p.ProtocolVersion, protocol.ProtocolMajor, protocol.ProtocolMinor)
		for i, cap := range protocol.SupportedCapabilities() {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, "%q", cap)
		}
		fmt.Fprint(w, `],"recent_negotiations":[`)
		for i, ev := range events {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"timestamp":%q,"local_role":%q,"remote_role":%q,"stream_protocol_id":%q,"local_protocol_version":%q,"remote_protocol_version":%q,"capabilities":[`,
				ev.Timestamp.Format(time.RFC3339), ev.LocalRole, ev.RemoteRole, ev.StreamProtocolID, ev.LocalProtocolVersion, ev.RemoteProtocolVersion)
			for j, cap := range ev.Capabilities {
				if j > 0 {
					fmt.Fprint(w, ",")
				}
				fmt.Fprintf(w, "%q", cap)
			}
			fmt.Fprint(w, "]}")
		}
		fmt.Fprint(w, "]}\n")
	})
	return m
}
func dialBootstrapPeers(h host.Host, peers []string) {
	for _, raw := range peers {
		m, err := multiaddr.NewMultiaddr(raw)
		if err != nil {
			log.Printf("invalid bootstrap peer %q: %v", raw, err)
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(m)
		if err != nil {
			log.Printf("bootstrap peer parse %q: %v", raw, err)
			continue
		}
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		err = h.Connect(ctx, *info)
		c()
		if err != nil {
			log.Printf("failed to dial bootstrap peer %s: %v", info.ID, err)
		}
	}
}
func parseAddrInfos(ss []string) []peer.AddrInfo {
	var out []peer.AddrInfo
	for _, s := range ss {
		m, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			continue
		}
		i, err := peer.AddrInfoFromP2pAddr(m)
		if err == nil {
			out = append(out, *i)
		}
	}
	return out
}
func first(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func csv(s string) []string {
	var o []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			o = append(o, p)
		}
	}
	return o
}
func parseBool(s string, d bool) bool {
	if s == "" {
		return d
	}
	return s == "true" || s == "1"
}
