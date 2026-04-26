package p2p

import (
	"testing"
)

func TestLoadAllowedPeersFromEnvUnset(t *testing.T) {
	t.Setenv(allowedPeersEnv, "")
	allowed, configured, err := LoadAllowedPeersFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if configured {
		t.Fatalf("expected configured=false")
	}
	if allowed != nil {
		t.Fatalf("expected nil map when env unset")
	}
}

func TestLoadAllowedPeersFromEnvInvalid(t *testing.T) {
	t.Setenv(allowedPeersEnv, "not-a-peer-id")
	_, configured, err := LoadAllowedPeersFromEnv()
	if !configured {
		t.Fatalf("expected configured=true")
	}
	if err == nil {
		t.Fatalf("expected parse error for invalid peer ID")
	}
}

func TestLoadAllowedPeersFromEnvValid(t *testing.T) {
	pid1, err := PeerIDFromSeed("allowlist-seed-1")
	if err != nil {
		t.Fatalf("peerid seed1: %v", err)
	}
	pid2, err := PeerIDFromSeed("allowlist-seed-2")
	if err != nil {
		t.Fatalf("peerid seed2: %v", err)
	}

	t.Setenv(allowedPeersEnv, pid1.String()+", "+pid2.String())
	allowed, configured, err := LoadAllowedPeersFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !configured {
		t.Fatalf("expected configured=true")
	}
	if len(allowed) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(allowed))
	}
	if _, ok := allowed[pid1]; !ok {
		t.Fatalf("peer1 missing in allowlist")
	}
	if _, ok := allowed[pid2]; !ok {
		t.Fatalf("peer2 missing in allowlist")
	}

	g := NewPeerAllowlistConnectionGater(allowed)
	if !g.InterceptPeerDial(pid1) {
		t.Fatalf("expected pid1 allowed")
	}
	pid3, err := PeerIDFromSeed("allowlist-seed-3")
	if err != nil {
		t.Fatalf("peerid seed3: %v", err)
	}
	if g.InterceptPeerDial(pid3) {
		t.Fatalf("expected unknown peer denied")
	}
}
