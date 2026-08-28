package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/reachability"
	"github.com/origama/tubo-rebuilt/internal/store"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

// ---- Parsing ----

func TestParseRejectsUnknownFlagWithCommandContext(t *testing.T) {
	for _, args := range [][]string{
		{"get", "pipes", "--scpoe", "solar/default"},
		{"get", "pipes", "--scpoe=solar/default"},
		{"attach", "http://127.0.0.1:1", "--name", "x", "--detail"},
		{"scope", "invite", "solar/default", "--role", "member", "--export"},
	} {
		_, err := parseCommandForTest(t, args)
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("%v: expected unknown-flag error, got %v", args, err)
		}
		if !strings.Contains(err.Error(), "see '"+strings.Join(args[:len(args)-2], " ")+" --help'") && !strings.Contains(err.Error(), "--help") {
			t.Fatalf("%v: error lacks help pointer: %v", args, err)
		}
	}
}

func TestParseTypoCannotSilentlyChangeSemantics(t *testing.T) {
	// The mission example: `get pipes --scpoe` must fail instead of acting
	// as an unscoped listing.
	_, err := parseCommandForTest(t, []string{"get", "pipes", "--scpoe", "solar/default"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag --scpoe") {
		t.Fatalf("typo accepted: %v", err)
	}
	// A near-typo of a boolean flag must fail too, and a valid neighbor must
	// not rescue it.
	_, err = parseCommandForTest(t, []string{"get", "pipes", "--refersh"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag --refersh") {
		t.Fatalf("boolean typo accepted: %v", err)
	}
	f, err := parseCommandForTest(t, []string{"get", "pipes", "--scope", "solar/default"})
	if err != nil {
		t.Fatal(err)
	}
	if f.values["scope"] != "solar/default" {
		t.Fatalf("scope = %q", f.values["scope"])
	}
}

func TestParseEnforcesRequiredFlags(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{{
		args: []string{"attach", "http://127.0.0.1:1"},
		want: "--name is required",
	},
		{
			args: []string{"connect", "--local", "127.0.0.1:9"},
			want: "--token is required",
		},
		{
			args: []string{"connect", "--token", "x"},
			want: "--local is required",
		},
		{
			args: []string{"service", "create", "x"},
			want: "--target is required",
		},
		{
			args: []string{"scope", "invite", "solar/default"},
			want: "--role is required",
		},
	} {
		_, err := parseCommandForTest(t, tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%v: want %q, got %v", tc.args, tc.want, err)
		}
	}
}

func TestParseRejectsExtraArguments(t *testing.T) {
	for _, args := range [][]string{
		{"share", "pipe/a", "pipe/b"},
		{"revoke", "invite", "x", "y"},
		{"ps", "extra"},
		{"top", "extra"},
		{"start", "pipe/a", "pipe/b"},
	} {
		_, err := parseCommandForTest(t, args)
		if err == nil || !strings.Contains(err.Error(), "unexpected argument") {
			t.Fatalf("%v: want unexpected-argument error, got %v", args, err)
		}
	}
}

func TestParseMissingArguments(t *testing.T) {
	for _, args := range [][]string{
		{"revoke", "invite"},
		{"share"},
		{"inspect"},
	} {
		_, err := parseCommandForTest(t, args)
		if err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("%v: want missing-argument error, got %v", args, err)
		}
	}
}

func TestParseAcceptsEqualsForm(t *testing.T) {
	f, err := parseCommandForTest(t, []string{"join", "overlay", "--relay=/ip4/1.1.1.1/tcp/4001/p2p/x", "--swarm-key=/tmp/k", "--force-reachability=public"})
	if err != nil {
		t.Fatal(err)
	}
	if f.values["relay"] != "/ip4/1.1.1.1/tcp/4001/p2p/x" || f.values["swarm-key"] != "/tmp/k" || f.values["force-reachability"] != "public" {
		t.Fatalf("equals-form values: %#v", f.values)
	}
}

func TestParseBooleanEqualsForm(t *testing.T) {
	f, err := parseCommandForTest(t, []string{"get", "pipes", "--json=true"})
	if err != nil || !f.bools["json"] {
		t.Fatalf("--json=true: %v %#v", err, f.bools)
	}
	f, err = parseCommandForTest(t, []string{"get", "pipes", "--json=false"})
	if err != nil || f.bools["json"] {
		t.Fatalf("--json=false: %v %#v", err, f.bools)
	}
}

func TestParseRejectsDuplicateFlags(t *testing.T) {
	_, err := parseCommandForTest(t, []string{"logs", "pipe/a", "--tail", "1", "--tail", "2"})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate accepted: %v", err)
	}
}

