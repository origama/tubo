package runtime

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	coremetrics "github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo-rebuilt/internal/authority"
	"github.com/origama/tubo-rebuilt/internal/discovery"
	"github.com/origama/tubo-rebuilt/internal/node"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
)

func OpenOverlayHost(ctx context.Context, state *store.Store, keyName string) (host.Host, string, error) {
	return OpenOverlayHostWithReporter(ctx, state, keyName, nil)
}

func OpenOverlayHostWithReporter(ctx context.Context, state *store.Store, keyName string, reporter coremetrics.Reporter) (host.Host, string, error) {
	relay, key, reachability, err := state.Overlay(ctx)
	if err != nil {
		return nil, "", err
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
		if strings.Contains(err.Error(), "authorization_denied") || strings.Contains(err.Error(), "invalid_input") || strings.Contains(err.Error(), "not_found") {
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

func FetchRecords(ctx context.Context, state *store.Store, scope string) ([]discovery.PipeRecord, error) {
	h, _, err := OpenOverlayHost(ctx, state, fmt.Sprintf("query-%d", os.Getpid()))
	if err != nil {
		return nil, err
	}
	defer h.Close()
	response, err := authorityRequest(ctx, state, h, scope, "records.list", "")
	if err != nil {
		return nil, err
	}
	membership, err := state.Membership(ctx, scope)
	if err != nil {
		return nil, err
	}
	public, err := authority.PublicFromString(membership.AuthorityPublic)
	if err != nil {
		return nil, err
	}
	parsedScope, err := parseScope(scope)
	if err != nil {
		return nil, err
	}
	result := make([]discovery.PipeRecord, 0, len(response.Records))
	for _, raw := range response.Records {
		record, err := discovery.Parse(raw)
		if err != nil {
			return nil, err
		}
		if err := record.Verify(public, *parsedScope, time.Now().UTC()); err != nil {
			return nil, fmt.Errorf("invalid discovered record: %w", err)
		}
		data, _ := json.Marshal(record)
		if err := state.PutRecord(ctx, store.Record{Scope: scope, PipeID: record.PipeID, Name: record.DisplayName, Payload: data, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence, Discovered: true}); err != nil {
			return nil, err
		}
		result = append(result, record)
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
	response, err := authorityRequest(ctx, state, h, scope, "connect_access.issue", pipeID)
	if err != nil {
		return "", err
	}
	return response.Lease, nil
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
