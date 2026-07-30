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
	"github.com/origama/tubo-rebuilt/internal/node"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
)

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
	if spec.Target.Kind != domain.KindService {
		return errors.New("forwarding pipe runtime requires pipe.forward and is not implemented")
	}
	service, err := state.Service(ctx, spec.Target)
	if err != nil {
		return err
	}
	relay, swarm, reachability, err := state.Overlay(ctx)
	if err != nil {
		return err
	}
	bandwidth := coremetrics.NewBandwidthCounter()
	h, err := node.Open(ctx, node.Options{KeyFile: state.SecretPath("nodes", strings.ReplaceAll(spec.Ref.String(), "/", "-")+".key"), SwarmKeyFile: swarm, Private: reachability == "private", BandwidthReporter: bandwidth})
	if err != nil {
		return err
	}
	defer h.Close()
	relayInfo, err := node.ConnectRelay(ctx, h, relay, true)
	if err != nil {
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
		record := discovery.PipeRecord{Version: 2, PipeID: spec.PipeID, DisplayName: spec.Ref.Name, RuntimePeerID: h.ID().String(), Scope: *spec.Scope, Protocol: wire.TunnelID, Capabilities: []string{"raw-duplex", "half-close"}, Addresses: []string{address}, Visibility: spec.Visibility, Access: spec.Access, PublishGrant: publishGrant, IssuedAt: now, ExpiresAt: expires, Sequence: next}
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
	handler := &tunnelServer{state: state, spec: spec, service: service, authorityPublic: authorityPublic}
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
	lastRenewed := time.Now()
	return waitSignalRenew(ctx, 5*time.Second, func() error {
		disconnected := !node.PeerConnected(h, relayInfo.ID)
		recovery := disconnected || run.Status == "degraded"
		full := time.Since(lastRenewed) >= 5*time.Minute
		if !disconnected && !full {
			return nil
		}
		if err := node.EnsureRelay(ctx, h, *relayInfo, true); err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := refreshGrant(now); err != nil {
			return err
		}
		if err := publish(now); err != nil {
			return err
		}
		lastRenewed = time.Now()
		run.Status = "ready"
		_ = state.PutRun(context.Background(), run)
		if recovery {
			runtimeInfof("recovered %s relay=%s", spec.Ref, relayInfo.ID)
		}
		return nil
	}, func(err error) {
		first := run.Status != "degraded"
		run.Status = "degraded"
		_ = state.PutRun(context.Background(), run)
		if first {
			runtimeWarnf("degraded %s reason=relay_or_authority_unavailable detail=%s", spec.Ref, safeLogError(err))
		}
	})
}

type tunnelServer struct {
	state           *store.Store
	spec            domain.PipeSpec
	service         domain.ServiceSpec
	authorityPublic ed25519.PublicKey
}

