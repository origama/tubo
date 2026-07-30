package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	coremetrics "github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo-rebuilt/internal/authority"
	"github.com/origama/tubo-rebuilt/internal/discovery"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/node"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
)

func OpenOverlayHost(ctx context.Context, state *store.Store, keyName string) (host.Host, string, error) {
	return openOverlayHost(ctx, state, keyName, nil, nil, "")
}

func OpenOverlayHostWithReporter(ctx context.Context, state *store.Store, keyName string, reporter coremetrics.Reporter) (host.Host, string, error) {
	return openOverlayHost(ctx, state, keyName, reporter, nil, "")
}

func openOverlayHost(ctx context.Context, state *store.Store, keyName string, reporter coremetrics.Reporter, observer NetworkOperationObserver, scope string) (host.Host, string, error) {
	relay, key, reachability, err := state.Overlay(ctx)
	if err != nil {
		return nil, "", err
	}
	if observer != nil {
		observer(scope)
	}
	h, err := node.Open(ctx, node.Options{KeyFile: state.SecretPath("nodes", keyName+".key"), SwarmKeyFile: key, Private: reachability == "private", BandwidthReporter: reporter})
	if err != nil {
		return nil, "", err
	}
	if _, err := node.ConnectRelay(ctx, h, relay, false); err != nil {
		h.Close()
		return nil, "", err
	}
	return h, relay, nil
}

func authorityRequest(ctx context.Context, state *store.Store, h host.Host, scope, operation, pipeID string) (wire.AuthorityResponse, error) {
	return authorityRequestPayload(ctx, state, h, scope, operation, pipeID, nil)
}

func authorityRequestPayload(ctx context.Context, state *store.Store, h host.Host, scope, operation, pipeID string, record json.RawMessage) (wire.AuthorityResponse, error) {
	var response wire.AuthorityResponse
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		response, err = authorityRequestOnce(ctx, state, h, scope, operation, pipeID, record)
		if err == nil {
			return response, nil
		}
		if strings.Contains(err.Error(), "authorization_denied") || strings.Contains(err.Error(), "invalid_input") || strings.Contains(err.Error(), "not_found") || strings.Contains(err.Error(), "incomplete authoritative snapshot") {
			return response, err
		}
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return response, err
}