func TestParseObsoleteFlagHint(t *testing.T) {
	_, err := parseCommandForTest(t, []string{"get", "pipes", "--live"})
	if err == nil || !strings.Contains(err.Error(), "--live is obsolete; use --refresh") {
		t.Fatalf("obsolete hint: %v", err)
	}
}

func TestParseShortFlags(t *testing.T) {
	f, err := parseCommandForTest(t, []string{"logs", "pipe/a", "-f"})
	if err != nil || !f.bools["f"] {
		t.Fatalf("-f: %v %#v", err, f.bools)
	}
	f, err = parseCommandForTest(t, []string{"attach", "http://x", "--name", "y", "-d"})
	if err != nil || !f.bools["d"] {
		t.Fatalf("-d: %v %#v", err, f.bools)
	}
}

func TestParseRejectsUndefinedShortAndGroupWithoutSubcommand(t *testing.T) {
	if _, err := parseCommandForTest(t, []string{"get", "pipes", "-x"}); err == nil || !strings.Contains(err.Error(), "unknown flag -x") {
		t.Fatalf("-x accepted: %v", err)
	}
	if _, err := parseCommandForTest(t, []string{"get"}); err == nil || !strings.Contains(err.Error(), "requires a subcommand") {
		t.Fatalf("bare group accepted: %v", err)
	}
	if _, err := parseCommandForTest(t, []string{"supervise"}); err == nil || !strings.Contains(err.Error(), "requires a subcommand") {
		t.Fatalf("bare supervise accepted: %v", err)
	}
}

func TestParseUnknownCommand(t *testing.T) {
	if _, err := parseCommandForTest(t, []string{"garble"}); err == nil || !strings.Contains(err.Error(), `unknown command "garble"`) {
		t.Fatalf("unknown command: %v", err)
	}
}

// ---- Help ----

func TestHelpNoStoreSideEffects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TUBO_HOME", home)
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut}
	for _, args := range [][]string{
		{}, {"help"}, {"-h"}, {"--help"},
		{"help", "get"}, {"help", "get", "pipes"},
		{"attach", "--help"}, {"get", "--help"}, {"scope", "invite", "-h"},
		{"supervise", "install", "--help"}, {"help", "scope", "invite"},
	} {
		out.Reset()
		if err := app.Run(context.Background(), args); err != nil {
			t.Fatalf("%v failed: %v", args, err)
		}
	}
	// Help must not create a Store, config, or secrets.
	for _, name := range []string{"state.db", "config.yaml", "secrets"} {
		if _, err := os.Stat(filepath.Join(home, name)); err == nil {
			t.Fatalf("help created %s", name)
		}
	}
}

func TestHelpTopLevelSurface(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	if err := app.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"attach", "connect", "share", "get", "inspect", "start", "stop",
		"restart", "rm", "logs", "ps", "top", "watch", "scope", "peers",
		"config", "supervise", "service", "pipe", "relay", "revoke", "keygen",
		"version", "--quiet", "--log-level", "stdout carries the primary result",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("top help missing %q", want)
		}
	}
	if strings.Contains(text, "_run-pipe") {
		t.Fatal("top help advertises hidden _run-pipe")
	}
}

func TestHelpReflectsRequiredFlags(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	app.printHelp(specFor("attach"))
	if !strings.Contains(out.String(), "--name <value>") || !strings.Contains(out.String(), "(required)") {
		t.Fatalf("attach help missing required marker: %s", out.String())
	}
	out.Reset()
	app.printHelp(specFor("get pipes"))
	if strings.Contains(out.String(), "obsolete; use --refresh") == false {
		t.Fatalf("get pipes help misses obsolete hint: %s", out.String())
	}
	if strings.Contains(out.String(), "--refresh") == false {
		t.Fatalf("get pipes help misses --refresh: %s", out.String())
	}
}

func TestHelpGroupAndNestedSubcommand(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	if err := app.Run(context.Background(), []string{"help", "scope"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Subcommands:", "invite", "join", "create", "use"} {
		if !strings.Contains(text, want) {
			t.Fatalf("scope help missing %q", want)
		}
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"scope", "invite", "--help"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--role <value>", "(required)", "member, viewer, publisher, or forwarder"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("scope invite help missing %q", want)
		}
	}
}

