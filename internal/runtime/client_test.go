package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	core "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/multiformats/go-multiaddr"

	"github.com/origama/tubo/internal/authority"
	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/domain"
	wire "github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/store"
)

func signedSnapshotRecord(t *testing.T, authorityPrivate ed25519.PrivateKey, scope domain.Scope, sequence uint64) discovery.PipeRecord {
	t.Helper()
	ownerPublic, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pipeID := authority.PipeID(ownerPublic)
	grant, err := authority.NewToken("pipe.publish", scope, pipeID, pipeID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	encodedGrant, err := authority.Encode(grant)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := discovery.PipeRecord{Version: 2, PipeID: pipeID, DisplayName: "echo", RuntimePeerID: "peer", Scope: scope, Protocol: wire.TunnelID, Capabilities: []string{"raw-duplex"}, Addresses: []string{"/ip4/127.0.0.1/tcp/1/p2p/peer"}, Visibility: domain.VisibilityDiscoverable, Access: domain.AccessMembers, PublishGrant: encodedGrant, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Sequence: sequence}
	if err := record.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestReconcileAuthorityResponseVerifiesWholeSnapshotBeforeCommit(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	record := signedSnapshotRecord(t, authorityPrivate, scope, 1)
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	response := wire.AuthoritySnapshotResponse{OK: true, Generation: 1, HighWater: 1, Records: []json.RawMessage{raw}}
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, response, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	stored, err := state.RecordInScope(ctx, scope.String(), record.PipeID)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []wire.AuthoritySnapshotResponse{
		{OK: true, Generation: 2, HighWater: 2, Records: make([]json.RawMessage, store.MaxSnapshotRecords+1)},
		{OK: true, Generation: 2, HighWater: 2, Records: []json.RawMessage{json.RawMessage(`{"broken":`)}},
	} {
		if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, invalid, time.Now().UTC()); err == nil {
			t.Fatal("invalid snapshot accepted")
		}
		after, err := state.RecordInScope(ctx, scope.String(), record.PipeID)
		if err != nil || string(after.Payload) != string(stored.Payload) {
			t.Fatalf("invalid snapshot changed cache: %#v %v", after, err)
		}
	}
	tampered := record
	tampered.DisplayName = "tampered"
	tamperedRaw, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, wire.AuthoritySnapshotResponse{OK: true, Generation: 2, HighWater: 2, Records: []json.RawMessage{tamperedRaw}}, time.Now().UTC()); err == nil {
		t.Fatal("bad-signature snapshot accepted")
	}
	otherScope := domain.Scope{Cluster: "other", Namespace: "default"}
	mixed := signedSnapshotRecord(t, authorityPrivate, otherScope, 2)
	mixedRaw, err := json.Marshal(mixed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, wire.AuthoritySnapshotResponse{OK: true, Generation: 2, HighWater: 2, Records: []json.RawMessage{mixedRaw}}, time.Now().UTC()); err == nil {
		t.Fatal("mixed-scope snapshot accepted")
	}
	if _, err := state.RecordInScope(ctx, scope.String(), record.PipeID); err != nil {
		t.Fatalf("invalid snapshot removed valid record: %v", err)
	}
}

