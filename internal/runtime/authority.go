package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	coremetrics "github.com/libp2p/go-libp2p/core/metrics"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo/internal/authority"
	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/node"
	wire "github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/store"
)

func RunAuthority(ctx context.Context, state *store.Store, cluster, scopeName string) error {
	scope, err := state.Scope(ctx, scopeName)
	if err != nil {
		return err
	}
	parsed, err := parseScope(scopeName)
	if err != nil {
		return err
	}
	if parsed.Cluster != cluster {
		return fmt.Errorf("authority cluster %s does not match scope %s", cluster, scopeName)
	}
	if err := state.EnsureAuthoritySnapshot(ctx, scopeName); err != nil {
		return fmt.Errorf("initialize authority snapshot: %w", err)
	}
	private, err := authority.ReadPrivate(scope.AuthorityKeyFile)
	if err != nil {
		return err
	}
	relay, key, reachability, err := state.Overlay(ctx)
	if err != nil {
		return err
	}
	bandwidth := coremetrics.NewBandwidthCounter()
	h, err := node.Open(ctx, node.Options{KeyFile: state.SecretPath("nodes", "authority-"+cluster+".key"), SwarmKeyFile: key, Private: reachability == "private", BandwidthReporter: bandwidth})
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
	handler := &authorityHandler{state: state, scope: *parsed, private: private, public: private.Public().(ed25519.PublicKey)}
	h.SetStreamHandler(protocol.ID(wire.AuthorityID), handler.handle)
	h.SetStreamHandler(protocol.ID(wire.AuthoritySnapshotID), handler.handleSnapshot)
	ref := "authority/" + cluster
	run := store.Run{PrimaryRef: ref, Kind: "authority", PID: os.Getpid(), Source: RunSource(), Status: "ready", PeerID: h.ID().String(), Address: address, StartedAt: time.Now().UTC()}
	if err := state.PutRun(ctx, run); err != nil {
		return err
	}
	runtimeInfof("ready %s peer=%s", ref, h.ID())
	stopTelemetry := startRuntimeTelemetry(ctx, state, ref, h, bandwidth)
	defer stopTelemetry()
	defer func() {
		run.Status = "stopped"
		_ = state.PutRun(context.Background(), run)
		runtimeInfof("stopped %s", ref)
	}()
	lastRenewed := time.Now()
	return waitSignalRenew(ctx, 5*time.Second, func() error {
		disconnected := !node.PeerConnected(h, relayInfo.ID)
		recovery := disconnected || run.Status == "degraded"
		full := time.Since(lastRenewed) >= 10*time.Minute
		if !disconnected && !full {
			return nil
		}
		if err := node.EnsureRelay(ctx, h, *relayInfo, true); err != nil {
			return err
		}
		lastRenewed = time.Now()
		run.Status = "ready"
		_ = state.PutRun(context.Background(), run)
		if recovery {
			runtimeInfof("recovered %s relay=%s", ref, relayInfo.ID)
		}
		return nil
	}, func(err error) {
		first := run.Status != "degraded"
		run.Status = "degraded"
		_ = state.PutRun(context.Background(), run)
		if first {
			runtimeWarnf("degraded %s reason=relay_unavailable detail=%s", ref, safeLogError(err))
		}
	})
}

type authorityHandler struct {
	state   *store.Store
	scope   domain.Scope
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}

