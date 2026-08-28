package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/node"
	"github.com/origama/tubo-rebuilt/internal/reachability"
	"github.com/origama/tubo-rebuilt/internal/store"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

const (
	shareTestRelayPeer = "12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME"
	shareTestOwnerPeer = "12D3KooWRJCtSj2fWsnyHYdpcmQoHwDzVzr39VmFrRwmJrwwW57G"
	shareTestDNSRelay  = "/dns4/relay.tubo.click/tcp/4001/p2p/" + shareTestRelayPeer
	shareTestIPRelay   = "/ip4/172.232.189.160/tcp/4001/p2p/" + shareTestRelayPeer
)

func TestZeroConfigShareEmbedsCanonicalRunningOwnerLocator(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	locator := prepareSharePipe(t, ctx, shareTestDNSRelay, true, nil)
	provider := mustProvider(t, shareTestDNSRelay)
	var out bytes.Buffer
	app := App{
		Out: &out,
		Err: &bytes.Buffer{},
		resolveDefault: func(context.Context) (*reachability.Provider, error) {
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
	if result.Invite == "" {
		t.Fatal("zero-config share emitted empty invite")
	}
	invite, err := accesslease.DecodeInvite(result.Invite)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := accesslease.DecodeOwnerBinding(invite.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if binding.RedemptionPeers != locator {
		t.Fatalf("redemption locator = %q, want %q", binding.RedemptionPeers, locator)
	}
	if binding.RedemptionProtocol != "/tubo/exact-redemption/1" {
		t.Fatalf("redemption protocol = %q", binding.RedemptionProtocol)
	}
	assertCanonicalOwnerCircuit(t, binding.RedemptionPeers)
	if strings.Contains(binding.RedemptionPeers, "127.0.0.1:18080") {
		t.Fatal("locator leaked local HTTP Service target")
	}
	if err := invite.Verify(time.Now().UTC(), result.PipeID); err != nil {
		t.Fatalf("emitted ExactInvite invalid: %v", err)
	}
}

func TestConfiguredOverlayShareKeepsCanonicalOwnerLocator(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	locator := prepareSharePipe(t, ctx, shareTestIPRelay, true, func(state *store.Store, run *store.Run) {
		if err := state.PutOverlay(ctx, shareTestIPRelay, state.SecretPath("test-swarm.key"), "private"); err != nil {
			t.Fatal(err)
		}
	})
	defaultCalls := 0
	var out bytes.Buffer
	app := App{
		Out: &out,
		Err: &bytes.Buffer{},
		resolveDefault: func(context.Context) (*reachability.Provider, error) {
			defaultCalls++
			return nil, errors.New("default resolver must not run")
		},
	}
	if err := app.Run(ctx, []string{"share", "pipe/shared", "--json"}); err != nil {
		t.Fatal(err)
	}
	if defaultCalls != 0 {
		t.Fatalf("configured overlay forced default resolver %d times", defaultCalls)
	}
	var result struct {
		Invite string `json:"invite"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	invite, err := accesslease.DecodeInvite(result.Invite)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := accesslease.DecodeOwnerBinding(invite.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if binding.RedemptionPeers != locator {
		t.Fatalf("configured locator = %q, want %q", binding.RedemptionPeers, locator)
	}
	assertCanonicalOwnerCircuit(t, binding.RedemptionPeers)
}

func TestShareRuntimeUnavailableEmitsNoInvite(t *testing.T) {
	tests := []struct {
		name       string
		createRun  bool
		mutate     func(*store.Run)
		configure  func(*store.Store) error
		resolveNil bool
		resolveErr error
	}{
		{name: "missing runtime"},
		{name: "stopped runtime", createRun: true, mutate: func(run *store.Run) { run.Status = "stopped" }},
		{name: "degraded runtime", createRun: true, mutate: func(run *store.Run) { run.Status = "degraded" }},
		{name: "stale process", createRun: true, mutate: func(run *store.Run) { run.ProcessIdentity = "stale-process" }},
		{name: "wrong runtime kind", createRun: true, mutate: func(run *store.Run) { run.Kind = "relay" }},
		{name: "malformed owner peer", createRun: true, mutate: func(run *store.Run) { run.PeerID = "not-a-peer" }},
		{name: "mismatched owner peer", createRun: true, mutate: func(run *store.Run) { run.PeerID = shareTestRelayPeer }},
		{name: "relay-only locator", createRun: true, mutate: func(run *store.Run) { run.Address = shareTestDNSRelay }},
		{name: "malformed locator", createRun: true, mutate: func(run *store.Run) { run.Address = "not-a-multiaddr" }},
		{name: "malformed configured relay", createRun: true, configure: func(state *store.Store) error {
			return state.PutOverlay(context.Background(), "not-a-multiaddr", state.SecretPath("unused.key"), "private")
		}},
		{name: "verified relay missing", createRun: true, resolveNil: true},
		{name: "verified relay unavailable", createRun: true, resolveErr: errors.New("provider unreachable")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TUBO_HOME", t.TempDir())
			ctx := context.Background()
			prepareSharePipe(t, ctx, shareTestDNSRelay, test.createRun, func(state *store.Store, run *store.Run) {
				if test.configure != nil {
					if err := test.configure(state); err != nil {
						t.Fatal(err)
					}
				}
				if test.mutate != nil {
					test.mutate(run)
				}
			})
			provider := mustProvider(t, shareTestDNSRelay)
			var out bytes.Buffer
			app := App{
				Out: &out,
				Err: &bytes.Buffer{},
				resolveDefault: func(context.Context) (*reachability.Provider, error) {
					if test.resolveErr != nil {
						return nil, test.resolveErr
					}
					if test.resolveNil {
						return nil, nil
					}
					return provider, nil
				},
			}
			err := app.Run(ctx, []string{"share", "pipe/shared", "--json"})
			if err == nil || !strings.Contains(err.Error(), "runtime_unavailable:") {
				t.Fatalf("share error = %v, want runtime_unavailable", err)
			}
			if out.Len() != 0 {
				t.Fatalf("failed share emitted output/invite: %q", out.String())
			}
		})
	}
}

func TestOwnerLocatorRejectsWrongPipeIdentity(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	prepareSharePipe(t, ctx, shareTestDNSRelay, true, nil)
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	locator := newOwnerRedemptionLocator(state, func(context.Context) (*reachability.Provider, error) {
		return mustProvider(t, shareTestDNSRelay), nil
	})
	ref, err := domain.NewRef(domain.KindPipe, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if value, err := locator.LocateOwner(ctx, ref, "pip_wrong"); err == nil || value != "" {
		t.Fatalf("wrong Pipe locator=%q err=%v", value, err)
	}
}

func prepareSharePipe(t *testing.T, ctx context.Context, relay string, createRun bool, mutate func(*store.Store, *store.Run)) string {
	t.Helper()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	u := usecase.New(store.NewServiceRepoAdapter(state), store.NewPipeRepoAdapter(state))
	u.Secrets = state.SecretPath()
	attached, err := u.Attach(ctx, usecase.AttachInput{Name: "shared", Target: "http://127.0.0.1:18080"})
	if err != nil {
		t.Fatal(err)
	}
	owner, err := peer.Decode(shareTestOwnerPeer)
	if err != nil {
		t.Fatal(err)
	}
	locator, err := node.CircuitAddress(relay, owner)
	if err != nil {
		t.Fatal(err)
	}
	run := store.Run{
		PrimaryRef: attached.PipeRef.String(),
		Kind:       "pipe",
		PID:        os.Getpid(),
		Status:     "ready",
		PeerID:     owner.String(),
		Address:    locator,
	}
	if mutate != nil {
		mutate(state, &run)
	}
	if createRun {
		if err := state.PutRun(ctx, run); err != nil {
			t.Fatal(err)
		}
	}
	return locator
}

func mustProvider(t *testing.T, address string) *reachability.Provider {
	t.Helper()
	provider, err := reachability.FromAddress(address)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func assertCanonicalOwnerCircuit(t *testing.T, address string) {
	t.Helper()
	value, err := ma.NewMultiaddr(address)
	if err != nil {
		t.Fatal(err)
	}
	var peers []string
	circuits := 0
	ma.ForEach(value, func(component ma.Component) bool {
		switch component.Protocol().Code {
		case ma.P_P2P:
			peers = append(peers, component.Value())
		case ma.P_CIRCUIT:
			circuits++
		}
		return true
	})
	if len(peers) != 2 || peers[0] != shareTestRelayPeer || peers[1] != shareTestOwnerPeer || circuits != 1 {
		t.Fatalf("locator not canonical: peers=%v circuits=%d", peers, circuits)
	}
}