func (s *tunnelServer) handle(stream core.Stream) {
	if err := s.serve(stream); err != nil {
		_ = stream.Reset()
	} else {
		_ = stream.Close()
	}
}
func (s *tunnelServer) serve(stream core.Stream) error {
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	var hello wire.ClientHello
	if err := wire.ReadFrame(stream, &hello); err != nil {
		return err
	}
	if hello.Version != "2.0" || len(hello.Capabilities) > 64 || !boundedStrings(8192, append([]string{hello.ClientPublic}, hello.Capabilities...)...) {
		return errors.New("protocol mismatch or field exceeds limit")
	}
	clientPublic, err := authority.PublicFromString(hello.ClientPublic)
	if err != nil {
		return err
	}
	challenge := randomNonce()
	if err := wire.WriteFrame(stream, wire.ServerHello{Version: "2.0", PipeID: s.spec.PipeID, Challenge: challenge, Capabilities: []string{"raw-duplex", "half-close"}}); err != nil {
		return err
	}
	var open wire.OpenPipe
	if err := wire.ReadFrame(stream, &open); err != nil {
		return err
	}
	if !boundedStrings(8192, append([]string{open.PipeID, open.ChallengeProof}, open.VisitedPipeIDs...)...) || len(open.AccessLease) > authority.MaxTokenSize {
		return s.openError(stream, "invalid_input", "open field exceeds limit")
	}
	if open.PipeID != s.spec.PipeID {
		return s.openError(stream, "not_found", "pipe_id mismatch")
	}
	if open.HopLimit <= 0 || open.HopLimit > domain.DefaultMaxPipeHops || len(open.VisitedPipeIDs) >= open.HopLimit {
		return s.openError(stream, "hop_limit_exceeded", "logical hop limit exceeded")
	}
	seen := map[string]bool{}
	for _, id := range open.VisitedPipeIDs {
		if seen[id] || id == s.spec.PipeID {
			return s.openError(stream, "loop_detected", "repeated pipe_id")
		}
		seen[id] = true
	}
	lease, err := authority.Decode(open.AccessLease)
	if err != nil {
		return s.openError(stream, "authorization_denied", "invalid access lease")
	}
	if err := lease.Verify(s.authorityPublic, time.Now().UTC()); err != nil {
		return s.openError(stream, "authorization_denied", "invalid access lease")
	}
	if lease.Type != "connect.access" || lease.Audience != s.spec.PipeID || lease.Subject != hello.ClientPublic || lease.Scope != *s.spec.Scope {
		return s.openError(stream, "authorization_denied", "access lease binding mismatch")
	}
	revoked, revokeErr := s.state.IsRevoked(context.Background(), s.spec.Scope.String(), "connect.access", lease.JTI)
	if revokeErr != nil {
		return s.openError(stream, "authorization_denied", "revocation state unavailable")
	}
	if revoked {
		return s.openError(stream, "authorization_denied", "access revoked")
	}
	accessEpoch, revokeErr := s.state.RevocationEpoch(context.Background(), s.spec.Scope.String(), "pipe-access", s.spec.PipeID)
	if revokeErr != nil {
		return s.openError(stream, "authorization_denied", "revocation state unavailable")
	}
	if lease.AccessEpoch < accessEpoch {
		return s.openError(stream, "authorization_denied", "access epoch revoked")
	}
	if !authority.VerifyProof(clientPublic, open.ChallengeProof, "tubo-open-v2", challenge, s.spec.PipeID) {
		return s.openError(stream, "authorization_denied", "invalid challenge proof")
	}
	target, err := dialService(s.service)
	if err != nil {
		return s.openError(stream, "target_unreachable", "local target unavailable")
	}
	defer target.Close()
	authorizationTimer := time.AfterFunc(time.Until(lease.ExpiresAt), func() { _ = target.Close(); _ = stream.Close() })
	defer authorizationTimer.Stop()
	sessionID, err := authority.SessionID()
	if err != nil {
		return err
	}
	revoked, revokeErr = s.state.IsRevoked(context.Background(), s.spec.Scope.String(), "session", sessionID)
	if revokeErr != nil || revoked {
		return s.openError(stream, "authorization_denied", "session revoked")
	}
	if err := wire.WriteFrame(stream, wire.OpenResult{OK: true, SessionID: sessionID, Protocol: "2.0"}); err != nil {
		return err
	}
	_ = stream.SetDeadline(time.Time{})
	session := store.Session{ID: sessionID, PipeRef: s.spec.Ref.String(), InitialTransportPath: "relayed", StartedAt: time.Now().UTC()}
	_ = s.state.PutSession(context.Background(), session)
	runtimeInfof("session opened pipe=%s session=%s path=relayed", s.spec.Ref, sessionID)
	in, out := copyDuplex(target, stream)
	session.BytesIn = in
	session.BytesOut = out
	_ = s.state.PutSession(context.Background(), session)
	runtimeInfof("session closed pipe=%s session=%s rx=%d tx=%d", s.spec.Ref, sessionID, in, out)
	return nil
}
func (s *tunnelServer) openError(stream core.Stream, code, message string) error {
	_ = wire.WriteFrame(stream, wire.OpenResult{OK: false, ErrorCode: code, Message: message, RetryClass: "terminal"})
	return errors.New(code + ": " + message)
}

