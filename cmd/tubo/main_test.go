package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
