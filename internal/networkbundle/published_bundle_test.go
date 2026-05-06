package networkbundle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/origama/tubo/internal/trust"
)

func TestPublishedPublicBundleVerifies(t *testing.T) {
	bundlePath := filepath.Join("..", "..", "docs", ".well-known", "tubo", "networks", "tubo-public.bundle")
	bundleBytes, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read published bundle: %v", err)
	}
	bundle, err := Parse(bundleBytes)
	if err != nil {
		t.Fatalf("parse published bundle: %v", err)
	}
	payloadBytes, keyID, err := Verify(bundle, trust.BundleSigningKeys)
	if err != nil {
		t.Fatalf("verify published bundle: %v", err)
	}
	if keyID != "tubo-root-2026" {
		t.Fatalf("key_id = %q", keyID)
	}
	payload, err := DecodePayload(payloadBytes)
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if err := ValidatePayload(payload); err != nil {
		t.Fatalf("validate payload: %v", err)
	}
	if payload.Name != trust.DefaultPublicNetworkName {
		t.Fatalf("payload name = %q, want %q", payload.Name, trust.DefaultPublicNetworkName)
	}
}
