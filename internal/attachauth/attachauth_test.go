package attachauth

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"os"
	"strings"
	"testing"

	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

type fakeIdentityStore struct {
	cfg       cfgpkg.Config
	svc       cfgpkg.NamespaceService
	peerID    string
	ensureErr error
	peerErr   error
}

type fakeArtifactStore struct {
	publishLeaseErr error
	serviceClaimErr error
	membershipFile  string
	membershipErr   error
	shareToken      string
	shareErr        error
}

type fakeAuthoritySigner struct {
	calls int
	err   error
}

type fakeGrantClient struct {
	cfg   cfgpkg.Config
	svc   cfgpkg.NamespaceService
	token string
	err   error
	calls int
}

func (f fakeIdentityStore) EnsureAttachServiceIdentity(string, cfgpkg.Config) (cfgpkg.Config, cfgpkg.NamespaceService, error) {
	if f.ensureErr != nil {
		return cfgpkg.Config{}, cfgpkg.NamespaceService{}, f.ensureErr
	}
	return f.cfg, f.svc, nil
}

func (f fakeIdentityStore) ServicePeerID(string) (string, error) {
	if f.peerErr != nil {
		return "", f.peerErr
	}
	return f.peerID, nil
}

func (f fakeArtifactStore) VerifyPublishLease(string, ed25519.PublicKey, string, string, string, string) error {
	return f.publishLeaseErr
}

func (f fakeArtifactStore) VerifyServiceClaim(string, ed25519.PublicKey, string, string, string, string) error {
	return f.serviceClaimErr
}

func (f fakeArtifactStore) ResolveMembershipCapabilityFile(string, cfgpkg.Cluster, string, string, string) (string, error) {
	return f.membershipFile, f.membershipErr
}

func (f fakeArtifactStore) BuildShareToken(cfgpkg.Cluster, string, string, string, cfgpkg.NamespaceService) (string, error) {
	return f.shareToken, f.shareErr
}

func (f fakeArtifactStore) ReadPublishLease(string) (grantspkg.PublishLease, error) {
	return grantspkg.PublishLease{}, os.ErrNotExist
}

func (f *fakeAuthoritySigner) MintLocalPublishLease(cfgpkg.Cluster, string, string, string, cfgpkg.NamespaceService) error {
	f.calls++
	return f.err
}

func (f *fakeGrantClient) RequestPublishGrant(string, cfgpkg.Config, cfgpkg.NamespaceService, string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	f.calls++
	return f.cfg, f.svc, f.token, f.err
}

func (f *fakeGrantClient) RenewPublishAuthorization(string, cfgpkg.Config, cfgpkg.NamespaceService, string) (cfgpkg.Config, cfgpkg.NamespaceService, string, error) {
	f.calls++
	return f.cfg, f.svc, f.token, f.err
}

