package trust

import "testing"

func TestDefaultPublicNetworkMetadata(t *testing.T) {
	if DefaultPublicNetworkName == "" {
		t.Fatal("DefaultPublicNetworkName must not be empty")
	}
	if DefaultPublicNetworkBundleURL == "" {
		t.Fatal("DefaultPublicNetworkBundleURL must not be empty")
	}
}

func TestBundleSigningKeyExists(t *testing.T) {
	key, ok := BundleSigningKeys["tubo-root-2026"]
	if !ok {
		t.Fatal("missing tubo-root-2026 bundle signing key")
	}
	if key == "" {
		t.Fatal("bundle signing key must not be empty")
	}
}
