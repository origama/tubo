package cli

import (
	"strings"
	"testing"
)

func relayArgs(more ...string) []string {
	base := []string{"relay", "--listen", "/ip4/0.0.0.0/tcp/4001", "--public-addr", "/dns4/relay.example/tcp/4001"}
	return append(base, more...)
}

func TestRelayParseAcceptsWithoutSwarmKey(t *testing.T) {
	f, err := parseCommandForTest(t, relayArgs())
	if err != nil {
		t.Fatalf("relay without --swarm-key rejected: %v", err)
	}
	if f.values["swarm-key"] != "" {
		t.Fatalf("expected empty swarm-key, got %q", f.values["swarm-key"])
	}
	if f.values["listen"] != "/ip4/0.0.0.0/tcp/4001" {
		t.Fatalf("listen = %q", f.values["listen"])
	}
}

func TestRelayParseAcceptsSwarmKeyInBothForms(t *testing.T) {
	for _, args := range [][]string{
		{"--swarm-key", "/path/to/swarm.key"},
		{"--swarm-key=/path/to/swarm.key"},
	} {
		f, err := parseCommandForTest(t, relayArgs(args...))
		if err != nil {
			t.Fatalf("relay %v rejected: %v", args, err)
		}
		if f.values["swarm-key"] != "/path/to/swarm.key" {
			t.Fatalf("%v swarm-key = %q", args, f.values["swarm-key"])
		}
	}
}

func TestRelayParseRejectsMissingRequiredFlags(t *testing.T) {
	for _, args := range [][]string{
		{"relay", "--public-addr", "/dns4/relay.example/tcp/4001"},
		{"relay", "--listen", "/ip4/0.0.0.0/tcp/4001"},
		{"relay"},
	} {
		_, err := parseCommandForTest(t, args)
		if err == nil || !strings.Contains(err.Error(), "is required") {
			t.Fatalf("%v: expected required-flag error, got %v", args, err)
		}
	}
}

func TestRelayParseRejectsUnknownFlag(t *testing.T) {
	_, err := parseCommandForTest(t, relayArgs("--swarm-kye", "/tmp/key"))
	if err == nil || !strings.Contains(err.Error(), "unknown flag --swarm-kye") {
		t.Fatalf("unknown flag error = %v", err)
	}
}
