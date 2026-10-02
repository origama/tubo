package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
	"time"

	"github.com/origama/tubo/internal/authority"
	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/openauth"
	"github.com/origama/tubo/internal/store"
)

const scopeAuthPipe = "pip_scope"

// scopeTokenFixture builds a real store plus a pinned authority key pair and a
// client identity so the transitional adapter can be exercised exactly as the
// production scope path does (issue #48). The store and keys are used by
// malformed/missing/mismatch fail-closed tests only; no network/sleep.
func scopeTokenFixture(t *testing.T) (*scopeTokenOpenAuthorizer, *store.Store, domain.Scope, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scope := domain.Scope{Cluster: "c", Namespace: "default"}
	authPub, authPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, clientPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	// Construct directly to bypass quarantine panic in newScopeTokenOpenAuthorizer.
	a := &scopeTokenOpenAuthorizer{state: state, scope: scope.String(), authorityPublic: authPub}
	return a, state, scope, authPriv, clientPriv
}

func signedConnectLease(authPriv ed25519.PrivateKey, scope domain.Scope, clientPub, pipeID string, lifetime time.Duration) (string, authority.Token) {
	token, _ := authority.NewToken("connect.access", scope, clientPub, pipeID, lifetime)
	_ = token.Sign(authPriv)
	enc, _ := authority.Encode(token)
	return enc, token
}

func scopeTokenRequest(clientPriv ed25519.PrivateKey, clientPub, pipeID, lease string) openauth.OpenRequest {
	return openauth.OpenRequest{
		PipeID:         pipeID,
		ClientPublic:   clientPub,
		Challenge:      "challenge-fresh",
		ChallengeProof: authority.SignProof(clientPriv, "tubo-open-v2", "challenge-fresh", pipeID),
		AccessLease:    lease,
		Now:            time.Now().UTC(),
	}
}

func TestScopeTokenOpenAuthorizerFailsClosedForMalformedOrMissingLease(t *testing.T) {
	a, _, scope, authPriv, clientPriv := scopeTokenFixture(t)
	clientPub := authority.PublicString(clientPriv.Public().(ed25519.PublicKey))
	cases := []struct {
		name  string
		lease string
	}{
		{name: "missing lease", lease: ""},
		{name: "malformed lease", lease: "not-base64!!!"},
		{name: "other token type", lease: mustOtherType(authPriv, scope, clientPub)},
		{name: "wrong signature key", lease: mustWrongKey(scope, clientPub)},
		{name: "wrong scope", lease: mustWrongScope(authPriv, clientPub)},
		{name: "wrong audience", lease: mustWrongAudience(authPriv, scope, clientPub)},
		{name: "tampered payload", lease: mustTamperedPayload(authPriv, scope, clientPub)},
		{name: "expired lease", lease: mustLifetime(authPriv, scope, clientPub, -time.Minute)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth, err := a.AuthorizeOpen(context.Background(), scopeTokenRequest(clientPriv, clientPub, scopeAuthPipe, tc.lease))
			if err == nil {
				t.Fatalf("expected fail-closed denial, got success %#v", auth)
			}
		})
	}
}

func TestScopeTokenOpenAuthorizerScopedPathSucceedsAndHonorsRevocation(t *testing.T) {
	a, state, scope, authPriv, clientPriv := scopeTokenFixture(t)
	clientPub := authority.PublicString(clientPriv.Public().(ed25519.PublicKey))
	lease, token := signedConnectLease(authPriv, scope, clientPub, scopeAuthPipe, time.Minute)
	req := scopeTokenRequest(clientPriv, clientPub, scopeAuthPipe, lease)
	auth, err := a.AuthorizeOpen(context.Background(), req)
	if err != nil {
		t.Fatalf("happy scoped path failed through adapter: %v", err)
	}
	if auth.PipeID != scopeAuthPipe || auth.Subject != clientPub || auth.LeaseID != token.JTI || !auth.ExpiresAt.After(req.Now) {
		t.Fatalf("unexpected authorization: %#v", auth)
	}

	// Revoke the same access JTI, then the adapter must fail closed on the
	// exact already-valid credential.
	if err := state.Revoke(context.Background(), scope.String(), "connect.access", token.JTI, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := a.AuthorizeOpen(context.Background(), req); err == nil {
		t.Fatal("revoked access lease must fail closed through adapter")
	}
}

func mustOtherType(authPriv ed25519.PrivateKey, scope domain.Scope, clientPub string) string {
	token, _ := authority.NewToken("share.invite", scope, clientPub, scopeAuthPipe, time.Minute)
	_ = token.Sign(authPriv)
	enc, _ := authority.Encode(token)
	return enc
}
func mustWrongKey(scope domain.Scope, clientPub string) string {
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	enc, _ := signedConnectLease(otherPriv, scope, clientPub, scopeAuthPipe, time.Minute)
	return enc
}
func mustWrongScope(authPriv ed25519.PrivateKey, clientPub string) string {
	other := domain.Scope{Cluster: "other", Namespace: "default"}
	enc, _ := signedConnectLease(authPriv, other, clientPub, scopeAuthPipe, time.Minute)
	return enc
}
func mustWrongAudience(authPriv ed25519.PrivateKey, scope domain.Scope, clientPub string) string {
	enc, _ := signedConnectLease(authPriv, scope, clientPub, "pip_not_the_pipe", time.Minute)
	return enc
}

// mustTamperedPayload takes a valid signed lease and corrupts one payload byte
// between base64 decode and re-encode, so the token decodes to a well-formed
// envelope whose signature no longer matches the altered body.
func mustTamperedPayload(authPriv ed25519.PrivateKey, scope domain.Scope, clientPub string) string {
	enc, _ := signedConnectLease(authPriv, scope, clientPub, scopeAuthPipe, time.Minute)
	data, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return "!tamper-failure"
	}
	// Flip a byte well inside the JSON body so the base64 envelope stays valid
	// but the signed message no longer matches the altered body.
	flip := len(data) / 2
	data[flip] ^= 0x01
	return base64.RawURLEncoding.EncodeToString(data)
}

func mustLifetime(authPriv ed25519.PrivateKey, scope domain.Scope, clientPub string, lifetime time.Duration) string {
	enc, _ := signedConnectLease(authPriv, scope, clientPub, scopeAuthPipe, lifetime)
	return enc
}
