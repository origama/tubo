package serviceidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestGenerateAndLoadKeepServiceIDStable(t *testing.T) {
	path := t.TempDir() + "/service.owner.key"
	first, created, err := Ensure(path)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected first ensure to create a key")
	}
	second, created, err := Ensure(path)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected second ensure to reuse the key")
	}
	if first.ServiceID != second.ServiceID {
		t.Fatalf("service id changed: %q vs %q", first.ServiceID, second.ServiceID)
	}
	if err := MatchServiceID(first.PublicKey, first.ServiceID); err != nil {
		t.Fatal(err)
	}
}

func TestDifferentKeysYieldDifferentServiceIDs(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if first.ServiceID == second.ServiceID {
		t.Fatalf("service ids unexpectedly equal: %q", first.ServiceID)
	}
}

func TestValidateServiceIDRejectsMalformed(t *testing.T) {
	for _, tc := range []string{"", "service", "service-bad", "svc-123", "service-XYZ"} {
		if err := ValidateServiceID(tc); err == nil {
			t.Fatalf("ValidateServiceID(%q) = nil, want error", tc)
		}
	}
}

func TestMatchServiceIDRejectsMismatch(t *testing.T) {
	identity, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := MatchServiceID(identity.PublicKey, identity.ServiceID); err != nil {
		t.Fatal(err)
	}
	other, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := MatchServiceID(identity.PublicKey, other.ServiceID); err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestServiceIDFromPublicKeyUsesPublicKey(t *testing.T) {
	pubA, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if gotA, gotB := ServiceIDFromPublicKey(pubA), ServiceIDFromPublicKey(pubB); gotA == gotB {
		t.Fatal("expected distinct service ids for distinct public keys")
	}
}
