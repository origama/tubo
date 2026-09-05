package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	coremetrics "github.com/libp2p/go-libp2p/core/metrics"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo-rebuilt/internal/authority"
	"github.com/origama/tubo-rebuilt/internal/discovery"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/engine"
	"github.com/origama/tubo-rebuilt/internal/node"
	"github.com/origama/tubo-rebuilt/internal/openauth"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/reachability"
	"github.com/origama/tubo-rebuilt/internal/store"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

// baseSessionCapabilities is the only capability set advertised on the A0
// authorized /tubo/tunnel/2.0 path. Session resume is never advertised,
// offered, selected, or accepted here.
func baseSessionCapabilities() []string {
	return []string{"raw-duplex", "half-close"}
}

// authorizedOpenResult is the OpenOK sent only after successful authorization
// and target selection. It never carries a ResumeOffer.
func authorizedOpenResult(sessionID string) wire.OpenResult {
	return wire.OpenResult{OK: true, SessionID: sessionID, Protocol: "2.0"}
}

func RunPipe(ctx context.Context, state *store.Store, identifier string) error {
	spec, ownerKey, grant, err := state.Pipe(ctx, identifier)
	if err != nil {
		return err
	}
	runtimeInfof("starting %s", spec.Ref)
	switch spec.Ingress.Kind {
	case domain.IngressTubo:
		return runPublishedPipe(ctx, state, spec, ownerKey, grant)
	case domain.IngressLocal:
		return runLocalPipe(ctx, state, spec)
	default:
		return fmt.Errorf("unsupported runtime ingress %s", spec.Ingress.Kind)
	}
}

