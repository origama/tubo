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
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	iversion "github.com/origama/tubo/internal/version"
	"golang.org/x/crypto/ssh"
)

func capture(f func() error) (string, error) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	err := f()
	_ = w.Close()
	os.Stdout = old
	var b bytes.Buffer
	_, _ = io.Copy(&b, r)
	return b.String(), err
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
		{name: "attach shorthand name and port", in: []string{"attach", "dummysvc", "--port", "8080"}, wantRole: "service", wantArgs: []string{"--target", "http://127.0.0.1:8080", "--name", "dummysvc"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRole, gotArgs, ok, err := resolveRuntimeRole(tc.in)
			if err != nil {
				t.Fatalf("resolveRuntimeRole(%v) err = %v", tc.in, err)
			}
			if !ok {
				t.Fatalf("resolveRuntimeRole(%v) did not resolve a runtime role", tc.in)
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
		if _, _, _, err := resolveRuntimeRole(args); err == nil {
			t.Fatalf("expected legacy command rejection for %v", args)
		}
	}
}

func TestResolveRuntimeRoleRejectsDuplicateAttachTarget(t *testing.T) {
	if _, _, _, err := resolveRuntimeRole([]string{"attach", "http://127.0.0.1:1234", "--target", "http://127.0.0.1:11434", "--name", "lmstudio"}); err == nil {
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
		if _, _, _, err := resolveRuntimeRole(args); err == nil {
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
			out, err := capture(func() error { return ensureJoinedPublicNetwork(command, false) })
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"No Tubo network configured.", "Fetching default network bundle: tubo-public", "Signature verified: tubo-root-2026", "Joined network: tubo-public"} {
				if !strings.Contains(out, want) {
					t.Fatalf("output missing %q for %s: %s", want, command, out)
				}
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
	for _, want := range []string{"Cluster: home", "Current namespace: true", "Current overlay: public"} {
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
					Role:               clusterInviteDefaultRole,
					Permissions:        []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish},
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

	expiredOut, err := capture(func() error {
		return run([]string{"share", "cluster/home", "--config", configPath, "--expires", "-1h"})
	})
	if err != nil {
		t.Fatal(err)
	}
	expiredToken := extractClusterInviteToken(t, expiredOut)
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
	}(expiredToken)

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
	if payload.ClusterName != "home" || payload.Namespace != "default" || payload.ServiceName != "myapi" {
		t.Fatalf("unexpected service share scope: %#v", payload)
	}
	if payload.Grant.ClusterID != payload.ClusterID || payload.Grant.NamespaceID != payload.NamespaceID || payload.Grant.ServiceID != payload.ServiceID {
		t.Fatalf("grant scope mismatch: %#v", payload.Grant)
	}
	if len(payload.Grant.Permissions) != 1 || payload.Grant.Permissions[0] != capability.PermissionConnect {
		t.Fatalf("service share is not connect-only: %#v", payload.Grant.Permissions)
	}
	if connectName, scope, err := connectServiceShareSetup("", token, "", ""); err != nil {
		t.Fatal(err)
	} else if connectName != "myapi" || scope.Cluster != "home" || scope.Namespace != "default" {
		t.Fatalf("unexpected connect setup: name=%q scope=%#v", connectName, scope)
	}
	if _, _, err := connectServiceShareSetup("other", token, "", ""); err == nil || !strings.Contains(err.Error(), "service share is for") {
		t.Fatalf("expected service mismatch error, got %v", err)
	}
	if _, _, err := connectServiceShareSetup("", token, "other", ""); err == nil || !strings.Contains(err.Error(), "cluster") {
		t.Fatalf("expected cluster mismatch error, got %v", err)
	}
	if _, _, err := connectServiceShareSetup("", token, "", "other"); err == nil || !strings.Contains(err.Error(), "namespace") {
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

func TestResolveAttachAuthorizationAcceptsExistingClaimWithoutAuthority(t *testing.T) {
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
	cluster.AuthorityPrivateKeyFile = ""
	cfg.Clusters["home"] = cluster
	if err := cfgpkg.WriteFile(configPath, cfg, true); err != nil {
		t.Fatal(err)
	}

	authz, err := resolveAttachAuthorization(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if authz.MintedServiceClaim {
		t.Fatal("non-authority resolver unexpectedly minted a claim")
	}
	if authz.ServicePeerID == "" || authz.ServiceClaimFile != svc.ServiceClaimFile || authz.MembershipCapabilityFile == "" {
		t.Fatalf("unexpected authz: %#v", authz)
	}
	if authz.ServiceShareToken != "" {
		t.Fatalf("expected no share token without authority key, got %q", authz.ServiceShareToken)
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
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{ClusterID: cluster.ClusterID, NamespaceID: "default", ServiceID: svc.ServiceID, SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Approve(svc.GrantRequestID, claim, nil, nil, ""); err != nil {
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
	if authz.ServiceClaimFile == "" || authz.MembershipCapabilityFile == "" {
		t.Fatalf("expected approved authz to save claim and membership: %#v", authz)
	}
}

func TestResolveAttachAuthorizationHandlesDeniedAndExpiredGrantRoute(t *testing.T) {
	for _, tc := range []struct {
		name    string
		finish  func(*testing.T, *grantspkg.Store, string)
		wantErr string
	}{
		{name: "denied", finish: func(t *testing.T, store *grantspkg.Store, id string) {
			_, err := store.Deny(id, "no")
			if err != nil {
				t.Fatal(err)
			}
		}, wantErr: "denied"},
		{name: "expired", finish: func(t *testing.T, store *grantspkg.Store, id string) {
			b, err := os.ReadFile(store.Path())
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
				if state.Requests[i].ID == id {
					state.Requests[i].Status = grantspkg.StatusExpired
				}
			}
			b, err = json.MarshalIndent(state, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(store.Path(), append(b, '\n'), 0600); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "expired"},
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
			serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "grant-route-"+tc.name)
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
			if err == nil || !strings.Contains(err.Error(), "pending") {
				t.Fatalf("expected pending, got %v", err)
			}
			reloaded, err := cfgpkg.LoadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			reqID := reloaded.Clusters["home"].Namespaces["default"].Services["myapi"].GrantRequestID
			tc.finish(t, store, reqID)
			_, err = resolveAttachAuthorization(configPath, reloaded)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected %q, got %v", tc.wantErr, err)
			}
		})
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
			name: "expired claim",
			mutate: func(t *testing.T, cluster cfgpkg.Cluster, svc cfgpkg.NamespaceService) {
				if err := writeTestServiceClaim(t, cluster, "default", svc, time.Now().Add(-time.Hour), ""); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "expired",
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
		printServiceDescription(serviceResource{Name: "myapi", Kind: "service", Status: "online", PeerID: "12D3KooWTestPeer", Addresses: []string{
			"/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWTestPeer",
			"/ip4/1.2.3.4/tcp/4001/p2p/relay/p2p-circuit/p2p/12D3KooWTestPeer",
		}}, []string{"starting temporary observer for 5s..."})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Path: direct", "Dial policy:", "preferred: direct", "fallback: relay", "Addresses:", "  Direct:", "  Relayed:"} {
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

func TestTopologyRenderMinimal(t *testing.T) {
	d := t.TempDir()
	topo := filepath.Join(d, "topology.yaml")
	if err := os.WriteFile(topo, []byte(topoExample()), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(d, "gen")
	if err := run([]string{"topology", "render", "--config", topo, "--out", out}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "relay.yaml")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "lmstudio.yaml")); err != nil {
		t.Fatal(err)
	}
}

func TestTopologyRenderResolvesRelayPeers(t *testing.T) {
	d := t.TempDir()
	topo := filepath.Join(d, "topology.yaml")
	if err := os.WriteFile(topo, []byte(`swarm:
  key_file: /tmp/swarm.key
nodes:
  relay:
    role: relay
    seed: public-relay-seed
    p2p_listen: /ip4/0.0.0.0/tcp/4001
    public_addr: /ip4/172.232.189.160/tcp/4001
  edge:
    role: edge
    seed: edge-seed
    p2p_listen: /ip4/0.0.0.0/tcp/4001
    listen: :8443
    admin_listen: 127.0.0.1:8444
    relay: relay
  lmstudio:
    role: service
    seed: service-lmstudio-seed
    p2p_listen: /ip4/0.0.0.0/tcp/40123
    service_name: lmstudio
    target: http://127.0.0.1:1234
    relay: relay
`), 0600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(d, "gen")
	if err := run([]string{"topology", "render", "--config", topo, "--out", out}); err != nil {
		t.Fatal(err)
	}
	relayID, err := p2p.PeerIDFromSeed("public-relay-seed")
	if err != nil {
		t.Fatal(err)
	}
	expectedRelay := "/ip4/172.232.189.160/tcp/4001/p2p/" + relayID.String()
	for _, name := range []string{"edge.yaml", "lmstudio.yaml"} {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		got := string(b)
		if !strings.Contains(got, "bootstrap_peers:") {
			t.Fatalf("%s missing bootstrap_peers: %s", name, got)
		}
		if !strings.Contains(got, "relay_peers:") {
			t.Fatalf("%s missing relay_peers: %s", name, got)
		}
		if !strings.Contains(got, expectedRelay) {
			t.Fatalf("%s missing resolved relay addr %q: %s", name, expectedRelay, got)
		}
	}
}