func TestDelayedAuthoritySnapshotsRejectRollbackAndAmbiguousGeneration(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	q1 := signedSnapshotRecord(t, authorityPrivate, scope, 1)
	q2 := signedSnapshotRecord(t, authorityPrivate, scope, 2)
	q1raw, err := q1.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	q2raw, err := q2.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	// Q2 returns first; delayed Q1 must not delete Q2's cache state.
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, wire.AuthoritySnapshotResponse{OK: true, Generation: 2, HighWater: 2, Records: []json.RawMessage{q2raw}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, wire.AuthoritySnapshotResponse{OK: true, Generation: 1, HighWater: 1, Records: []json.RawMessage{q1raw}}, time.Now().UTC()); err == nil {
		t.Fatal("delayed rollback snapshot accepted")
	}
	if _, err := state.RecordInScope(ctx, scope.String(), q2.PipeID); err != nil {
		t.Fatalf("delayed Q1 removed newer Q2 record: %v", err)
	}
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, wire.AuthoritySnapshotResponse{OK: true, Generation: 2, HighWater: 2, Records: []json.RawMessage{q1raw}}, time.Now().UTC()); err == nil {
		t.Fatal("same generation changed snapshot accepted")
	}
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, wire.AuthoritySnapshotResponse{OK: true, Generation: 2, HighWater: 3, Records: []json.RawMessage{q2raw}}, time.Now().UTC()); err == nil {
		t.Fatal("same generation changed high-water accepted")
	}
	if _, err := state.RecordInScope(ctx, scope.String(), q2.PipeID); err != nil {
		t.Fatalf("ambiguous snapshot changed valid cache: %v", err)
	}
}

func directAuthorityAddress(t *testing.T, id peer.ID, addresses []multiaddr.Multiaddr) string {
	t.Helper()
	values, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{ID: id, Addrs: addresses})
	if err != nil || len(values) == 0 {
		t.Fatalf("authority address: %#v %v", values, err)
	}
	return values[0].String()
}