func runPublishedPipe(ctx context.Context, state *store.Store, spec domain.PipeSpec, ownerKeyFile, publishGrant string) error {
	nodeRuntime := NewNodeRuntime(ctx)
	defer nodeRuntime.Close()
	pipeRuntime, err := nodeRuntime.NewPipeRuntime()
	if err != nil {
		return err
	}
	if spec.Scope == nil {
		// Scope-less published ingress (Journey A): open baseline L1 host,
		// wire exact authorizer, install tunnel handler and redemption handler,
		// serve. No discovery, no publish grant, no membership, no scope sync,
		// no record announcement. Remote open still requires canonical AccessLease.
		return runScopeLessPublishedPipe(ctx, state, spec, ownerKeyFile, nodeRuntime, pipeRuntime)
	}
	// Target resolution is derived from the spec through the Pipe Engine. A
	// Service target is resolved locally; a Pipe target is validated against
	// the full logical path at startup and forwarded hop-by-hop through the
	// Engine in the serve handler (no pipe.forward special case).
	pipeEngine := newPipeEngine(state)
	var service domain.ServiceSpec
	if spec.Target.Kind == domain.KindService {
		service, err = state.Service(ctx, spec.Target)
		if err != nil {
			return err
		}
	} else {
		if _, err := pipeEngine.ResolvePath(spec.Ref, domain.DefaultMaxPipeHops); err != nil {
			return err
		}
	}
	relay, swarm, transport, err := relayOrBootstrap(ctx, state)
	if err != nil {
		return err
	}
	bandwidth := coremetrics.NewBandwidthCounter()
	h, err := node.Open(ctx, node.Options{KeyFile: state.SecretPath("nodes", strings.ReplaceAll(spec.Ref.String(), "/", "-")+".key"), SwarmKeyFile: swarm, Private: transport == "private", BandwidthReporter: bandwidth})
	if err != nil {
		return err
	}
	defer h.Close()
	relayInfo, err := node.RelayInfo(relay)
	if err != nil {
		return err
	}
	// L1 RelayReservation owns the relay connection and Circuit Relay v2
	// reservation lifecycle (initial acquisition, periodic renewal, reconnect,
	// reacquisition, failure/recovery). The initial reservation must succeed
	// before this pipe exposes a circuit locator (fail closed).
	reservation := reachability.NewRelayReservation(h, *relayInfo, 0)
	if err := reservation.Acquire(ctx); err != nil {
		return err
	}
	address, err := node.CircuitAddress(relay, h.ID())
	if err != nil {
		return err
	}
	owner, err := authority.ReadPrivate(ownerKeyFile)
	if err != nil {
		return err
	}
	localScope, localScopeErr := state.Scope(ctx, spec.Scope.String())
	remotePublisher := errors.Is(localScopeErr, domain.ErrNotFound)
	if localScopeErr != nil && !remotePublisher {
		return localScopeErr
	}
	if remotePublisher {
		if _, err := state.Membership(ctx, spec.Scope.String()); err != nil {
			return err
		}
	}
	authorityPublic, err := AuthorityPublicForScope(ctx, state, spec.Scope.String())
	if err != nil {
		return err
	}
	grant, err := authority.Decode(publishGrant)
	if err != nil || grant.Verify(authorityPublic, time.Now().UTC()) != nil || grant.Type != "pipe.publish" || grant.Audience != spec.PipeID || grant.Scope != *spec.Scope {
		return errors.New("authorization_denied: invalid publish grant")
	}
	publishEpoch, err := state.RevocationEpoch(ctx, spec.Scope.String(), "pipe-publish", spec.PipeID)
	if err != nil {
		return fmt.Errorf("persistence_failed: publish epoch: %w", err)
	}
	if grant.PublishEpoch < publishEpoch {
		return errors.New("authorization_denied: publish epoch revoked")
	}
	refreshGrant := func(now time.Time) error {
		if grant.ExpiresAt.After(now.Add(24 * time.Hour)) {
			return nil
		}
		var encoded string
		var refreshErr error
		if remotePublisher {
			encoded, refreshErr = RequestPublishGrant(context.Background(), state, h, spec.Scope.String(), spec.PipeID)
		} else {
			private, err := authority.ReadPrivate(localScope.AuthorityKeyFile)
			if err != nil {
				return err
			}
			value, err := authority.NewToken("pipe.publish", *spec.Scope, authority.PublicString(owner.Public().(ed25519.PublicKey)), spec.PipeID, 30*24*time.Hour)
			if err != nil {
				return err
			}
			value.PublishEpoch, err = state.RevocationEpoch(context.Background(), spec.Scope.String(), "pipe-publish", spec.PipeID)
			if err != nil {
				return err
			}
			if err := value.Sign(private); err != nil {
				return err
			}
			encoded, refreshErr = authority.Encode(value)
		}
		if refreshErr != nil {
			return refreshErr
		}
		value, err := authority.Decode(encoded)
		if err != nil || value.Verify(authorityPublic, now) != nil || value.Type != "pipe.publish" || value.Audience != spec.PipeID || value.Scope != *spec.Scope {
			return errors.New("authorization_denied: invalid refreshed publish grant")
		}
		if err := state.SetPipePublishGrant(context.Background(), spec.Ref.String(), encoded); err != nil {
			return err
		}
		publishGrant, grant = encoded, value
		return nil
	}
	sequence, err := state.RecordSequence(ctx, spec.Scope.String(), spec.PipeID)
	if err != nil {
		return err
	}
	publish := func(now time.Time) error {
		expires := now.Add(time.Hour)
		if grant.ExpiresAt.Before(expires) {
			expires = grant.ExpiresAt
		}
		if !expires.After(now) {
			return errors.New("authorization_denied: publish grant expired")
		}
		next := uint64(now.UnixNano())
		if next <= sequence {
			next = sequence + 1
		}
		caps := []string{"raw-duplex", "half-close"}
		record := discovery.PipeRecord{Version: 2, PipeID: spec.PipeID, DisplayName: spec.Ref.Name, RuntimePeerID: h.ID().String(), Scope: *spec.Scope, Protocol: wire.TunnelID, Capabilities: caps, Addresses: []string{address}, Visibility: spec.Visibility, Access: spec.Access, PublishGrant: publishGrant, IssuedAt: now, ExpiresAt: expires, Sequence: next}
		if err := record.Sign(owner); err != nil {
			return err
		}
		payload, err := record.Marshal()
		if err != nil {
			return err
		}
		stored := store.Record{Scope: spec.Scope.String(), PipeID: spec.PipeID, Name: spec.Ref.Name, Payload: payload, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence}
		if remotePublisher {
			if err := state.PutRecord(context.Background(), stored); err != nil {
				return err
			}
			if err := AnnounceRecord(context.Background(), state, h, record); err != nil {
				return err
			}
		} else if err := state.PutAuthoritativeRecord(context.Background(), stored); err != nil {
			return err
		}
		sequence = next
		return nil
	}
	if err := publish(time.Now().UTC()); err != nil {
		return err
	}
	// Wire the concrete ExactOpenAuthorizer behind the neutral openauth seam.
	// Load the exact authority key. If the key does not exist (e.g. pipe was
	// created before exact authority provisioning), fall back to nil authorizer
	// which denies all incoming opens.
	exactAuthorizer := loadExactAuthorizer(state, spec.PipeID)
	handler := &tunnelServer{
		state:            state,
		spec:             spec,
		service:          service,
		authorizer:       exactAuthorizer,
		runtime:          pipeRuntime,
		engine:           pipeEngine,
		downstreamOpener: newDownstreamPipeOpener(state, h, pipeRuntime),
	}
	h.SetStreamHandler(protocol.ID(wire.TunnelID), handler.handle)
	run := store.Run{PrimaryRef: spec.Ref.String(), Kind: "pipe", PID: os.Getpid(), Source: RunSource(), Status: "ready", PeerID: h.ID().String(), Address: address, StartedAt: time.Now().UTC()}
	if err := state.PutRun(ctx, run); err != nil {
		return err
	}
	runtimeInfof("ready %s peer=%s", spec.Ref, h.ID())
	stopTelemetry := startRuntimeTelemetry(ctx, state, spec.Ref.String(), h, bandwidth)
	defer stopTelemetry()
	defer func() {
		run.Status = "stopped"
		_ = state.PutRun(context.Background(), run)
		runtimeInfof("stopped %s", spec.Ref)
	}()
	stopSync, err := StartScopeSync(ctx, state, spec.Scope.String(), spec.Ref.String())
	if err != nil {
		return err
	}
	defer stopSync()
	// Relay reservation maintenance is an L1 Reachability responsibility
	// (internal/reachability.RelayReservation). The runtime only maps
	// maintenance outcomes onto the Run lifecycle and runs its scoped record
	// publication on each successful maintenance; it no longer decides when a
	// reservation is due.
	onReady, onFailure := publishedPipeLifecycle(state, &run, spec.Ref.String(), relayInfo.ID.String(), func() error {
		now := time.Now().UTC()
		if err := refreshGrant(now); err != nil {
			return err
		}
		return publish(now)
	})
	return reservation.Serve(ctx, onReady, onFailure)
}

