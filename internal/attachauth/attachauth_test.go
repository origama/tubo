package attachauth

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

type fakeIdentityStore struct{}
type fakeArtifactStore struct{}
type fakeAuthoritySigner struct{}
type fakeGrantClient struct{}

func (fakeIdentityStore) EnsureAttachServiceIdentity(string, cfgpkg.Config) (cfgpkg.Config, cfgpkg.NamespaceService, error) {
	panic("not used")
}

func (fakeIdentityStore) ServicePeerID(string) (string, error) { panic("not used") }

func (fakeArtifactStore) VerifyPublishLease(string, ed25519.PublicKey, string, string, string, string) error {
	panic("not used")
}

func (fakeArtifactStore) VerifyServiceClaim(string, ed25519.PublicKey, string, string, string, string) error {
	panic("not used")
}

func (fakeArtifactStore) ResolveMembershipCapabilityFile(string, cfgpkg.Cluster, string, string, string) (string, error) {
	panic("not used")
}

func (fakeArtifactStore) BuildShareToken(cfgpkg.Cluster, string, string, string, cfgpkg.NamespaceService) (string, error) {
	panic("not used")
}

func (fakeArtifactStore) ReadPublishLease(string) (grantspkg.PublishLease, error) {
	panic("not used")
}

func (fakeAuthoritySigner) MintLocalPublishLease(cfgpkg.Cluster, string, string, string, cfgpkg.NamespaceService) error {
	panic("not used")
}

func (fakeGrantClient) RequestPublishGrant(string, cfgpkg.Config, cfgpkg.NamespaceService, string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	panic("not used")
}

func (fakeGrantClient) RenewPublishAuthorization(string, cfgpkg.Config, cfgpkg.NamespaceService, string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	panic("not used")
}

func TestNewReturnsResolver(t *testing.T) {
	resolver := New(Dependencies{
		IdentityStore:   fakeIdentityStore{},
		ArtifactStore:   fakeArtifactStore{},
		AuthoritySigner: fakeAuthoritySigner{},
		GrantClient:     fakeGrantClient{},
		Clock:           SystemClock{},
	})
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