func TestAuthoritySnapshotProtocolRoundTripAndNoLegacyFallback(t *testing.T) {
	ctx := context.Background()
	authorityHome, consumerHome := t.TempDir(), t.TempDir()
	t.Setenv("TUBO_HOME", authorityHome)
	authorityState, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer authorityState.Close()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	if err := authorityState.EnsureAuthoritySnapshot(ctx, scope.String()); err != nil {
		t.Fatal(err)
	}
	record := signedSnapshotRecord(t, authorityPrivate, scope, 1)
	raw, err := record.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := authorityState.PutAuthoritativeRecord(ctx, store.Record{Scope: scope.String(), PipeID: record.PipeID, Name: record.DisplayName, Payload: raw, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence}); err != nil {
		t.Fatal(err)
	}
	authorityHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer authorityHost.Close()
	handler := &authorityHandler{state: authorityState, scope: scope, private: authorityPrivate, public: authorityPublic}
	authorityHost.SetStreamHandler(protocol.ID(wire.AuthoritySnapshotID), handler.handleSnapshot)

	t.Setenv("TUBO_HOME", consumerHome)
	consumerState, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer consumerState.Close()
	clientKey, err := authority.GeneratePrivate(consumerState.SecretPath("clients", "snapshot.key"))
	if err != nil {
		t.Fatal(err)
	}
	invite, err := authority.NewToken("membership.invite", scope, authority.PublicString(clientKey.Public().(ed25519.PublicKey)), "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	invite.Role = "member"
	if err := invite.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	token, err := authority.Encode(invite)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumerState.PutMembership(ctx, store.Membership{Scope: scope.String(), Role: "member", Token: token, AuthorityPublic: authority.PublicString(authorityPublic), AuthorityAddress: directAuthorityAddress(t, authorityHost.ID(), authorityHost.Addrs()), ClientKeyFile: consumerState.SecretPath("clients", "snapshot.key")}); err != nil {
		t.Fatal(err)
	}
	clientHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	response, err := authoritySnapshotRequest(ctx, consumerState, clientHost, scope.String())
	if err != nil || !response.OK || response.Generation == 0 || len(response.Records) != 1 {
		t.Fatalf("authority 2.1 request failed: %#v %v", response, err)
	}
	if _, err := reconcileAuthorityResponse(ctx, consumerState, scope.String(), authorityPublic, scope, response, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := consumerState.RecordInScope(ctx, scope.String(), record.PipeID); err != nil {
		t.Fatalf("authority 2.1 response did not commit cache: %v", err)
	}
	for range 100 {
		extra := signedSnapshotRecord(t, authorityPrivate, scope, 1)
		extraRaw, err := extra.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := authorityState.PutAuthoritativeRecord(ctx, store.Record{Scope: scope.String(), PipeID: extra.PipeID, Name: extra.DisplayName, Payload: extraRaw, ExpiresAt: extra.ExpiresAt, Sequence: extra.Sequence}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := authoritySnapshotRequest(ctx, consumerState, clientHost, scope.String()); err == nil || !strings.Contains(err.Error(), "incomplete authoritative snapshot") {
		t.Fatalf("oversized authority 2.1 snapshot accepted: %v", err)
	}
	if _, err := consumerState.RecordInScope(ctx, scope.String(), record.PipeID); err != nil {
		t.Fatalf("oversized authority 2.1 response changed cache: %v", err)
	}

	legacyHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer legacyHost.Close()
	legacyCalled := make(chan struct{}, 1)
	legacyHost.SetStreamHandler(protocol.ID(wire.AuthorityID), func(stream core.Stream) {
		legacyCalled <- struct{}{}
		_ = stream.Close()
	})
	if err := consumerState.PutMembership(ctx, store.Membership{Scope: scope.String(), Role: "member", Token: token, AuthorityPublic: authority.PublicString(authorityPublic), AuthorityAddress: directAuthorityAddress(t, legacyHost.ID(), legacyHost.Addrs()), ClientKeyFile: consumerState.SecretPath("clients", "snapshot.key")}); err != nil {
		t.Fatal(err)
	}
	if _, err := authoritySnapshotRequestOnce(ctx, consumerState, clientHost, scope.String()); err == nil || !strings.Contains(err.Error(), "authority 2.1 unavailable") {
		t.Fatalf("unavailable authority 2.1 did not fail closed: %v", err)
	}
	select {
	case <-legacyCalled:
		t.Fatal("authority 2.1 client fell back to authority 2.0")
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := consumerState.RecordInScope(ctx, scope.String(), record.PipeID); err != nil {
		t.Fatalf("unavailable authority 2.1 changed cache: %v", err)
	}
}

func TestFetchRecordsHonorsPersistedRetryDeadline(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	if _, err := FetchRecords(ctx, state, "solar/default"); err == nil {
		t.Fatal("first failed refresh accepted")
	}
	freshness, err := state.ScopeFreshness(ctx, "solar/default")
	if err != nil || freshness.NextRetry == nil {
		t.Fatalf("failed refresh did not persist next retry: %#v %v", freshness, err)
	}
	remaining := time.Until(*freshness.NextRetry)
	if remaining <= 0 || remaining > snapshotRetryDelay+time.Second {
		t.Fatalf("next retry is not bounded: %s", remaining)
	}
	if _, err := FetchRecords(ctx, state, "solar/default"); err == nil || !strings.Contains(err.Error(), "discovery_retry_after") {
		t.Fatalf("retry deadline did not prevent fresh dial: %v", err)
	}
}

func TestLegacyAuthorityResponseMetadataCannotDeleteCache(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	record := signedSnapshotRecord(t, authorityPrivate, scope, 1)
	raw, err := record.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, wire.AuthoritySnapshotResponse{OK: true, Generation: 1, HighWater: 1, Records: []json.RawMessage{raw}}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	// Authority 2.0 has no generation/high-water fields. Its equivalent empty
	// metadata must fail closed rather than reconciling an empty response.
	legacyEquivalent := wire.AuthoritySnapshotResponse{OK: true}
	if _, err := reconcileAuthorityResponse(ctx, state, scope.String(), authorityPublic, scope, legacyEquivalent, time.Now().UTC()); err == nil {
		t.Fatal("metadata-free legacy response authorized deletion")
	}
	if _, err := state.RecordInScope(ctx, scope.String(), record.PipeID); err != nil {
		t.Fatalf("legacy response changed cache: %v", err)
	}
}