// publishedPipeLifecycle maps an L1 RelayReservation's maintenance outcomes
// onto the existing Run degraded/ready lifecycle. It is the single status
// transition both scoped and scope-less published pipes consume: a maintenance
// failure marks degraded; a later successful maintenance restores ready. This
// centralizes the degraded/recovery translation beside the consumer so L2 holds
// no reservation timing or validity policy. A nil publish runs no record
// publication (Journey A scope-less).
func publishedPipeLifecycle(state *store.Store, run *store.Run, ref, relayID string, publish func() error) (onReady func(), onFailure func(error)) {
	onFailure = func(err error) {
		first := run.Status != "degraded"
		run.Status = "degraded"
		_ = state.PutRun(context.Background(), *run)
		if first {
			runtimeWarnf("degraded %s reason=relay_unavailable detail=%s", ref, safeLogError(err))
		}
	}
	onReady = func() {
		if publish != nil {
			if err := publish(); err != nil {
				onFailure(err)
				return
			}
		}
		recovered := run.Status == "degraded"
		run.Status = "ready"
		_ = state.PutRun(context.Background(), *run)
		if recovered {
			runtimeInfof("recovered %s relay=%s", ref, relayID)
		}
	}
	return onReady, onFailure
}

// errInitialCarrierTransferred signals that the tunnel data plane transferred
// terminal carrier action to an owner-bound Session. The stream handler must
// not issue a second reset/close once a Session owns the carrier.
var errInitialCarrierTransferred = errors.New("initial carrier ownership transferred to session")

// transferSessionAuthorizationTimer moves lease-expiry handling from the raw
// pre-transfer target ownership to Session.Stop after Carrier transfer. Session
// then owns Carrier terminal action and owner close waits all its cleanup.
func transferSessionAuthorizationTimer(timer *time.Timer, expiresAt time.Time, session *sessionPump) *time.Timer {
	if timer != nil {
		timer.Stop()
	}
	return time.AfterFunc(time.Until(expiresAt), session.Stop)
}

type tunnelServer struct {
	state            *store.Store
	spec             domain.PipeSpec
	service          domain.ServiceSpec
	authorizer       openauth.OpenAuthorizer
	dialFunc         func(domain.ServiceSpec) (net.Conn, error)
	runtime          *PipeRuntime
	engine           *engine.Engine
	downstreamOpener engine.DownstreamOpener
}

func runLocalPipe(ctx context.Context, state *store.Store, spec domain.PipeSpec) error {
	nodeRuntime := NewNodeRuntime(ctx)
	defer nodeRuntime.Close()
	pipeRuntime, err := nodeRuntime.NewPipeRuntime()
	if err != nil {
		return err
	}
	if spec.Target.Kind != domain.KindPipe {
		// Local ingress with a Service target (local passthrough) is an L4
		// workflow: it opens no remote hop, so no traversal, lease, or
		// authorization is involved. Step 8 owns that CLI/persistence slice;
		// this explicit deferred error replaces the prototype product-semantics
		// branch, which required Pipe targets only.
		return errors.New("invalid_input: local ingress with Service target (local passthrough) is deferred to Step 8 (L4 workflows)")
	}
	// Validate the full logical path (local pipe -> remote pipe chain ->
	// terminal service) through the Engine at startup: cycles, hop limits, and
	// invalid definitions fail closed before any listener accept.
	pipeEngine := newPipeEngine(state)

	// Check if this is a Journey A exact connect path (Scope-less, no discovery).
	// The target pipe ID is derived from the spec.Target.Name. A durable
	// accepted remote-Pipe binding provides relationship + reachability state;
	// the actual short-lived AccessLease is reacquired per open through the
	// binding (AcquireAcceptedPipeAccess), so an expired initial lease never
	// blocks reconnection.
	if _, bindErr := state.GetRemotePipeBinding(ctx, spec.Target.Name); bindErr == nil {
		return runLocalPipeExact(ctx, state, spec, nodeRuntime, pipeRuntime, pipeEngine)
	} else if !errors.Is(bindErr, domain.ErrNotFound) {
		return fmt.Errorf("persistence_failed: remote pipe binding: %w", bindErr)
	}

	if _, err := pipeEngine.ResolvePath(spec.Ref, domain.DefaultMaxPipeHops); err != nil {
		return err
	}
	// Fall through to the scoped discovery path for backward compatibility.
	recordStored, err := state.Record(ctx, spec.Target.Name)
	if err != nil {
		return err
	}
	record, err := ParseVerifiedRecord(ctx, state, recordStored)
	if err != nil {
		return err
	}
	bandwidth := coremetrics.NewBandwidthCounter()
	h, relayAddress, err := OpenOverlayHostWithReporter(ctx, state, strings.ReplaceAll(spec.Ref.String(), "/", "-"), bandwidth)
	if err != nil {
		return err
	}
	defer h.Close()
	relayInfo, err := node.RelayInfo(relayAddress)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", spec.Ingress.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	actual := listener.Addr().String()
	run := store.Run{PrimaryRef: spec.Ref.String(), Kind: "pipe", PID: os.Getpid(), Source: RunSource(), Status: "ready", PeerID: h.ID().String(), Address: actual, StartedAt: time.Now().UTC()}
	if err := state.PutRun(ctx, run); err != nil {
		return err
	}
	runtimeInfof("ready %s listen=%s", spec.Ref, actual)
	stopTelemetry := startRuntimeTelemetry(ctx, state, spec.Ref.String(), h, bandwidth)
	defer stopTelemetry()
	stopRecovery := startRelayRecovery(ctx, state, spec.Ref.String(), h, *relayInfo)
	defer stopRecovery()
	defer func() {
		run.Status = "stopped"
		_ = state.PutRun(context.Background(), run)
		runtimeInfof("stopped %s", spec.Ref)
	}()
	stopSync, err := StartScopeSync(ctx, state, record.Scope.String(), spec.Ref.String())
	if err != nil {
		return err
	}
	defer stopSync()
	resolver := newLocalPipeRecordResolver(record)
	go resolver.periodicallyResolve(ctx, state)
	go func() { <-ctx.Done(); listener.Close() }()
	go func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
		select {
		case <-signals:
			listener.Close()
		case <-ctx.Done():
		}
	}()
	leases := &localLeaseCache{}
	if scope, access, refresh, expires, leaseErr := state.ConnectLease(ctx, record.PipeID); leaseErr == nil && scope == record.Scope.String() && expires.After(time.Now().UTC()) {
		leases.access, leases.refresh, leases.expires = access, refresh, expires
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go serveLocalConnection(ctx, state, h, pipeRuntime, spec, resolver, leases, connection)
	}
}