func TestSpecTreeIsCoherent(t *testing.T) {
	if err := validateSpecTree(); err != nil {
		t.Fatal(err)
	}
	// Every leaf path must resolve and parse with minimal required args, so
	// the dispatch surface and the spec tree cannot drift in existence.
	for _, path := range leafPaths {
		args := strings.Fields(path)
		if _, _, err := resolveCommand(args); err != nil {
			t.Fatalf("resolve %q: %v", path, err)
		}
	}
}

// ---- Human views ----

// seedViewState populates a store with one service, one pipe, one scope, one
// peer alias, and an overlay so view handlers have data to render.
func seedViewState(t *testing.T, ctx context.Context, state *store.Store) {
	t.Helper()
	serviceRef, _ := domain.NewRef(domain.KindService, "echo")
	if _, err := state.PutService(ctx, domain.ServiceSpec{Ref: serviceRef, Kind: domain.ServiceHTTP, Target: "http://127.0.0.1:8080"}); err != nil {
		t.Fatal(err)
	}
	pipeRef, _ := domain.NewRef(domain.KindPipe, "origin")
	target, _ := domain.NewRef(domain.KindPipe, "pip_remote")
	if _, err := state.PutPipe(ctx, domain.PipeSpec{Ref: pipeRef, PipeID: "pip_local", Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:1"}, Target: target, Visibility: domain.VisibilityPrivate, Access: domain.AccessMembers, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := state.PutScope(ctx, store.Scope{Name: "solar/default", AuthorityPublic: "pub"}); err != nil {
		t.Fatal(err)
	}
	if err := state.PutPeerAlias(ctx, store.PeerAlias{PeerID: "12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv", Name: "relay"}); err != nil {
		t.Fatal(err)
	}
	if err := state.PutOverlay(ctx, "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv", state.SecretPath("missing.key"), "private"); err != nil {
		t.Fatal(err)
	}
}

func runViewCommand(t *testing.T, state *store.Store, args ...string) string {
	t.Helper()
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut}
	if err := app.runCommandForTest(t, context.Background(), state, args); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String()
}

func TestGetViewsHaveUsefulTables(t *testing.T) {
	state, ctx := openPipeViewState(t)
	seedViewState(t, ctx, state)

	services := runViewCommand(t, state, "get", "services")
	for _, want := range []string{"NAME", "KIND", "TARGET", "echo", "http://127.0.0.1:8080"} {
		if !strings.Contains(services, want) {
			t.Fatalf("get services missing %q:\n%s", want, services)
		}
	}

	pipes := runViewCommand(t, state, "get", "pipes")
	for _, want := range []string{"NAME", "ID", "STATUS", "SOURCE", "PATH", "ACCESS", "SCOPE", "origin", "defined", "local"} {
		if !strings.Contains(pipes, want) {
			t.Fatalf("get pipes missing %q:\n%s", want, pipes)
		}
		if strings.Contains(pipes, "INGRESS") || strings.Contains(pipes, "RECORD AGE") {
			t.Fatalf("compact get pipes leaked wide columns:\n%s", pipes)
		}
	}
	wide := runViewCommand(t, state, "get", "pipes", "--wide")
	for _, want := range []string{"INGRESS", "TARGET", "SESSIONS", "RECORD AGE"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("get pipes --wide missing %q", want)
		}
	}

	scopes := runViewCommand(t, state, "get", "scopes")
	for _, want := range []string{"SCOPE", "ROLE", "CURRENT", "solar/default", "authority"} {
		if !strings.Contains(scopes, want) {
			t.Fatalf("get scopes missing %q:\n%s", want, scopes)
		}
	}

	peers := runViewCommand(t, state, "get", "peers")
	for _, want := range []string{"PEER ID", "ALIAS", "relay"} {
		if !strings.Contains(peers, want) {
			t.Fatalf("get peers missing %q:\n%s", want, peers)
		}
	}

	overlays := runViewCommand(t, state, "get", "overlays")
	for _, want := range []string{"REF", "RELAY", "overlay/default"} {
		if !strings.Contains(overlays, want) {
			t.Fatalf("get overlays missing %q:\n%s", want, overlays)
		}
	}
}