func TestResolveReturnsReadyForReusablePublishLease(t *testing.T) {
	cfg := testAttachConfig()
	svc := cfgpkg.NamespaceService{ServiceID: "service-1234567890abcdef", ServiceSeed: "seed", ServiceClaimFile: "/tmp/service.claim", ServicePublishLeaseFile: "/tmp/service.lease"}
	resolver := New(Dependencies{
		IdentityStore: fakeIdentityStore{cfg: cfg, svc: svc, peerID: "12D3KooWPeer"},
		ArtifactStore: fakeArtifactStore{membershipFile: "/tmp/membership.cap", shareToken: "share-token"},
		Clock:         SystemClock{},
	})

	got, err := resolver.Resolve(context.Background(), ResolveRequest{ConfigPath: "/tmp/tubo.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if got.Decision != DecisionReady {
		t.Fatalf("Decision = %q, want %q", got.Decision, DecisionReady)
	}
	if !got.PublishLeaseReused {
		t.Fatal("expected PublishLeaseReused")
	}
	if got.MembershipCapabilityFile != "/tmp/membership.cap" {
		t.Fatalf("MembershipCapabilityFile = %q", got.MembershipCapabilityFile)
	}
	if got.ServiceShareToken != "share-token" {
		t.Fatalf("ServiceShareToken = %q", got.ServiceShareToken)
	}
}

func TestResolveMintsLocallyWhenAuthorityKeyIsPresent(t *testing.T) {
	cfg := testAttachConfig()
	cfg.Clusters["home"] = cfgpkg.Cluster{
		ClusterID:               "cluster-123",
		AuthorityPublicKey:      testAuthorityPublicKey,
		AuthorityPrivateKeyFile: "/tmp/authority.key",
		Namespaces:              map[string]cfgpkg.Namespace{"default": {Services: map[string]cfgpkg.NamespaceService{}}},
	}
	svc := cfgpkg.NamespaceService{ServiceID: "service-1234567890abcdef", ServiceSeed: "seed", ServiceClaimFile: "/tmp/service.claim", ServicePublishLeaseFile: "/tmp/service.lease"}
	signer := &fakeAuthoritySigner{}
	resolver := New(Dependencies{
		IdentityStore:   fakeIdentityStore{cfg: cfg, svc: svc, peerID: "12D3KooWPeer"},
		ArtifactStore:   fakeArtifactStore{publishLeaseErr: os.ErrNotExist, membershipFile: "/tmp/membership.cap", shareToken: "share-token"},
		AuthoritySigner: signer,
		Clock:           SystemClock{},
	})

	got, err := resolver.Resolve(context.Background(), ResolveRequest{ConfigPath: "/tmp/tubo.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if got.Decision != DecisionReady {
		t.Fatalf("Decision = %q, want %q", got.Decision, DecisionReady)
	}
	if !got.MintedLocally {
		t.Fatal("expected MintedLocally")
	}
	if signer.calls != 1 {
		t.Fatalf("MintLocalPublishLease calls = %d, want 1", signer.calls)
	}
}

func TestResolveReturnsReadyWhenGrantPathApproves(t *testing.T) {
	cfg := testAttachConfigWithGrantPeer()
	svc := cfgpkg.NamespaceService{ServiceID: "service-1234567890abcdef", ServiceSeed: "seed", ServiceClaimFile: "/tmp/service.claim", ServicePublishLeaseFile: "/tmp/service.lease", GrantServicePeer: "/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWGrant"}
	grantClient := &fakeGrantClient{cfg: cfg, svc: svc, token: "share-token"}
	resolver := New(Dependencies{
		IdentityStore: fakeIdentityStore{cfg: cfg, svc: svc, peerID: "12D3KooWPeer"},
		ArtifactStore: fakeArtifactStore{publishLeaseErr: os.ErrNotExist, membershipFile: "/tmp/membership.cap"},
		GrantClient:   grantClient,
		Clock:         SystemClock{},
	})

	got, err := resolver.Resolve(context.Background(), ResolveRequest{ConfigPath: "/tmp/tubo.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if got.Decision != DecisionReady {
		t.Fatalf("Decision = %q, want %q", got.Decision, DecisionReady)
	}
	if got.ServiceShareToken != "share-token" {
		t.Fatalf("ServiceShareToken = %q", got.ServiceShareToken)
	}
	if grantClient.calls != 1 {
		t.Fatalf("GrantClient calls = %d, want 1", grantClient.calls)
	}
}

func TestResolveInterpretsGrantPendingAndDenied(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		decision Decision
	}{
		{name: "pending", err: fmt.Errorf("publish grant request %q is pending; publication requires an approved publish lease", "gr_123"), decision: DecisionPendingApproval},
		{name: "denied", err: fmt.Errorf("grant request %s denied: no", "gr_123"), decision: DecisionDenied},
		{name: "expired", err: fmt.Errorf("grant request %s expired: too late", "gr_123"), decision: DecisionRetryable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testAttachConfigWithGrantPeer()
			svc := cfgpkg.NamespaceService{ServiceID: "service-1234567890abcdef", ServiceSeed: "seed", ServiceClaimFile: "/tmp/service.claim", ServicePublishLeaseFile: "/tmp/service.lease", GrantRequestID: "gr_123", GrantServicePeer: "/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWGrant"}
			grantClient := &fakeGrantClient{cfg: cfg, svc: svc, err: tt.err}
			resolver := New(Dependencies{
				IdentityStore: fakeIdentityStore{cfg: cfg, svc: svc, peerID: "12D3KooWPeer"},
				ArtifactStore: fakeArtifactStore{publishLeaseErr: os.ErrNotExist, membershipFile: "/tmp/membership.cap"},
				GrantClient:   grantClient,
				Clock:         SystemClock{},
			})

			got, err := resolver.Resolve(context.Background(), ResolveRequest{ConfigPath: "/tmp/tubo.yaml", Config: cfg})
			if err != nil {
				t.Fatalf("Resolve error = %v", err)
			}
			if got.Decision != tt.decision {
				t.Fatalf("Decision = %q, want %q", got.Decision, tt.decision)
			}
			if !strings.Contains(got.ShareRecoveryHint, "tubo grants request service/myapi --poll --peer /ip4/127.0.0.1/tcp/40123/p2p/12D3KooWGrant --cluster home --namespace default") {
				t.Fatalf("ShareRecoveryHint = %q", got.ShareRecoveryHint)
			}
			if grantClient.calls != 1 {
				t.Fatalf("GrantClient calls = %d, want 1", grantClient.calls)
			}
		})
	}
}

func TestResolveReturnsReadyWhenOnlyStoredClaimIsAvailable(t *testing.T) {
	cfg := testAttachConfig()
	svc := cfgpkg.NamespaceService{ServiceID: "service-1234567890abcdef", ServiceSeed: "seed", ServiceClaimFile: "/tmp/service.claim", ServicePublishLeaseFile: "/tmp/service.lease"}
	resolver := New(Dependencies{
		IdentityStore: fakeIdentityStore{cfg: cfg, svc: svc, peerID: "12D3KooWPeer"},
		ArtifactStore: fakeArtifactStore{publishLeaseErr: os.ErrNotExist, membershipFile: "/tmp/membership.cap"},
		Clock:         SystemClock{},
	})

	got, err := resolver.Resolve(context.Background(), ResolveRequest{ConfigPath: "/tmp/tubo.yaml", Config: cfg})
	if err != nil {
		t.Fatalf("Resolve error = %v", err)
	}
	if got.Decision != DecisionReady {
		t.Fatalf("Decision = %q, want %q", got.Decision, DecisionReady)
	}
	if got.PublishLeaseReused {
		t.Fatal("did not expect PublishLeaseReused")
	}
}

func TestRenewUsesGrantClient(t *testing.T) {
	cfg := testAttachConfigWithGrantPeer()
	svc := cfgpkg.NamespaceService{ServiceID: "service-1234567890abcdef", ServiceSeed: "seed", ServiceClaimFile: "/tmp/service.claim", ServicePublishLeaseFile: "/tmp/service.lease", GrantServicePeer: "/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWGrant"}
	grantClient := &fakeGrantClient{cfg: cfg, svc: svc, token: "share-token"}
	resolver := New(Dependencies{
		ArtifactStore: fakeArtifactStore{membershipFile: "/tmp/membership.cap"},
		GrantClient:   grantClient,
		Clock:         SystemClock{},
	})

	got, err := resolver.Renew(context.Background(), RenewRequest{ConfigPath: "/tmp/tubo.yaml", Config: cfg, Service: svc, ServicePeerID: "12D3KooWPeer"})
	if err != nil {
		t.Fatalf("Renew error = %v", err)
	}
	if got.Decision != DecisionReady {
		t.Fatalf("Decision = %q, want %q", got.Decision, DecisionReady)
	}
	if got.ServiceShareToken != "share-token" {
		t.Fatalf("ServiceShareToken = %q", got.ServiceShareToken)
	}
	if grantClient.calls != 1 {
		t.Fatalf("GrantClient calls = %d, want 1", grantClient.calls)
	}
}

func TestRenewRequiresARefreshPath(t *testing.T) {
	resolver := New(Dependencies{ArtifactStore: fakeArtifactStore{membershipFile: "/tmp/membership.cap"}})
	if _, err := resolver.Renew(context.Background(), RenewRequest{Config: testAttachConfig(), Service: cfgpkg.NamespaceService{ServiceSeed: "seed"}, ServicePeerID: "12D3KooWPeer"}); err == nil || !strings.Contains(err.Error(), "renewal requires") {
		t.Fatalf("Renew error = %v, want renewal path error", err)
	}
}

const testAuthorityPublicKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAII0U1wP0i0fWQJ8YjLkNn6M2I7vWl7f7Yc8N5Q3w0N9A tubo-test"

func testAttachConfig() cfgpkg.Config {
	return cfgpkg.Config{
		CurrentCluster:   "home",
		CurrentNamespace: "default",
		Service:          cfgpkg.Service{Name: "myapi"},
		Clusters: map[string]cfgpkg.Cluster{
			"home": {
				ClusterID:          "cluster-123",
				AuthorityPublicKey: testAuthorityPublicKey,
				Namespaces:         map[string]cfgpkg.Namespace{"default": {Services: map[string]cfgpkg.NamespaceService{}}},
			},
		},
	}
}

func testAttachConfigWithGrantPeer() cfgpkg.Config {
	cfg := testAttachConfig()
	cfg.Clusters["home"] = cfgpkg.Cluster{
		ClusterID:          "cluster-123",
		AuthorityPublicKey: testAuthorityPublicKey,
		MembershipGrant: &cfgpkg.ClusterMembershipGrant{
			GrantServiceProtocol: grantspkg.ProtocolID,
			GrantServicePeers:    []string{"/ip4/127.0.0.1/tcp/40123/p2p/12D3KooWGrant"},
		},
		Namespaces: map[string]cfgpkg.Namespace{"default": {Services: map[string]cfgpkg.NamespaceService{}}},
	}
	return cfg
}