func (h *authorityHandler) handle(stream core.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	var request wire.AuthorityRequest
	if err := wire.ReadFrame(stream, &request); err != nil {
		_ = stream.Reset()
		return
	}
	response := h.process(context.Background(), request)
	if err := wire.WriteFrame(stream, response); err != nil {
		_ = stream.Reset()
	}
}
func (h *authorityHandler) process(ctx context.Context, request wire.AuthorityRequest) wire.AuthorityResponse {
	fail := func(err error) wire.AuthorityResponse { return wire.AuthorityResponse{OK: false, Error: err.Error()} }
	if !boundedStrings(8192, request.Operation, request.PipeID, request.ClientPublic, request.Nonce, request.Proof, request.AccessLease) || len(request.Token) > authority.MaxTokenSize || len(request.AccessLease) > authority.MaxTokenSize || len(request.Record) > discovery.MaxRecordSize || len(request.Subjects) > 256 {
		return fail(errors.New("invalid_input: authority field exceeds limit"))
	}
	invite, err := authority.Decode(request.Token)
	if err != nil {
		return fail(fmt.Errorf("authorization_denied: %w", err))
	}
	if err := invite.Verify(h.public, time.Now().UTC()); err != nil {
		return fail(fmt.Errorf("authorization_denied: %w", err))
	}
	if invite.Scope != h.scope {
		return fail(errors.New("authorization_denied: token scope mismatch"))
	}
	clientPublic, err := authority.PublicFromString(request.ClientPublic)
	if err != nil {
		return fail(errors.New("authorization_denied: invalid client key"))
	}
	if !authority.VerifyProof(clientPublic, request.Proof, "tubo-authority-v2", request.Operation, request.PipeID, request.Nonce, invite.JTI) {
		return fail(errors.New("authorization_denied: invalid proof of possession"))
	}
	membership := invite.Type == "membership.invite"
	requireMembership := func() error {
		if !membership {
			return errors.New("authorization_denied: invalid membership invite")
		}
		return nil
	}
	mintLeases := func(subject, pipeID string) (string, string, error) {
		epoch, err := h.state.RevocationEpoch(ctx, h.scope.String(), "pipe-access", pipeID)
		if err != nil {
			return "", "", fmt.Errorf("persistence_failed: revocation epoch: %w", err)
		}
		access, err := authority.NewToken("connect.access", h.scope, subject, pipeID, authority.AccessLeaseTTL)
		if err != nil {
			return "", "", err
		}
		refresh, err := authority.NewToken("connect.refresh", h.scope, subject, pipeID, authority.RefreshLeaseTTL)
		access.AccessEpoch, refresh.AccessEpoch = epoch, epoch
		if err != nil {
			return "", "", err
		}
		if err = access.Sign(h.private); err != nil {
			return "", "", err
		}
		if err = refresh.Sign(h.private); err != nil {
			return "", "", err
		}
		a, err := authority.Encode(access)
		if err != nil {
			return "", "", err
		}
		r, err := authority.Encode(refresh)
		return a, r, err
	}
	switch request.Operation {
	case "pipe_publish.request":
		if err := requireMembership(); err != nil {
			return fail(err)
		}
		if invite.Role != "publisher" {
			return fail(errors.New("authorization_denied: role cannot publish"))
		}
		if request.PipeID == "" {
			return fail(errors.New("invalid_input: pipe_id required"))
		}
		grant, err := authority.NewToken("pipe.publish", h.scope, request.ClientPublic, request.PipeID, 30*24*time.Hour)
		if err != nil {
			return fail(err)
		}
		if invite.ExpiresAt.Before(grant.ExpiresAt) {
			grant.ExpiresAt = invite.ExpiresAt
		}
		grant.PublishEpoch, err = h.state.RevocationEpoch(ctx, h.scope.String(), "pipe-publish", request.PipeID)
		if err != nil {
			return fail(fmt.Errorf("persistence_failed: publish epoch: %w", err))
		}
		if err := grant.Sign(h.private); err != nil {
			return fail(err)
		}
		encoded, err := authority.Encode(grant)
		if err != nil {
			return fail(err)
		}
		return wire.AuthorityResponse{OK: true, Grant: encoded}
	case "records.announce":
		if err := requireMembership(); err != nil {
			return fail(err)
		}
		if invite.Role != "publisher" {
			return fail(errors.New("authorization_denied: role cannot announce records"))
		}
		if len(request.Record) == 0 {
			return fail(errors.New("invalid_input: record required"))
		}
		record, err := discovery.Parse(request.Record)
		if err != nil {
			return fail(fmt.Errorf("invalid_input: record: %w", err))
		}
		if record.PipeID != request.PipeID || record.Scope != h.scope {
			return fail(errors.New("authorization_denied: record request binding mismatch"))
		}
		if err := record.Verify(h.public, h.scope, time.Now().UTC()); err != nil {
			return fail(fmt.Errorf("authorization_denied: record: %w", err))
		}
		grant, err := authority.Decode(record.PublishGrant)
		if err != nil || grant.Subject != request.ClientPublic {
			return fail(errors.New("authorization_denied: publish grant subject mismatch"))
		}
		payload, err := record.Marshal()
		if err != nil {
			return fail(err)
		}
		if err := h.state.PutAuthoritativeRecord(ctx, store.Record{Scope: h.scope.String(), PipeID: record.PipeID, Name: record.DisplayName, Payload: payload, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence}); err != nil {
			return fail(err)
		}
		return wire.AuthorityResponse{OK: true}
	case "records.list":
		if err := requireMembership(); err != nil {
			return fail(err)
		}
		if invite.Role != "member" && invite.Role != "viewer" && invite.Role != "publisher" && invite.Role != "forwarder" {
			return fail(errors.New("authorization_denied: role cannot discover"))
		}
		records, _, _, complete, err := h.state.AuthoritySnapshot(ctx, h.scope.String(), store.MaxSnapshotRecords)
		if err != nil {
			return fail(err)
		}
		result := make([]json.RawMessage, 0, len(records))
		size := 0
		for _, stored := range records {
			record, err := discovery.Parse(stored.Payload)
			if err != nil || record.Verify(h.public, h.scope, time.Now().UTC()) != nil {
				return fail(errors.New("persistence_failed: authority stored invalid discovery record"))
			}
			if record.Visibility != domain.VisibilityDiscoverable {
				continue
			}
			if size+len(stored.Payload) > wire.MaxFrame/2 {
				complete = false
				break
			}
			result = append(result, json.RawMessage(stored.Payload))
			size += len(stored.Payload)
		}
		if !complete {
			return fail(errors.New("incomplete authoritative snapshot: response exceeds bounds"))
		}
		return wire.AuthorityResponse{OK: true, Records: result}
	case "connect_access.issue":
		if err := requireMembership(); err != nil {
			return fail(err)
		}
		if invite.Role != "member" && invite.Role != "publisher" && invite.Role != "forwarder" {
			return fail(errors.New("authorization_denied: role cannot connect"))
		}
		if request.PipeID == "" {
			return fail(errors.New("invalid_input: pipe_id required"))
		}
		stored, err := h.state.RecordInScope(ctx, h.scope.String(), request.PipeID)
		if err != nil {
			return fail(errors.New("not_found: pipe record"))
		}
		record, err := discovery.Parse(stored.Payload)
		if err != nil || record.Verify(h.public, h.scope, time.Now().UTC()) != nil {
			return fail(errors.New("authorization_denied: invalid pipe publication"))
		}
		if record.Access != domain.AccessMembers {
			return fail(errors.New("authorization_denied: pipe requires an exact share invite"))
		}
		lease, refresh, err := mintLeases(request.ClientPublic, request.PipeID)
		if err != nil {
			return fail(err)
		}
		return wire.AuthorityResponse{OK: true, Lease: lease, RefreshLease: refresh}
	case "connect_refresh.exchange":
		if invite.Type != "connect.refresh" || invite.Audience != request.PipeID || invite.Subject != request.ClientPublic {
			return fail(errors.New("authorization_denied: invalid refresh lease"))
		}
		access, err := authority.Decode(request.AccessLease)
		if err != nil || access.Verify(h.public, time.Now().UTC()) != nil || access.Type != "connect.access" || access.Audience != invite.Audience || access.Subject != invite.Subject {
			return fail(errors.New("authorization_denied: invalid current access lease"))
		}
		epoch, epochErr := h.state.RevocationEpoch(ctx, h.scope.String(), "pipe-access", invite.Audience)
		if epochErr != nil {
			return fail(fmt.Errorf("persistence_failed: revocation epoch: %w", epochErr))
		}
		if access.AccessEpoch < epoch || invite.AccessEpoch < epoch {
			return fail(errors.New("authorization_denied: access epoch revoked"))
		}
		for _, subject := range [][2]string{{"connect.refresh", invite.JTI}, {"connect.access", access.JTI}} {
			revoked, checkErr := h.state.IsRevoked(ctx, h.scope.String(), subject[0], subject[1])
			if checkErr != nil {
				return fail(fmt.Errorf("persistence_failed: revocation: %w", checkErr))
			}
			if revoked {
				return fail(errors.New("authorization_denied: lease revoked"))
			}
		}
		lease, refresh, err := mintLeases(request.ClientPublic, request.PipeID)
		if err != nil {
			return fail(err)
		}
		return wire.AuthorityResponse{OK: true, Lease: lease, RefreshLease: refresh}
	case "share_invite.issue":
		if err := requireMembership(); err != nil {
			return fail(err)
		}
		if invite.Role != "publisher" {
			return fail(errors.New("authorization_denied: role cannot share"))
		}
		stored, err := h.state.RecordInScope(ctx, h.scope.String(), request.PipeID)
		if err != nil {
			return fail(errors.New("not_found: pipe record"))
		}
		record, err := discovery.Parse(stored.Payload)
		if err != nil || record.Verify(h.public, h.scope, time.Now().UTC()) != nil {
			return fail(errors.New("authorization_denied: invalid pipe publication"))
		}
		publishGrant, err := authority.Decode(record.PublishGrant)
		if err != nil || publishGrant.Subject != request.ClientPublic {
			return fail(errors.New("authorization_denied: publish permission required"))
		}
		share, err := authority.NewToken("share.invite", h.scope, request.ClientPublic, request.PipeID, 48*time.Hour)
		if err != nil {
			return fail(err)
		}
		var route struct {
			AuthorityAddress string `json:"authority_address"`
		}
		if err := json.Unmarshal(request.Record, &route); err != nil || route.AuthorityAddress == "" || len(route.AuthorityAddress) > 8192 {
			return fail(errors.New("invalid_input: authority route required"))
		}
		share.OneTime = true
		share.AccessEpoch, err = h.state.RevocationEpoch(ctx, h.scope.String(), "pipe-access", request.PipeID)
		if err != nil {
			return fail(fmt.Errorf("persistence_failed: access epoch: %w", err))
		}
		share.PublishEpoch, err = h.state.RevocationEpoch(ctx, h.scope.String(), "pipe-publish", request.PipeID)
		if err != nil {
			return fail(fmt.Errorf("persistence_failed: publish epoch: %w", err))
		}
		share.AuthorityAddress = route.AuthorityAddress
		share.PipeEndpoint = &authority.PipeEndpoint{PeerID: record.RuntimePeerID, Addresses: append([]string(nil), record.Addresses...), Protocol: record.Protocol}
		share.DisplayNameHint = record.DisplayName
		if err := share.Sign(h.private); err != nil {
			return fail(err)
		}
		encoded, err := authority.Encode(share)
		if err != nil {
			return fail(err)
		}
		return wire.AuthorityResponse{OK: true, Invite: encoded}
	case "share_invite.redeem":
		if invite.Type != "share.invite" || !invite.OneTime || invite.Audience != request.PipeID || invite.PipeEndpoint == nil {
			return fail(errors.New("authorization_denied: invalid share invite"))
		}
		stored, err := h.state.RecordInScope(ctx, h.scope.String(), invite.Audience)
		if err != nil {
			return fail(errors.New("not_found: pipe record"))
		}
		record, err := discovery.Parse(stored.Payload)
		if err != nil || record.Verify(h.public, h.scope, time.Now().UTC()) != nil {
			return fail(errors.New("authorization_denied: invalid pipe publication"))
		}
		revoked, err := h.state.IsRevoked(ctx, h.scope.String(), "share.invite", invite.JTI)
		if err != nil {
			return fail(fmt.Errorf("persistence_failed: revocation: %w", err))
		}
		if revoked {
			return fail(errors.New("authorization_denied: invite revoked"))
		}
		sessionID, err := authority.SessionID()
		if err != nil {
			return fail(err)
		}
		_, err = h.state.RedeemInvite(ctx, invite.JTI, invite.Audience, sessionID, request.ClientPublic)
		if err != nil {
			if errors.Is(err, store.ErrInviteRedeemed) {
				return fail(errors.New("authorization_denied: invite already redeemed"))
			}
			return fail(fmt.Errorf("persistence_failed: redeem invite: %w", err))
		}
		lease, refresh, err := mintLeases(request.ClientPublic, request.PipeID)
		if err != nil {
			return fail(err)
		}
		return wire.AuthorityResponse{OK: true, Lease: lease, RefreshLease: refresh}
	case "revocation.issue":
		if err := requireMembership(); err != nil {
			return fail(err)
		}
		if invite.Role != "publisher" {
			return fail(errors.New("authorization_denied: role cannot revoke"))
		}
		var value struct {
			SubjectType string `json:"subject_type"`
			SubjectJTI  string `json:"subject_jti"`
			Epoch       uint64 `json:"epoch"`
		}
		if err := json.Unmarshal(request.Record, &value); err != nil {
			return fail(errors.New("invalid_input: invalid revocation"))
		}
		if err := h.state.Revoke(ctx, h.scope.String(), value.SubjectType, value.SubjectJTI, value.Epoch); err != nil {
			return fail(fmt.Errorf("persistence_failed: revoke: %w", err))
		}
		return wire.AuthorityResponse{OK: true}
	case "revocation.query":
		if !membership && invite.Type != "connect.refresh" && invite.Type != "share.invite" {
			return fail(errors.New("authorization_denied: invalid query token"))
		}
		values, err := h.state.Revocations(ctx, h.scope.String(), request.Subjects)
		if err != nil {
			return fail(fmt.Errorf("persistence_failed: revocation: %w", err))
		}
		out := make([]wire.Revocation, 0, len(values))
		for _, value := range values {
			out = append(out, wire.Revocation{SubjectType: value.SubjectType, SubjectJTI: value.SubjectJTI, Epoch: value.Epoch})
		}
		return wire.AuthorityResponse{OK: true, Revocations: out}
	default:
		return fail(errors.New("invalid_input: unknown authority operation"))
	}
}