const (
	localPipeReresolveInterval = 5 * time.Minute
	localPipeRecoveryCooldown  = 30 * time.Second
)

// localPipeRecordResolver owns the selected verified remote endpoint. It only
// swaps records accepted through the pinned-authority snapshot path. Existing
// raw tunnels retain their stream; only a setup that has not reached OpenOK may
// use a newly resolved endpoint.
type localPipeRecordResolver struct {
	mu     sync.RWMutex
	record discovery.PipeRecord
	fetch  func(context.Context, *store.Store, string, FetchRecordsOptions) ([]discovery.PipeRecord, error)

	refreshMu    sync.Mutex
	lastRecovery time.Time
}

func newLocalPipeRecordResolver(record discovery.PipeRecord) *localPipeRecordResolver {
	return &localPipeRecordResolver{record: clonePipeRecord(record), fetch: FetchRecordsWithOptions}
}

func clonePipeRecord(record discovery.PipeRecord) discovery.PipeRecord {
	record.Addresses = append([]string(nil), record.Addresses...)
	record.Capabilities = append([]string(nil), record.Capabilities...)
	return record
}

func (r *localPipeRecordResolver) current() discovery.PipeRecord {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return clonePipeRecord(r.record)
}

func sameEndpoint(a, b discovery.PipeRecord) bool {
	if a.RuntimePeerID != b.RuntimePeerID || len(a.Addresses) != len(b.Addresses) {
		return false
	}
	for i := range a.Addresses {
		if a.Addresses[i] != b.Addresses[i] {
			return false
		}
	}
	return true
}

// resolve refreshes only the remote pipe's pinned-authority scope. A stale,
// replayed, tampered, expired, or same-sequence-conflicting record never
// replaces the in-memory endpoint. Concurrent failure recovery is coalesced
// and bounded by the same cooldown as persisted discovery refresh failures.
func (r *localPipeRecordResolver) resolve(ctx context.Context, state *store.Store, recovery bool) (bool, error) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()
	now := time.Now().UTC()
	if recovery && now.Sub(r.lastRecovery) < localPipeRecoveryCooldown {
		return false, nil
	}
	if recovery {
		r.lastRecovery = now
	}
	current := r.current()
	records, err := r.fetch(ctx, state, current.Scope.String(), FetchRecordsOptions{})
	if err != nil {
		return false, err
	}
	var candidate *discovery.PipeRecord
	for i := range records {
		if records[i].PipeID == current.PipeID {
			candidate = &records[i]
			break
		}
	}
	if candidate == nil {
		return false, errors.New("pipe_unreachable: record absent from authoritative snapshot")
	}
	if candidate.Scope != current.Scope || candidate.Sequence < current.Sequence {
		return false, errors.New("invalid discovered record: replayed remote pipe record")
	}
	if candidate.Sequence == current.Sequence {
		if !sameEndpoint(*candidate, current) {
			return false, errors.New("invalid discovered record: conflicting remote pipe record sequence")
		}
		return false, nil
	}
	r.mu.Lock()
	// A concurrent reader cannot mutate records, but retain monotonicity if a
	// future resolver path updates while this fetch was in progress.
	if candidate.Sequence > r.record.Sequence {
		r.record = clonePipeRecord(*candidate)
		r.mu.Unlock()
		return true, nil
	}
	r.mu.Unlock()
	return false, nil
}

func (r *localPipeRecordResolver) periodicallyResolve(ctx context.Context, state *store.Store) {
	ticker := time.NewTicker(localPipeReresolveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if changed, err := r.resolve(ctx, state, false); err != nil {
				runtimeWarnf("remote pipe record refresh failed pipe=%s reason=%s", r.current().PipeID, safeLogError(err))
			} else if changed {
				runtimeInfof("remote pipe record updated pipe=%s", r.current().PipeID)
			}
		}
	}
}

type localLeaseCache struct {
	mu             sync.Mutex
	access         string
	refresh        string
	expires        time.Time
	refreshStarted bool
}

func (c *localLeaseCache) current() (string, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.access, c.refresh, c.access != "" && c.refresh != "" && time.Until(c.expires) > time.Minute
}
func (c *localLeaseCache) set(access, refresh string) error {
	lease, err := authority.Decode(access)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.access, c.refresh, c.expires = access, refresh, lease.ExpiresAt
	c.mu.Unlock()
	return nil
}
func (c *localLeaseCache) startRefresh(ctx context.Context, state *store.Store, h host.Host, scope, pipeID string) {
	c.mu.Lock()
	if c.refreshStarted {
		c.mu.Unlock()
		return
	}
	c.refreshStarted = true
	c.mu.Unlock()
	go func() {
		defer func() { c.mu.Lock(); c.refreshStarted = false; c.mu.Unlock() }()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				access, refresh, ok := c.current()
				if !ok {
					continue
				}
				setup, cancel := context.WithTimeout(ctx, 10*time.Second)
				newAccess, newRefresh, err := RequestRefreshLease(setup, state, h, scope, pipeID, access, refresh)
				cancel()
				if err == nil && c.set(newAccess, newRefresh) == nil {
					c.mu.Lock()
					expires := c.expires
					c.mu.Unlock()
					_ = state.PutConnectLease(context.Background(), pipeID, scope, newAccess, newRefresh, expires)
				}
			}
		}
	}()
}