func authorityRequestOnce(ctx context.Context, state *store.Store, h host.Host, scope, operation, pipeID string, record json.RawMessage) (wire.AuthorityResponse, error) {
	membership, err := state.Membership(ctx, scope)
	if err != nil {
		return wire.AuthorityResponse{}, err
	}
	private, err := authority.ReadPrivate(membership.ClientKeyFile)
	if err != nil {
		return wire.AuthorityResponse{}, err
	}
	peerID, err := node.DialPeer(ctx, h, membership.AuthorityAddress)
	if err != nil {
		return wire.AuthorityResponse{}, fmt.Errorf("authority_unreachable: %w", err)
	}
	stream, err := h.NewStream(network.WithAllowLimitedConn(ctx, "tubo authority via relay"), peerID, protocol.ID(wire.AuthorityID))
	if err != nil {
		return wire.AuthorityResponse{}, fmt.Errorf("authority_unreachable: %w", err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	nonce := randomNonce()
	public := authority.PublicString(private.Public().(ed25519.PublicKey))
	request := wire.AuthorityRequest{Operation: operation, Token: membership.Token, PipeID: pipeID, ClientPublic: public, Nonce: nonce, Record: record}
	invite, err := authority.Decode(membership.Token)
	if err != nil {
		return wire.AuthorityResponse{}, err
	}
	request.Proof = authority.SignProof(private, "tubo-authority-v2", operation, pipeID, nonce, invite.JTI)
	if err := wire.WriteFrame(stream, request); err != nil {
		return wire.AuthorityResponse{}, err
	}
	var response wire.AuthorityResponse
	if err := wire.ReadFrame(stream, &response); err != nil {
		return response, err
	}
	if !response.OK {
		return response, fmt.Errorf("%s", response.Error)
	}
	return response, nil
}

func authoritySnapshotRequest(ctx context.Context, state *store.Store, h host.Host, scope string) (wire.AuthoritySnapshotResponse, error) {
	var response wire.AuthoritySnapshotResponse
	var err error
	for attempt := 0; attempt < 10; attempt++ {
		response, err = authoritySnapshotRequestOnce(ctx, state, h, scope)
		if err == nil || strings.Contains(err.Error(), "authorization_denied") || strings.Contains(err.Error(), "invalid_input") || strings.Contains(err.Error(), "incomplete authoritative snapshot") {
			return response, err
		}
		select {
		case <-ctx.Done():
			return response, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return response, err
}

func authoritySnapshotRequestOnce(ctx context.Context, state *store.Store, h host.Host, scope string) (wire.AuthoritySnapshotResponse, error) {
	membership, err := state.Membership(ctx, scope)
	if err != nil {
		return wire.AuthoritySnapshotResponse{}, err
	}
	private, err := authority.ReadPrivate(membership.ClientKeyFile)
	if err != nil {
		return wire.AuthoritySnapshotResponse{}, err
	}
	peerID, err := node.DialPeer(ctx, h, membership.AuthorityAddress)
	if err != nil {
		return wire.AuthoritySnapshotResponse{}, fmt.Errorf("authority_unreachable: %w", err)
	}
	stream, err := h.NewStream(network.WithAllowLimitedConn(ctx, "tubo authority snapshot via relay"), peerID, protocol.ID(wire.AuthoritySnapshotID))
	if err != nil {
		return wire.AuthoritySnapshotResponse{}, fmt.Errorf("authority_unreachable: authority 2.1 unavailable: %w", err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	nonce := randomNonce()
	public := authority.PublicString(private.Public().(ed25519.PublicKey))
	invite, err := authority.Decode(membership.Token)
	if err != nil {
		return wire.AuthoritySnapshotResponse{}, err
	}
	request := wire.AuthoritySnapshotRequest{Token: membership.Token, ClientPublic: public, Nonce: nonce}
	request.Proof = authority.SignProof(private, "tubo-authority-v2", "records.list", "", nonce, invite.JTI)
	if err := wire.WriteFrame(stream, request); err != nil {
		return wire.AuthoritySnapshotResponse{}, err
	}
	var response wire.AuthoritySnapshotResponse
	if err := wire.ReadFrame(stream, &response); err != nil {
		return response, err
	}
	if !response.OK {
		return response, fmt.Errorf("%s", response.Error)
	}
	return response, nil
}

func discoveryErrorClass(err error) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "authority_unreachable"):
		return "authority_unreachable"
	case strings.Contains(message, "authorization_denied"):
		return "authorization_denied"
	case strings.Contains(message, "incomplete authoritative snapshot"):
		return "partial_snapshot"
	case strings.Contains(message, "invalid discovered record"), strings.Contains(message, "invalid authoritative snapshot"):
		return "invalid_snapshot"
	default:
		return "discovery_unreachable"
	}
}

func reconcileAuthorityResponse(ctx context.Context, state *store.Store, scope string, public ed25519.PublicKey, expected domain.Scope, response wire.AuthoritySnapshotResponse, now time.Time) ([]discovery.PipeRecord, error) {
	return reconcileAuthorityResponseWithLease(ctx, state, scope, public, expected, response, now, nil)
}

func reconcileAuthorityResponseWithLease(ctx context.Context, state *store.Store, scope string, public ed25519.PublicKey, expected domain.Scope, response wire.AuthoritySnapshotResponse, now time.Time, lease *store.ScopeSyncLease) ([]discovery.PipeRecord, error) {
	if response.Generation == 0 || response.HighWater == 0 || len(response.Records) > store.MaxSnapshotRecords {
		return nil, errors.New("invalid authoritative snapshot metadata")
	}
	result := make([]discovery.PipeRecord, 0, len(response.Records))
	stored := make([]store.Record, 0, len(response.Records))
	seen := make(map[string]struct{}, len(response.Records))
	for _, raw := range response.Records {
		record, err := discovery.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid discovered record: %w", err)
		}
		if err := record.Verify(public, expected, now); err != nil {
			return nil, fmt.Errorf("invalid discovered record: %w", err)
		}
		if _, duplicate := seen[record.PipeID]; duplicate {
			return nil, errors.New("invalid authoritative snapshot duplicate pipe_id")
		}
		seen[record.PipeID] = struct{}{}
		data, err := record.Marshal()
		if err != nil {
			return nil, fmt.Errorf("invalid discovered record: %w", err)
		}
		stored = append(stored, store.Record{Scope: scope, PipeID: record.PipeID, Name: record.DisplayName, Payload: data, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence, Discovered: true})
		result = append(result, record)
	}
	keys := make([]string, 0, len(stored))
	for _, record := range stored {
		keys = append(keys, record.PipeID+"\x00"+string(record.Payload))
	}
	sort.Strings(keys)
	digestInput := sha256.New()
	for _, key := range keys {
		_, _ = digestInput.Write([]byte(key))
		_, _ = digestInput.Write([]byte{0})
	}
	digest := hex.EncodeToString(digestInput.Sum(nil))
	snapshot := store.AuthoritativeSnapshot{Source: store.SnapshotSourcePinnedAuthority, Complete: true, Generation: response.Generation, HighWater: response.HighWater, Digest: digest, Records: stored}
	var reconcileErr error
	if lease != nil {
		reconcileErr = state.ReconcileAuthoritativeSnapshotWithLease(ctx, *lease, snapshot)
	} else {
		reconcileErr = state.ReconcileAuthoritativeSnapshot(ctx, scope, snapshot)
	}
	if reconcileErr != nil {
		return nil, reconcileErr
	}
	return result, nil
}

const snapshotRetryDelay = 30 * time.Second

// NetworkOperationObserver observes an attempted overlay-host operation at its
// runtime boundary. It is request-scoped and never installed globally.
type NetworkOperationObserver func(scope string)

// FetchRecordsOptions controls caller-visible refresh behavior. Force skips
// only persisted retry cooldown; authority request and verification bounds remain.
type FetchRecordsOptions struct {
	Force                    bool
	NetworkOperationObserver NetworkOperationObserver
	// ScopeSyncLease fences periodic owner final writes. Foreground callers
	// intentionally leave it nil until their ownership contract changes.
	ScopeSyncLease *store.ScopeSyncLease
}

func FetchRecords(ctx context.Context, state *store.Store, scope string) ([]discovery.PipeRecord, error) {
	return FetchRecordsWithOptions(ctx, state, scope, FetchRecordsOptions{})
}

func FetchRecordsWithOptions(ctx context.Context, state *store.Store, scope string, options FetchRecordsOptions) (result []discovery.PipeRecord, err error) {
	now := time.Now().UTC()
	freshness, freshnessErr := state.ScopeFreshness(ctx, scope)
	if !options.Force && freshnessErr == nil && freshness.NextRetry != nil && now.Before(*freshness.NextRetry) {
		return nil, errors.New("discovery_retry_after")
	}
	if freshnessErr != nil && !errors.Is(freshnessErr, domain.ErrNotFound) {
		return nil, fmt.Errorf("persistence_failed: discovery freshness: %w", freshnessErr)
	}
	fail := func(cause error) ([]discovery.PipeRecord, error) {
		nextRetry := time.Now().UTC().Add(snapshotRetryDelay)
		var markErr error
		if options.ScopeSyncLease != nil {
			markErr = state.RecordSnapshotFailureWithLease(ctx, *options.ScopeSyncLease, store.SnapshotSourcePinnedAuthority, discoveryErrorClass(cause), &nextRetry)
		} else {
			markErr = state.RecordSnapshotFailure(ctx, scope, store.SnapshotSourcePinnedAuthority, discoveryErrorClass(cause), &nextRetry)
		}
		if markErr != nil {
			return nil, fmt.Errorf("persistence_failed: record refresh failure: %w", markErr)
		}
		return nil, cause
	}
	membership, err := state.Membership(ctx, scope)
	if err != nil {
		return fail(err)
	}
	public, err := authority.PublicFromString(membership.AuthorityPublic)
	if err != nil {
		return fail(err)
	}
	parsedScope, err := parseScope(scope)
	if err != nil {
		return fail(err)
	}
	h, _, err := openOverlayHost(ctx, state, fmt.Sprintf("query-%d", os.Getpid()), nil, options.NetworkOperationObserver, scope)
	if err != nil {
		return fail(err)
	}
	defer h.Close()
	response, err := authoritySnapshotRequest(ctx, state, h, scope)
	if err != nil {
		return fail(err)
	}
	result, err = reconcileAuthorityResponseWithLease(ctx, state, scope, public, *parsedScope, response, time.Now().UTC(), options.ScopeSyncLease)
	if err != nil {
		return fail(err)
	}
	return result, nil
}

func ParseVerifiedRecord(ctx context.Context, state *store.Store, stored store.Record) (discovery.PipeRecord, error) {
	record, err := discovery.Parse(stored.Payload)
	if err != nil {
		return record, err
	}
	if record.PipeID != stored.PipeID || record.Scope.String() != stored.Scope || record.Sequence != stored.Sequence {
		return record, fmt.Errorf("cached record index binding mismatch")
	}
	membership, err := state.Membership(ctx, record.Scope.String())
	if err != nil {
		return record, err
	}
	public, err := authority.PublicFromString(membership.AuthorityPublic)
	if err != nil {
		return record, err
	}
	if err := record.Verify(public, record.Scope, time.Now().UTC()); err != nil {
		return record, fmt.Errorf("cached record verification: %w", err)
	}
	return record, nil
}

func RequestLease(ctx context.Context, state *store.Store, h host.Host, scope, pipeID string) (string, error) {
	lease, _, err := RequestLeasePair(ctx, state, h, scope, pipeID)
	return lease, err
}

func RequestLeasePair(ctx context.Context, state *store.Store, h host.Host, scope, pipeID string) (string, string, error) {
	response, err := authorityRequest(ctx, state, h, scope, "connect_access.issue", pipeID)
	if err != nil {
		return "", "", err
	}
	if response.Lease == "" || response.RefreshLease == "" {
		return "", "", errors.New("authority returned empty lease")
	}
	return response.Lease, response.RefreshLease, nil
}

// RequestRefreshLease exchanges an unexpired access/refresh pair online. New
// leases are for later session setup; it does not modify an open tunnel.
func RequestRefreshLease(ctx context.Context, state *store.Store, h host.Host, scope, pipeID, accessLease, refreshLease string) (string, string, error) {
	response, err := authorityRequestWithToken(ctx, state, h, scope, "connect_refresh.exchange", pipeID, refreshLease, accessLease)
	if err != nil {
		return "", "", err
	}
	if response.Lease == "" || response.RefreshLease == "" {
		return "", "", errors.New("authority returned empty refreshed lease")
	}
	return response.Lease, response.RefreshLease, nil
}

func RequestRevoke(ctx context.Context, state *store.Store, h host.Host, scope, subjectType, subjectJTI string, epoch uint64) error {
	payload, err := json.Marshal(map[string]any{"subject_type": subjectType, "subject_jti": subjectJTI, "epoch": epoch})
	if err != nil {
		return err
	}
	_, err = authorityRequestPayload(ctx, state, h, scope, "revocation.issue", "", payload)
	return err
}

func RequestShareInvite(ctx context.Context, state *store.Store, h host.Host, scope, pipeID string) (string, error) {
	membership, err := state.Membership(ctx, scope)
	if err != nil {
		return "", err
	}
	route, err := json.Marshal(map[string]string{"authority_address": membership.AuthorityAddress})
	if err != nil {
		return "", err
	}
	response, err := authorityRequestPayload(ctx, state, h, scope, "share_invite.issue", pipeID, route)
	if err != nil {
		return "", err
	}
	if response.Invite == "" {
		return "", errors.New("authority returned empty share invite")
	}
	return response.Invite, nil
}

// RedeemShareInvite requires a locally trusted authority route for invite
// foundation callers. Token itself carries only pipe endpoint, never service.
func RedeemShareInvite(ctx context.Context, state *store.Store, h host.Host, scope, inviteToken string) (string, string, error) {
	invite, err := authority.Decode(inviteToken)
	if err != nil {
		return "", "", err
	}
	response, err := authorityRequestWithToken(ctx, state, h, scope, "share_invite.redeem", invite.Audience, inviteToken, "")
	if err != nil {
		return "", "", err
	}
	if response.Lease == "" || response.RefreshLease == "" {
		return "", "", errors.New("authority returned empty redeemed lease")
	}
	return response.Lease, response.RefreshLease, nil
}

func authorityRequestWithToken(ctx context.Context, state *store.Store, h host.Host, scope, operation, pipeID, token, accessLease string) (wire.AuthorityResponse, error) {
	membership, err := state.Membership(ctx, scope)
	if err != nil {
		return wire.AuthorityResponse{}, err
	}
	private, err := authority.ReadPrivate(membership.ClientKeyFile)
	if err != nil {
		return wire.AuthorityResponse{}, err
	}
	peerID, err := node.DialPeer(ctx, h, membership.AuthorityAddress)
	if err != nil {
		return wire.AuthorityResponse{}, fmt.Errorf("authority_unreachable: %w", err)
	}
	stream, err := h.NewStream(network.WithAllowLimitedConn(ctx, "tubo authority via relay"), peerID, protocol.ID(wire.AuthorityID))
	if err != nil {
		return wire.AuthorityResponse{}, fmt.Errorf("authority_unreachable: %w", err)
	}
	defer stream.Close()
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	decoded, err := authority.Decode(token)
	if err != nil {
		return wire.AuthorityResponse{}, err
	}
	nonce := randomNonce()
	public := authority.PublicString(private.Public().(ed25519.PublicKey))
	request := wire.AuthorityRequest{Operation: operation, Token: token, PipeID: pipeID, ClientPublic: public, Nonce: nonce, AccessLease: accessLease}
	request.Proof = authority.SignProof(private, "tubo-authority-v2", operation, pipeID, nonce, decoded.JTI)
	if err := wire.WriteFrame(stream, request); err != nil {
		return wire.AuthorityResponse{}, err
	}
	var response wire.AuthorityResponse
	if err := wire.ReadFrame(stream, &response); err != nil {
		return response, err
	}
	if !response.OK {
		return response, errors.New(response.Error)
	}
	return response, nil
}

func RequestPublishGrant(ctx context.Context, state *store.Store, h host.Host, scope, pipeID string) (string, error) {
	response, err := authorityRequest(ctx, state, h, scope, "pipe_publish.request", pipeID)
	if err != nil {
		return "", err
	}
	if response.Grant == "" {
		return "", fmt.Errorf("authority returned empty publish grant")
	}
	return response.Grant, nil
}

func AnnounceRecord(ctx context.Context, state *store.Store, h host.Host, record discovery.PipeRecord) error {
	data, err := record.Marshal()
	if err != nil {
		return err
	}
	_, err = authorityRequestPayload(ctx, state, h, record.Scope.String(), "records.announce", record.PipeID, json.RawMessage(data))
	return err
}

func AuthorityPublicForScope(ctx context.Context, state *store.Store, scope string) (ed25519.PublicKey, error) {
	if local, err := state.Scope(ctx, scope); err == nil {
		return authority.PublicFromString(local.AuthorityPublic)
	}
	membership, err := state.Membership(ctx, scope)
	if err != nil {
		return nil, err
	}
	return authority.PublicFromString(membership.AuthorityPublic)
}