func TestInspectViewsAreLabeled(t *testing.T) {
	state, ctx := openPipeViewState(t)
	seedViewState(t, ctx, state)

	service := runViewCommand(t, state, "inspect", "service/echo")
	for _, want := range []string{"Service:", "Ref", "service/echo", "Kind", "http", "Target", "http://127.0.0.1:8080"} {
		if !strings.Contains(service, want) {
			t.Fatalf("inspect service missing %q:\n%s", want, service)
		}
	}

	pipe := runViewCommand(t, state, "inspect", "pipe/origin")
	for _, want := range []string{"Pipe:", "Ref", "pipe/origin", "ID", "pip_local", "Status", "defined", "Target", "pipe/pip_remote", "Path"} {
		if !strings.Contains(pipe, want) {
			t.Fatalf("inspect pipe missing %q:\n%s", want, pipe)
		}
	}

	scope := runViewCommand(t, state, "inspect", "scope/solar/default")
	for _, want := range []string{"Ref", "solar/default", "Role", "authority"} {
		if !strings.Contains(scope, want) {
			t.Fatalf("inspect scope missing %q:\n%s", want, scope)
		}
	}

	overlay := runViewCommand(t, state, "inspect", "overlay/default")
	for _, want := range []string{"Ref", "overlay/default", "Reachability", "private"} {
		if !strings.Contains(overlay, want) {
			t.Fatalf("inspect overlay missing %q:\n%s", want, overlay)
		}
	}

	if err := state.PutSession(ctx, store.Session{ID: "ses_1", PipeRef: "pipe/origin", InitialTransportPath: "relayed", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	session := runViewCommand(t, state, "inspect", "session/ses_1")
	for _, want := range []string{"Ref", "ses_1", "Pipe", "pipe/origin", "Path", "relayed"} {
		if !strings.Contains(session, want) {
			t.Fatalf("inspect session missing %q:\n%s", want, session)
		}
	}

	peer := runViewCommand(t, state, "inspect", "peer/12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv")
	for _, want := range []string{"Ref", "12D3KooWSoLbunv7D2wE4QFsRAdZR8oqSD2ruhh18gPN1A9d3Rnv", "Name", "relay"} {
		if !strings.Contains(peer, want) {
			t.Fatalf("inspect peer missing %q:\n%s", want, peer)
		}
	}
}

func TestPSIsCompactAndTopIsMetrics(t *testing.T) {
	state, ctx := openPipeViewState(t)
	seedViewState(t, ctx, state)
	ref := "pipe/origin"
	if err := state.PutRun(ctx, store.Run{PrimaryRef: ref, Kind: "pipe", PID: 4242, ProcessIdentity: "id", Source: "detached", Status: "ready", PeerID: "peer", Address: "127.0.0.1:8080", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}

	psOut := runViewCommand(t, state, "ps")
	for _, want := range []string{"REF", "KIND", "STATUS", "PID", "SOURCE", "SESS", "PATH", "ADDRESS", "pipe/origin", "detached"} {
		if !strings.Contains(psOut, want) {
			t.Fatalf("ps missing %q:\n%s", want, psOut)
		}
	}
	if strings.Contains(psOut, "NET RX") || strings.Contains(psOut, "RX/s") {
		t.Fatalf("ps leaked live metrics:\n%s", psOut)
	}

	topOut := runViewCommand(t, state, "top", "--once")
	for _, want := range []string{"NET RX", "NET TX", "RX/s", "TX/s", "PEERS", "CONNS"} {
		if !strings.Contains(topOut, want) {
			t.Fatalf("top missing %q", want)
		}
	}
}

// ---- Workflow emitters ----

func TestEmitAttachCreatedVsReused(t *testing.T) {
	pipeRef, _ := domain.NewRef(domain.KindPipe, "origin")
	serviceRef, _ := domain.NewRef(domain.KindService, "origin")

	cases := []struct {
		name            string
		result          usecase.AttachResult
		run             *store.Run
		wantHuman, want string
	}{
		{"created", usecase.AttachResult{ServiceRef: serviceRef, PipeRef: pipeRef, PipeID: "pip_1", Created: true, PipeCreated: true}, nil, "service/origin created\npipe/origin created\n", ""},
		{"reused", usecase.AttachResult{ServiceRef: serviceRef, PipeRef: pipeRef, PipeID: "pip_1"}, nil, "service/origin reused\npipe/origin reused\n", ""},
		{"ready", usecase.AttachResult{ServiceRef: serviceRef, PipeRef: pipeRef, PipeID: "pip_1", Created: true, PipeCreated: false}, &store.Run{PID: 7}, "service/origin created\npipe/origin reused\npipe/origin ready (pid 7)\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			app := App{Out: &out, Err: &errOut}
			var runPtr *store.Run
			if tc.run != nil {
				value := *tc.run
				runPtr = &value
			}
			if err := app.emitAttach(false, tc.result, runPtr); err != nil {
				t.Fatal(err)
			}
			if out.String() != tc.wantHuman {
				t.Fatalf("human output = %q, want %q", out.String(), tc.wantHuman)
			}
			out.Reset()
			if err := app.emitAttach(true, tc.result, runPtr); err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(out.Bytes(), &value); err != nil {
				t.Fatalf("json: %v: %q", err, out.String())
			}
			if value["service_status"] != "created" && value["service_status"] != "reused" {
				t.Fatalf("service status: %v", value["service_status"])
			}
			if tc.run != nil && (value["status"] != "ready" || value["pid"] == nil) {
				t.Fatalf("ready status missing: %v", value)
			}
		})
	}
}

func TestEmitConnectShapes(t *testing.T) {
	pipeRef, _ := domain.NewRef(domain.KindPipe, "west")
	result := usecase.ConnectExactResult{PipeRef: pipeRef, PipeID: "pip_1", TargetPipeID: "pip_remote"}

	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut}
	if err := app.emitConnect(false, result, "127.0.0.1:5555", nil); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "pipe/west created (local listen: 127.0.0.1:5555)\n"; got != want {
		t.Fatalf("human = %q, want %q", got, want)
	}
	out.Reset()
	if err := app.emitConnect(true, result, "127.0.0.1:5555", &store.Run{PID: 9}); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value["ref"] != "pipe/west" || value["local"] != "127.0.0.1:5555" || value["target_id"] != "pip_remote" || value["status"] != "ready" || value["pid"] != float64(9) {
		t.Fatalf("json shape: %v", value)
	}
}

func TestShareHumanStdoutIsRawInvite(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	prepareSharePipe(t, ctx, shareTestDNSRelay, true, nil)
	provider := mustProvider(t, shareTestDNSRelay)
	var out bytes.Buffer
	app := App{
		Out:            &out,
		Err:            &bytes.Buffer{},
		resolveDefault: func(context.Context) (*reachability.Provider, error) { return provider, nil },
	}
	if err := app.Run(ctx, []string{"share", "pipe/shared"}); err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimSpace(out.String())
	if len(trimmed) < 40 {
		t.Fatalf("stdout is not a raw invite token: %q", trimmed)
	}
	if out.String() != trimmed+"\n" {
		t.Fatalf("stdout must be exactly one raw invite line, got: %q", out.String())
	}
	if strings.Contains(out.String(), "sharing") || strings.Contains(out.String(), "recipient") {
		t.Fatalf("explanation leaked to stdout: %q", out.String())
	}
}

func TestShareJSONIsSingleValue(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	prepareSharePipe(t, ctx, shareTestDNSRelay, true, nil)
	provider := mustProvider(t, shareTestDNSRelay)
	var out bytes.Buffer
	app := App{
		Out:            &out,
		Err:            &bytes.Buffer{},
		resolveDefault: func(context.Context) (*reachability.Provider, error) { return provider, nil },
	}
	if err := app.Run(ctx, []string{"share", "pipe/shared", "--json"}); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatalf("share --json not single value: %v: %q", err, out.String())
	}
	if value["invite"] == "" {
		t.Fatal("share --json missing invite")
	}
}