func serveLocalConnection(ctx context.Context, state *store.Store, h host.Host, runtime *PipeRuntime, spec domain.PipeSpec, resolver *localPipeRecordResolver, leases *localLeaseCache, local net.Conn) {
	serveLocalConnectionWithOpener(ctx, state, spec, resolver, local, func(record discovery.PipeRecord) localConnectionResult {
		return openLocalConnection(ctx, state, h, runtime, spec, record, leases, local)
	})
}

// serveLocalConnectionWithOpener owns the recovery decision separately from
// transport setup. Keeping this seam internal makes the safety boundary
// testable: an opener may be retried exactly once only after a transient
// pre-OpenOK failure and a verified endpoint change. The opener owns all
// application reads, so this loop never consumes local bytes itself.
func serveLocalConnectionWithOpener(ctx context.Context, state *store.Store, spec domain.PipeSpec, resolver *localPipeRecordResolver, local net.Conn, open func(discovery.PipeRecord) localConnectionResult) {
	defer local.Close()
	opened := false
	failureReason := "authority_unreachable"
	defer func() {
		if !opened && setRunStatus(state, spec.Ref.String(), "degraded") {
			runtimeWarnf("degraded %s reason=%s", spec.Ref, failureReason)
		}
	}()
	// One bounded retry is permitted only after a classified transient setup
	// failure and only before OpenOK. No data is read from local until OpenOK.
	for attempt := 0; attempt < 2; attempt++ {
		result := open(resolver.current())
		failureReason = result.reason
		if result.opened {
			opened = true
			return
		}
		if !result.transient || attempt == 1 {
			return
		}
		recovery, cancel := context.WithTimeout(ctx, 10*time.Second)
		changed, err := resolver.resolve(recovery, state, true)
		cancel()
		if err != nil || !changed {
			return
		}
	}
}

type localConnectionResult struct {
	opened    bool
	transient bool
	reason    string
}

func openLocalConnection(ctx context.Context, state *store.Store, h host.Host, runtime *PipeRuntime, spec domain.PipeSpec, record discovery.PipeRecord, leases *localLeaseCache, local net.Conn) localConnectionResult {
	result := localConnectionResult{reason: "authority_unreachable"}
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lease, refresh, cached := leases.current()
	var err error
	if !cached {
		lease, refresh, err = RequestLeasePair(setup, state, h, record.Scope.String(), record.PipeID)
		if err != nil {
			return result
		}
		if err := leases.set(lease, refresh); err != nil {
			return result
		}
		leases.mu.Lock()
		expires := leases.expires
		leases.mu.Unlock()
		if err := state.PutConnectLease(context.Background(), record.PipeID, record.Scope.String(), lease, refresh, expires); err != nil {
			return result
		}
	}
	leases.startRefresh(ctx, state, h, record.Scope.String(), record.PipeID)
	result.reason = "pipe_unreachable"
	if len(record.Addresses) == 0 {
		return result
	}
	remotePeerID, err := node.DialPeer(setup, h, record.Addresses[0])
	if err != nil {
		result.transient = true
		return result
	}
	stream, err := h.NewStream(core.WithAllowLimitedConn(setup, "tubo tunnel via relay"), remotePeerID, protocol.ID(wire.TunnelID))
	if err != nil {
		result.transient = true
		return result
	}
	streamOwned := false
	defer func() {
		if !streamOwned {
			_ = stream.Close()
		}
	}()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	result.reason = "local_authorization_state"
	membership, err := state.Membership(setup, record.Scope.String())
	if err != nil {
		return result
	}
	private, err := authority.ReadPrivate(membership.ClientKeyFile)
	if err != nil {
		return result
	}
	result.reason = "tunnel_handshake_failed"
	public := authority.PublicString(private.Public().(ed25519.PublicKey))
	if err := wire.WriteFrame(stream, wire.ClientHello{Version: "2.0", ClientPublic: public, Capabilities: baseSessionCapabilities()}); err != nil {
		result.transient = true
		return result
	}
	var server wire.ServerHello
	if err := wire.ReadFrame(stream, &server); err != nil {
		result.transient = true
		return result
	}
	if server.PipeID != record.PipeID || server.Version != "2.0" {
		return result
	}
	open := wire.OpenPipe{PipeID: record.PipeID, ChallengeProof: authority.SignProof(private, "tubo-open-v2", server.Challenge, record.PipeID), AccessLease: lease, VisitedPipeIDs: []string{spec.PipeID}, HopLimit: domain.DefaultMaxPipeHops}
	if err := wire.WriteFrame(stream, open); err != nil {
		result.transient = true
		return result
	}
	var openResult wire.OpenResult
	if err := wire.ReadFrame(stream, &openResult); err != nil {
		result.transient = true
		return result
	}
	if !openResult.OK {
		return result
	}
	result.opened = true
	if setRunStatus(state, spec.Ref.String(), "ready") {
		runtimeInfof("recovered %s through successful session", spec.Ref)
	}
	_ = stream.SetDeadline(time.Time{})

	sessionID := openResult.SessionID
	session := store.Session{ID: sessionID, PipeRef: spec.Ref.String(), InitialTransportPath: "relayed", CurrentTransportPath: "relayed", StartedAt: time.Now().UTC()}
	_ = state.PutSession(context.Background(), session)
	runtimeInfof("session opened pipe=%s session=%s path=relayed", spec.Ref, sessionID)

	// Re-grounded client data plane: an owner-bound Session owns the Carrier and
	// the local application connection. The authorized OpenOK above must precede
	// any local byte flow; the Session pump preserves raw full-duplex + exact
	// half-close. On transfer the Session owns stream terminal action, so the
	// deferred raw close must be suppressed to avoid a second terminal action.
	carrier := node.WrapStream(stream)
	sp, sessionErr := runtime.NewSession(sessionConfig{
		SessionID:    sessionID,
		PipeID:       record.PipeID,
		Scope:        record.Scope.String(),
		TargetConn:   local,
		InitialEpoch: 1,
		IsClient:     true,
	})
	if sessionErr != nil {
		result.opened = false
		return result
	}
	if !sp.SetActiveCarrier(carrier) {
		// Session already terminalized the wrapped Carrier; suppress the raw close.
		result.opened = false
		return result
	}
	streamOwned = true
	sp.runClientSession()
	in, out := sp.ByteCounts()
	session.BytesIn = in
	session.BytesOut = out
	_ = state.PutSession(context.Background(), session)
	runtimeInfof("session closed pipe=%s session=%s rx=%d tx=%d", spec.Ref, sessionID, in, out)
	return result
}

