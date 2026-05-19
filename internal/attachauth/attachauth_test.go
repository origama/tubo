package attachauth

import (
	"context"
	"errors"
	"testing"
)

func TestNewReturnsResolver(t *testing.T) {
	resolver := New(Dependencies{})
	if resolver == nil {
		t.Fatal("expected resolver")
	}
	if _, err := resolver.Resolve(context.Background(), ResolveRequest{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Resolve error = %v, want ErrNotImplemented", err)
	}
	if _, err := resolver.Renew(context.Background(), RenewRequest{}); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("Renew error = %v, want ErrNotImplemented", err)
	}
}
