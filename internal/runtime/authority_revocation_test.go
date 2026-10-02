package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/authority"
	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/domain"
	wire "github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/store"
)

func authorityTestRequest(private ed25519.PrivateKey, token, operation, pipeID string) wire.AuthorityRequest {
	decoded, _ := authority.Decode(token)
	public := authority.PublicString(private.Public().(ed25519.PublicKey))
	nonce := "test-nonce"
	return wire.AuthorityRequest{Operation: operation, Token: token, PipeID: pipeID, ClientPublic: public, Nonce: nonce, Proof: authority.SignProof(private, "tubo-authority-v2", operation, pipeID, nonce, decoded.JTI)}
}

func signedAuthorityRecord(t *testing.T, authorityPrivate ed25519.PrivateKey, scope domain.Scope, subject string) discovery.PipeRecord {
	t.Helper()
	ownerPublic, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := authority.NewToken("pipe.publish", scope, subject, authority.PipeID(ownerPublic), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	encoded, _ := authority.Encode(grant)
	now := time.Now().UTC()
	record := discovery.PipeRecord{Version: 2, PipeID: authority.PipeID(ownerPublic), DisplayName: "echo", RuntimePeerID: "peer", Scope: scope, Protocol: wire.TunnelID, Capabilities: []string{"raw-duplex"}, Addresses: []string{"/ip4/127.0.0.1/tcp/1/p2p/peer"}, Visibility: domain.VisibilityDiscoverable, Access: domain.AccessMembers, PublishGrant: encoded, IssuedAt: now, ExpiresAt: now.Add(30 * time.Minute), Sequence: 1}
	if err := record.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestAuthorityRefreshRevocationQueryAndShareRedemption(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	record := signedAuthorityRecord(t, authorityPrivate, scope, authority.PublicString(clientPublic))
	raw, err := record.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.PutAuthoritativeRecord(ctx, store.Record{Scope: scope.String(), PipeID: record.PipeID, Name: record.DisplayName, Payload: raw, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence}); err != nil {
		t.Fatal(err)
	}
	membership, err := authority.NewToken("membership.invite", scope, authority.PublicString(clientPublic), "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	membership.Role = "publisher"
	if err := membership.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	membershipToken, _ := authority.Encode(membership)
	h := &authorityHandler{state: state, scope: scope, private: authorityPrivate, public: authorityPublic}

	accessIssue := h.process(ctx, authorityTestRequest(clientPrivate, membershipToken, "connect_access.issue", record.PipeID))
	if !accessIssue.OK || accessIssue.Lease == "" || accessIssue.RefreshLease == "" {
		t.Fatalf("access issue: %#v", accessIssue)
	}
	refreshReq := authorityTestRequest(clientPrivate, accessIssue.RefreshLease, "connect_refresh.exchange", record.PipeID)
	refreshReq.AccessLease = accessIssue.Lease
	refreshed := h.process(ctx, refreshReq)
	if !refreshed.OK || refreshed.Lease == "" || refreshed.RefreshLease == "" {
		t.Fatalf("refresh: %#v", refreshed)
	}
	expiredRefresh, _ := authority.Decode(accessIssue.RefreshLease)
	expiredRefresh.ExpiresAt = time.Now().UTC().Add(-time.Second)
	if err := expiredRefresh.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	expiredEncoded, _ := authority.Encode(expiredRefresh)
	expiredReq := authorityTestRequest(clientPrivate, expiredEncoded, "connect_refresh.exchange", record.PipeID)
	expiredReq.AccessLease = accessIssue.Lease
	if got := h.process(ctx, expiredReq); got.OK {
		t.Fatal("expired refresh accepted")
	}
	refreshToken, _ := authority.Decode(accessIssue.RefreshLease)
	if err := state.Revoke(ctx, scope.String(), "connect.refresh", refreshToken.JTI, 1); err != nil {
		t.Fatal(err)
	}
	if got := h.process(ctx, refreshReq); got.OK {
		t.Fatal("revoked refresh accepted")
	}
	query := authorityTestRequest(clientPrivate, membershipToken, "revocation.query", "")
	query.Subjects = []string{refreshToken.JTI}
	if got := h.process(ctx, query); !got.OK || len(got.Revocations) != 1 {
		t.Fatalf("query: %#v", got)
	}

	shareReq := authorityTestRequest(clientPrivate, membershipToken, "share_invite.issue", record.PipeID)
	shareReq.Record, _ = json.Marshal(map[string]string{"authority_address": "/ip4/127.0.0.1/tcp/1/p2p/authority"})
	share := h.process(ctx, shareReq)
	if !share.OK || share.Invite == "" {
		t.Fatalf("share issue: %#v", share)
	}
	invite, _ := authority.Decode(share.Invite)
	if invite.PipeEndpoint == nil || invite.PipeEndpoint.PeerID != record.RuntimePeerID {
		t.Fatal("invite omitted pipe endpoint")
	}
	redeemerPublic, redeemerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = redeemerPublic
	redeemReq := authorityTestRequest(redeemerPrivate, share.Invite, "share_invite.redeem", record.PipeID)
	if got := h.process(ctx, redeemReq); !got.OK || got.Lease == "" {
		t.Fatalf("redeem: %#v", got)
	}
	if got := h.process(ctx, redeemReq); !got.OK {
		t.Fatalf("same client retry: %#v", got)
	}
	_, otherPrivate, _ := ed25519.GenerateKey(rand.Reader)
	if got := h.process(ctx, authorityTestRequest(otherPrivate, share.Invite, "share_invite.redeem", record.PipeID)); got.OK {
		t.Fatal("different client redeemed used invite")
	}
}

// TestScopedInviteRevocationRejectedByAuthority proves real compatibility with
// the existing scoped authority code: a `revoke invite` that resolves to the
// scoped family persists subject_type=share.invite under the current Scope
// (the exact resolution rule), and the authority's share_invite redemption
// then rejects that invite.
func TestScopedInviteRevocationRejectedByAuthority(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	record := signedAuthorityRecord(t, authorityPrivate, scope, authority.PublicString(clientPublic))
	raw, err := record.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.PutAuthoritativeRecord(ctx, store.Record{Scope: scope.String(), PipeID: record.PipeID, Name: record.DisplayName, Payload: raw, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence}); err != nil {
		t.Fatal(err)
	}
	membership, err := authority.NewToken("membership.invite", scope, authority.PublicString(clientPublic), "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	membership.Role = "publisher"
	if err := membership.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	membershipToken, _ := authority.Encode(membership)
	h := &authorityHandler{state: state, scope: scope, private: authorityPrivate, public: authorityPublic}

	// Issue a real scoped share invite.
	shareReq := authorityTestRequest(clientPrivate, membershipToken, "share_invite.issue", record.PipeID)
	shareReq.Record, _ = json.Marshal(map[string]string{"authority_address": "/ip4/127.0.0.1/tcp/1/p2p/authority"})
	share := h.process(ctx, shareReq)
	if !share.OK || share.Invite == "" {
		t.Fatalf("share issue: %#v", share)
	}
	invite, _ := authority.Decode(share.Invite)

	// Scoped redemption records kind='scoped', so the CLI resolution rule
	// classifies this JTI as the SCOPED family: subject_type=share.invite in
	// the current Scope. (Direct CLl assertion of that row lives in
	// internal/cli/revoke_test.go; here we seed the same persisted row through
	// the store the CLI writes.)
	_, redeemerPriv, _ := ed25519.GenerateKey(rand.Reader)
	redeemReq := authorityTestRequest(redeemerPriv, share.Invite, "share_invite.redeem", record.PipeID)
	if got := h.process(ctx, redeemReq); !got.OK {
		t.Fatalf("initial redeem: %#v", got)
	}
	if err := state.Revoke(ctx, scope.String(), "share.invite", invite.JTI, 1); err != nil {
		t.Fatal(err)
	}

	// The very same redemption request is now rejected by the authority.
	got := h.process(ctx, redeemReq)
	if got.OK {
		t.Fatal("scoped authority accepted a share.invite revoked through revoke invite")
	}
	if !strings.Contains(got.Error, "invite revoked") {
		t.Fatalf("expected invite-revoked denial, got %#v", got)
	}
}