var errNoRecordAddress = errors.New("pipe_unreachable: no record addresses")

// openRemotePipeCarrier opens an authorized tunnel to a remote Pipe and
// returns the established carrier and session ID. It performs the bounded
// tunnel handshake (ClientHello / ServerHello / OpenPipe / OpenResult) with
// the given traversal state, acquiring an access lease through the scope
// authority when no cached lease is available. It is the tunnel-open
// primitive shared by the local-pipe client path and the serve-side
// Pipe-to-Pipe forwarding opener (newDownstreamPipeOpener).
//
// Transient setup failures (dial/stream/frame) are wrapped so the caller can
// classify and retry exactly once before OpenOK; lease/authority failures are
// permanent (fail closed).
func openRemotePipeCarrier(ctx context.Context, state *store.Store, h host.Host, record discovery.PipeRecord, traversal engine.TraversalState, leases *localLeaseCache) (node.Carrier, string, error) {
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lease, refresh, cached := leases.current()
	var err error
	if !cached {
		lease, refresh, err = RequestLeasePair(setup, state, h, record.Scope.String(), record.PipeID)
		if err != nil {
			return nil, "", err
		}
		if err := leases.set(lease, refresh); err != nil {
			return nil, "", err
		}
		leases.mu.Lock()
		expires := leases.expires
		leases.mu.Unlock()
		if err := state.PutConnectLease(context.Background(), record.PipeID, record.Scope.String(), lease, refresh, expires); err != nil {
			return nil, "", err
		}
	}
	leases.startRefresh(ctx, state, h, record.Scope.String(), record.PipeID)
	if len(record.Addresses) == 0 {
		return nil, "", errNoRecordAddress
	}
	remotePeerID, err := node.DialPeer(setup, h, record.Addresses[0])
	if err != nil {
		return nil, "", transient(err)
	}
	stream, err := h.NewStream(core.WithAllowLimitedConn(setup, "tubo tunnel via relay"), remotePeerID, protocol.ID(wire.TunnelID))
	if err != nil {
		return nil, "", transient(err)
	}
	streamOwned := false
	defer func() {
		if !streamOwned {
			_ = stream.Close()
		}
	}()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))

	membership, err := state.Membership(setup, record.Scope.String())
	if err != nil {
		return nil, "", err
	}
	private, err := authority.ReadPrivate(membership.ClientKeyFile)
	if err != nil {
		return nil, "", err
	}
	public := authority.PublicString(private.Public().(ed25519.PublicKey))
	if err := wire.WriteFrame(stream, wire.ClientHello{Version: "2.0", ClientPublic: public, Capabilities: baseSessionCapabilities()}); err != nil {
		return nil, "", transient(err)
	}
	var server wire.ServerHello
	if err := wire.ReadFrame(stream, &server); err != nil {
		return nil, "", transient(err)
	}
	if server.PipeID != record.PipeID || server.Version != "2.0" {
		return nil, "", errors.New("tunnel_handshake_failed: pipe or version mismatch")
	}
	hopLimit := traversal.MaxHops
	if hopLimit <= 0 {
		hopLimit = domain.DefaultMaxPipeHops
	}
	open := wire.OpenPipe{PipeID: record.PipeID, ChallengeProof: authority.SignProof(private, "tubo-open-v2", server.Challenge, record.PipeID), AccessLease: lease, VisitedPipeIDs: traversal.VisitedIDs, HopLimit: hopLimit}
	if err := wire.WriteFrame(stream, open); err != nil {
		return nil, "", transient(err)
	}
	var openResult wire.OpenResult
	if err := wire.ReadFrame(stream, &openResult); err != nil {
		return nil, "", transient(err)
	}
	if !openResult.OK {
		return nil, "", fmt.Errorf("%s: %s", openResult.ErrorCode, openResult.Message)
	}
	_ = stream.SetDeadline(time.Time{})
	streamOwned = true
	return node.WrapStream(stream), openResult.SessionID, nil
}

// transientSetupError classifies pre-OpenOK setup failures that may be retried
// exactly once after a verified endpoint change. Lease/membership/authority
// failures are permanent and are not wrapped.
type transientSetupError struct {
	err error
}

func (e *transientSetupError) Error() string { return e.err.Error() }
func (e *transientSetupError) Unwrap() error { return e.err }

func transient(err error) error {
	if err == nil {
		return nil
	}
	return &transientSetupError{err: err}
}

func isTransientSetupError(err error) bool {
	var target *transientSetupError
	return errors.As(err, &target)
}

func dialService(service domain.ServiceSpec) (net.Conn, error) { return dialServiceTLS(service, nil) }