// ---- stderr purity ----

func TestJSONStdoutUncontaminatedByProgress(t *testing.T) {
	state, ctx := openPipeViewState(t)
	seedViewState(t, ctx, state)
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut}
	if err := app.runCommandForTest(t, ctx, state, []string{"get", "services", "--json"}); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatalf("get services --json not single value: %v", err)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr contaminated for --json: %q", errOut.String())
	}
}

// ---- Error hints ----

func TestErrorHintsAreConcise(t *testing.T) {
	state, ctx := openPipeViewState(t)
	var out, errOut bytes.Buffer
	app := App{Out: &out, Err: &errOut}
	err := app.runCommandForTest(t, ctx, state, []string{"stop", "pipe/missing"})
	if err == nil {
		t.Fatal("stop on missing pipe succeeded")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Fatalf("stop error: %v", err)
	}
	if Hint(err) == "" {
		t.Fatalf("stop error lacks a next-action hint: %v", err)
	}
}

func TestHintForClassAttachesTry(t *testing.T) {
	err := hintForClass(errorWith("not_found: pipe/x"))
	if Hint(err) == "" {
		t.Fatal("not_found hint missing")
	}
	err = hintForClass(errorWith("conflict: pipe/x is already running"))
	if Hint(err) == "" {
		t.Fatal("already-running conflict should hint stop-first")
	}
	err = hintForClass(errorWith("authorization_denied: bad"))
	if Hint(err) == "" {
		t.Fatal("authorization_denied hint missing")
	}
}

