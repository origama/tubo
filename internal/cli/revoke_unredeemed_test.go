package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/origama/tubo/internal/accesslease"
	"github.com/origama/tubo/internal/reachability"
	"github.com/origama/tubo/internal/store"
)

// TestRevokeUnredeemedExactInviteByToken: an unredeemed exact invite has no
// persisted redemption evidence, but the owner holds the invite credential
// itself. `revoke invite <full-token>` must classify it as exact.invite under
// the invite's exact Pipe audience — the exact row the redemption path
// checks — never as a scoped share.invite revocation the exact path ignores.
func TestRevokeUnredeemedExactInviteByToken(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	ctx := context.Background()

	// Real `share` flow against a running owner run yields a genuine exact
	// invite token (also provisions the owner/exact authority keys).
	prepareSharePipe(t, ctx, shareTestDNSRelay, true, nil)
	provider := mustProvider(t, shareTestDNSRelay)
	var out bytes.Buffer
	app := App{
		Out: &out,
		Err: &bytes.Buffer{},
		resolveDefault: func(context.Context) (p *reachability.Provider, e error) {
			return provider, nil
		},
	}
	if err := app.Run(ctx, []string{"share", "pipe/shared", "--json"}); err != nil {
		t.Fatal(err)
	}
	var result struct {
		PipeID string `json:"pipe_id"`
		Invite string `json:"invite"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatalf("share output: %v: %s", err, out.String())
	}
	invite, err := accesslease.DecodeInvite(result.Invite)
	if err != nil {
		t.Fatal(err)
	}

	// Revoke BEFORE any redemption, passing the full token.
	if err := app.Run(ctx, []string{"revoke", "invite", result.Invite}); err != nil {
		t.Fatalf("revoke unredeemed exact invite by token: %v", err)
	}

	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	revoked, err := state.IsRevoked(ctx, result.PipeID, "exact.invite", invite.JTI)
	if err != nil || !revoked {
		t.Fatalf("unredeemed exact invite not fenced for redemption: revoked=%v %v", revoked, err)
	}
	// No scoped share.invite row may exist for the JTI.
	if scoped, serr := state.IsRevoked(ctx, "solar/default", "share.invite", invite.JTI); serr == nil && scoped {
		t.Fatal("exact invite revocation leaked into scoped share.invite family")
	}
}
