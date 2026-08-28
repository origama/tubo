package usecase

import (
	"strings"
	"testing"
)

// TestRecipientRefForFriendlyName pins the L4 contract for the optional
// friendly name on ConnectExact: an explicit name wins, the deterministic
// derived name is the fallback, and evaluation is stable and bounded.
func TestRecipientRefForFriendlyName(t *testing.T) {
	// Explicit friendly name.
	ref, err := recipientRefFor("pip_abcdefghijklmnopqrstuvwxyz0123456789", "west")
	if err != nil {
		t.Fatal(err)
	}
	if ref.String() != "pipe/west" {
		t.Fatalf("named ref = %q", ref.String())
	}
	// Deterministic fallback from the remote Pipe ID.
	ref, err = recipientRefFor("pip_abcdefghijklmnopqrstuvwxyz0123456789", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref.Name, "connect-pip_") {
		t.Fatalf("fallback name = %q", ref.Name)
	}
	if _, err := recipientRefFor("pip_1", "A Nasty: Name!"); err == nil {
		t.Fatal("invalid friendly name accepted")
	}
	if _, err := recipientRefFor("pip_1", strings.Repeat("x", 200)); err == nil {
		t.Fatal("overlong friendly name accepted")
	}
}