func dialServiceTLS(service domain.ServiceSpec, base *tls.Config) (net.Conn, error) {
	u, err := url.Parse(service.Target)
	if err != nil {
		return nil, err
	}
	if service.Kind == domain.ServiceHTTP {
		if u.Scheme == "https" {
			config := &tls.Config{MinVersion: tls.VersionTLS12}
			if base != nil {
				config = base.Clone()
				if config.MinVersion < tls.VersionTLS12 {
					config.MinVersion = tls.VersionTLS12
				}
			}
			if config.ServerName == "" {
				config.ServerName = u.Hostname()
			}
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			return tls.DialWithDialer(dialer, "tcp", u.Host, config)
		}
		return net.DialTimeout("tcp", u.Host, 5*time.Second)
	}
	switch service.Kind {
	case domain.ServiceTCP:
		return net.DialTimeout("tcp", u.Host, 5*time.Second)
	case domain.ServiceUnix:
		return net.DialTimeout("unix", u.Path, 5*time.Second)
	default:
		return nil, errors.New("unsupported service kind")
	}
}

type closeWriter interface{ CloseWrite() error }
type countWriter struct {
	writer io.Writer
	n      atomic.Int64
}

func (w *countWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.n.Add(int64(n))
	return n, err
}
func copyDuplex(a, b io.ReadWriteCloser) (int64, int64) {
	var wg sync.WaitGroup
	var ab, ba countWriter
	ab.writer = b
	ba.writer = a
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&ab, a)
		if value, ok := b.(closeWriter); ok {
			_ = value.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(&ba, b)
		if value, ok := a.(closeWriter); ok {
			_ = value.CloseWrite()
		}
	}()
	wg.Wait()
	return ba.n.Load(), ab.n.Load()
}

// loadExactAuthorizer loads the exact authority key for the pipe's remote-open
// authorization and returns a concrete ExactOpenAuthorizer behind the neutral
// openauth seam. If the key is not present (pipe provisioned before exact
// authority), it returns nil, which denies all inbound opens (fail closed).
// runScopeLessPublishedPipe runs a Scope-less published pipe (Journey A).
// It opens a baseline L1 host, wires the exact authorizer, installs the
// tunnel handler and the redemption handler, and serves. No discovery,
// no publish grant, no membership, no scope sync, no record announcement.
func runScopeLessPublishedPipe(
	ctx context.Context,
	state *store.Store,
	spec domain.PipeSpec,
	ownerKeyFile string,
	nodeRuntime *NodeRuntime,
	pipeRuntime *PipeRuntime,
) error {
	// Target resolution for Service targets.
	var service domain.ServiceSpec
	var err error
	if spec.Target.Kind == domain.KindService {
		service, err = state.Service(ctx, spec.Target)
		if err != nil {
			return err
		}
	}

	// Open baseline L1 host using configured overlay or bootstrap.
	relay, swarm, transport, err := relayOrBootstrap(ctx, state)
	if err != nil {
		return err
	}
	bandwidth := coremetrics.NewBandwidthCounter()
	h, err := node.Open(ctx, node.Options{
		KeyFile:           state.SecretPath("nodes", strings.ReplaceAll(spec.Ref.String(), "/", "-")+".key"),
		SwarmKeyFile:      swarm,
		Private:           transport == "private",
		BandwidthReporter: bandwidth,
	})
	if err != nil {
		return err
	}
	defer h.Close()

	relayInfo, err := node.RelayInfo(relay)
	if err != nil {
		return err
	}
	// L1 RelayReservation owns the relay connection and Circuit Relay v2
	// reservation lifecycle (initial acquisition, periodic renewal, reconnect,
	// reacquisition, failure/recovery). The initial reservation must succeed
	// before this pipe exposes a circuit locator (fail closed).
	reservation := reachability.NewRelayReservation(h, *relayInfo, 0)
	if err := reservation.Acquire(ctx); err != nil {
		return err
	}
	address, err := node.CircuitAddress(relay, h.ID())
	if err != nil {
		return err
	}

	// Wire the exact open authorizer.
	pipeEngine := newPipeEngine(state)
	exactAuthorizer := loadExactAuthorizer(state, spec.PipeID)

	// Install the tunnel handler.
	handler := &tunnelServer{
		state:            state,
		spec:             spec,
		service:          service,
		authorizer:       exactAuthorizer,
		runtime:          pipeRuntime,
		engine:           pipeEngine,
		downstreamOpener: newDownstreamPipeOpener(state, h, pipeRuntime),
	}
	h.SetStreamHandler(protocol.ID(wire.TunnelID), handler.handle)

	// Install the exact redemption handler.
	h.SetStreamHandler(protocol.ID(wire.ExactRedemptionID), newExactRedemptionHandler(state, spec.PipeID, exactLeaseTTL()).handle)

	// Install the exact renewal handler (durable accepted-binding reissuance).
	h.SetStreamHandler(protocol.ID(wire.ExactRenewalID), newExactRenewalHandler(state, spec.PipeID, exactLeaseTTL()).handle)

	// Record the run.
	run := store.Run{
		PrimaryRef: spec.Ref.String(),
		Kind:       "pipe",
		PID:        os.Getpid(),
		Source:     RunSource(),
		Status:     "ready",
		PeerID:     h.ID().String(),
		Address:    address,
		StartedAt:  time.Now().UTC(),
	}
	if err := state.PutRun(ctx, run); err != nil {
		return err
	}
	runtimeInfof("ready %s peer=%s", spec.Ref, h.ID())

	stopTelemetry := startRuntimeTelemetry(ctx, state, spec.Ref.String(), h, bandwidth)
	defer stopTelemetry()

	defer func() {
		run.Status = "stopped"
		_ = state.PutRun(context.Background(), run)
		runtimeInfof("stopped %s", spec.Ref)
	}()

	// No discovery, no publish grant, no membership, no scope sync, no record
	// announcement. Relay reservation maintenance is the sole L1 responsibility,
	// owned by the RelayReservation primitive; the runtime shares the same
	// degraded/ready lifecycle mapping as the scoped path (no record publication).
	onReady, onFailure := publishedPipeLifecycle(state, &run, spec.Ref.String(), relayInfo.ID.String(), nil)
	return reservation.Serve(ctx, onReady, onFailure)
}