func runLocalPipe(ctx context.Context, state *store.Store, spec domain.PipeSpec) error {
	if spec.Target.Kind != domain.KindPipe {
		return errors.New("local pipe target must be remote pipe")
	}
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
		go serveLocalConnection(ctx, state, h, spec, record, leases, connection)
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

func serveLocalConnection(ctx context.Context, state *store.Store, h host.Host, spec domain.PipeSpec, record discovery.PipeRecord, leases *localLeaseCache, local net.Conn) {
	defer local.Close()
	opened := false
	failureReason := "authority_unreachable"
	defer func() {
		if !opened && setRunStatus(state, spec.Ref.String(), "degraded") {
			runtimeWarnf("degraded %s reason=%s", spec.Ref, failureReason)
		}
	}()
	setup, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	lease, refresh, cached := leases.current()
	var err error
	if !cached {
		lease, refresh, err = RequestLeasePair(setup, state, h, record.Scope.String(), record.PipeID)
		if err != nil {
			return
		}
		if err := leases.set(lease, refresh); err != nil {
			return
		}
		leases.mu.Lock()
		expires := leases.expires
		leases.mu.Unlock()
		if err := state.PutConnectLease(context.Background(), record.PipeID, record.Scope.String(), lease, refresh, expires); err != nil {
			return
		}
	}
	// Refresh only supplies later OpenPipe handshakes. Live tunnel remains
	// bounded by lease used below; no tunnel resume/switch exists in 2.0.
	leases.startRefresh(ctx, state, h, record.Scope.String(), record.PipeID)
	failureReason = "pipe_unreachable"
	peerID, err := node.DialPeer(setup, h, record.Addresses[0])
	if err != nil {
		return
	}
	stream, err := h.NewStream(core.WithAllowLimitedConn(setup, "tubo tunnel via relay"), peerID, protocol.ID(wire.TunnelID))
	if err != nil {
		return
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	failureReason = "local_authorization_state"
	membership, err := state.Membership(setup, record.Scope.String())
	if err != nil {
		return
	}
	private, err := authority.ReadPrivate(membership.ClientKeyFile)
	if err != nil {
		return
	}
	failureReason = "tunnel_handshake_failed"
	public := authority.PublicString(private.Public().(ed25519.PublicKey))
	if err := wire.WriteFrame(stream, wire.ClientHello{Version: "2.0", ClientPublic: public, Capabilities: []string{"raw-duplex", "half-close"}}); err != nil {
		return
	}
	var server wire.ServerHello
	if err := wire.ReadFrame(stream, &server); err != nil {
		return
	}
	if server.PipeID != record.PipeID || server.Version != "2.0" {
		return
	}
	open := wire.OpenPipe{PipeID: record.PipeID, ChallengeProof: authority.SignProof(private, "tubo-open-v2", server.Challenge, record.PipeID), AccessLease: lease, VisitedPipeIDs: []string{spec.PipeID}, HopLimit: domain.DefaultMaxPipeHops}
	if err := wire.WriteFrame(stream, open); err != nil {
		return
	}
	var result wire.OpenResult
	if err := wire.ReadFrame(stream, &result); err != nil || !result.OK {
		return
	}
	opened = true
	if setRunStatus(state, spec.Ref.String(), "ready") {
		runtimeInfof("recovered %s through successful session", spec.Ref)
	}
	_ = stream.SetDeadline(time.Time{})
	session := store.Session{ID: result.SessionID, PipeRef: spec.Ref.String(), InitialTransportPath: "relayed", StartedAt: time.Now().UTC()}
	_ = state.PutSession(context.Background(), session)
	runtimeInfof("session opened pipe=%s session=%s path=relayed", spec.Ref, result.SessionID)
	in, out := copyDuplex(local, stream)
	session.BytesIn = in
	session.BytesOut = out
	_ = state.PutSession(context.Background(), session)
	runtimeInfof("session closed pipe=%s session=%s rx=%d tx=%d", spec.Ref, result.SessionID, in, out)
}

func dialService(service domain.ServiceSpec) (net.Conn, error) { return dialServiceTLS(service, nil) }

func dialServiceTLS(service domain.ServiceSpec, base *tls.Config) (net.Conn, error) {
	u, err := url.Parse(service.Target)
	if err != nil {
		return nil, err
	}
	if service.Kind == domain.ServiceHTTP || service.Kind == domain.ServiceA2A || service.Kind == domain.ServiceMCP {
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
