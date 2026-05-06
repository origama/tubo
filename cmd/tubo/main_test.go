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

	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	"github.com/origama/tubo/internal/p2p"
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
				if len(gotArgs) < 2 || gotArgs[len(gotArgs)-2] != "--seed" || !strings.HasPrefix(gotArgs[len(gotArgs)-1], "attach-") {
					t.Fatalf("attach args missing generated seed: %#v", gotArgs)
				}
				gotArgs = gotArgs[:len(gotArgs)-2]
			}
			if strings.Join(gotArgs, "\x00") != strings.Join(tc.wantArgs, "\x00") {
				t.Fatalf("args = %#v, want %#v", gotArgs, tc.wantArgs)
			}
		})
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
	for _, want := range []string{"attach", "connect", "gateway", "relay", "join", "bundle-url"} {
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
	cfg, err := cfgpkg.LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
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
	if !strings.Contains(out, "joined swarm config") || !strings.Contains(out, "tubo get services") {
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
	if !strings.Contains(out, "joined network bundle") || !strings.Contains(out, "network: tubo-public") {
		t.Fatalf("unexpected output: %s", out)
	}
	configPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "tubo", "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		t.Fatalf("config not written: %v", err)
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
	if !strings.Contains(out, "joined network bundle") {
		t.Fatalf("unexpected output: %s", out)
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
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfgYAML := fmt.Sprintf("network:\n  private_key_file: %s\n  bootstrap_peers:\n    - %s\nedge:\n  admin_listen: 127.0.0.1:1\n", keyPath, p2p.PeerAddrs(server)[0])
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := discoverServices(configPath, 5*time.Second, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "remote-query" {
		t.Fatalf("mode = %q, want remote-query", result.Mode)
	}
	if result.Metadata == nil || result.Metadata.ServedByRole != "relay" {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
	}
	if len(result.Services) != 1 || result.Services[0].Name != "myapi" {
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
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	cfgYAML := fmt.Sprintf("network:\n  private_key_file: %s\n  bootstrap_peers:\n    - %s\nedge:\n  admin_listen: 127.0.0.1:1\n", keyPath, p2p.PeerAddrs(server)[0])
	if err := os.WriteFile(configPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatal(err)
	}
	result, service, err := discoverService(configPath, "myapi", 5*time.Second, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Mode != "remote-query" {
		t.Fatalf("mode = %q, want remote-query", result.Mode)
	}
	if result.Metadata == nil || result.Metadata.ServedByRole != "relay" {
		t.Fatalf("unexpected metadata: %#v", result.Metadata)
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
	if got := connectDirectMessage(service, []connectAttempt{{Path: "direct", Addr: service.DirectAddresses[0], Status: "failed", Error: "timeout"}, {Path: "relayed", Addr: service.RelayedAddresses[0], Status: "selected"}}, "relayed"); got != "attempted, failed" {
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
