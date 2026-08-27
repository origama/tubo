package cli

import (
	"testing"
)

func TestRelayParseAcceptsWithoutSwarmKey(t *testing.T) {
	// --swarm-key must be optional at parse level.
	// The relay command calls reserveForeground + RunRelay which need
	// a real store and network; we test that parse() accepts the args
	// without --swarm-key and that f.values["swarm-key"] is empty.
	args := []string{"--listen", "/ip4/0.0.0.0/tcp/4001", "--public-addr", "/dns4/relay.example/tcp/4001"}
	f, err := parse(args)
	if err != nil {
		t.Fatalf("parse() rejected valid relay args without --swarm-key: %v", err)
	}
	if f.values["swarm-key"] != "" {
		t.Fatalf("expected empty swarm-key, got %q", f.values["swarm-key"])
	}
	if f.values["listen"] != "/ip4/0.0.0.0/tcp/4001" {
		t.Fatalf("expected listen /ip4/0.0.0.0/tcp/4001, got %q", f.values["listen"])
	}
	if f.values["public-addr"] != "/dns4/relay.example/tcp/4001" {
		t.Fatalf("expected public-addr /dns4/relay.example/tcp/4001, got %q", f.values["public-addr"])
	}
}

func TestRelayParseAcceptsWithSwarmKey(t *testing.T) {
	// When --swarm-key IS provided, it must be in f.values.
	args := []string{"--listen", "/ip4/0.0.0.0/tcp/4001", "--public-addr", "/dns4/relay.example/tcp/4001", "--swarm-key", "/path/to/swarm.key"}
	f, err := parse(args)
	if err != nil {
		t.Fatalf("parse() rejected valid relay args with --swarm-key: %v", err)
	}
	if f.values["swarm-key"] != "/path/to/swarm.key" {
		t.Fatalf("expected swarm-key /path/to/swarm.key, got %q", f.values["swarm-key"])
	}
}

func TestRelayParseRejectsMissingListen(t *testing.T) {
	// --listen must remain required.
	args := []string{"--public-addr", "/dns4/relay.example/tcp/4001"}
	f, err := parse(args)
	if err != nil {
		t.Fatalf("parse() rejected args without --listen; should succeed at parse level: %v", err)
	}
	_, err = f.required("listen")
	if err == nil {
		t.Fatal("expected required('listen') to fail")
	}
}

func TestRelayParseRejectsMissingPublicAddr(t *testing.T) {
	// --public-addr must remain required.
	args := []string{"--listen", "/ip4/0.0.0.0/tcp/4001"}
	f, err := parse(args)
	if err != nil {
		t.Fatalf("parse() rejected args without --public-addr; should succeed at parse level: %v", err)
	}
	_, err = f.required("public-addr")
	if err == nil {
		t.Fatal("expected required('public-addr') to fail")
	}
}