// runLocalPipeExact runs the Journey A exact connect path for a local ingress
// Pipe that targets a remote Pipe. The remote Pipe must have an accepted
// durable binding (the one-time invite is never required again). Every local
// connection acquires verified current/fresh short-lived access material
// through the L4 AcquireAcceptedPipeAccess use case (ephemeral lease cache or
// post-acceptance renewal over the runtime's own host), then opens the remote
// tunnel. No discovery, no Scope, no membership, no PipeRecord.
func runLocalPipeExact(
	ctx context.Context,
	state *store.Store,
	spec domain.PipeSpec,
	nodeRuntime *NodeRuntime,
	pipeRuntime *PipeRuntime,
	pipeEngine *engine.Engine,
) error {
	bandwidth := coremetrics.NewBandwidthCounter()
	h, relayAddress, err := OpenOverlayHostWithReporter(ctx, state, strings.ReplaceAll(spec.Ref.String(), "/", "-"), bandwidth)
	if err != nil {
		return err
	}
	defer h.Close()
	relayInfo, err := node.RelayInfo(relayAddress)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", spec.Ingress.Listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	actual := listener.Addr().String()
	run := store.Run{PrimaryRef: spec.Ref.String(), Kind: "pipe", PID: os.Getpid(), Source: RunSource(), Status: "ready", PeerID: h.ID().String(), Address: actual, StartedAt: time.Now().UTC()}
	if err := state.PutRun(ctx, run); err != nil {
		return err
	}
	runtimeInfof("ready %s listen=%s", spec.Ref, actual)
	stopTelemetry := startRuntimeTelemetry(ctx, state, spec.Ref.String(), h, bandwidth)
	defer stopTelemetry()
	stopRecovery := startRelayRecovery(ctx, state, spec.Ref.String(), h, *relayInfo)
	defer stopRecovery()
	defer func() {
		run.Status = "stopped"
		_ = state.PutRun(context.Background(), run)
		runtimeInfof("stopped %s", spec.Ref)
	}()
	// No scope sync, no discovery, no membership.
	go func() { <-ctx.Done(); listener.Close() }()
	go func() {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(signals)
		select {
		case <-signals:
			listener.Close()
		case <-ctx.Done():
		}
	}()
	// Compose the L4 acquisition use case with narrow store adapters and the
	// renewal client. Reachability reuses this runtime's own host so no second
	// libp2p host is opened for renewal.
	uc := usecase.New(
		store.NewServiceRepoAdapter(state),
		store.NewPipeRepoAdapter(state),
	)
	uc.Secrets = state.SecretPath()
	uc.Bindings = store.NewRemotePipeBindingAdapter(state)
	uc.LeaseCache = store.NewAccessLeaseCacheAdapter(state)
	uc.Renewal = NewExactRenewalClient(state.SecretPath())
	uc.Reach = newHostCarrierAcquirer(h, protocol.ID(wire.ExactRenewalID))
	acquire := func(openCtx context.Context) (usecase.AcceptedPipeAccess, error) {
		return uc.AcquireAcceptedPipeAccess(openCtx, spec.Target.Name)
	}
	// Use the exact downstream opener to establish the remote tunnel.
	// Each local connection triggers a new tunnel open with fresh access.
	exactOpener := newExactDownstreamPipeOpener(state, h, spec.Target.Name, acquire)
	for {
		local, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go func(local net.Conn) {
			defer local.Close()
			carrier, sessionID, err := exactOpener(ctx, spec, engine.TraversalState{
				VisitedIDs: []string{spec.PipeID},
				MaxHops:    domain.DefaultMaxPipeHops,
			})
			if err != nil {
				runtimeWarnf("exact tunnel open failed: %s", safeLogError(err))
				if setRunStatus(state, spec.Ref.String(), "degraded") {
					runtimeWarnf("degraded %s reason=%s", spec.Ref, safeLogError(err))
				}
				return
			}
			defer carrier.Close()
			session := store.Session{ID: sessionID, PipeRef: spec.Ref.String(), InitialTransportPath: "relayed", CurrentTransportPath: "relayed", StartedAt: time.Now().UTC()}
			_ = state.PutSession(context.Background(), session)
			runtimeInfof("session opened pipe=%s session=%s path=relayed", spec.Ref, sessionID)
			sp, sessionErr := pipeRuntime.NewSession(sessionConfig{
				SessionID:    sessionID,
				PipeID:       spec.PipeID,
				Scope:        "",
				TargetConn:   local,
				InitialEpoch: 1,
				IsClient:     true,
			})
			if sessionErr != nil {
				return
			}
			if !sp.SetActiveCarrier(carrier) {
				return
			}
			sp.runClientSession()
			in, out := sp.ByteCounts()
			session.BytesIn = in
			session.BytesOut = out
			_ = state.PutSession(context.Background(), session)
			runtimeInfof("session closed pipe=%s session=%s rx=%d tx=%d", spec.Ref, sessionID, in, out)
		}(local)
	}
}

func loadExactAuthorizer(state *store.Store, pipeID string) openauth.OpenAuthorizer {
	if pipeID == "" {
		return nil
	}
	// Use lazy authorizer that loads the key on first AuthorizeOpen call.
	// This allows the pipe daemon to start before ShareExact provisions
	// the exact authority key.
	return NewLazyExactAuthorizer(state, pipeID)
}