func TestReserveForegroundAlreadyRunningHasStopHint(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	identity, err := store.ProcessIdentity(os.Getpid())
	if err != nil {
		t.Skipf("process identity unavailable: %v", err)
	}
	if err := state.PutRun(ctx, store.Run{PrimaryRef: "pipe/x", Kind: "pipe", PID: os.Getpid(), ProcessIdentity: identity, Status: "ready"}); err != nil {
		t.Fatal(err)
	}
	err = reserveForeground(ctx, state, "pipe/x", "pipe")
	if err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("already-running error = %v", err)
	}
	if Hint(err) == "" {
		t.Fatal("already-running error lacks stop-first hint")
	}
}

func TestRmWithoutArgsIsInvalidInputNotPanic(t *testing.T) {
	state, ctx := openPipeViewState(t)
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	err := app.runCommandForTest(t, ctx, state, []string{"rm"})
	if err == nil || !strings.Contains(err.Error(), "invalid_input: rm requires a resource ref or --stale") {
		t.Fatalf("bare rm error = %v", err)
	}
	err = app.runCommandForTest(t, ctx, state, []string{"rm", "--json"})
	if err == nil || !strings.Contains(err.Error(), "requires a resource ref") {
		t.Fatalf("rm --json error = %v", err)
	}
}

func TestLogsRejectsJSONWithFollow(t *testing.T) {
	state, ctx := openPipeViewState(t)
	if err := seedLogFile(t, state, "pipe/x", "line1\n"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	err := app.runCommandForTest(t, ctx, state, []string{"logs", "pipe/x", "--json", "-f"})
	if err == nil || !strings.Contains(err.Error(), "--json cannot be combined with -f") {
		t.Fatalf("logs --json -f error = %v", err)
	}
}

func TestStartJSONForegroundRejects(t *testing.T) {
	state, ctx := openPipeViewState(t)
	ref, _ := domain.NewRef(domain.KindPipe, "origin")
	target, _ := domain.NewRef(domain.KindPipe, "pip_remote")
	if _, err := state.PutPipe(ctx, domain.PipeSpec{Ref: ref, PipeID: "pip_local", Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:1"}, Target: target, Visibility: domain.VisibilityPrivate, Access: domain.AccessMembers, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, "", ""); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	err := app.start(ctx, state, flags{values: map[string]string{}, bools: map[string]bool{"json": true}, args: []string{"pipe/origin"}})
	if err == nil || !strings.Contains(err.Error(), "requires -d") {
		t.Fatalf("foreground start --json error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("foreground start --json emitted stdout: %q", out.String())
	}
}

func seedLogFile(t *testing.T, state *store.Store, ref, content string) error {
	t.Helper()
	path, err := resourceLogPath(state, ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o600)
}

func TestParseEmptyValueForRequiredFlagFails(t *testing.T) {
	_, err := parseCommandForTest(t, []string{"attach", "http://x", "--name="})
	if err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Fatalf("--name= accepted: %v", err)
	}
}

func TestLogsHumanKeepsRawLines(t *testing.T) {
	state, ctx := openPipeViewState(t)
	ref, _ := domain.NewRef(domain.KindPipe, "runner")
	target, _ := domain.NewRef(domain.KindPipe, "pip_remote")
	if _, err := state.PutPipe(ctx, domain.PipeSpec{Ref: ref, PipeID: "pip_local", Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:1"}, Target: target, Visibility: domain.VisibilityPrivate, Access: domain.AccessMembers, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}, "", ""); err != nil {
		t.Fatal(err)
	}
	if err := seedLogFile(t, state, "pipe/runner", "first line\nsecond line\n"); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := App{Out: &out, Err: &bytes.Buffer{}}
	if err := app.runCommandForTest(t, ctx, state, []string{"logs", "pipe/runner", "--tail", "10"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "first line\nsecond line\n"; got != want {
		t.Fatalf("logs human = %q, want %q", got, want)
	}
}

func TestGroupLevelUnknownFlagRejected(t *testing.T) {
	for _, args := range [][]string{
		{"get", "--bogus"},
		{"scope", "--bogus"},
		{"get", "pipes", "--bogus"},
	} {
		_, err := parseCommandForTest(t, args)
		if err == nil || !strings.Contains(err.Error(), "unknown flag") {
			t.Fatalf("%v accepted: %v", args, err)
		}
	}
}
