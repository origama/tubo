package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	logging "github.com/origama/tubo/internal/logging"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	iversion "github.com/origama/tubo/internal/version"
	"golang.org/x/crypto/ssh"
)

func captureOutputs(f func() error) (string, string, error) {
	oldOut := os.Stdout
	oldErr := os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout = outW
	os.Stderr = errW
	err := f()
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr
	var outBuf bytes.Buffer
	var errBuf bytes.Buffer
	_, _ = io.Copy(&outBuf, outR)
	_, _ = io.Copy(&errBuf, errR)
	return outBuf.String(), errBuf.String(), err
}

func capture(f func() error) (string, error) {
	out, _, err := captureOutputs(f)
	return out, err
}
func TestIDFromSeed(t *testing.T) {
	out, err := capture(func() error { return run([]string{"id", "from-seed", "abc"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "12D3") {
		t.Fatalf("peer id=%q", out)
	}
}
func TestKeygenSwarm(t *testing.T) {
	p := filepath.Join(t.TempDir(), "swarm.key")
	if err := run([]string{"keygen", "swarm", "--out", p}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "/key/swarm/psk/1.0.0/") {
		t.Fatal(string(b))
	}
	if err := run([]string{"keygen", "swarm", "--out", p}); err == nil {
		t.Fatal("expected no overwrite")
	}
}
func TestResolveRuntimeRoleAliases(t *testing.T) {
	cases := []struct {
		name     string
		in       []string
		wantRole string
		wantArgs []string
	}{
		{name: "short relay", in: []string{"relay", "--config", "relay.yaml"}, wantRole: "relay", wantArgs: []string{"--config", "relay.yaml"}},
		{name: "gateway alias", in: []string{"gateway", "--listen", ":8443"}, wantRole: "edge", wantArgs: []string{"--listen", ":8443"}},
		{name: "attach positional target", in: []string{"attach", "http://127.0.0.1:1234", "--name", "lmstudio"}, wantRole: "service", wantArgs: []string{"--target", "http://127.0.0.1:1234", "--name", "lmstudio"}},
		{name: "attach explicit target flag", in: []string{"attach", "--target", "http://127.0.0.1:1234", "--name", "lmstudio"}, wantRole: "service", wantArgs: []string{"--target", "http://127.0.0.1:1234", "--name", "lmstudio"}},
		{name: "attach tcp positional target", in: []string{"attach", "tcp://127.0.0.1:1234", "--name", "tlsdemo"}, wantRole: "service", wantArgs: []string{"--target", "tcp://127.0.0.1:1234", "--name", "tlsdemo"}},
		{name: "attach shorthand name and port", in: []string{"attach", "dummysvc", "--port", "8080"}, wantRole: "service", wantArgs: []string{"--target", "http://127.0.0.1:8080", "--name", "dummysvc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCommand, gotRole, gotArgs, ok, err := resolveRuntimeRole(tc.in)
			if err != nil {
				t.Fatalf("resolveRuntimeRole(%v) err = %v", tc.in, err)
			}
			if !ok {
				t.Fatalf("resolveRuntimeRole(%v) did not resolve a runtime role", tc.in)
			}
			if gotCommand != tc.in[0] {
				t.Fatalf("command = %q, want %q", gotCommand, tc.in[0])
			}
			if gotRole != tc.wantRole {
				t.Fatalf("role = %q, want %q", gotRole, tc.wantRole)
			}
			if gotRole == "service" && !hasLongFlag(tc.in, "--seed") {
				var seed string
				var ok bool
				gotArgs, seed, ok, err = consumeLongFlag(gotArgs, "--seed")
				if err != nil || !ok || !strings.HasPrefix(seed, "attach-") {
					t.Fatalf("attach args missing generated seed: args=%#v seed=%q ok=%t err=%v", gotArgs, seed, ok, err)
				}
			}
			if gotRole == "service" && !hasLongFlag(tc.in, "--p2p-listen") {
				var listen string
				var ok bool
				gotArgs, listen, ok, err = consumeLongFlag(gotArgs, "--p2p-listen")
				if err != nil || !ok || listen != "/ip4/0.0.0.0/tcp/0" {
					t.Fatalf("attach args missing default p2p listen: args=%#v listen=%q ok=%t err=%v", gotArgs, listen, ok, err)
				}
			}
			if strings.Join(gotArgs, "\x00") != strings.Join(tc.wantArgs, "\x00") {
				t.Fatalf("args = %#v, want %#v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestEnsureAttachRuntimeDefaultsSkipsConfigMode(t *testing.T) {
	got, err := ensureAttachRuntimeDefaults([]string{"--config", "service.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, "\x00") != strings.Join([]string{"--config", "service.yaml"}, "\x00") {
		t.Fatalf("config-mode defaults changed args: %#v", got)
	}
}

func TestResolveRuntimeRoleRejectsLegacyRoleCommands(t *testing.T) {
	for _, args := range [][]string{
		{"relay", "run", "--config", "relay.yaml"},
		{"edge", "run", "--config", "edge.yaml"},
		{"service", "run", "--config", "service.yaml"},
		{"bridge", "run", "--config", "bridge.yaml"},
	} {
		if _, _, _, _, err := resolveRuntimeRole(args); err == nil {
			t.Fatalf("expected legacy command rejection for %v", args)
		}
	}
}

func TestResolveRuntimeRoleRejectsDuplicateAttachTarget(t *testing.T) {
	if _, _, _, _, err := resolveRuntimeRole([]string{"attach", "http://127.0.0.1:1234", "--target", "http://127.0.0.1:11434", "--name", "lmstudio"}); err == nil {
		t.Fatal("expected duplicate attach target error")
	}
}

func TestResolveRuntimeRoleRejectsInvalidAttachShorthand(t *testing.T) {
	for _, args := range [][]string{
		{"attach", "dummysvc"},
		{"attach", "dummysvc", "--port", "8080", "--name", "other"},
		{"attach", "http://127.0.0.1:1234", "--port", "8080"},
		{"attach", "--port", "8080"},
	} {
		if _, _, _, _, err := resolveRuntimeRole(args); err == nil {
			t.Fatalf("expected shorthand rejection for %v", args)
		}
	}
}

func TestStripDetachArgs(t *testing.T) {
	got, detach := stripDetachArgs([]string{"http://127.0.0.1:1234", "-d", "--name", "lmstudio", "--detach"})
	if !detach {
		t.Fatal("expected detach flag to be detected")
	}
	want := []string{"http://127.0.0.1:1234", "--name", "lmstudio"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestStripNoInitArgs(t *testing.T) {
	got, noInit := stripNoInitArgs([]string{"--target", "http://127.0.0.1:1234", "--no-init", "--name", "lmstudio"})
	if !noInit {
		t.Fatal("expected --no-init to be detected")
	}
	want := []string{"--target", "http://127.0.0.1:1234", "--name", "lmstudio"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %#v, want %#v", got, want)
	}
}

func TestMaybeImplicitInitCreatesConfigAndKey(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "cfg")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("CI", "")
	if err := maybeImplicitInit("service", []string{"--target", "http://127.0.0.1:1234", "--name", "lmstudio", "--relay", "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWTestPeer"}, false); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(configHome, "tubo", "config.yaml")
	keyPath := filepath.Join(configHome, "tubo", "swarm.key")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config not created: %v", err)
	}
	if _, err := os.Stat(keyPath); err != nil {
		t.Fatalf("swarm key not created: %v", err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.PrivateKeyFile != keyPath {
		t.Fatalf("private_key_file = %q, want %q", cfg.Network.PrivateKeyFile, keyPath)
	}
	if len(cfg.Network.RelayPeers) != 1 {
		t.Fatalf("relay_peers = %#v", cfg.Network.RelayPeers)
	}
	if !cfg.Network.Autorelay || !cfg.Network.HolePunching {
		t.Fatalf("expected autorelay and hole punching defaults: %#v", cfg.Network)
	}
}

func TestMaybeImplicitInitDisabled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	if err := maybeImplicitInit("service", []string{"--target", "http://127.0.0.1:1234", "--name", "lmstudio"}, true); err == nil {
		t.Fatal("expected --no-init to disable implicit init")
	}
	t.Setenv("CI", "true")
	if err := maybeImplicitInit("service", []string{"--target", "http://127.0.0.1:1234", "--name", "lmstudio"}, false); err == nil {
		t.Fatal("expected CI to disable implicit init")
	}
}

func TestEnsureJoinedPublicNetworkInstallsSignedBundle(t *testing.T) {
	t.Setenv("CI", "")
	useTestBundleDefaults(t, true)
	for _, command := range []string{"connect", "relay"} {
		t.Run(command, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
			stdout, stderr, err := captureOutputs(func() error { return ensureJoinedPublicNetwork(command, false) })
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("expected clean stdout for implicit join, got: %q", stdout)
			}
			if strings.TrimSpace(stderr) != "" {
				t.Fatalf("expected quiet default stderr for implicit join, got: %q", stderr)
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tubo", "config.yaml")); err != nil {
				t.Fatalf("config not written: %v", err)
			}
		})
	}
}

func TestEnsureJoinedPublicNetworkDisabled(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	if err := ensureJoinedPublicNetwork("connect", true); err == nil {
		t.Fatal("expected --no-init to disable implicit public join")
	}
	t.Setenv("CI", "true")
	if err := ensureJoinedPublicNetwork("connect", false); err == nil {
		t.Fatal("expected CI to disable implicit public join")
	}
}

func TestParseGlobalCLIOptionsAfterSubcommand(t *testing.T) {
	cases := []struct {
		args          []string
		wantVerbosity int
		wantRest      string
	}{
		{args: []string{"-vv", "share", "service/myapi"}, wantVerbosity: 2, wantRest: "share service/myapi"},
		{args: []string{"share", "-v", "service/myapi"}, wantVerbosity: 1, wantRest: "share service/myapi"},
		{args: []string{"share", "service/myapi", "-v"}, wantVerbosity: 1, wantRest: "share service/myapi"},
		{args: []string{"share", "service/myapi", "--log-level", "debug"}, wantVerbosity: 0, wantRest: "share service/myapi"},
	}
	for _, tt := range cases {
		opts, rest, err := parseGlobalCLIOptions(tt.args)
		if err != nil {
			t.Fatalf("parseGlobalCLIOptions(%v): %v", tt.args, err)
		}
		if opts.Verbosity != tt.wantVerbosity || strings.Join(rest, " ") != tt.wantRest {
			t.Fatalf("parseGlobalCLIOptions(%v) => opts=%+v rest=%v, want verbosity=%d rest=%q", tt.args, opts, rest, tt.wantVerbosity, tt.wantRest)
		}
	}
}

func TestPrintForegroundRuntimeNoticeUsesStderr(t *testing.T) {
	stdout, stderr, err := captureOutputs(func() error {
		if err := logging.Configure(logging.Config{}); err != nil {
			return err
		}
		printForegroundRuntimeNotice("gateway", "edge", cfgpkg.Config{})
		printForegroundRuntimeNotice("relay", "relay", cfgpkg.Config{})
		printForegroundRuntimeNotice("attach", "service", cfgpkg.Config{Service: cfgpkg.Service{Name: "myapi"}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout, got: %q", stdout)
	}
	for _, want := range []string{"gateway running in foreground", "relay running in foreground", "service \"myapi\" running in foreground"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q: %q", want, stderr)
		}
	}
}

func TestRuntimeLoggingConfigEnablesDetachedLogsByDefault(t *testing.T) {
	t.Setenv("TUBO_DETACHED_CHILD", "1")
	cfg := runtimeLoggingConfig(globalCLIOptions{})
	if !cfg.Runtime || cfg.Verbosity != 1 {
		t.Fatalf("unexpected detached runtime logging config: %+v", cfg)
	}
	if err := logging.Configure(cfg); err != nil {
		t.Fatal(err)
	}
	if logging.Current().Verbosity != 1 {
		t.Fatalf("logging state = %+v, want verbosity 1", logging.Current())
	}
}

func TestPrintMessagesUsesStderr(t *testing.T) {
	stdout, stderr, err := captureOutputs(func() error {
		printMessages([]string{"using remote query", "fallback observer"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("expected empty stdout, got: %q", stdout)
	}
	if !strings.Contains(stderr, "using remote query") || !strings.Contains(stderr, "fallback observer") {
		t.Fatalf("expected messages on stderr, got: %q", stderr)
	}
}

func TestGetClustersJSONAutoJoinKeepsStdoutParseable(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("CI", "")
	useTestBundleDefaults(t, true)
	stdout, stderr, err := captureOutputs(func() error { return run([]string{"get", "clusters", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected clean default stderr for json command, got: %q", stderr)
	}
	var payload struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid json: %v\n%s", err, stdout)
	}
	if len(payload.Items) == 0 || payload.Items[0].Name == "" {
		t.Fatalf("unexpected json payload: %#v", payload)
	}
}

func TestGetClustersJSONAutoJoinVerboseProgressUsesStderr(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("CI", "")
	useTestBundleDefaults(t, true)
	stdout, stderr, err := captureOutputs(func() error { return run([]string{"-v", "get", "clusters", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "Fetching default network bundle: tubo-public") {
		t.Fatalf("expected progress on stderr with -v, got: %q", stderr)
	}
	var payload struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid json: %v\n%s", err, stdout)
	}
}

func TestGetClustersJSONAutoJoinQuietSuppressesProgress(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("CI", "")
	useTestBundleDefaults(t, true)
	stdout, stderr, err := captureOutputs(func() error { return run([]string{"--quiet", "-v", "get", "clusters", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected quiet stderr, got: %q", stderr)
	}
	var payload struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(stdout), &payload); err != nil {
		t.Fatalf("stdout is not valid json: %v\n%s", err, stdout)
	}
}

func TestConnectAutoJoinsDefaultPublicNetwork(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	t.Setenv("CI", "")
	useTestBundleDefaults(t, true)
	_, err := capture(func() error { return run([]string{"connect", "myapi", "--cached-only", "--timeout", "1ms"}) })
	if err == nil {
		t.Fatal("expected connect to fail after auto-join because no service is available")
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tubo", "config.yaml")); err != nil {
		t.Fatalf("config not written: %v", err)
	}
}

func TestConnectNoInitBlocksImplicitJoin(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
	if _, err := capture(func() error { return run([]string{"connect", "myapi", "--no-init"}) }); err == nil {
		t.Fatal("expected --no-init to block implicit connect join")
	}
}

func TestSanitizeProcessName(t *testing.T) {
	if got := sanitizeProcessName("Reviewer.GPU Box"); got != "reviewer-gpu-box" {
		t.Fatalf("sanitizeProcessName = %q", got)
	}
}

func TestBuildDetachedSpec(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	spec, err := buildDetachedSpec("attach", cfgpkg.Config{Service: cfgpkg.Service{Name: "lmstudio", Target: "http://127.0.0.1:1234"}, HealthListen: "127.0.0.1:8091"}, []string{"http://127.0.0.1:1234", "--name", "lmstudio"})
	if err != nil {
		t.Fatal(err)
	}
	if spec.State.ID != "process/attach-lmstudio" {
		t.Fatalf("id = %q", spec.State.ID)
	}
	if !strings.Contains(spec.State.LogFile, filepath.Join("tubo", "logs", "attach-lmstudio.log")) {
		t.Fatalf("unexpected log path: %q", spec.State.LogFile)
	}
	if spec.HealthURL != "http://127.0.0.1:8091/healthz" {
		t.Fatalf("health url = %q", spec.HealthURL)
	}
}

func TestBuildDetachedConnectSpec(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfg := cfgpkg.Config{
		CurrentOverlay:   "manual",
		CurrentCluster:   "home",
		CurrentNamespace: "team",
		Overlays:         map[string]cfgpkg.Overlay{"manual": {}},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {Namespaces: map[string]cfgpkg.Namespace{"team": {}}},
		},
	}
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	args := []string{"service/myapi", "--config", configPath, "--timeout", "3s"}
	req, err := parseConnectCLIArgs(args)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := buildDetachedConnectSpec(req, args)
	if err != nil {
		t.Fatal(err)
	}
	if spec.State.Command != "connect" || spec.State.Service != "myapi" {
		t.Fatalf("unexpected connect state: %#v", spec.State)
	}
	if spec.State.Cluster != "home" || spec.State.Namespace != "team" {
		t.Fatalf("unexpected connect scope: %#v", spec.State)
	}
	if spec.State.Local == "" || spec.HealthURL != "http://"+spec.State.Local+"/healthz" {
		t.Fatalf("unexpected local/health: local=%q health=%q", spec.State.Local, spec.HealthURL)
	}
	if spec.State.Target != "myapi" {
		t.Fatalf("target = %q", spec.State.Target)
	}
	if got := strings.Join(spec.ChildArgs, " "); !strings.Contains(got, "connect") || !strings.Contains(got, "--local "+spec.State.Local) {
		t.Fatalf("child args missing injected local: %v", spec.ChildArgs)
	}
	if !strings.HasPrefix(spec.State.ID, "process/connect-myapi-") {
		t.Fatalf("unexpected process id: %q", spec.State.ID)
	}
}

func TestProcessStateListingAndLookup(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(processStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processRunDir(), 0700); err != nil {
		t.Fatal(err)
	}
	running := detachedProcessState{ID: "process/attach-myapi", Kind: "process", Command: "attach", Name: "attach-myapi", PID: os.Getpid(), PIDFile: filepath.Join(processRunDir(), "attach-myapi.pid"), StateFile: filepath.Join(processStateDir(), "attach-myapi.json"), LogFile: filepath.Join(processLogDir(), "attach-myapi.log")}
	stale := detachedProcessState{ID: "process/relay-default", Kind: "process", Command: "relay", Name: "relay-default", PID: 999999, PIDFile: filepath.Join(processRunDir(), "relay-default.pid"), StateFile: filepath.Join(processStateDir(), "relay-default.json"), LogFile: filepath.Join(processLogDir(), "relay-default.log")}
	_ = os.WriteFile(running.PIDFile, []byte(fmt.Sprintf("%d\n", running.PID)), 0600)
	for _, st := range []detachedProcessState{running, stale} {
		b, _ := json.Marshal(st)
		if err := os.WriteFile(st.StateFile, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	items, err := listProcessViews(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != running.ID {
		t.Fatalf("unexpected running items: %#v", items)
	}
	items, err = listProcessViews(true)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items with --all, got %#v", items)
	}
	if _, status, err := loadProcessState("attach-myapi"); err != nil || status != "running" {
		t.Fatalf("loadProcessState running err=%v status=%q", err, status)
	}
}

func TestLogsCmdAndRmStale(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(processStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processLogDir(), 0700); err != nil {
		t.Fatal(err)
	}
	state := detachedProcessState{ID: "process/attach-myapi", Kind: "process", Command: "attach", Name: "attach-myapi", PID: 999999, PIDFile: filepath.Join(processRunDir(), "attach-myapi.pid"), StateFile: filepath.Join(processStateDir(), "attach-myapi.json"), LogFile: filepath.Join(processLogDir(), "attach-myapi.log")}
	if err := os.MkdirAll(processRunDir(), 0700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(state.LogFile, []byte("line1\nline2\nline3\n"), 0600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0600)
	out, err := capture(func() error { return logsCmd([]string{"--tail", "2", state.ID}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "line2") || !strings.Contains(out, "line3") {
		t.Fatalf("unexpected logs output: %s", out)
	}
	out, err = capture(func() error { return rmCmd([]string{"--stale"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "removed 1 stale process artifacts") {
		t.Fatalf("unexpected rm output: %s", out)
	}
	if _, err := os.Stat(state.StateFile); !os.IsNotExist(err) {
		t.Fatalf("expected state file removed, stat err=%v", err)
	}
}

func TestStopCmd(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(processStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processRunDir(), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	state := detachedProcessState{ID: "process/relay-default", Kind: "process", Command: "relay", Name: "relay-default", PID: cmd.Process.Pid, PIDFile: filepath.Join(processRunDir(), "relay-default.pid"), StateFile: filepath.Join(processStateDir(), "relay-default.json"), LogFile: filepath.Join(processLogDir(), "relay-default.log")}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0600)
	out, err := capture(func() error { return stopCmd([]string{state.ID}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stopped "+state.ID) {
		t.Fatalf("unexpected stop output: %s", out)
	}
	if pidRunning(state.PID) {
		t.Fatal("expected process to stop")
	}
}

func TestStopCmdWarnsForExternallyManagedProcess(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(processStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processRunDir(), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	state := detachedProcessState{ID: "process/relay-default", Kind: "process", Command: "relay", Name: "relay-default", PID: cmd.Process.Pid, PIDFile: filepath.Join(processRunDir(), "relay-default.pid"), StateFile: filepath.Join(processStateDir(), "relay-default.json"), Source: "systemd"}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0600)
	stdout, stderr, err := captureOutputs(func() error { return stopCmd([]string{state.ID}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "stopped "+state.ID) {
		t.Fatalf("unexpected stop stdout: %s", stdout)
	}
	if !strings.Contains(stderr, "externally managed Tubo runtime") {
		t.Fatalf("expected external supervisor warning, got stderr=%s", stderr)
	}
}

func TestDescribeAndInspectProcessIncludeSourceAndConfidence(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(processStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processRunDir(), 0700); err != nil {
		t.Fatal(err)
	}
	cmdline, ok := processCommandLine(os.Getpid())
	if !ok || len(cmdline) == 0 {
		t.Fatal("expected current process cmdline")
	}
	state := detachedProcessState{ID: "process/attach-myapi", Kind: "process", Command: "attach", Name: "attach-myapi", PID: os.Getpid(), PIDFile: filepath.Join(processRunDir(), "attach-myapi.pid"), StateFile: filepath.Join(processStateDir(), "attach-myapi.json"), Source: "foreground", CommandLine: cmdline}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0600)
	out, err := capture(func() error { return describeCmd([]string{state.ID}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Source: foreground", "Status confidence: pid+cmdline", "Command line:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe output missing %q: %s", want, out)
		}
	}
	inspectOut, err := capture(func() error { return inspectCmd([]string{state.ID, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Status string               `json:"status"`
		State  detachedProcessState `json:"state"`
	}
	if err := json.Unmarshal([]byte(inspectOut), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Status != "running" || payload.State.Source != "foreground" || payload.State.StatusConfidence != "pid+cmdline" {
		t.Fatalf("unexpected inspect payload: %+v", payload)
	}
}

func TestLogsCmdShowsSystemdHintWhenNoLogFile(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	if err := os.MkdirAll(processStateDir(), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(processRunDir(), 0700); err != nil {
		t.Fatal(err)
	}
	cmdline, ok := processCommandLine(os.Getpid())
	if !ok || len(cmdline) == 0 {
		t.Fatal("expected current process cmdline")
	}
	state := detachedProcessState{ID: "process/grants-serve-lab-default", Kind: "process", Command: "grants serve", Name: "grants-serve-lab-default", PID: os.Getpid(), PIDFile: filepath.Join(processRunDir(), "grants-serve-lab-default.pid"), StateFile: filepath.Join(processStateDir(), "grants-serve-lab-default.json"), Source: "systemd", CommandLine: cmdline}
	_ = os.WriteFile(state.PIDFile, []byte(fmt.Sprintf("%d\n", state.PID)), 0600)
	b, _ := json.Marshal(state)
	_ = os.WriteFile(state.StateFile, b, 0600)
	err := logsCmd([]string{state.ID})
	if err == nil || !strings.Contains(err.Error(), "journalctl --user-unit") {
		t.Fatalf("expected journalctl hint, got err=%v", err)
	}
}

func TestRegisterCurrentProcessSkipsDetachedChild(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	t.Setenv("TUBO_DETACHED_CHILD", "1")
	state := detachedProcessState{ID: "process/relay-default", Kind: "process", Command: "relay", Name: "relay-default", StateFile: filepath.Join(processStateDir(), "relay-default.json"), PIDFile: filepath.Join(processRunDir(), "relay-default.pid")}
	registered, cleanup, err := registerCurrentProcess(state)
	if err != nil {
		t.Fatal(err)
	}
	if cleanup != nil {
		t.Fatal("expected nil cleanup for detached child")
	}
	if registered.ID != state.ID {
		t.Fatalf("unexpected registered state: %#v", registered)
	}
	if _, err := os.Stat(state.StateFile); !os.IsNotExist(err) {
		t.Fatalf("expected no state file written, stat err=%v", err)
	}
}

func TestUsageMentionsIntentCommands(t *testing.T) {
	err := usage()
	if err == nil {
		t.Fatal("expected usage error")
	}
	for _, want := range []string{"attach", "connect", "gateway", "relay", "join", "use", "share", "bundle-url"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("usage missing %q: %s", want, err)
		}
	}
}

func TestJoinCreatesConfigAndInstallsSwarmKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	swarmKey := filepath.Join(t.TempDir(), "input.swarm.key")
	if err := os.WriteFile(swarmKey, []byte("/key/swarm/psk/1.0.0/\n/base16/\n00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\n"), 0600); err != nil {
		t.Fatal(err)
	}
	relayID, err := p2p.PeerIDFromSeed("join-relay-seed")
	if err != nil {
		t.Fatal(err)
	}
	relay := fmt.Sprintf("/ip4/127.0.0.1/tcp/4001/p2p/%s", relayID)
	out, err := capture(func() error { return run([]string{"join", "--relay", relay, "--swarm-key", swarmKey}) })
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tubo", "config.yaml")
	installedKeyPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tubo", "swarm.key")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if _, err := os.Stat(installedKeyPath); err != nil {
		t.Fatalf("swarm key not written: %v", err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"current_overlay: manual", "current_cluster: home", "current_namespace: default", "overlays:", "network:"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config yaml missing %q:\n%s", want, string(raw))
		}
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentOverlay != "manual" || cfg.CurrentCluster != "home" || cfg.CurrentNamespace != "default" {
		t.Fatalf("current context = %#v", cfg)
	}
	if cfg.Network.PrivateKeyFile != installedKeyPath {
		t.Fatalf("private_key_file = %q, want %q", cfg.Network.PrivateKeyFile, installedKeyPath)
	}
	if len(cfg.Network.BootstrapPeers) != 1 || cfg.Network.BootstrapPeers[0] != relay {
		t.Fatalf("bootstrap_peers = %#v", cfg.Network.BootstrapPeers)
	}
	if len(cfg.Network.RelayPeers) != 1 || cfg.Network.RelayPeers[0] != relay {
		t.Fatalf("relay_peers = %#v", cfg.Network.RelayPeers)
	}
	if got := cfg.Overlays["manual"]; got.SwarmKeyFile != installedKeyPath {
		t.Fatalf("manual overlay = %#v", got)
	}
	if _, ok := cfg.Clusters["home"].Namespaces["default"]; !ok {
		t.Fatalf("default namespace missing: %#v", cfg.Clusters)
	}
	if !strings.Contains(out, "joined manual overlay") || !strings.Contains(out, "tubo get services") {
		t.Fatalf("unexpected join output: %s", out)
	}
	keyBytes, err := os.ReadFile(installedKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(keyBytes), "/key/swarm/psk/1.0.0/") {
		t.Fatalf("unexpected installed swarm key: %s", string(keyBytes))
	}
}

func TestJoinExplicitManualOverlayWritesOverlayFields(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	swarmKey := filepath.Join(t.TempDir(), "input.swarm.key")
	if err := os.WriteFile(swarmKey, []byte("/key/swarm/psk/1.0.0/\n/base16/\n00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\n"), 0600); err != nil {
		t.Fatal(err)
	}
	relayID, err := p2p.PeerIDFromSeed("join-explicit-manual-relay")
	if err != nil {
		t.Fatal(err)
	}
	relay := fmt.Sprintf("/ip4/127.0.0.1/tcp/4001/p2p/%s", relayID)
	out, err := capture(func() error {
		return run([]string{"join", "overlay/manual", "--relay", relay, "--swarm-key", swarmKey, "--config-dir", configDir})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "joined manual overlay") || !strings.Contains(out, "network: manual") {
		t.Fatalf("unexpected output: %s", out)
	}
	cfg, err := cfgpkg.LoadFile(filepath.Join(configDir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentOverlay != "manual" || cfg.CurrentCluster != "home" || cfg.CurrentNamespace != "default" {
		t.Fatalf("unexpected context: %#v", cfg)
	}
	if cfg.Network.PrivateKeyFile != filepath.Join(configDir, "swarm.key") {
		t.Fatalf("private_key_file = %q", cfg.Network.PrivateKeyFile)
	}
	if got := cfg.Overlays["manual"]; got.SwarmKeyFile != filepath.Join(configDir, "swarm.key") {
		t.Fatalf("manual overlay = %#v", got)
	}
}

func TestJoinJSONAndCheck(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), "config")
	relayID, err := p2p.PeerIDFromSeed("join-relay-check-seed")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	relay := fmt.Sprintf("/ip4/127.0.0.1/tcp/%d/p2p/%s", port, relayID)
	keyB64 := base64.StdEncoding.EncodeToString([]byte("/key/swarm/psk/1.0.0/\n/base16/\n00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\n"))
	out, err := capture(func() error {
		return run([]string{"join", "--relay", relay, "--swarm-key-b64", keyB64, "--config-dir", configDir, "--check", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var got joinResult
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("join json parse: %v\nout=%s", err, out)
	}
	if !got.Checked {
		t.Fatal("expected checked=true")
	}
	if got.ConfigPath != filepath.Join(configDir, "config.yaml") {
		t.Fatalf("config_path = %q", got.ConfigPath)
	}
	if len(got.RelayPeers) != 1 || got.RelayPeers[0] != relay {
		t.Fatalf("relay_peers = %#v", got.RelayPeers)
	}
}

func TestJoinRejectsInvalidInput(t *testing.T) {
	if _, err := capture(func() error { return run([]string{"join"}) }); err == nil {
		t.Fatal("expected missing relay/key error")
	}
	configDir := filepath.Join(t.TempDir(), "config")
	badKey := filepath.Join(t.TempDir(), "bad.key")
	if err := os.WriteFile(badKey, []byte("not-a-swarm-key"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error {
		return run([]string{"join", "--relay", "not-a-multiaddr", "--swarm-key", badKey, "--config-dir", configDir})
	}); err == nil {
		t.Fatal("expected invalid relay error")
	}
	if _, err := capture(func() error {
		return run([]string{"join", "--relay", "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWTest", "--swarm-key", badKey, "--config-dir", configDir})
	}); err == nil {
		t.Fatal("expected invalid swarm key error")
	}
}

func TestJoinDefaultPublicNetworkFromSignedBundle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	serverURL, trusted := testSignedBundleServer(t, true)
	oldURL := joinDefaultPublicBundleURL
	oldKeys := joinTrustedBundleSigningKey
	joinDefaultPublicBundleURL = serverURL
	joinTrustedBundleSigningKey = trusted
	defer func() {
		joinDefaultPublicBundleURL = oldURL
		joinTrustedBundleSigningKey = oldKeys
	}()

	out, err := capture(func() error { return run([]string{"join"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "joined public overlay") || !strings.Contains(out, "network: tubo-public") {
		t.Fatalf("unexpected output: %s", out)
	}
	if !strings.Contains(out, "tubo connect --token <share-invite>") || strings.Contains(out, "tubo get services") || strings.Contains(out, "tubo connect lmstudio") {
		t.Fatalf("unexpected public join next steps: %s", out)
	}
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tubo", "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config not written: %v", err)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"current_overlay: tubo-public", "current_cluster: home", "current_namespace: default", "overlays:", "network:"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config yaml missing %q:\n%s", want, string(raw))
		}
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentOverlay != "tubo-public" || cfg.CurrentCluster != "home" || cfg.CurrentNamespace != "default" {
		t.Fatalf("current context = %#v", cfg)
	}
	if cfg.Network.PrivateKeyFile == "" || len(cfg.Network.BootstrapPeers) != 1 || len(cfg.Network.RelayPeers) != 1 {
		t.Fatalf("network not materialized: %#v", cfg.Network)
	}
}

func TestJoinExplicitPublicOverlayUsesSignedBundle(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	serverURL, trusted := testSignedBundleServer(t, true)
	oldURL := joinDefaultPublicBundleURL
	oldKeys := joinTrustedBundleSigningKey
	joinDefaultPublicBundleURL = serverURL
	joinTrustedBundleSigningKey = trusted
	defer func() {
		joinDefaultPublicBundleURL = oldURL
		joinTrustedBundleSigningKey = oldKeys
	}()

	out, err := capture(func() error { return run([]string{"join", "overlay/public"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "joined public overlay") || !strings.Contains(out, "network: tubo-public") {
		t.Fatalf("unexpected output: %s", out)
	}
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tubo", "config.yaml")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"current_overlay: tubo-public", "current_cluster: home", "current_namespace: default", "overlays:", "network:"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("config yaml missing %q:\n%s", want, string(raw))
		}
	}
}

func TestJoinRejectsInvalidBundleSignature(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	serverURL, trusted := testSignedBundleServer(t, false)
	oldURL := joinDefaultPublicBundleURL
	oldKeys := joinTrustedBundleSigningKey
	joinDefaultPublicBundleURL = serverURL
	joinTrustedBundleSigningKey = trusted
	defer func() {
		joinDefaultPublicBundleURL = oldURL
		joinTrustedBundleSigningKey = oldKeys
	}()

	if _, err := capture(func() error { return run([]string{"join"}) }); err == nil {
		t.Fatal("expected invalid signature error")
	}
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tubo", "config.yaml")
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("config should not be written on invalid signature, stat err=%v", err)
	}
}

func TestJoinDefaultPublicNetworkUsesEnvOverrideBundleURL(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	serverURL, trusted := testSignedBundleServer(t, true)
	oldKeys := joinTrustedBundleSigningKey
	joinTrustedBundleSigningKey = trusted
	defer func() { joinTrustedBundleSigningKey = oldKeys }()
	t.Setenv("TUBO_DEFAULT_PUBLIC_BUNDLE_URL", serverURL)

	out, err := capture(func() error { return run([]string{"join"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "joined public overlay") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestHelpTextExplainsInviteOnlyPublicDefaultAndCollaborationPaths(t *testing.T) {
	out, err := capture(func() error { return run([]string{"help"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Public default happy path (invite-only)") || !strings.Contains(out, "tubo connect --token <share-invite>") {
		t.Fatalf("top-level help missing invite-only flow: %s", out)
	}
	if !strings.Contains(out, "Collaboration namespace flow") || !strings.Contains(out, "tubo connect myapp") {
		t.Fatalf("top-level help missing collaboration flow: %s", out)
	}
	if !strings.Contains(out, "--quiet") || !strings.Contains(out, "-v | -vv | -vvv") || !strings.Contains(out, "--log-level error|warn|info|debug|trace") {
		t.Fatalf("top-level help missing global logging flags: %s", out)
	}
	attachHelp, err := capture(func() error { return run([]string{"help", "attach"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(attachHelp, "public default") || !strings.Contains(attachHelp, "discovery-enabled custom/private namespace") {
		t.Fatalf("attach help missing scope behavior: %s", attachHelp)
	}
	connectHelp, err := capture(func() error { return run([]string{"help", "connect"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(connectHelp, "connect --token") || !strings.Contains(connectHelp, "collaboration path") || !strings.Contains(connectHelp, "--detach") || !strings.Contains(connectHelp, "tubo ps") {
		t.Fatalf("connect help missing detached/mode guidance: %s", connectHelp)
	}
}

func writeLocalResourceConfig(t *testing.T) string {
	t.Helper()
	configHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "tubo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	yaml := `role: service
current_overlay: public
current_cluster: home
current_namespace: default
overlays:
  public:
    relays:
      - /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWOverlayRelay
    bootstrap_peers:
      - /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWOverlayBootstrap
    swarm_key_file: /etc/p2p/swarm.key
  staging:
    relays: []
    bootstrap_peers: []
    swarm_key_file: ""
  remote:
    relays:
      - /ip4/203.0.113.1/tcp/4001/p2p/12D3KooWRemoteRelay
    bootstrap_peers:
      - /ip4/203.0.113.1/tcp/4001/p2p/12D3KooWRemoteBootstrap
    swarm_key_file: /etc/p2p/remote.swarm.key
clusters:
  home:
    cluster_id: home-cluster
    authority_public_key: home-pub
    capabilities:
      - discovery
    namespaces:
      default: {}
      lab: {}
  ops:
    cluster_id: ops-cluster
    authority_public_key: ops-pub
    capabilities:
      - ingress
    namespaces:
      prod: {}
network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers:
    - /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWLegacyBootstrap
  relay_peers:
    - /ip4/127.0.0.1/tcp/4001/p2p/12D3KooWLegacyRelay
service:
  name: api
  target: http://127.0.0.1:9000
`
	if err := os.WriteFile(configPath, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestLocalResourceCommandsListDescribeAndUse(t *testing.T) {
	configPath := writeLocalResourceConfig(t)

	out, err := capture(func() error { return run([]string{"get", "overlays"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"public", "staging", "*"} {
		if !strings.Contains(out, want) {
			t.Fatalf("get overlays output missing %q: %s", want, out)
		}
	}

	out, err = capture(func() error { return run([]string{"get", "clusters", "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var clusters struct {
		Count int           `json:"count"`
		Items []clusterView `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &clusters); err != nil {
		t.Fatalf("cluster json parse: %v\nout=%s", err, out)
	}
	if clusters.Count != 2 || len(clusters.Items) != 2 || clusters.Items[0].Name != "home" || !clusters.Items[0].Current {
		t.Fatalf("unexpected clusters payload: %#v", clusters)
	}

	out, err = capture(func() error { return run([]string{"get", "namespaces"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"default", "lab", "*"} {
		if !strings.Contains(out, want) {
			t.Fatalf("get namespaces output missing %q: %s", want, out)
		}
	}

	out, err = capture(func() error { return run([]string{"describe", "overlay/public"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Name: public", "Current: true", "Swarm key file: /etc/p2p/swarm.key", "Relays:", "Bootstrap peers:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe overlay output missing %q: %s", want, out)
		}
	}

	out, err = capture(func() error { return run([]string{"describe", "cluster/home"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cluster ID: home-cluster", "Authority public key: home-pub", "Namespaces:", "default (current)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe cluster output missing %q: %s", want, out)
		}
	}

	out, err = capture(func() error { return run([]string{"describe", "namespace/default"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Cluster: home", "Current namespace: true", "Current overlay: public", "Discovery: enabled", "Connect policy: namespace_members"} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe namespace output missing %q: %s", want, out)
		}
	}

	out, err = capture(func() error { return run([]string{"use", "overlay/staging"}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "updated current_overlay: staging") {
		t.Fatalf("unexpected use overlay output: %s", out)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentOverlay != "staging" {
		t.Fatalf("current_overlay = %q, want staging", cfg.CurrentOverlay)
	}
	if cfg.Network.PrivateKeyFile != "/etc/p2p/swarm.key" {
		t.Fatalf("network private_key_file changed unexpectedly: %q", cfg.Network.PrivateKeyFile)
	}

	policyCfg := cfgpkg.Config{Role: "service", CurrentOverlay: "public", CurrentCluster: "home", CurrentNamespace: "default", Overlays: map[string]cfgpkg.Overlay{"public": {Kind: cfgpkg.OverlayKindPublicBundle, PublicDefaultCluster: "home", PublicDefaultNamespace: "default"}}, Clusters: map[string]cfgpkg.Cluster{"home": {Namespaces: map[string]cfgpkg.Namespace{"default": {Discovery: cfgpkg.NamespaceDiscoveryDisabled, ConnectPolicy: cfgpkg.ConnectPolicyInviteOnly}}}}, Service: cfgpkg.Service{Name: "api", Target: "http://127.0.0.1:9000"}}
	policyPath := filepath.Join(t.TempDir(), "policy-config.yaml")
	if err := cfgpkg.WriteFile(policyPath, policyCfg, true); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"config", "validate", "--config", policyPath}) }); err != nil {
		t.Fatalf("config validate should accept valid namespace policy: %v", err)
	}
	policyCfg.Clusters["home"] = cfgpkg.Cluster{Namespaces: map[string]cfgpkg.Namespace{"default": {Discovery: "bogus"}}}
	if err := cfgpkg.WriteFile(policyPath, policyCfg, true); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"config", "validate", "--config", policyPath}) }); err == nil || !strings.Contains(err.Error(), "clusters.home.namespaces.default.discovery") {
		t.Fatalf("config validate should reject invalid namespace policy, got %v", err)
	}

	if _, err := capture(func() error { return run([]string{"use", "overlay/remote"}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentOverlay != "remote" {
		t.Fatalf("current_overlay = %q, want remote", cfg.CurrentOverlay)
	}
	if cfg.Network.PrivateKeyFile != "/etc/p2p/remote.swarm.key" || len(cfg.Network.BootstrapPeers) != 1 || len(cfg.Network.RelayPeers) != 1 {
		t.Fatalf("remote overlay not materialized in network: %#v", cfg.Network)
	}

	if _, err := capture(func() error { return run([]string{"use", "cluster/ops"}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"use", "namespace/prod"}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentCluster != "ops" || cfg.CurrentNamespace != "prod" {
		t.Fatalf("current context = %#v", cfg)
	}
	out, err = capture(func() error { return run([]string{"get", "namespaces"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"prod", "*", "ops"} {
		if !strings.Contains(out, want) {
			t.Fatalf("get namespaces after use output missing %q: %s", want, out)
		}
	}
}

func TestLocalResourceCommandsRejectLegacyConfig(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "tubo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	legacy := `role: service
network:
  private_key_file: /etc/p2p/swarm.key
service:
  name: api
  target: http://127.0.0.1:9000
`
	if err := os.WriteFile(configPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"get", "overlays"}, {"describe", "overlay/public"}, {"use", "overlay/public"}, {"get", "services"}, {"get", "service/api"}, {"describe", "service/api"}, {"inspect", "service/api"}, {"watch", "services", "--timeout", "1s"}} {
		if _, err := capture(func() error { return run(args) }); err == nil {
			t.Fatalf("expected legacy config to reject %v", args)
		}
	}
}

func TestPublicDefaultDisablesAmbientDiscoveryCommandsButNotConnectToken(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "tubo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	authorityKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))
	cfg := cfgpkg.Config{
		Role:             "service",
		CurrentOverlay:   joinDefaultNetworkName,
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Overlays: map[string]cfgpkg.Overlay{
			joinDefaultNetworkName: {
				Kind:                   cfgpkg.OverlayKindPublicBundle,
				PublicDefaultCluster:   "home",
				PublicDefaultNamespace: "default",
			},
		},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				ClusterID:          "cluster-public-2026",
				AuthorityPublicKey: authorityKey,
				MembershipGrant:    &cfgpkg.ClusterMembershipGrant{ClusterName: "home", ClusterID: "cluster-public-2026", Namespace: "default", Role: "member", ExpiresAt: time.Now().Add(time.Hour)},
				Namespaces:         map[string]cfgpkg.Namespace{"default": {Discovery: cfgpkg.NamespaceDiscoveryDisabled, ConnectPolicy: cfgpkg.ConnectPolicyInviteOnly}},
			},
		},
		Network: cfgpkg.Network{PrivateKeyFile: "/tmp/swarm.key", RelayPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWPublicRelay"}, BootstrapPeers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWPublicRelay"}},
		Service: cfgpkg.Service{Name: "api", Target: "http://127.0.0.1:9000"},
	}
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"get", "services"}, {"get", "service/api"}, {"describe", "service/api"}, {"inspect", "service/api"}, {"watch", "services", "--timeout", "1s"}, {"connect", "myapi"}} {
		_, err := capture(func() error { return run(args) })
		if err == nil || !strings.Contains(err.Error(), "tubo connect --token <invite>") {
			t.Fatalf("expected discovery-disabled guidance for %v, got %v", args, err)
		}
	}
	_, err = capture(func() error { return run([]string{"connect", "--token", "not-a-token"}) })
	if err == nil || strings.Contains(err.Error(), "tubo connect --token <invite>") {
		t.Fatalf("connect --token should not be blocked by public-default discovery policy, got %v", err)
	}
	legacyInvite, err := grantspkg.BuildServiceShareArtifacts(authorityPriv, "home", "cluster-public-2026", "default", "myapi", "service-legacy", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	_, err = capture(func() error { return run([]string{"connect", "--token", legacyInvite.Token}) })
	if err == nil || !strings.Contains(err.Error(), "missing a self-contained service endpoint") {
		t.Fatalf("expected legacy public-default token compatibility error, got %v", err)
	}
}

func extractClusterInviteToken(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, clusterInviteTokenPrefix)
	if idx < 0 {
		t.Fatalf("invite token not found in output: %s", out)
	}
	end := idx
	for end < len(out) {
		if strings.ContainsRune(" \t\r\n", rune(out[end])) {
			break
		}
		end++
	}
	token := out[idx:end]
	if !strings.HasPrefix(token, clusterInviteTokenPrefix) {
		t.Fatalf("invalid invite token extraction: %q", token)
	}
	return token
}

func extractServiceShareToken(t *testing.T, out string) string {
	t.Helper()
	idx := strings.Index(out, serviceShareTokenPrefix)
	if idx < 0 {
		t.Fatalf("service share token not found in output: %s", out)
	}
	end := idx
	for end < len(out) {
		if strings.ContainsRune(" \t\r\n", rune(out[end])) {
			break
		}
		end++
	}
	token := out[idx:end]
	if !strings.HasPrefix(token, serviceShareTokenPrefix) {
		t.Fatalf("invalid service share token extraction: %q", token)
	}
	return token
}

func extractLastServiceShareToken(t *testing.T, out string) string {
	t.Helper()
	idx := strings.LastIndex(out, serviceShareTokenPrefix)
	if idx < 0 {
		t.Fatalf("service share token not found in output: %s", out)
	}
	end := idx
	for end < len(out) {
		if strings.ContainsRune(" \t\r\n", rune(out[end])) {
			break
		}
		end++
	}
	token := out[idx:end]
	if !strings.HasPrefix(token, serviceShareTokenPrefix) {
		t.Fatalf("invalid service share token extraction: %q", token)
	}
	return token
}

func mustClusterAuthorityKey(t *testing.T, configPath string) ed25519.PrivateKey {
	t.Helper()
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Clusters) == 0 {
		t.Fatal("no clusters in config")
	}
	for _, cluster := range cfg.Clusters {
		if cluster.AuthorityPrivateKeyFile == "" {
			continue
		}
		key, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	t.Fatal("no authority private key file found")
	return nil
}

func tamperTokenPayload(t *testing.T, token, prefix string, oldBytes, newBytes []byte) string {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(token, prefix), ".")
	if len(parts) != 2 {
		t.Fatalf("unexpected token format: %s", token)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	payloadBytes = bytes.Replace(payloadBytes, oldBytes, newBytes, 1)
	return prefix + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + parts[1]
}

func assertJoinedClusterInviteConfig(t *testing.T, configPath, wantToken, wantNamespace string) {
	t.Helper()
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentCluster != "home" || cfg.CurrentNamespace != wantNamespace {
		t.Fatalf("unexpected current context: %#v", cfg)
	}
	if cfg.Network.PrivateKeyFile != "" || len(cfg.Network.BootstrapPeers) != 0 || len(cfg.Network.RelayPeers) != 0 {
		t.Fatalf("cluster invite join should not touch network config: %#v", cfg.Network)
	}
	cluster, ok := cfg.Clusters["home"]
	if !ok {
		t.Fatalf("cluster home missing after invite join: %#v", cfg.Clusters)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" {
		t.Fatalf("cluster metadata missing: %#v", cluster)
	}
	if cluster.MembershipGrant == nil {
		t.Fatalf("membership grant missing: %#v", cluster)
	}
	grant := cluster.MembershipGrant
	if grant.InviteToken != wantToken || grant.ClusterName != "home" || grant.ClusterID != cluster.ClusterID || grant.Namespace != wantNamespace || grant.Role != "member" || grant.InviteVersion != clusterInviteVersion {
		t.Fatalf("unexpected membership grant: %#v", grant)
	}
	if !stringSliceEqualSet(grant.Permissions, []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}) {
		t.Fatalf("unexpected membership grant permissions: %#v", grant.Permissions)
	}
}

func TestClusterInvitationShareAndJoin(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "namespace/observability", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}

	out, err := capture(func() error {
		return run([]string{"share", "cluster/home", "--config", configPath, "--namespace", "observability", "--permission", "member", "--expires", "2h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("share output leaked private key material: %s", out)
	}
	if !strings.Contains(out, "tubo join cluster/home --token ") {
		t.Fatalf("share output missing join command: %s", out)
	}
	token := extractClusterInviteToken(t, out)

	joinHome := filepath.Join(t.TempDir(), "join-home")
	t.Setenv("XDG_CONFIG_HOME", joinHome)
	out, err = capture(func() error { return run([]string{"join", "cluster/home", "--token", token}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "joined cluster \"home\"") {
		t.Fatalf("unexpected invite join output: %s", out)
	}
	assertJoinedClusterInviteConfig(t, filepath.Join(joinHome, "tubo", "config.yaml"), token, "observability")

	joinPositional := filepath.Join(t.TempDir(), "join-positional")
	t.Setenv("XDG_CONFIG_HOME", joinPositional)
	if _, err := capture(func() error { return run([]string{"join", token}) }); err != nil {
		t.Fatal(err)
	}
	assertJoinedClusterInviteConfig(t, filepath.Join(joinPositional, "tubo", "config.yaml"), token, "observability")
}

func TestClusterInvitationShareAndJoinJSONStayParseable(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := captureOutputs(func() error {
		return run([]string{"share", "cluster/home", "--config", configPath, "--expires", "2h", "--json", "-v"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected clean stderr for cluster share json, got: %q", stderr)
	}
	var shareResult struct {
		ClusterName string `json:"cluster_name"`
		Namespace   string `json:"namespace"`
		Permission  string `json:"permission"`
		Token       string `json:"token"`
	}
	if err := json.Unmarshal([]byte(stdout), &shareResult); err != nil {
		t.Fatalf("cluster share stdout is not valid json: %v\n%s", err, stdout)
	}
	if shareResult.ClusterName != "home" || shareResult.Token == "" {
		t.Fatalf("unexpected cluster share json: %#v", shareResult)
	}

	joinDir := filepath.Join(t.TempDir(), "join-json")
	stdout, stderr, err = captureOutputs(func() error {
		return run([]string{"join", "cluster/home", "--token", shareResult.Token, "--config-dir", joinDir, "--json", "-v"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected clean stderr for cluster join json, got: %q", stderr)
	}
	var joinResult struct {
		ConfigPath  string `json:"config_path"`
		ClusterName string `json:"cluster_name"`
		Namespace   string `json:"namespace"`
	}
	if err := json.Unmarshal([]byte(stdout), &joinResult); err != nil {
		t.Fatalf("cluster join stdout is not valid json: %v\n%s", err, stdout)
	}
	if joinResult.ClusterName != "home" || joinResult.ConfigPath == "" {
		t.Fatalf("unexpected cluster join json: %#v", joinResult)
	}
}

func TestViewerClusterInvitationShareJoinAllowsListButNotConnect(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "cluster/home", "--config", configPath, "--role", clusterInviteViewerRole, "--expires", "2h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)
	joinHome := filepath.Join(t.TempDir(), "join-viewer")
	if _, err := capture(func() error { return run([]string{"join", "cluster/home", "--token", token, "--config-dir", joinHome}) }); err != nil {
		t.Fatal(err)
	}
	joined, err := cfgpkg.LoadFile(filepath.Join(joinHome, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	grant := joined.Clusters["home"].MembershipGrant
	if grant == nil || grant.Role != clusterInviteViewerRole {
		t.Fatalf("unexpected viewer grant: %#v", grant)
	}
	if !clusterMembershipGrantAuthorizesNamespace(joined.Clusters["home"], "home", "default") {
		t.Fatal("viewer grant should authorize namespace listing")
	}
	if clusterMembershipGrantAuthorizesConnect(joined.Clusters["home"], "home", "default") {
		t.Fatal("viewer grant must not authorize connect")
	}
}

func TestGrantRequesterClusterInvitationShareJoinAndRequest(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "grant-invite-server")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	store := grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json"))
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	grantPeer := p2p.PeerAddrs(serverHost)[0]

	out, err := capture(func() error {
		return run([]string{"share", "cluster/home", "--config", configPath, "--role", "grant-requester", "--grant-peer", grantPeer, "--expires", "2h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)
	payload, err := parseAndVerifyClusterInviteToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if payload.JTI == "" || payload.Grant.Role != clusterInviteGrantRequesterRole || !stringSliceEqualSet(payload.Grant.Permissions, []string{clusterInviteGrantRequestPerm}) {
		t.Fatalf("unexpected grant-requester payload: %#v", payload)
	}
	if payload.GrantService.Protocol != grantspkg.ProtocolID || len(payload.GrantService.Peers) != 1 || payload.GrantService.Peers[0] != grantPeer {
		t.Fatalf("grant service metadata missing: %#v", payload.GrantService)
	}

	joinHome := filepath.Join(t.TempDir(), "join-grant-requester")
	if _, err := capture(func() error { return run([]string{"join", "cluster/home", "--token", token, "--config-dir", joinHome}) }); err != nil {
		t.Fatal(err)
	}
	joinedPath := filepath.Join(joinHome, "config.yaml")
	joined, err := cfgpkg.LoadFile(joinedPath)
	if err != nil {
		t.Fatal(err)
	}
	joinedCluster := joined.Clusters["home"]
	if joinedCluster.AuthorityPrivateKeyFile != "" {
		t.Fatal("grant-requester invite leaked authority private key path")
	}
	grant := joinedCluster.MembershipGrant
	if grant == nil || grant.Role != clusterInviteGrantRequesterRole || grant.InviteID != payload.JTI || grant.GrantServiceProtocol != grantspkg.ProtocolID || len(grant.GrantServicePeers) != 1 || grant.GrantServicePeers[0] != grantPeer {
		t.Fatalf("joined grant metadata missing: %#v", grant)
	}
	if clusterMembershipGrantAuthorizesNamespace(joinedCluster, "home", "default") {
		t.Fatal("grant-requester invite unexpectedly authorizes namespace publication/list rights")
	}

	out, err = capture(func() error {
		return run([]string{"grants", "request", "service/myapi", "--config", joinedPath, "--ttl", "2h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Status: pending") {
		t.Fatalf("unexpected grant request output: %s", out)
	}
	joined, err = cfgpkg.LoadFile(joinedPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := joined.Clusters["home"].Namespaces["default"].Services["myapi"]
	if svc.GrantRequestID == "" || svc.GrantServicePeer != grantPeer || svc.ServiceClaimFile == "" {
		t.Fatalf("service grant request metadata missing: %#v", svc)
	}
	if _, err := os.Stat(svc.ServiceClaimFile); !os.IsNotExist(err) {
		t.Fatalf("pending grant requester invite must not create ServiceClaim, stat err=%v", err)
	}
}

func TestClusterInviteReuseRejectedLocally(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return run([]string{"share", "cluster/home", "--config", configPath}) })
	if err != nil {
		t.Fatal(err)
	}
	token := extractClusterInviteToken(t, out)
	joinDir := t.TempDir()
	if _, err := capture(func() error { return run([]string{"join", "cluster/home", "--token", token, "--config-dir", joinDir}) }); err != nil {
		t.Fatal(err)
	}
	_, err = capture(func() error {
		return run([]string{"join", "cluster/home", "--token", token, "--config-dir", joinDir, "--force"})
	})
	if err == nil || !strings.Contains(err.Error(), "already used locally") {
		t.Fatalf("expected local invite reuse rejection, got %v", err)
	}
}

func TestGrantRequesterInviteRequiresGrantPeer(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	_, err := capture(func() error {
		return run([]string{"share", "cluster/home", "--config", configPath, "--role", "grant-requester"})
	})
	if err == nil || !strings.Contains(err.Error(), "requires --grant-peer") {
		t.Fatalf("expected grant-peer requirement, got %v", err)
	}
}

func TestClusterInviteGrantAuthorizesNamespaceQueries(t *testing.T) {
	authorityPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "pinamespace",
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				ClusterID:          "cluster-123",
				AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))),
				MembershipGrant: &cfgpkg.ClusterMembershipGrant{
					InviteToken:        "tubo-invite-v1.test",
					InviteVersion:      clusterInviteVersion,
					ClusterName:        "home",
					ClusterID:          "cluster-123",
					AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))),
					Namespace:          "pinamespace",
					Role:               clusterInviteViewerRole,
					Permissions:        []string{capability.PermissionSubscribe, capability.PermissionList},
					IssuedAt:           time.Now().Add(-time.Minute),
					ExpiresAt:          time.Now().Add(time.Hour),
				},
			},
		},
	}
	scopes, err := resolveAuthorizedServiceScopes(cfg, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0].Cluster != "home" || scopes[0].Namespace != "pinamespace" {
		t.Fatalf("unexpected scopes: %#v", scopes)
	}
}

func TestNamespaceMembershipCapabilityFilePrefersNamespaceSpecificFile(t *testing.T) {
	cluster := cfgpkg.Cluster{
		MembershipCapabilityFile: "/tmp/cluster.cap.json",
		Namespaces: map[string]cfgpkg.Namespace{
			"pinamespace": {MembershipCapabilityFile: "/tmp/namespace.cap.json"},
		},
	}
	path, err := namespaceMembershipCapabilityFile(cluster, "pinamespace")
	if err != nil {
		t.Fatal(err)
	}
	if path != "/tmp/namespace.cap.json" {
		t.Fatalf("unexpected capability file path: %s", path)
	}
}

func TestClusterInvitationRejectsExpiredAndTamperedTokens(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}

	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	priv, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := invitationGrantForPermission(clusterInviteDefaultRole)
	if err != nil {
		t.Fatal(err)
	}
	expiredPayload := clusterInvitePayload{Version: clusterInviteVersion, Kind: clusterInviteKind, JTI: "expired-jti", ClusterName: "home", ClusterID: cluster.ClusterID, AuthorityPublicKey: cluster.AuthorityPublicKey, Namespace: "default", Grant: grant, IssuedAt: time.Now().Add(-2 * time.Hour).UTC(), ExpiresAt: time.Now().Add(-time.Hour).UTC()}
	expiredPayloadBytes, err := json.Marshal(expiredPayload)
	if err != nil {
		t.Fatal(err)
	}
	expiredToken := clusterInviteTokenPrefix + base64.RawURLEncoding.EncodeToString(expiredPayloadBytes) + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, expiredPayloadBytes))
	shareOut, err := capture(func() error {
		return run([]string{"share", "cluster/home", "--config", configPath, "--expires", "2h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	validToken := extractClusterInviteToken(t, shareOut)
	tamperedToken := func(token string) string {
		parts := strings.Split(strings.TrimPrefix(token, clusterInviteTokenPrefix), ".")
		if len(parts) != 2 {
			t.Fatalf("unexpected token format: %s", token)
		}
		payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			t.Fatal(err)
		}
		payloadBytes = bytes.Replace(payloadBytes, []byte(`"cluster_name":"home"`), []byte(`"cluster_name":"evil"`), 1)
		return clusterInviteTokenPrefix + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + parts[1]
	}(validToken)

	if _, err := capture(func() error { return run([]string{"join", expiredToken}) }); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired invite error, got %v", err)
	}
	joinHome := filepath.Join(t.TempDir(), "join-expired")
	t.Setenv("XDG_CONFIG_HOME", joinHome)
	if _, err := capture(func() error { return run([]string{"join", tamperedToken}) }); err == nil || !strings.Contains(err.Error(), "invalid cluster invite signature") {
		t.Fatalf("expected tampered invite signature error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(joinHome, "tubo", "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("tampered invite should not create config, stat err=%v", err)
	}
}

func TestServiceShareTokenAndConnectSetup(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "namespace/observability", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"use", "namespace/observability", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}

	out, err := capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--cluster", "home", "--namespace", "default", "--expires", "2h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("share output leaked private key material: %s", out)
	}
	if !strings.Contains(out, "tubo connect --token ") {
		t.Fatalf("share output missing connect command: %s", out)
	}
	token := extractServiceShareToken(t, out)
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ClusterName != "home" || payload.Namespace != "default" || payload.ServiceName != "myapi" || payload.DisplayNameHint != "myapi" || payload.TargetServiceID != payload.ServiceID || payload.JTI == "" {
		t.Fatalf("unexpected service share scope: %#v", payload)
	}
	if payload.Grant.ClusterID != "" || payload.Grant.NamespaceID != "" || payload.Grant.ServiceID != "" || len(payload.Grant.Permissions) != 0 {
		t.Fatalf("expected no embedded legacy grant in service share token, got %#v", payload.Grant)
	}
	_, roguePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rogue, err := grantspkg.BuildServiceShareArtifacts(roguePriv, "home", payload.ClusterID, payload.Namespace, payload.ServiceName, payload.TargetServiceID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := importServiceShareDiscoveryContext(cfg, rogue.Payload); err == nil || !strings.Contains(err.Error(), "issuer mismatch") {
		t.Fatalf("expected issuer mismatch, got %v", err)
	}
	if connectName, serviceID, scope, err := connectServiceShareSetup("", token, "", ""); err != nil {
		t.Fatal(err)
	} else if connectName != "myapi" || serviceID != payload.TargetServiceID || scope.Cluster != "home" || scope.Namespace != "default" {
		t.Fatalf("unexpected connect setup: name=%q id=%q scope=%#v", connectName, serviceID, scope)
	}
	if _, _, _, err := connectServiceShareSetup("other", token, "", ""); err == nil || !strings.Contains(err.Error(), "service share is for") {
		t.Fatalf("expected service mismatch error, got %v", err)
	}
	if _, _, _, err := connectServiceShareSetup("", token, "other", ""); err == nil || !strings.Contains(err.Error(), "cluster") {
		t.Fatalf("expected cluster mismatch error, got %v", err)
	}
	if _, _, _, err := connectServiceShareSetup("", token, "", "other"); err == nil || !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("expected namespace mismatch error, got %v", err)
	}

	expired := payload
	expired.IssuedAt = time.Now().UTC().Add(-2 * time.Hour)
	expired.ExpiresAt = time.Now().UTC().Add(-time.Hour)
	expired.Grant.ExpiresAt = expired.ExpiresAt
	expiredToken, err := signServiceShareToken(expired, mustClusterAuthorityKey(t, configPath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAndVerifyServiceShareToken(expiredToken); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired service share error, got %v", err)
	}
	tamperedToken := tamperTokenPayload(t, token, serviceShareTokenPrefix, []byte(`"service_name":"myapi"`), []byte(`"service_name":"evil"`))
	if _, err := parseAndVerifyServiceShareToken(tamperedToken); err == nil || !strings.Contains(err.Error(), "invalid service share signature") {
		t.Fatalf("expected tampered service share error, got %v", err)
	}
	if _, err := capture(func() error { return run([]string{"share", "revoke", token, "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if err := ensureShareInviteAvailable(filepath.Dir(configPath), payload); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("expected revoked invite rejection, got %v", err)
	}
}

func configurePublicDefaultScopeForTests(t *testing.T, configPath string, relayPeers []string) cfgpkg.Config {
	t.Helper()
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CurrentOverlay = joinDefaultNetworkName
	cfg.CurrentCluster = "home"
	cfg.CurrentNamespace = "default"
	if cfg.Overlays == nil {
		cfg.Overlays = map[string]cfgpkg.Overlay{}
	}
	cfg.Overlays[joinDefaultNetworkName] = cfgpkg.Overlay{Kind: cfgpkg.OverlayKindPublicBundle, PublicDefaultCluster: "home", PublicDefaultNamespace: "default"}
	cfg.Network.RelayPeers = append([]string(nil), relayPeers...)
	cfg.Network.BootstrapPeers = append([]string(nil), relayPeers...)
	cluster := cfg.Clusters["home"]
	ns := cluster.Namespaces["default"]
	ns.Discovery = cfgpkg.NamespaceDiscoveryDisabled
	ns.ConnectPolicy = cfgpkg.ConnectPolicyInviteOnly
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestLocalShareServicePublicDefaultIncludesServiceEndpoint(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	relayPeerID, err := p2p.PeerIDFromSeed("relay-public-default-share")
	if err != nil {
		t.Fatal(err)
	}
	relayPeer := "/dns4/relay.tubo.click/tcp/4001/p2p/" + relayPeerID.String()
	cfg := configurePublicDefaultScopeForTests(t, configPath, []string{relayPeer})
	serviceSeed := cfg.Clusters["home"].Namespaces["default"].Services["myapi"].ServiceSeed
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--cluster", "home", "--namespace", "default", "--expires", "2h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseAndVerifyServiceShareToken(extractServiceShareToken(t, out))
	if err != nil {
		t.Fatal(err)
	}
	wantAddr := relayPeer + "/p2p-circuit/p2p/" + servicePeerID.String()
	if payload.ServiceEndpoint.PeerID != servicePeerID.String() || len(payload.ServiceEndpoint.Addresses) != 1 || payload.ServiceEndpoint.Addresses[0] != wantAddr {
		t.Fatalf("unexpected service endpoint payload: %#v", payload.ServiceEndpoint)
	}
}

func TestLocalShareServicePublicDefaultRejectsMissingServiceEndpoint(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	configurePublicDefaultScopeForTests(t, configPath, nil)
	_, err := capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--cluster", "home", "--namespace", "default", "--expires", "2h"})
	})
	if err == nil || !strings.Contains(err.Error(), "remote-dialable service endpoint") {
		t.Fatalf("expected missing endpoint error, got %v", err)
	}
}

func TestBuildAttachServiceShareTokenPublicDefaultRejectsMissingServiceEndpoint(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg := configurePublicDefaultScopeForTests(t, configPath, nil)
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if _, err := buildAttachServiceShareToken(cfg, cluster, "home", "default", "myapi", svc); err == nil || !strings.Contains(err.Error(), "remote-dialable service endpoint") {
		t.Fatalf("expected missing endpoint error, got %v", err)
	}
}

func writeCreateClusterConfig(t *testing.T) string {
	t.Helper()
	configHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "tubo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{
		Role:           "service",
		CurrentOverlay: "public",
		Overlays: map[string]cfgpkg.Overlay{
			"public": {},
		},
	}
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestEnsureAttachServiceIdentityCreatesReusesAndSeparates(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg.Node.Seed = "service-demo-seed"
	authz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg = authz.Config
	svc := authz.Service
	if svc.ServiceID == "" || svc.ServiceSeed == "" || svc.ServiceClaimFile == "" || svc.ServicePublishLeaseFile == "" || svc.ServiceOwnerKeyFile == "" {
		t.Fatalf("service identity incomplete: %#v", svc)
	}
	for _, path := range []string{svc.ServiceOwnerKeyFile, svc.ServiceClaimFile, svc.ServicePublishLeaseFile} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("service artifact missing %s: %v", path, err)
		}
	}
	_, deterministicSeed := serviceIdentityFor(cfg.Clusters["home"].ClusterID, "default", "myapi")
	if svc.ServiceSeed == "service-demo-seed" || svc.ServiceSeed == deterministicSeed {
		t.Fatalf("attach service seed should be generated and scoped, got %q", svc.ServiceSeed)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		t.Fatalf("service peer id from seed: %v", err)
	}
	claimBytes, err := os.ReadFile(svc.ServiceClaimFile)
	if err != nil {
		t.Fatalf("claim file missing: %v", err)
	}
	if info, err := os.Stat(svc.ServiceClaimFile); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("claim file permissions = %v err=%v, want 0600", info.Mode().Perm(), err)
	}
	var claim capability.ServiceClaim
	if err := json.Unmarshal(claimBytes, &claim); err != nil {
		t.Fatal(err)
	}
	edPub := mustClusterAuthorityKey(t, configPath).Public().(ed25519.PublicKey)
	if err := capability.VerifyServiceClaim(claim, edPub, cfg.Clusters["home"].ClusterID, "default", svc.ServiceID, servicePeerID.String()); err != nil {
		t.Fatalf("claim verification failed: %v", err)
	}

	reloaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.Service.Name = "myapi"
	reloaded.Service.Target = "http://127.0.0.1:8080"
	reusedAuthz, err := resolveAttachAuthorization(configPath, reloaded)
	if err != nil {
		t.Fatal(err)
	}
	reused := reusedAuthz.Service
	if reused != svc {
		t.Fatalf("second attach changed identity: %#v vs %#v", reused, svc)
	}

	if _, err := capture(func() error { return run([]string{"create", "namespace/observability", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	obsCfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	obsCfg.Service.Name = "myapi"
	obsCfg.Service.Target = "http://127.0.0.1:8080"
	obsCfg.CurrentNamespace = "observability"
	obsAuthz, err := resolveAttachAuthorization(configPath, obsCfg)
	if err != nil {
		t.Fatal(err)
	}
	obsSvc := obsAuthz.Service
	if obsSvc.ServiceID == svc.ServiceID || obsSvc.ServiceSeed == svc.ServiceSeed {
		t.Fatalf("same service name in different namespace reused identity: default=%#v obs=%#v", svc, obsSvc)
	}
}

func TestDeletingLocalConfigCreatesNewServiceIdentity(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg.Node.Seed = "service-demo-seed"
	firstAuthz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	firstID := firstAuthz.Service.ServiceID
	configRoot := filepath.Dir(filepath.Dir(configPath))
	if err := os.RemoveAll(configRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cfgpkg.WriteFile(configPath, cfgpkg.Config{Role: "service", CurrentOverlay: "public", Overlays: map[string]cfgpkg.Overlay{"public": {}}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg.Node.Seed = "service-demo-seed"
	secondAuthz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if secondAuthz.Service.ServiceID == firstID {
		t.Fatalf("expected new service id after deleting config, got %q", secondAuthz.Service.ServiceID)
	}
}

func TestGrantsRequestSubmitsPollsAndSavesApprovedClaim(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "grant-request-server")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	store := grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json"))
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	grantPeer := p2p.PeerAddrs(serverHost)[0]

	out, err := capture(func() error {
		return run([]string{"grants", "request", "service/myapi", "--config", configPath, "--peer", grantPeer, "--ttl", "168h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Status: pending") {
		t.Fatalf("unexpected request output: %s", out)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	svc := cfg.Clusters["home"].Namespaces["default"].Services["myapi"]
	if svc.GrantRequestID == "" || svc.GrantServicePeer != grantPeer {
		t.Fatalf("grant request metadata not saved: %#v", svc)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		t.Fatal(err)
	}
	priv := mustClusterAuthorityKey(t, configPath)
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{ClusterID: cluster.ClusterID, NamespaceID: "default", ServiceID: svc.ServiceID, SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, ExpiresAt: time.Now().Add(time.Hour)}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(svc.GrantRequestID, claim, nil, nil, ""); err != nil {
		t.Fatal(err)
	}
	out, err = capture(func() error {
		return run([]string{"grants", "request", "service/myapi", "--config", configPath, "--poll"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Service claim saved") {
		t.Fatalf("unexpected poll output: %s", out)
	}
	claimBytes, err := os.ReadFile(svc.ServiceClaimFile)
	if err != nil {
		t.Fatal(err)
	}
	var saved capability.ServiceClaim
	if err := json.Unmarshal(claimBytes, &saved); err != nil {
		t.Fatal(err)
	}
	edPub := priv.Public().(ed25519.PublicKey)
	if err := capability.VerifyServiceClaim(saved, edPub, cluster.ClusterID, "default", svc.ServiceID, servicePeerID.String()); err != nil {
		t.Fatalf("saved claim invalid: %v", err)
	}
}

func TestGrantsRequestIgnoresExpiredApprovedGrantCollision(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	authz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg = authz.Config
	svc := authz.Service
	storePath := filepath.Join(t.TempDir(), "requests.json")
	store := grantspkg.NewStore(storePath)
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	seedExpiresAt := time.Now().UTC().Add(time.Hour)
	if err := seedApprovedClaimGrant(t, store, "home", cfg.Clusters["home"], cfg.CurrentNamespace, cfg.Service.Name, svc, authorityPriv, "12D3-stale-peer", seedExpiresAt); err != nil {
		t.Fatal(err)
	}
	expireApprovedGrantRecord(t, storePath, "12D3-stale-peer", time.Now().UTC().Add(-time.Hour))
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "grant-request-expired-approved")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cfg.Clusters["home"].ClusterID, NamespaceID: cfg.CurrentNamespace, Store: store, AutoApprove: true, AuthorityPrivateKey: authorityPriv, ClaimTTL: time.Hour, ServiceShareTTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	grantPeer := p2p.PeerAddrs(serverHost)[0]

	out, err := capture(func() error {
		return run([]string{"grants", "request", "service/myapi", "--config", configPath, "--peer", grantPeer, "--ttl", "168h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Grant request approved.") {
		t.Fatalf("unexpected request output: %s", out)
	}
	freshPeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		t.Fatal(err)
	}
	all, err := store.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	var sawExpiredOld, sawApprovedFresh bool
	for _, req := range all {
		if req.ServicePeerID == "12D3-stale-peer" && req.Status == grantspkg.StatusExpired {
			sawExpiredOld = true
		}
		if req.ServicePeerID == freshPeerID.String() && req.Status == grantspkg.StatusApproved {
			sawApprovedFresh = true
		}
	}
	if !sawExpiredOld || !sawApprovedFresh {
		t.Fatalf("expected expired old grant and approved fresh grant, got %#v", all)
	}
}

func makePendingGrantRequest(t *testing.T, clusterName, clusterID, namespaceID, requesterPeerID, serviceName, servicePeerID string, requestedAt, expiresAt time.Time) grantspkg.Request {
	t.Helper()
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:             clusterID,
		NamespaceID:           namespaceID,
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       servicePeerID,
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		Nonce:                 serviceName + "-nonce",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	return grantspkg.Request{
		ClusterName:           clusterName,
		ClusterID:             clusterID,
		NamespaceID:           namespaceID,
		RequesterPeerID:       requesterPeerID,
		ServiceName:           serviceName,
		ServiceID:             serviceID,
		ServicePublicKey:      leaseReq.ServicePublicKey,
		ServiceOwnerSignature: leaseReq.ServiceOwnerSignature,
		RequestNonce:          leaseReq.Nonce,
		ServicePeerID:         servicePeerID,
		RequestedPermissions:  []string{capability.PermissionAttach, capability.PermissionAnnounce},
		RequestedTTLSeconds:   int64((7 * 24 * time.Hour).Seconds()),
		RequestedAt:           requestedAt,
		ExpiresAt:             expiresAt,
	}
}

func TestGrantsAuthorityCLIApprovesDeniesAndShowsRequests(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	storePath := filepath.Join(t.TempDir(), "requests.json")
	store := grantspkg.NewStore(storePath)
	now := time.Now().UTC()
	first, err := store.CreatePending(makePendingGrantRequest(t, "home", cluster.ClusterID, "default", "12D3-requester", "myapi", "12D3-service", now, now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.CreatePending(makePendingGrantRequest(t, "home", cluster.ClusterID, "default", "12D3-requester-2", "other", "12D3-other", now, now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}

	out, err := capture(func() error { return run([]string{"grants", "pending", "--store", storePath}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, first.ID) || !strings.Contains(out, second.ID) {
		t.Fatalf("pending output missing requests: %s", out)
	}
	out, err = capture(func() error { return run([]string{"grants", "describe", first.ID, "--store", storePath}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Service PeerID: 12D3-service") {
		t.Fatalf("describe output missing peer: %s", out)
	}
	if _, err := capture(func() error {
		return run([]string{"grants", "approve", first.ID, "--config", configPath, "--store", storePath, "--ttl", "168h"})
	}); err != nil {
		t.Fatal(err)
	}
	approved, ok, err := store.Get(first.ID)
	if err != nil || !ok || approved.Status != grantspkg.StatusApproved || approved.ServiceClaim == nil {
		t.Fatalf("approval not persisted ok=%t err=%v req=%#v", ok, err, approved)
	}
	edPub := mustClusterAuthorityKey(t, configPath).Public().(ed25519.PublicKey)
	if err := capability.VerifyServiceClaim(*approved.ServiceClaim, edPub, cluster.ClusterID, "default", approved.ServiceID, approved.ServicePeerID); err != nil {
		t.Fatalf("approved claim invalid: %v", err)
	}
	if _, err := capture(func() error {
		return run([]string{"grants", "deny", second.ID, "--store", storePath, "--reason", "no"})
	}); err != nil {
		t.Fatal(err)
	}
	denied, ok, err := store.Get(second.ID)
	if err != nil || !ok || denied.Status != grantspkg.StatusDenied || denied.ServiceClaim != nil {
		t.Fatalf("deny not persisted ok=%t err=%v req=%#v", ok, err, denied)
	}
	out, err = capture(func() error { return run([]string{"grants", "history", "--store", storePath}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, grantspkg.StatusApproved) || !strings.Contains(out, grantspkg.StatusDenied) {
		t.Fatalf("history missing statuses: %s", out)
	}
}

func TestGrantsApproveRejectsExpiredAndMissingAuthority(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	storePath := filepath.Join(t.TempDir(), "requests.json")
	store := grantspkg.NewStore(storePath)
	now := time.Now().UTC()
	expired, err := store.CreatePending(makePendingGrantRequest(t, "home", cluster.ClusterID, "default", "12D3-requester", "expired", "12D3-expired", now.Add(-2*time.Hour), now.Add(-time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error {
		return run([]string{"grants", "approve", expired.ID, "--config", configPath, "--store", storePath})
	}); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired approval error, got %v", err)
	}
	pending, err := store.CreatePending(makePendingGrantRequest(t, "home", cluster.ClusterID, "default", "12D3-requester", "myapi", "12D3-service", now, now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	cluster.AuthorityPrivateKeyFile = ""
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error {
		return run([]string{"grants", "approve", pending.ID, "--config", configPath, "--store", storePath})
	}); err == nil || !strings.Contains(err.Error(), "missing authority private key") {
		t.Fatalf("expected missing authority key error, got %v", err)
	}
}

func TestPrintAttachShareHintShowsConnectToken(t *testing.T) {
	cfg := cfgpkg.Config{CurrentOverlay: joinDefaultNetworkName, CurrentCluster: "home", CurrentNamespace: "default", Overlays: map[string]cfgpkg.Overlay{joinDefaultNetworkName: {Kind: cfgpkg.OverlayKindPublicBundle, PublicDefaultCluster: "home", PublicDefaultNamespace: "default"}}, Clusters: map[string]cfgpkg.Cluster{"home": {Namespaces: map[string]cfgpkg.Namespace{"default": {Discovery: cfgpkg.NamespaceDiscoveryDisabled, ConnectPolicy: cfgpkg.ConnectPolicyInviteOnly}}}}, Service: cfgpkg.Service{Name: "myapi"}}
	authz := attachAuthorization{Config: cfg, Service: cfgpkg.NamespaceService{ServiceID: "service-123"}, ServiceShareToken: "tubo-service-share-v1.test-token"}
	out, err := capture(func() error {
		printAttachShareHint(cfg, authz)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "visibility: unlisted") || !strings.Contains(out, "access: invite token required") || !strings.Contains(out, "tubo connect --token tubo-service-share-v1.test-token") {
		t.Fatalf("unexpected attach token output: %s", out)
	}
}

func TestServiceShareUsesDelegatedGrantServiceWhenAuthorityKeyMissing(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	svc := cluster.Namespaces["default"].Services["myapi"]
	if err := mintLocalServicePublishLease(cluster, "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "delegated-share-mint-server")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	grantPeer := p2p.PeerAddrs(serverHost)[0]
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{
		ClusterName:         "home",
		ClusterID:           cluster.ClusterID,
		NamespaceID:         "default",
		Store:               grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json")),
		AuthorityPrivateKey: authorityPriv,
		ServiceShareTTL:     time.Hour,
		GrantServicePeersProvider: func() []string {
			return []string{"/dns4/grants.tubo.test/tcp/4001/p2p/12D3KooWGrantService"}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	cfg.Network.RelayPeers = []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWQZ6qwLp7C7mdkAXMJsa2zXKoGNSXYpQNsPxpQQz4g2v3"}
	cluster.AuthorityPrivateKeyFile = ""
	svc.GrantServicePeer = grantPeer
	cluster.Namespaces["default"].Services["myapi"] = svc
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	out1, err := capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--expires", "45m"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token1 := extractServiceShareToken(t, out1)
	payload1, err := parseAndVerifyServiceShareToken(token1)
	if err != nil {
		t.Fatal(err)
	}
	if payload1.TargetServiceID != svc.ServiceID || payload1.ServiceEndpoint.PeerID == "" || len(payload1.GrantService.Peers) == 0 {
		t.Fatalf("unexpected delegated share payload: %#v", payload1)
	}
	out2, err := capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--expires", "45m"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token2 := extractServiceShareToken(t, out2)
	payload2, err := parseAndVerifyServiceShareToken(token2)
	if err != nil {
		t.Fatal(err)
	}
	if payload1.JTI == payload2.JTI {
		t.Fatalf("expected fresh JTI across delegated mint invocations, got %q", payload1.JTI)
	}
}

func TestServiceShareUsesAuthorityLocalMintWhenAuthorityKeyExists(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	svc := cluster.Namespaces["default"].Services["myapi"]
	svc.GrantServicePeer = "not-a-multiaddr"
	svc.ServiceOwnerKeyFile = ""
	cluster.Namespaces["default"].Services["myapi"] = svc
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--expires", "45m"})
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseAndVerifyServiceShareToken(extractServiceShareToken(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if payload.TargetServiceID != svc.ServiceID {
		t.Fatalf("unexpected authority-local share payload: %#v", payload)
	}
}

func TestServiceShareRenewsExpiredPublishLeaseForExistingServiceID(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CurrentCluster = "home"
	cfg.CurrentNamespace = "default"
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if err := mintLocalServicePublishLease(cluster, "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	originalServiceID := svc.ServiceID
	expirePublishLeaseFile(t, svc.ServicePublishLeaseFile, time.Now().UTC().Add(-time.Hour))
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "share-expired-lease-renew")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json")), AutoApprove: true, AuthorityPrivateKey: authorityPriv, ServiceShareTTL: time.Hour, GrantServicePeersProvider: func() []string { return []string{"/dns4/grants.tubo.test/tcp/4001/p2p/12D3KooWGrantService"} }})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	grantPeer := p2p.PeerAddrs(serverHost)[0]
	cfg.Network.RelayPeers = []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWQZ6qwLp7C7mdkAXMJsa2zXKoGNSXYpQNsPxpQQz4g2v3"}
	cluster.AuthorityPrivateKeyFile = ""
	svc.GrantServicePeer = grantPeer
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := captureOutputs(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--expires", "45m"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Grant request approved.") || strings.Contains(out, "Grant request sent.") {
		t.Fatalf("expected clean final stdout only, got: %s", out)
	}
	if strings.Contains(stderr, "grants-client p2p connected") || strings.Contains(stderr, "publish authorization refreshed") {
		t.Fatalf("expected no default diagnostics/progress on stderr, got: %s", stderr)
	}
	payload, err := parseAndVerifyServiceShareToken(extractLastServiceShareToken(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if payload.TargetServiceID != originalServiceID {
		t.Fatalf("expected renewed share token to keep same service id %q, got %#v", originalServiceID, payload)
	}
	reloaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	renewedSvc := reloaded.Clusters["home"].Namespaces["default"].Services["myapi"]
	if renewedSvc.ServiceID != originalServiceID {
		t.Fatalf("expected renewal to keep service id %q, got %#v", originalServiceID, renewedSvc)
	}
	lease, err := readPublishLeaseFile(renewedSvc.ServicePublishLeaseFile)
	if err != nil {
		t.Fatal(err)
	}
	if lease.ServiceID != originalServiceID {
		t.Fatalf("expected renewed lease for same service id %q, got %#v", originalServiceID, lease)
	}
	if !lease.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("expected renewed lease expiry in the future, got %#v", lease)
	}
}

func TestServiceShareJSONWithExpiredPublishLeaseKeepsStdoutParseable(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CurrentCluster = "home"
	cfg.CurrentNamespace = "default"
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if err := mintLocalServicePublishLease(cluster, "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	expirePublishLeaseFile(t, svc.ServicePublishLeaseFile, time.Now().UTC().Add(-time.Hour))
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "share-expired-lease-json")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json")), AutoApprove: true, AuthorityPrivateKey: authorityPriv, ServiceShareTTL: time.Hour, GrantServicePeersProvider: func() []string { return []string{"/dns4/grants.tubo.test/tcp/4001/p2p/12D3KooWGrantService"} }})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	grantPeer := p2p.PeerAddrs(serverHost)[0]
	cfg.Network.RelayPeers = []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWQZ6qwLp7C7mdkAXMJsa2zXKoGNSXYpQNsPxpQQz4g2v3"}
	cluster.AuthorityPrivateKeyFile = ""
	svc.GrantServicePeer = grantPeer
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err := captureOutputs(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--expires", "45m", "--json"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("expected clean default stderr for share json, got: %q", stderr)
	}
	var result struct {
		ClusterName string `json:"cluster_name"`
		Namespace   string `json:"namespace"`
		ServiceName string `json:"service_name"`
		ServiceID   string `json:"service_id"`
		Token       string `json:"token"`
	}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid json: %v\n%s", err, stdout)
	}
	if result.ServiceName != "myapi" || result.ServiceID == "" || !strings.HasPrefix(result.Token, serviceShareTokenPrefix) {
		t.Fatalf("unexpected json result: %#v", result)
	}
}

func TestServiceShareDebugVerbosityShowsDiagnosticsOnStderr(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CurrentCluster = "home"
	cfg.CurrentNamespace = "default"
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if err := mintLocalServicePublishLease(cluster, "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	expirePublishLeaseFile(t, svc.ServicePublishLeaseFile, time.Now().UTC().Add(-time.Hour))
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "share-expired-lease-debug")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json")), AutoApprove: true, AuthorityPrivateKey: authorityPriv, ServiceShareTTL: time.Hour, GrantServicePeersProvider: func() []string { return []string{"/dns4/grants.tubo.test/tcp/4001/p2p/12D3KooWGrantService"} }})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	grantPeer := p2p.PeerAddrs(serverHost)[0]
	cfg.Network.RelayPeers = []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWQZ6qwLp7C7mdkAXMJsa2zXKoGNSXYpQNsPxpQQz4g2v3"}
	cluster.AuthorityPrivateKeyFile = ""
	svc.GrantServicePeer = grantPeer
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	out, stderr, err := captureOutputs(func() error {
		return run([]string{"share", "-vv", "service/myapi", "--config", configPath, "--expires", "45m"})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "shared service \"myapi\"") {
		t.Fatalf("unexpected stdout: %s", out)
	}
	if !strings.Contains(stderr, "grants-client p2p connected") {
		t.Fatalf("expected diagnostics on stderr with -vv, got: %s", stderr)
	}
}

func TestServiceShareMissingPublishLeaseRequestsGrantAndSurfacesPending(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.CurrentCluster = "home"
	cfg.CurrentNamespace = "default"
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	originalServiceID := svc.ServiceID
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	cluster := cfg.Clusters["home"]
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "share-missing-lease-pending")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json")), AutoApprove: false, AuthorityPrivateKey: authorityPriv})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	grantPeer := p2p.PeerAddrs(serverHost)[0]
	cluster.AuthorityPrivateKeyFile = ""
	svc.GrantServicePeer = grantPeer
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(svc.ServicePublishLeaseFile)
	_, err = capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--expires", "45m"})
	})
	if err == nil || !strings.Contains(err.Error(), "is pending; publication requires an approved publish lease") {
		t.Fatalf("expected pending renewal guidance, got %v", err)
	}
	reloaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	pendingSvc := reloaded.Clusters["home"].Namespaces["default"].Services["myapi"]
	if pendingSvc.ServiceID != originalServiceID {
		t.Fatalf("expected pending request to keep service id %q, got %#v", originalServiceID, pendingSvc)
	}
	if strings.TrimSpace(pendingSvc.GrantRequestID) == "" {
		t.Fatalf("expected pending grant request to be saved: %#v", pendingSvc)
	}
}

func TestServiceShareDelegatedMintErrorsAreActionable(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	serviceWithPaths := svc
	clusterWithAuthority := cfg.Clusters["home"]
	cluster := clusterWithAuthority
	svc.ServicePublishLeaseFile = ""
	svc.GrantServicePeer = ""
	cluster.AuthorityPrivateKeyFile = ""
	cluster.MembershipGrant = nil
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	_, err = capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--expires", "45m"})
	})
	if err == nil || !strings.Contains(err.Error(), "service publish lease renewal requires a grant service peer or local authority key") {
		t.Fatalf("expected missing renewal path guidance, got %v", err)
	}

	cluster = clusterWithAuthority
	svc = serviceWithPaths
	if err := mintLocalServicePublishLease(cluster, "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster = cfg.Clusters["home"]
	svc = cluster.Namespaces["default"].Services["myapi"]
	svc.ServicePublishLeaseFile = serviceWithPaths.ServicePublishLeaseFile
	svc.ServiceClaimFile = serviceWithPaths.ServiceClaimFile
	cluster.AuthorityPrivateKeyFile = ""
	cluster.MembershipGrant = nil
	svc.GrantServicePeer = ""
	ns = cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	_, err = capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--expires", "45m"})
	})
	if err == nil || !strings.Contains(err.Error(), "missing grant service peer; attach or request a publish grant from an authority node first") {
		t.Fatalf("expected missing grant peer guidance, got %v", err)
	}
}

func TestServiceShareByExactServiceIDUsesRequestedScopedService(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defaultSvc := cfg.Clusters["home"].Namespaces["default"].Services["myapi"]
	if _, err := capture(func() error { return run([]string{"create", "namespace/observability", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	otherSvc := cfg.Clusters["home"].Namespaces["observability"].Services["myapi"]
	if defaultSvc.ServiceID == otherSvc.ServiceID {
		t.Fatalf("expected namespace-scoped duplicate service names to keep distinct ids: %#v %#v", defaultSvc, otherSvc)
	}
	out, err := capture(func() error {
		return run([]string{"share", "service/" + defaultSvc.ServiceID, "--config", configPath, "--cluster", "home", "--namespace", "default", "--expires", "45m"})
	})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := parseAndVerifyServiceShareToken(extractServiceShareToken(t, out))
	if err != nil {
		t.Fatal(err)
	}
	if payload.TargetServiceID != defaultSvc.ServiceID || payload.TargetServiceID == otherSvc.ServiceID {
		t.Fatalf("share by exact service id minted wrong target: %#v default=%#v other=%#v", payload, defaultSvc, otherSvc)
	}
}

func TestRequireShareTokenEndpointForPublicDefaultRejectsMissingEndpoint(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := grantspkg.BuildServiceShareArtifacts(priv, "home", "cluster-public-2026", "default", "myapi", "service-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{CurrentOverlay: joinDefaultNetworkName, CurrentCluster: "home", CurrentNamespace: "default", Overlays: map[string]cfgpkg.Overlay{joinDefaultNetworkName: {Kind: cfgpkg.OverlayKindPublicBundle, PublicDefaultCluster: "home", PublicDefaultNamespace: "default"}}, Clusters: map[string]cfgpkg.Cluster{"home": {Namespaces: map[string]cfgpkg.Namespace{"default": {Discovery: cfgpkg.NamespaceDiscoveryDisabled, ConnectPolicy: cfgpkg.ConnectPolicyInviteOnly}}}}}
	if err := requireShareTokenEndpointForPublicDefault(cfg, artifacts.Token); err == nil || !strings.Contains(err.Error(), "remote-dialable service endpoint") {
		t.Fatalf("expected missing endpoint error, got %v", err)
	}
}

func TestPrintAttachShareHintShowsRecoveryHint(t *testing.T) {
	cfg := cfgpkg.Config{CurrentOverlay: joinDefaultNetworkName, CurrentCluster: "home", CurrentNamespace: "default", Service: cfgpkg.Service{Name: "myapi"}}
	authz := attachAuthorization{Config: cfg, Service: cfgpkg.NamespaceService{ServiceID: "service-123"}, PublishLeaseReused: true, ShareRecoveryHint: "run `tubo share service/myapi --cluster home --namespace default` to mint a fresh invite token"}
	out, err := capture(func() error {
		printAttachShareHint(cfg, authz)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "publish lease: reused") || !strings.Contains(out, "tubo share service/myapi --cluster home --namespace default") {
		t.Fatalf("unexpected attach output: %s", out)
	}
}

func TestResolveAttachAuthorizationGeneratesShareTokenWithAuthorityKey(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if err := writeTestServiceClaim(t, cluster, "default", svc, time.Now().Add(time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	authz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if authz.ServiceShareToken == "" {
		t.Fatal("expected authority node to generate a service share token")
	}
	payload, err := parseAndVerifyServiceShareToken(authz.ServiceShareToken)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ClusterName != "home" || payload.Namespace != "default" || payload.ServiceName != "myapi" {
		t.Fatalf("unexpected token payload: %#v", payload)
	}
}

func TestResolveAttachAuthorizationRequestsAndUsesGrantRoute(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "grant-route-server")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	store := grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json"))
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: store})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	svc.GrantServicePeer = p2p.PeerAddrs(serverHost)[0]
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cluster.AuthorityPrivateKeyFile = ""
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}

	_, err = resolveAttachAuthorization(configPath, cfg)
	if err == nil || !strings.Contains(err.Error(), "is pending") {
		t.Fatalf("expected pending error, got %v", err)
	}
	reloaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	svc = reloaded.Clusters["home"].Namespaces["default"].Services["myapi"]
	if svc.GrantRequestID == "" {
		t.Fatal("pending grant request id was not persisted")
	}
	servicePeerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
	if err != nil {
		t.Fatal(err)
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: cluster.ClusterID, NamespaceID: "default", ServiceID: svc.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey), PublisherPeerID: servicePeerID.String(), RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "grant-route-approved"}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := grantspkg.BuildApprovalArtifacts(authorityPriv, "home", cluster.ClusterID, "default", "myapi", svc.ServiceID, servicePeerID.String(), time.Hour, time.Hour, leaseReq.RequestedCapabilities, leaseReq.ServicePublicKey, leaseReq.Nonce, leaseReq.ServiceOwnerSignature)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(svc.GrantRequestID, artifacts.ServiceClaim, &artifacts.PublishLease, &artifacts.MembershipCapability, artifacts.ServiceShareToken); err != nil {
		t.Fatal(err)
	}
	authz, err := resolveAttachAuthorization(configPath, reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if authz.ServiceClaimFile == "" || authz.ServicePeerID != servicePeerID.String() {
		t.Fatalf("unexpected approved authz: %#v", authz)
	}
}

func expirePublishLeaseFile(t *testing.T, path string, expiresAt time.Time) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var lease grantspkg.PublishLease
	if err := json.Unmarshal(b, &lease); err != nil {
		t.Fatal(err)
	}
	lease.ExpiresAt = expiresAt
	lease.ServiceClaim.ExpiresAt = expiresAt
	out, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAttachAuthorizationIgnoresExpiredServiceClaimWithValidPublishLease(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if err := mintLocalServicePublishLease(cluster, "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	if err := writeTestServiceClaim(t, cluster, "default", svc, time.Now().Add(-time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	cluster.AuthorityPrivateKeyFile = ""
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	reloaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.Service.Name = "myapi"
	reloaded.Service.Target = "http://127.0.0.1:8080"
	authz, err := resolveAttachAuthorization(configPath, reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if !authz.PublishLeaseReused {
		t.Fatalf("expected valid publish lease reuse despite expired claim: %#v", authz)
	}
}

func TestResolveAttachAuthorizationTreatsExpiredServiceClaimAsMissingWithAuthority(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if err := writeTestServiceClaim(t, cluster, "default", svc, time.Now().Add(-time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	authz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !authz.MintedServiceClaim {
		t.Fatalf("expected local renewal despite expired claim: %#v", authz)
	}
}

func TestResolveAttachAuthorizationTreatsExpiredServiceClaimAsMissingWithGrantPeer(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if err := mintLocalServicePublishLease(cluster, "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	if err := writeTestServiceClaim(t, cluster, "default", svc, time.Now().Add(-time.Hour), ""); err != nil {
		t.Fatal(err)
	}
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "expired-claim-renewal")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	store := grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json"))
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: store, AutoApprove: true, AuthorityPrivateKey: authorityPriv})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	svc.GrantServicePeer = p2p.PeerAddrs(serverHost)[0]
	cluster.AuthorityPrivateKeyFile = ""
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	expirePublishLeaseFile(t, svc.ServicePublishLeaseFile, time.Now().UTC().Add(-time.Hour))
	reloaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.Service.Name = "myapi"
	reloaded.Service.Target = "http://127.0.0.1:8080"
	cluster = reloaded.Clusters["home"]
	cluster.AuthorityPrivateKeyFile = ""
	ns = cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	reloaded.Clusters["home"] = cluster
	authz, err := resolveAttachAuthorization(configPath, reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if authz.PublishLeaseReused {
		t.Fatalf("expected grant renewal, not reused lease: %#v", authz)
	}
	if authz.ServicePublishLeaseFile == "" {
		t.Fatalf("expected renewed publish authorization: %#v", authz)
	}
}

func TestResolveAttachAuthorizationTreatsExpiredPublishLeaseAsMissing(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	if err := mintLocalServicePublishLease(cluster, "home", "default", "myapi", svc); err != nil {
		t.Fatal(err)
	}
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "expired-lease-renewal")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	store := grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json"))
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: store, AutoApprove: true, AuthorityPrivateKey: authorityPriv})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	svc.GrantServicePeer = p2p.PeerAddrs(serverHost)[0]
	cluster.AuthorityPrivateKeyFile = ""
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	expirePublishLeaseFile(t, svc.ServicePublishLeaseFile, time.Now().UTC().Add(-time.Hour))
	reloaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.Service.Name = "myapi"
	reloaded.Service.Target = "http://127.0.0.1:8080"
	cluster = reloaded.Clusters["home"]
	cluster.AuthorityPrivateKeyFile = ""
	ns = cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	reloaded.Clusters["home"] = cluster
	authz, err := resolveAttachAuthorization(configPath, reloaded)
	if err != nil {
		t.Fatal(err)
	}
	if authz.PublishLeaseReused {
		t.Fatalf("expected expired lease to be renewed, not reused: %#v", authz)
	}
	if authz.ServiceShareToken == "" {
		t.Fatal("expected renewed publish to return a share token")
	}
	if err := verifyPublishLeaseFile(authz.ServicePublishLeaseFile, authorityPriv.Public().(ed25519.PublicKey), cluster.ClusterID, "default", authz.Service.ServiceID, authz.ServicePeerID); err != nil {
		t.Fatalf("renewed lease invalid: %v", err)
	}
}

func TestResolveAttachAuthorizationRequestsGrantAndReceivesShareToken(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	relayPeerID, err := p2p.PeerIDFromSeed("relay-endpoint-seed")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Network.RelayPeers = []string{"/dns4/relay.tubo.click/tcp/4001/p2p/" + relayPeerID.String()}
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "grant-route-auto-server")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	store := grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json"))
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: store, AutoApprove: true, AuthorityPrivateKey: authorityPriv})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	svc.GrantServicePeer = p2p.PeerAddrs(serverHost)[0]
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cluster.AuthorityPrivateKeyFile = ""
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	authz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if authz.ServiceShareToken == "" {
		t.Fatal("expected approved grant to return a service share token")
	}
	payload, err := parseAndVerifyServiceShareToken(authz.ServiceShareToken)
	if err != nil {
		t.Fatal(err)
	}
	if payload.ClusterName != "home" || payload.Namespace != "default" || payload.ServiceName != "myapi" {
		t.Fatalf("unexpected token payload: %#v", payload)
	}
	if payload.ServiceEndpoint.PeerID != authz.ServicePeerID || len(payload.ServiceEndpoint.Addresses) != 1 || !strings.Contains(payload.ServiceEndpoint.Addresses[0], "/p2p-circuit/") || !strings.Contains(payload.ServiceEndpoint.Addresses[0], relayPeerID.String()) {
		t.Fatalf("expected relay-aware service endpoint in token, got %#v", payload.ServiceEndpoint)
	}
	if authz.ServiceClaimFile == "" || authz.MembershipCapabilityFile == "" || authz.ServicePublishLeaseFile == "" {
		t.Fatalf("expected approved authz to save claim, publish lease, and membership: %#v", authz)
	}
}

func TestResolveAttachAuthorizationPublicBundleGrantFallbackProducesRuntimeMembershipFile(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	authorityPriv := mustClusterAuthorityKey(t, configPath)
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "public-bundle-grant-server")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	store := grantspkg.NewStore(filepath.Join(t.TempDir(), "requests.json"))
	server, err := grantspkg.NewServer(grantspkg.ServerConfig{ClusterName: "home", ClusterID: cluster.ClusterID, NamespaceID: "default", Store: store, AutoApprove: true, AuthorityPrivateKey: authorityPriv})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	grantPeer := p2p.PeerAddrs(serverHost)[0]
	ns := cluster.Namespaces["default"]
	ns.Services["myapi"] = svc
	cluster.Namespaces["default"] = ns
	cluster.AuthorityPrivateKeyFile = ""
	cluster.MembershipCapabilityFile = ""
	cluster.MembershipGrant = &cfgpkg.ClusterMembershipGrant{
		ClusterName:          "home",
		ClusterID:            cluster.ClusterID,
		AuthorityPublicKey:   cluster.AuthorityPublicKey,
		Namespace:            "default",
		Role:                 "member",
		Permissions:          []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish},
		GrantServiceProtocol: grantspkg.ProtocolID,
		GrantServicePeers:    []string{grantPeer},
		IssuedAt:             time.Now().UTC(),
		ExpiresAt:            time.Now().UTC().Add(time.Hour),
	}
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	authz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if authz.MembershipCapabilityFile == "" {
		t.Fatalf("expected runtime membership capability file, got %#v", authz)
	}
	if _, err := os.Stat(authz.MembershipCapabilityFile); err != nil {
		t.Fatalf("membership capability file stat: %v", err)
	}
	reloaded, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Clusters["home"].Namespaces["default"].Services["myapi"].GrantServicePeer; got != grantPeer {
		t.Fatalf("GrantServicePeer=%q want %q", got, grantPeer)
	}
}

func TestImportServiceShareDiscoveryContextIgnoresAuthorizedKeyCommentDifferences(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error {
		return run([]string{"share", "service/myapi", "--config", configPath, "--cluster", "home", "--namespace", "default", "--expires", "2h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	token := extractServiceShareToken(t, out)
	payload, err := parseAndVerifyServiceShareToken(token)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	withComment := payload
	withComment.AuthorityPublicKey = payload.AuthorityPublicKey + " bettersafethansorry@tubo.click"
	imported, err := importServiceShareDiscoveryContext(cfg, withComment)
	if err != nil {
		t.Fatalf("expected issuer comment variation to be accepted, got %v", err)
	}
	if imported.Clusters["home"].AuthorityPublicKey == "" {
		t.Fatalf("expected imported cluster authority key, got %#v", imported.Clusters["home"])
	}
}

func TestResolveAttachAuthorizationRejectsMissingOrBadClaimWithoutAuthority(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(t *testing.T, cluster cfgpkg.Cluster, svc cfgpkg.NamespaceService)
		wantErr string
	}{
		{
			name:    "missing claim",
			wantErr: "no service publish grant",
		},
		{
			name: "wrong peer claim",
			mutate: func(t *testing.T, cluster cfgpkg.Cluster, svc cfgpkg.NamespaceService) {
				if err := writeTestServiceClaim(t, cluster, "default", svc, time.Now().Add(time.Hour), "12D3KooWDifferentPeer"); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "subject peer id mismatch",
		},
		{
			name: "wrong namespace claim",
			mutate: func(t *testing.T, cluster cfgpkg.Cluster, svc cfgpkg.NamespaceService) {
				priv, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
				if err != nil {
					t.Fatal(err)
				}
				peerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
				if err != nil {
					t.Fatal(err)
				}
				claim, err := capability.SignServiceClaim(capability.ServiceClaim{ClusterID: cluster.ClusterID, NamespaceID: "observability", ServiceID: svc.ServiceID, SubjectPeerID: peerID.String(), Permissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, ExpiresAt: time.Now().Add(time.Hour)}, priv)
				if err != nil {
					t.Fatal(err)
				}
				if err := writeServiceClaimFile(svc.ServiceClaimFile, claim); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "namespace id mismatch",
		},
		{
			name: "malformed claim",
			mutate: func(t *testing.T, _ cfgpkg.Cluster, svc cfgpkg.NamespaceService) {
				if err := os.WriteFile(svc.ServiceClaimFile, []byte("{not-json\n"), 0600); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "invalid character",
		},
		{
			name: "expired claim",
			mutate: func(t *testing.T, cluster cfgpkg.Cluster, svc cfgpkg.NamespaceService) {
				if err := writeTestServiceClaim(t, cluster, "default", svc, time.Now().Add(-time.Hour), ""); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "no service publish grant",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := writeCreateClusterConfig(t)
			if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
				t.Fatal(err)
			}
			cfg, err := cfgpkg.LoadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Service.Name = "myapi"
			cfg.Service.Target = "http://127.0.0.1:8080"
			cfg, svc, err := ensureAttachServiceIdentity(configPath, cfg)
			if err != nil {
				t.Fatal(err)
			}
			cluster := cfg.Clusters["home"]
			if tc.mutate != nil {
				tc.mutate(t, cluster, svc)
			}
			cluster.AuthorityPrivateKeyFile = ""
			cfg.Clusters["home"] = cluster
			if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
				t.Fatal(err)
			}
			_, err = resolveAttachAuthorization(configPath, cfg)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q error, got %v", tc.wantErr, err)
			}
			if strings.Contains(err.Error(), "missing identity metadata") {
				t.Fatalf("ambiguous old error leaked: %v", err)
			}
		})
	}
}

func seedApprovedClaimGrant(t *testing.T, store *grantspkg.Store, clusterName string, cluster cfgpkg.Cluster, namespace, serviceName string, svc cfgpkg.NamespaceService, authorityPriv ed25519.PrivateKey, requesterPeerID string, expiresAt time.Time) error {
	t.Helper()
	owner, _, err := serviceidentity.Load(svc.ServiceOwnerKeyFile)
	if err != nil {
		return err
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: cluster.ClusterID, NamespaceID: namespace, ServiceID: svc.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey), PublisherPeerID: requesterPeerID, RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: serviceName + "-nonce"}, owner.PrivateKey)
	if err != nil {
		return err
	}
	created, err := store.CreatePending(grantspkg.Request{ClusterName: clusterName, ClusterID: cluster.ClusterID, NamespaceID: namespace, RequesterPeerID: requesterPeerID, ServiceName: serviceName, ServiceID: svc.ServiceID, ServicePublicKey: leaseReq.ServicePublicKey, ServiceOwnerSignature: leaseReq.ServiceOwnerSignature, RequestNonce: leaseReq.Nonce, ServicePeerID: requesterPeerID, RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, RequestedAt: expiresAt.Add(-24 * time.Hour), ExpiresAt: expiresAt})
	if err != nil {
		return err
	}
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{ClusterID: cluster.ClusterID, NamespaceID: namespace, ServiceID: svc.ServiceID, SubjectPeerID: requesterPeerID, Permissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, ExpiresAt: expiresAt}, authorityPriv)
	if err != nil {
		return err
	}
	_, err = store.Approve(created.ID, claim, nil, nil, "")
	return err
}

func expireApprovedGrantRecord(t *testing.T, storePath, servicePeerID string, expiresAt time.Time) {
	t.Helper()
	b, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Requests []grantspkg.Request `json:"requests"`
	}
	if err := json.Unmarshal(b, &state); err != nil {
		t.Fatal(err)
	}
	for i := range state.Requests {
		if state.Requests[i].ServicePeerID != servicePeerID || state.Requests[i].Status != grantspkg.StatusApproved {
			continue
		}
		state.Requests[i].ExpiresAt = expiresAt
		if state.Requests[i].ServiceClaim != nil {
			state.Requests[i].ServiceClaim.ExpiresAt = expiresAt
		}
		if state.Requests[i].PublishLease != nil {
			state.Requests[i].PublishLease.ExpiresAt = expiresAt
			state.Requests[i].PublishLease.ServiceClaim.ExpiresAt = expiresAt
		}
	}
	out, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(storePath, append(out, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeTestServiceClaim(t *testing.T, cluster cfgpkg.Cluster, namespace string, svc cfgpkg.NamespaceService, expiresAt time.Time, subjectOverride string) error {
	t.Helper()
	priv, err := loadClusterAuthorityPrivateKey(cluster.AuthorityPrivateKeyFile)
	if err != nil {
		return err
	}
	subject := subjectOverride
	if subject == "" {
		peerID, err := p2p.PeerIDFromSeed(svc.ServiceSeed)
		if err != nil {
			return err
		}
		subject = peerID.String()
	}
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{
		ClusterID:     cluster.ClusterID,
		NamespaceID:   namespace,
		ServiceID:     svc.ServiceID,
		SubjectPeerID: subject,
		Permissions:   []string{capability.PermissionAttach, capability.PermissionAnnounce},
		ExpiresAt:     expiresAt,
	}, priv)
	if err != nil {
		return err
	}
	return writeServiceClaimFile(svc.ServiceClaimFile, claim)
}

func TestEnsureAttachServiceIdentityRejectsInvalidConfig(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	cfg := cfgpkg.Config{Role: "service", Service: cfgpkg.Service{Name: "myapi", Target: "http://127.0.0.1:8080"}}
	if _, _, err := ensureAttachServiceIdentity(configPath, cfg); err == nil || !strings.Contains(err.Error(), "no current cluster selected") {
		t.Fatalf("expected current cluster error, got %v", err)
	}

	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Service.Name = "myapi"
	cfg.Service.Target = "http://127.0.0.1:8080"
	cluster := cfg.Clusters["home"]
	namespace := cluster.Namespaces[cfg.CurrentNamespace]
	namespace.Services = map[string]cfgpkg.NamespaceService{"myapi": {ServiceID: "service-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ServiceSeed: "existing-seed"}}
	cluster.Namespaces[cfg.CurrentNamespace] = namespace
	cfg.Clusters["home"] = cluster
	if _, err := resolveAttachAuthorization(configPath, cfg); err == nil || !strings.Contains(err.Error(), "service_owner_key_file") {
		t.Fatalf("expected service owner key error, got %v", err)
	}
}

func TestCreateClusterAndNamespace(t *testing.T) {
	configPath := writeCreateClusterConfig(t)

	out, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("create output leaked private key material: %s", out)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster, ok := cfg.Clusters["home"]
	if !ok {
		t.Fatalf("cluster home not created: %#v", cfg.Clusters)
	}
	if cluster.ClusterID == "" || cluster.AuthorityPublicKey == "" {
		t.Fatalf("cluster missing identity data: %#v", cluster)
	}
	if cluster.AuthorityPrivateKeyFile == "" || cluster.MembershipCapabilityFile == "" {
		t.Fatalf("cluster missing persisted paths: %#v", cluster)
	}
	if cfg.CurrentCluster != "home" || cfg.CurrentNamespace != "default" {
		t.Fatalf("unexpected current context: %#v", cfg)
	}
	if _, err := os.Stat(cluster.AuthorityPrivateKeyFile); err != nil {
		t.Fatalf("private key file missing: %v", err)
	}
	capBytes, err := os.ReadFile(cluster.MembershipCapabilityFile)
	if err != nil {
		t.Fatalf("capability file missing: %v", err)
	}
	var membership capability.MembershipCapability
	if err := json.Unmarshal(capBytes, &membership); err != nil {
		t.Fatalf("capability json invalid: %v", err)
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(cluster.AuthorityPublicKey))
	if err != nil {
		t.Fatalf("parse authority public key: %v", err)
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		t.Fatalf("authority public key does not expose crypto key: %T", pubKey)
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		t.Fatalf("authority public key is not ed25519: %T", cryptoPub.CryptoPublicKey())
	}
	if err := capability.VerifyMembershipCapability(membership, edPub, cluster.ClusterID, "default", cluster.ClusterID); err != nil {
		t.Fatalf("membership capability verification failed: %v", err)
	}

	out, err = capture(func() error { return run([]string{"create", "namespace/observability", "--config", configPath}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "created namespace \"observability\"") {
		t.Fatalf("unexpected namespace create output: %s", out)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CurrentNamespace != "observability" {
		t.Fatalf("current_namespace = %q, want observability", cfg.CurrentNamespace)
	}
	observabilityNamespace, ok := cfg.Clusters["home"].Namespaces["observability"]
	if !ok {
		t.Fatalf("namespace not added: %#v", cfg.Clusters["home"].Namespaces)
	}
	if observabilityNamespace.MembershipCapabilityFile == "" {
		t.Fatalf("namespace membership capability not created: %#v", observabilityNamespace)
	}
	observabilityCapBytes, err := os.ReadFile(observabilityNamespace.MembershipCapabilityFile)
	if err != nil {
		t.Fatalf("namespace capability file missing: %v", err)
	}
	var observabilityMembership capability.MembershipCapability
	if err := json.Unmarshal(observabilityCapBytes, &observabilityMembership); err != nil {
		t.Fatalf("namespace capability json invalid: %v", err)
	}
	if err := capability.VerifyMembershipCapability(observabilityMembership, edPub, cluster.ClusterID, "observability", cluster.ClusterID); err != nil {
		t.Fatalf("namespace membership capability verification failed: %v", err)
	}

	if _, err := capture(func() error { return run([]string{"use", "namespace/default", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	out, err = capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PRIVATE KEY") {
		t.Fatalf("create service output leaked secret material: %s", out)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	defaultSvc, ok := cfg.Clusters["home"].Namespaces["default"].Services["myapi"]
	if !ok || defaultSvc.ServiceID == "" || defaultSvc.ServiceSeed == "" || defaultSvc.ServiceClaimFile == "" {
		t.Fatalf("default namespace service not created: %#v", cfg.Clusters["home"].Namespaces)
	}
	defaultClaimBytes, err := os.ReadFile(defaultSvc.ServiceClaimFile)
	if err != nil {
		t.Fatalf("read default service claim: %v", err)
	}
	var defaultClaim capability.ServiceClaim
	if err := json.Unmarshal(defaultClaimBytes, &defaultClaim); err != nil {
		t.Fatalf("decode default service claim: %v", err)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(defaultSvc.ServiceSeed)
	if err != nil {
		t.Fatal(err)
	}
	if err := capability.VerifyServiceClaim(defaultClaim, edPub, cluster.ClusterID, "default", defaultSvc.ServiceID, servicePeerID.String()); err != nil {
		t.Fatalf("default service claim verification failed: %v", err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Clusters["home"].Namespaces["default"].Services["myapi"]; got.ServiceID != defaultSvc.ServiceID {
		t.Fatalf("duplicate service create changed identity: %#v vs %#v", got, defaultSvc)
	}
	if _, err := capture(func() error { return run([]string{"use", "namespace/observability", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "service/myapi", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err = cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	obsSvc, ok := cfg.Clusters["home"].Namespaces["observability"].Services["myapi"]
	if !ok || obsSvc.ServiceID == "" || obsSvc.ServiceID == defaultSvc.ServiceID {
		t.Fatalf("observability service identity not distinct: default=%#v observability=%#v", defaultSvc, obsSvc)
	}

	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err == nil {
		t.Fatal("expected duplicate cluster error")
	}
	if _, err := capture(func() error { return run([]string{"create", "namespace/observability", "--config", configPath}) }); err == nil {
		t.Fatal("expected duplicate namespace error")
	}

	blankConfigHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", blankConfigHome)
	blankConfigPath := filepath.Join(blankConfigHome, "tubo", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(blankConfigPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := cfgpkg.WriteFile(blankConfigPath, cfgpkg.Config{Role: "service"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(func() error { return run([]string{"create", "namespace/ops", "--config", blankConfigPath}) }); err == nil {
		t.Fatal("expected namespace creation to require a current cluster")
	}
}

func useTestBundleDefaults(t *testing.T, validSignature bool) {
	t.Helper()
	serverURL, trusted := testSignedBundleServer(t, validSignature)
	oldURL := joinDefaultPublicBundleURL
	oldKeys := joinTrustedBundleSigningKey
	joinDefaultPublicBundleURL = serverURL
	joinTrustedBundleSigningKey = trusted
	t.Cleanup(func() {
		joinDefaultPublicBundleURL = oldURL
		joinTrustedBundleSigningKey = oldKeys
	})
}

func testSignedBundleServer(t *testing.T, validSignature bool) (string, map[string]string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"name":       "tubo-public",
		"id":         "tubo-public-v1",
		"visibility": "public",
		"relays":     []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWFAEdvKQVbtqdo435wBxoCJxXSUpjC77MEwjVHmZk31t1"},
		"swarm_key": map[string]any{
			"type":     "libp2p-pnet",
			"encoding": "text",
			"value":    "/key/swarm/psk/1.0.0/\n/base16/\n00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\n",
		},
		"network": map[string]any{
			"autorelay":          true,
			"hole_punching":      true,
			"force_reachability": "private",
		},
		"validity": map[string]any{
			"not_before": time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339),
			"not_after":  time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
		},
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, payloadBytes)
	if !validSignature {
		sig = []byte("broken-signature")
	}
	env := map[string]any{
		"kind":             "tubo.network.bundle",
		"version":          1,
		"payload_encoding": "base64url",
		"payload":          base64.RawURLEncoding.EncodeToString(payloadBytes),
		"signature": map[string]any{
			"alg":    "ed25519",
			"key_id": "tubo-root-2026",
			"value":  base64.RawURLEncoding.EncodeToString(sig),
		},
	}
	envBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(envBytes)
	}))
	t.Cleanup(server.Close)
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	trusted := map[string]string{"tubo-root-2026": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))}
	return server.URL, trusted
}

func TestServiceResourceFromEntry(t *testing.T) {
	entry := &discovery.ServiceEntry{
		ServiceName: "lmstudio",
		PeerID:      "12D3KooWTestPeer",
		Addresses: []string{
			"/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWTestPeer",
			"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWTestPeer",
		},
		TTL:        30 * time.Second,
		Registered: time.Now().Add(-5 * time.Second),
	}
	got := serviceResourceFromEntry(entry)
	if got.Name != "lmstudio" || got.Kind != "service" {
		t.Fatalf("unexpected service view: %#v", got)
	}
	if got.Path != "direct" {
		t.Fatalf("path = %q, want direct", got.Path)
	}
	if len(got.DirectAddresses) != 1 || len(got.RelayedAddresses) != 1 {
		t.Fatalf("unexpected address split: %#v", got)
	}
	if got.ExpiresInSeconds <= 0 || got.ExpiresInSeconds > 30 {
		t.Fatalf("unexpected expires_in_seconds: %d", got.ExpiresInSeconds)
	}
}

func TestFetchLocalServiceCache(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/services" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(servicesAdminResponse{Count: 1, Items: []serviceResource{{Kind: "service", Name: "myapi", Status: "online", Path: "relayed", PeerID: "12D3KooWTestPeer"}}})
	}))
	defer ts.Close()
	cfg := cfgpkg.Config{Edge: cfgpkg.Edge{AdminListen: strings.TrimPrefix(ts.URL, "http://")}}
	items, addr, err := fetchLocalServiceCache(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if addr == "" {
		t.Fatal("expected admin addr")
	}
	if len(items) != 1 || items[0].Name != "myapi" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestDiscoverServicesUsesRemoteQueryBeforeLiveObserver(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "swarm.key")
	keyData, err := newSwarmKeyData()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		t.Fatal(err)
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	server, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "remote-query-server", psk)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	if err := cache.Add(server.ID(), "myapi", []string{p2p.PeerAddrs(server)[0]}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	server.SetStreamHandler(discoveryquery.ProtocolID, discoveryquery.HandleStream(server, "relay", cache))
	authorityPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	membershipPath := filepath.Join(t.TempDir(), "membership.cap.json")
	if err := os.WriteFile(membershipPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "observability",
		Network:          cfgpkg.Network{PrivateKeyFile: keyPath, BootstrapPeers: []string{p2p.PeerAddrs(server)[0]}},
		Edge:             cfgpkg.Edge{AdminListen: "127.0.0.1:1"},
		Clusters:         map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: membershipPath}},
	}
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	result, err := discoverServices(configPath, 5*time.Second, false, false, serviceScope{Cluster: "home", Namespace: "observability"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "remote-query" {
		t.Fatalf("mode = %q, want remote-query", result.Mode)
	}
	if result.Metadata == nil || result.Metadata.ServedByRole != "relay" {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if result.Scope.Cluster != "home" || result.Scope.Namespace != "observability" {
		t.Fatalf("unexpected scope: %#v", result.Scope)
	}
	if len(result.Services) != 1 || result.Services[0].Name != "myapi" || result.Services[0].Namespace != "observability" {
		t.Fatalf("unexpected services: %#v", result.Services)
	}
	joined := strings.Join(result.Messages, "\n")
	if !strings.Contains(joined, "querying discovery cache from relay") || !strings.Contains(joined, "received 1 services") {
		t.Fatalf("unexpected messages: %s", joined)
	}
}

func TestDiscoverServiceUsesRemoteQueryBeforeLiveObserver(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "swarm.key")
	keyData, err := newSwarmKeyData()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		t.Fatal(err)
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	server, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "remote-query-service-server", psk)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	cache := discovery.NewCache(30*time.Second, time.Second)
	defer cache.Stop()
	if err := cache.Add(server.ID(), "myapi", []string{p2p.PeerAddrs(server)[0]}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	server.SetStreamHandler(discoveryquery.ProtocolID, discoveryquery.HandleStream(server, "relay", cache))
	authorityPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	membershipPath := filepath.Join(t.TempDir(), "membership.cap.json")
	if err := os.WriteFile(membershipPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "observability",
		Network:          cfgpkg.Network{PrivateKeyFile: keyPath, BootstrapPeers: []string{p2p.PeerAddrs(server)[0]}},
		Edge:             cfgpkg.Edge{AdminListen: "127.0.0.1:1"},
		Clusters:         map[string]cfgpkg.Cluster{"home": {ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: membershipPath}},
	}
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}
	result, service, err := discoverService(configPath, "myapi", 5*time.Second, false, false, serviceScope{Cluster: "home", Namespace: "observability"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "remote-query" {
		t.Fatalf("mode = %q, want remote-query", result.Mode)
	}
	if result.Metadata == nil || result.Metadata.ServedByRole != "relay" {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if result.Scope.Cluster != "home" || result.Scope.Namespace != "observability" || service.Namespace != "observability" {
		t.Fatalf("unexpected scope/service: %#v %#v", result.Scope, service)
	}
	if service.Name != "myapi" {
		t.Fatalf("unexpected service: %#v", service)
	}
	joined := strings.Join(result.Messages, "\n")
	if !strings.Contains(joined, "querying discovery cache from relay") || !strings.Contains(joined, "received service myapi") {
		t.Fatalf("unexpected messages: %s", joined)
	}
}

func newDuplicateServiceDiscoveryFixture(t *testing.T) (cfgpkg.Config, serviceScope, string, string) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), "swarm.key")
	keyData, err := newSwarmKeyData()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		t.Fatal(err)
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	server, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "duplicate-service-server", psk)
	if err != nil {
		t.Fatal(err)
	}
	cache := discovery.NewCache(30*time.Second, time.Second)
	t.Cleanup(cache.Stop)
	t.Cleanup(func() { _ = server.Close() })
	addr := p2p.PeerAddrs(server)[0]
	pubA, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceIDA := serviceidentity.ServiceIDFromPublicKey(pubA)
	serviceIDB := serviceidentity.ServiceIDFromPublicKey(pubB)
	if err := cache.AddV2(server.ID(), serviceIDA, "myapi", "http", serviceidentity.EncodePublicKey(pubA), "", nil, []string{addr}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := cache.AddV2(server.ID(), serviceIDB, "myapi", "http", serviceidentity.EncodePublicKey(pubB), "", nil, []string{addr}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	server.SetStreamHandler(discoveryquery.ProtocolID, discoveryquery.HandleStream(server, "relay", cache))
	authorityPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	membershipPath := filepath.Join(t.TempDir(), "membership.cap.json")
	if err := os.WriteFile(membershipPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "observability",
		Network:          cfgpkg.Network{PrivateKeyFile: keyPath, BootstrapPeers: []string{addr}},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: membershipPath},
		},
	}
	return cfg, serviceScope{Cluster: "home", Namespace: "observability"}, serviceIDA, serviceIDB
}

func TestDiscoverServiceRejectsDuplicateDisplayNames(t *testing.T) {
	cfg, scope, serviceIDA, serviceIDB := newDuplicateServiceDiscoveryFixture(t)
	_, _, err := discoverServiceWithConfig(cfg, 5*time.Second, false, false, scope, "myapi")
	if err == nil {
		t.Fatal("expected duplicate display name error")
	}
	if !isAmbiguousServiceError(err) {
		t.Fatalf("expected ambiguous service error, got %v", err)
	}
	for _, want := range []string{"tubo connect service/" + serviceIDA, "tubo connect service/" + serviceIDB} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ambiguous error missing %q: %v", want, err)
		}
	}
}

func TestDiscoverServiceExactByServiceIDReturnsMatchingDuplicate(t *testing.T) {
	cfg, scope, _, serviceIDB := newDuplicateServiceDiscoveryFixture(t)
	result, service, err := discoverServiceExactWithConfig(cfg, 5*time.Second, false, false, scope, "", serviceIDB)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "remote-query" {
		t.Fatalf("mode = %q, want remote-query", result.Mode)
	}
	if service.ServiceID != serviceIDB || service.Name != "myapi" {
		t.Fatalf("unexpected exact service: %#v", service)
	}
	joined := strings.Join(result.Messages, "\n")
	if !strings.Contains(joined, "received service myapi") {
		t.Fatalf("exact lookup missing service hint: %s", joined)
	}
}

func TestDiscoverServiceExactFallsBackToDisplayNameWhenServiceIDMissing(t *testing.T) {
	keyPath := filepath.Join(t.TempDir(), "swarm.key")
	keyData, err := newSwarmKeyData()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyData, 0600); err != nil {
		t.Fatal(err)
	}
	psk, _, err := p2p.LoadPrivateNetworkPSK(keyPath, "")
	if err != nil {
		t.Fatal(err)
	}
	server, err := p2p.NewHostWithSeedAndPSK("/ip4/127.0.0.1/tcp/0", "fallback-service-server", psk)
	if err != nil {
		t.Fatal(err)
	}
	cache := discovery.NewCache(30*time.Second, time.Second)
	t.Cleanup(cache.Stop)
	t.Cleanup(func() { _ = server.Close() })
	addr := p2p.PeerAddrs(server)[0]
	if err := cache.Add(server.ID(), "myapi", []string{addr}, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	server.SetStreamHandler(discoveryquery.ProtocolID, discoveryquery.HandleStream(server, "relay", cache))
	authorityPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	membershipPath := filepath.Join(t.TempDir(), "membership.cap.json")
	if err := os.WriteFile(membershipPath, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "observability",
		Network:          cfgpkg.Network{PrivateKeyFile: keyPath, BootstrapPeers: []string{addr}},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {ClusterID: "cluster-123", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: membershipPath},
		},
	}
	result, service, err := discoverServiceExactWithConfig(cfg, 5*time.Second, false, false, serviceScope{Cluster: "home", Namespace: "observability"}, "myapi", "service-fallback")
	if err != nil {
		t.Fatal(err)
	}
	if service.Name != "myapi" || service.ServiceID != "" {
		t.Fatalf("unexpected fallback service: %#v", service)
	}
	if result.Mode != "remote-query" && result.Mode != "cache" && result.Mode != "live" {
		t.Fatalf("unexpected fallback mode: %q", result.Mode)
	}
}

func TestResolveLocalServiceForShareMatchesServiceID(t *testing.T) {
	pubA, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceIDA := serviceidentity.ServiceIDFromPublicKey(pubA)
	serviceIDB := serviceidentity.ServiceIDFromPublicKey(pubB)
	svc, name, ok := resolveLocalServiceForShare(map[string]cfgpkg.NamespaceService{
		"lmstudio": {ServiceID: serviceIDA},
		"ollama":   {ServiceID: serviceIDB},
	}, serviceIDB)
	if !ok || name != "ollama" || svc.ServiceID != serviceIDB {
		t.Fatalf("unexpected share resolution: ok=%t name=%q svc=%#v", ok, name, svc)
	}
}

func TestPrintServicesTableIncludesServiceMetadata(t *testing.T) {
	out, err := capture(func() error {
		printServicesTable([]serviceResource{{Name: "lmstudio", ServiceID: "service-a", Cluster: "home", Namespace: "default", Status: "online", Path: "direct", PeerID: "12D3-peer", Capabilities: []string{"connect"}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SERVICE ID", "SCOPE", "ACCESS", "service-a", "home/default", "unknown"} {
		if !strings.Contains(out, want) {
			t.Fatalf("services table missing %q: %s", want, out)
		}
	}
}

func TestPrintProcessesTableIncludesServiceMetadata(t *testing.T) {
	out, err := capture(func() error {
		printProcessesTable([]processView{{Name: "attach-lmstudio", Command: "attach", ServiceID: "service-a", Cluster: "home", Namespace: "default", Status: "running", PID: 1234, Local: "127.0.0.1:51234", Target: "http://127.0.0.1:1234"}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SERVICE ID", "SCOPE", "service-a", "home/default"} {
		if !strings.Contains(out, want) {
			t.Fatalf("process table missing %q: %s", want, out)
		}
	}
}

func TestGrantsHistoryIncludesServiceMetadataAndSource(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "requests.json")
	store := grantspkg.NewStore(storePath)
	now := time.Now().UTC()
	reqB := grantspkg.Request{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", RequesterPeerID: "12D3-requester", ServiceName: "myapi", ServiceID: "service-b", ServicePublicKey: "pk-b", ServiceOwnerSignature: []byte("sig-b"), RequestNonce: "nonce-b", ServicePeerID: "12D3-service-b", RequestedPermissions: []string{capability.PermissionAttach}, RequestedAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(time.Hour)}
	reqA := grantspkg.Request{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", RequesterPeerID: "12D3-requester", ServiceName: "myapi", ServiceID: "service-a", ServicePublicKey: "pk-a", ServiceOwnerSignature: []byte("sig-a"), RequestNonce: "nonce-a", ServicePeerID: "12D3-service-a", RequestedPermissions: []string{capability.PermissionAttach}, RequestedAt: now, ExpiresAt: now.Add(time.Hour)}
	if _, err := store.CreatePending(reqB); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreatePending(reqA); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return grantsHistoryCmd([]string{"--store", storePath}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"history source: authority/local store", "SERVICE_ID", "SCOPE", "service-a", "service-b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("grants history missing %q: %s", want, out)
		}
	}
	if strings.Index(out, "service-a") > strings.Index(out, "service-b") {
		t.Fatalf("expected service-a before service-b: %s", out)
	}
}

func TestChooseConnectLocal(t *testing.T) {
	listen, url, err := chooseConnectLocal("127.0.0.1:51234")
	if err != nil {
		t.Fatal(err)
	}
	if listen != "127.0.0.1:51234" || url != "http://127.0.0.1:51234" {
		t.Fatalf("unexpected explicit local result: %q %q", listen, url)
	}
	listen, url, err = chooseConnectLocal("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(listen, "127.0.0.1:") || !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("unexpected auto local result: %q %q", listen, url)
	}
}

func TestParseServiceRef(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{name: "bare", in: "grafana", want: "grafana"},
		{name: "scoped", in: "service/grafana", want: "grafana"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseServiceRef(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("parseServiceRef(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestResolveServiceScope(t *testing.T) {
	cfg := cfgpkg.Config{CurrentCluster: "home", CurrentNamespace: "observability"}
	scope, err := resolveServiceScope(cfg, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Cluster != "home" || scope.Namespace != "observability" || scope.AllNamespaces {
		t.Fatalf("unexpected default scope: %#v", scope)
	}
	scope, err = resolveServiceScope(cfg, "ops", "metrics", false)
	if err != nil {
		t.Fatal(err)
	}
	if scope.Cluster != "ops" || scope.Namespace != "metrics" {
		t.Fatalf("unexpected override scope: %#v", scope)
	}
	scope, err = resolveServiceScope(cfg, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if !scope.AllNamespaces || scope.Namespace != "" {
		t.Fatalf("unexpected all-namespaces scope: %#v", scope)
	}
	if _, err := resolveServiceScope(cfgpkg.Config{}, "", "metrics", false); err == nil {
		t.Fatal("expected missing cluster context error")
	}
	if _, err := resolveServiceScope(cfg, "", "observability", true); err == nil {
		t.Fatal("expected all-namespaces conflict error")
	}
}

func TestResolveAuthorizedServiceScopes(t *testing.T) {
	pubKey, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ssh.NewPublicKey(pubKey)
	if err != nil {
		t.Fatal(err)
	}
	authorityKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	clusterID := "cluster-123"
	defaultCap := mustWriteMembershipCapability(t, priv, capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   "default",
		SubjectPeerID: clusterID,
		Permissions:   []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	})
	metricsCap := mustWriteMembershipCapability(t, priv, capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   "metrics",
		SubjectPeerID: clusterID,
		Permissions:   []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect},
		ExpiresAt:     time.Now().Add(time.Hour),
	})
	cfg := cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				ClusterID:                clusterID,
				AuthorityPublicKey:       authorityKey,
				MembershipCapabilityFile: defaultCap,
				Namespaces: map[string]cfgpkg.Namespace{
					"default": {MembershipCapabilityFile: defaultCap},
					"metrics": {MembershipCapabilityFile: metricsCap},
				},
			},
		},
	}
	scopes, err := resolveAuthorizedServiceScopes(cfg, "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 1 || scopes[0].Namespace != "default" {
		t.Fatalf("unexpected current namespace scopes: %#v", scopes)
	}
	scopes, err = resolveAuthorizedServiceScopes(cfg, "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes) != 2 || scopes[0].Namespace != "default" || scopes[1].Namespace != "metrics" {
		t.Fatalf("unexpected all-namespaces scopes: %#v", scopes)
	}
	cfg.Clusters["home"] = cfgpkg.Cluster{
		ClusterID:                clusterID,
		AuthorityPublicKey:       authorityKey,
		MembershipCapabilityFile: defaultCap,
		Namespaces: map[string]cfgpkg.Namespace{
			"default": {MembershipCapabilityFile: defaultCap},
			"metrics": {},
		},
	}
	if _, err := resolveAuthorizedServiceScopes(cfg, "", "", true); err == nil {
		t.Fatal("expected all-namespaces denial for missing namespace capability")
	}
	if _, err := resolveAuthorizedServiceScopes(cfgpkg.Config{}, "", "", false); err == nil {
		t.Fatal("expected cluster discovery requirement error")
	}
}

func mustWriteMembershipCapability(t *testing.T, priv ed25519.PrivateKey, cap capability.MembershipCapability) string {
	t.Helper()
	signed, err := capability.SignMembershipCapability(cap, priv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), fmt.Sprintf("%s-%s.cap.json", cap.ClusterID, cap.NamespaceID))
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRewriteAttachArgsAcceptsScopedServiceRef(t *testing.T) {
	args, err := rewriteAttachArgs([]string{"service/grafana", "--port", "3000"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{"--target http://127.0.0.1:3000", "--name grafana"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("rewritten attach args missing %q: %v", want, args)
		}
	}
}

func TestConnectCandidatesPreferDirectThenRelay(t *testing.T) {
	service := serviceResource{Name: "myapi", Addresses: []string{
		"/ip4/5.6.7.8/tcp/4001/p2p/target",
		"/ip4/1.2.3.4/tcp/4001/p2p/relay/p2p-circuit/p2p/target",
	}}
	candidates, err := connectCandidates(service)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d", len(candidates))
	}
	if candidates[0].Path != "direct" || strings.Contains(candidates[0].Addr, "/p2p-circuit") {
		t.Fatalf("first candidate = %#v, want direct", candidates[0])
	}
	if candidates[1].Path != "relayed" || !strings.Contains(candidates[1].Addr, "/p2p-circuit") {
		t.Fatalf("second candidate = %#v, want relayed", candidates[1])
	}
}

func TestDoctorWarnsWhenCurrentNamespaceLacksConnectPermission(t *testing.T) {
	configPath := writeCreateClusterConfig(t)
	if _, err := capture(func() error { return run([]string{"create", "cluster/home", "--config", configPath}) }); err != nil {
		t.Fatal(err)
	}
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cluster := cfg.Clusters["home"]
	cap, err := loadMembershipCapability(cluster.MembershipCapabilityFile)
	if err != nil {
		t.Fatal(err)
	}
	cap.Permissions = []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}
	b, err := json.MarshalIndent(cap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cluster.MembershipCapabilityFile, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return run([]string{"doctor", "--config", configPath}) })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "lacks connect permission") {
		t.Fatalf("expected doctor warning, got: %s", out)
	}
}

func TestConnectStatusMessages(t *testing.T) {
	service := normalizeServiceResource(serviceResource{Name: "myapi", Addresses: []string{
		"/ip4/5.6.7.8/tcp/4001/p2p/target",
		"/ip4/1.2.3.4/tcp/4001/p2p/relay/p2p-circuit/p2p/target",
	}})
	if got := connectDirectMessage(service, []connectAttempt{{Path: "direct", Addr: service.DirectAddresses[0], Status: "selected"}}, "direct"); got != "selected" {
		t.Fatalf("direct selected message = %q", got)
	}
	if got := connectRelayMessage(service, service.DirectAddresses[0], "direct"); got != "available as fallback" {
		t.Fatalf("relay fallback message = %q", got)
	}
	if got := connectDirectMessage(service, []connectAttempt{{Path: "direct", Addr: service.DirectAddresses[0], Status: "failed", Error: "timeout"}, {Path: "relayed", Addr: service.RelayedAddresses[0], Status: "selected"}}, "relayed"); got != "attempted, failed; relay selected and hole punching may still upgrade later" {
		t.Fatalf("direct fallback message = %q", got)
	}
	relayOnly := normalizeServiceResource(serviceResource{Name: "myapi", Addresses: []string{service.RelayedAddresses[0]}})
	if got := connectDirectMessage(relayOnly, []connectAttempt{{Path: "relayed", Addr: relayOnly.RelayedAddresses[0], Status: "selected"}}, "relayed"); got != "unavailable, no direct addresses advertised" {
		t.Fatalf("relay-only direct message = %q", got)
	}
}

func TestPrintServiceDescriptionShowsAddressClasses(t *testing.T) {
	out, err := capture(func() error {
		printServiceDescription(serviceResource{Name: "myapi", Kind: "service", Status: "online", ConnectPolicy: "namespace_members", GrantService: &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWGrant"}}, PeerID: "12D3KooWTestPeer", Addresses: []string{
			"/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWTestPeer",
			"/ip4/1.2.3.4/tcp/4001/p2p/relay/p2p-circuit/p2p/12D3KooWTestPeer",
		}}, []string{"starting temporary observer for 5s..."})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Connect policy: namespace_members", "Grant service:", "Protocol: /tubo/grants/1.0", "Path: direct", "Dial policy:", "preferred: direct", "fallback: relay", "Addresses:", "  Direct:", "  Relayed:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("description missing %q: %s", want, out)
		}
	}
}

func TestRequireService(t *testing.T) {
	service, err := requireService([]serviceResource{{Name: "myapi"}}, "myapi")
	if err != nil {
		t.Fatal(err)
	}
	if service.Name != "myapi" {
		t.Fatalf("unexpected service: %#v", service)
	}
	if _, err := requireService([]serviceResource{{Name: "myapi"}}, "missing"); err == nil {
		t.Fatal("expected missing service error")
	}
}

func TestVersionCommand(t *testing.T) {
	oldProduct := iversion.ProductVersion
	oldCommit := iversion.Commit
	oldBuildDate := iversion.BuildDate
	iversion.ProductVersion = "v9.9.9"
	iversion.Commit = "abc123"
	iversion.BuildDate = "2026-05-01T00:00:00Z"
	defer func() {
		iversion.ProductVersion = oldProduct
		iversion.Commit = oldCommit
		iversion.BuildDate = oldBuildDate
	}()

	out, err := capture(func() error { return run([]string{"version"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tubo v9.9.9", "protocol 1.1", "commit abc123", "build_date 2026-05-01T00:00:00Z"} {
		if !strings.Contains(out, want) {
			t.Fatalf("version output missing %q: %s", want, out)
		}
	}

	shortOut, err := capture(func() error { return run([]string{"version", "--short"}) })
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(shortOut) != "v9.9.9" {
		t.Fatalf("short version=%q", shortOut)
	}
}
