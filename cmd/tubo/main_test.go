package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	for _, want := range []string{"attach", "gateway", "relay", "service|bridge"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("usage missing %q: %s", want, err)
		}
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