// handleSnapshot serves only negotiated authority 2.1 records.list. Legacy
// authority 2.0 remains registered for its explicit compatibility surface, but
// cannot provide deletion-capable snapshot metadata.
func (h *authorityHandler) handleSnapshot(stream core.Stream) {
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	var request wire.AuthoritySnapshotRequest
	if err := wire.ReadFrame(stream, &request); err != nil {
		_ = stream.Reset()
		return
	}
	response := h.processSnapshot(context.Background(), request)
	if err := wire.WriteFrame(stream, response); err != nil {
		_ = stream.Reset()
	}
}

func (h *authorityHandler) processSnapshot(ctx context.Context, request wire.AuthoritySnapshotRequest) wire.AuthoritySnapshotResponse {
	fail := func(err error) wire.AuthoritySnapshotResponse {
		return wire.AuthoritySnapshotResponse{OK: false, Error: err.Error()}
	}
	if !boundedStrings(8192, request.ClientPublic, request.Nonce, request.Proof) || len(request.Token) > authority.MaxTokenSize {
		return fail(errors.New("invalid_input: authority field exceeds limit"))
	}
	invite, err := authority.Decode(request.Token)
	if err != nil {
		return fail(fmt.Errorf("authorization_denied: %w", err))
	}
	if err := invite.Verify(h.public, time.Now().UTC()); err != nil {
		return fail(fmt.Errorf("authorization_denied: %w", err))
	}
	if invite.Type != "membership.invite" || invite.Scope != h.scope {
		return fail(errors.New("authorization_denied: invalid membership invite"))
	}
	if invite.Role != "member" && invite.Role != "viewer" && invite.Role != "publisher" && invite.Role != "forwarder" {
		return fail(errors.New("authorization_denied: role cannot discover"))
	}
	clientPublic, err := authority.PublicFromString(request.ClientPublic)
	if err != nil {
		return fail(errors.New("authorization_denied: invalid client key"))
	}
	if !authority.VerifyProof(clientPublic, request.Proof, "tubo-authority-v2", "records.list", "", request.Nonce, invite.JTI) {
		return fail(errors.New("authorization_denied: invalid proof of possession"))
	}
	records, generation, highWater, complete, err := h.state.AuthoritySnapshot(ctx, h.scope.String(), store.MaxSnapshotRecords)
	if err != nil {
		return fail(err)
	}
	if !complete {
		return fail(errors.New("incomplete authoritative snapshot: response exceeds bounds"))
	}
	result := make([]json.RawMessage, 0, len(records))
	for _, stored := range records {
		record, err := discovery.Parse(stored.Payload)
		if err != nil || record.Verify(h.public, h.scope, time.Now().UTC()) != nil {
			return fail(errors.New("persistence_failed: authority stored invalid discovery record"))
		}
		if record.Visibility == domain.VisibilityDiscoverable {
			result = append(result, json.RawMessage(stored.Payload))
		}
	}
	response := wire.AuthoritySnapshotResponse{OK: true, Generation: generation, HighWater: highWater, Records: result}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > wire.MaxFrame {
		return fail(errors.New("incomplete authoritative snapshot: response exceeds bounds"))
	}
	return response
}

func waitSignal(ctx context.Context) error { return waitSignalRenew(ctx, 0, nil, nil) }
func waitSignalRenew(ctx context.Context, interval time.Duration, renew func() error, failed func(error)) error {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	var ticker *time.Ticker
	var ticks <-chan time.Time
	if interval > 0 {
		ticker = time.NewTicker(interval)
		ticks = ticker.C
		defer ticker.Stop()
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-signals:
			return nil
		case <-ticks:
			if err := renew(); err != nil && failed != nil {
				failed(err)
			}
		}
	}
}
func parseScope(value string) (*domain.Scope, error) {
	var scope domain.Scope
	for i := 0; i < len(value); i++ {
		if value[i] == '/' {
			scope.Cluster = value[:i]
			scope.Namespace = value[i+1:]
			if err := scope.Validate(); err != nil {
				return nil, err
			}
			return &scope, nil
		}
	}
	return nil, fmt.Errorf("scope must be cluster/namespace")
}
func boundedStrings(limit int, values ...string) bool {
	for _, value := range values {
		if len(value) > limit {
			return false
		}
	}
	return true
}

func randomNonce() string {
	data := make([]byte, 24)
	_, _ = rand.Read(data)
	return base64.RawURLEncoding.EncodeToString(data)
}
