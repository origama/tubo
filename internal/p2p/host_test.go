package p2p

import (
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPrivateNetworkPSKFromEnv_Empty(t *testing.T) {
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY", "")
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY_B64", "")

	psk, ok, err := LoadPrivateNetworkPSKFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false when envs are unset")
	}
	if len(psk) != 0 {
		t.Fatalf("expected empty psk, got len=%d", len(psk))
	}
}

func TestLoadPrivateNetworkPSKFromEnv_B64(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 1)
	}
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY", "")
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY_B64", base64.StdEncoding.EncodeToString(raw))

	psk, ok, err := LoadPrivateNetworkPSKFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true for b64 key")
	}
	if len(psk) != 32 {
		t.Fatalf("expected psk len=32, got %d", len(psk))
	}
	for i := 0; i < 32; i++ {
		if psk[i] != raw[i] {
			t.Fatalf("byte mismatch at %d: got %d want %d", i, psk[i], raw[i])
		}
	}
}

func TestLoadPrivateNetworkPSKFromEnv_File(t *testing.T) {
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(255 - i)
	}
	hexKey := hex.EncodeToString(raw)
	content := "/key/swarm/psk/1.0.0/\n/base16/\n" + hexKey + "\n"

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "swarm.key")
	if err := os.WriteFile(keyPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write swarm key: %v", err)
	}

	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY", keyPath)
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY_B64", "")

	psk, ok, err := LoadPrivateNetworkPSKFromEnv()
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !ok {
		t.Fatalf("expected ok=true for file key")
	}
	if len(psk) != 32 {
		t.Fatalf("expected psk len=32, got %d", len(psk))
	}
	for i := 0; i < 32; i++ {
		if psk[i] != raw[i] {
			t.Fatalf("byte mismatch at %d: got %d want %d", i, psk[i], raw[i])
		}
	}
}

func TestLoadPrivateNetworkPSKFromEnv_BothSet(t *testing.T) {
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY", "/tmp/swarm.key")
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY_B64", "AQID")

	_, _, err := LoadPrivateNetworkPSKFromEnv()
	if err == nil {
		t.Fatalf("expected error when both env vars are set")
	}
}

func TestLoadPrivateNetworkPSKFromEnv_B64WrongLength(t *testing.T) {
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY", "")
	t.Setenv("LIBP2P_PRIVATE_NETWORK_KEY_B64", base64.StdEncoding.EncodeToString([]byte{1, 2, 3}))

	_, _, err := LoadPrivateNetworkPSKFromEnv()
	if err == nil {
		t.Fatalf("expected error for short b64 key")
	}
}
