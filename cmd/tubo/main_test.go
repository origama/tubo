package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cfgpkg "p2p-api-tunnel/internal/config"
	"p2p-api-tunnel/internal/discovery"
	"p2p-api-tunnel/internal/p2p"
	iversion "p2p-api-tunnel/internal/version"
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
		{name: "legacy relay run", in: []string{"relay", "run", "--config", "relay.yaml"}, wantRole: "relay", wantArgs: []string{"--config", "relay.yaml"}},
		{name: "short relay", in: []string{"relay", "--config", "relay.yaml"}, wantRole: "relay", wantArgs: []string{"--config", "relay.yaml"}},
		{name: "gateway alias", in: []string{"gateway", "--listen", ":8443"}, wantRole: "edge", wantArgs: []string{"--listen", ":8443"}},
		{name: "attach positional target", in: []string{"attach", "http://127.0.0.1:1234", "--name", "lmstudio"}, wantRole: "service", wantArgs: []string{"--target", "http://127.0.0.1:1234", "--name", "lmstudio"}},
		{name: "attach explicit target flag", in: []string{"attach", "--target", "http://127.0.0.1:1234", "--name", "lmstudio"}, wantRole: "service", wantArgs: []string{"--target", "http://127.0.0.1:1234", "--name", "lmstudio"}},
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
			if strings.Join(gotArgs, "\x00") != strings.Join(tc.wantArgs, "\x00") {
				t.Fatalf("args = %#v, want %#v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestResolveRuntimeRoleRejectsLegacyRoleWithoutRun(t *testing.T) {
	if _, _, _, err := resolveRuntimeRole([]string{"service", "--name", "lmstudio"}); err == nil {
		t.Fatal("expected error for legacy service command without run")
	}
}

func TestResolveRuntimeRoleRejectsDuplicateAttachTarget(t *testing.T) {
	if _, _, _, err := resolveRuntimeRole([]string{"attach", "http://127.0.0.1:1234", "--target", "http://127.0.0.1:11434", "--name", "lmstudio"}); err == nil {
		t.Fatal("expected duplicate attach target error")
	}
}

func TestUsageMentionsIntentCommands(t *testing.T) {
	err := usage()
	if err == nil {
		t.Fatal("expected usage error")
	}
	for _, want := range []string{"attach", "gateway", "relay", "join", "service|bridge"} {
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

func TestServiceResourceFromEntry(t *testing.T) {
	entry := &discovery.ServiceEntry{
		ServiceName: "lmstudio",
		PeerID:      "12D3KooWTestPeer",
		Addresses:   []string{"/ip4/1.2.3.4/tcp/4001/p2p-circuit/p2p/12D3KooWTestPeer"},
		TTL:         30 * time.Second,
		Registered:  time.Now().Add(-5 * time.Second),
	}
	got := serviceResourceFromEntry(entry)
	if got.Name != "lmstudio" || got.Kind != "service" {
		t.Fatalf("unexpected service view: %#v", got)
	}
	if got.Path != "relayed" {
		t.Fatalf("path = %q, want relayed", got.Path)
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
