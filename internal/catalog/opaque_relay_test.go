package catalog

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/crypto/ssh"

	"github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/serviceidentity"
)

// opaqueFixture builds a signed AnnouncementV3 for a synthetic private cluster
// and a caller config that trusts that cluster's authority. Returned config is
// suitable for exercising validateOpaqueAnnouncementsV3.
type opaqueFixture struct {
	Cfg          cfgpkg.Config
	Announcement discovery.AnnouncementV3
	Ctx          discovery.NamespaceDiscoveryContext
	AuthorityPub ed25519.PublicKey
	SignerPeerID peer.ID
	ServiceName  string
}

func buildOpaqueFixture(t *testing.T) opaqueFixture {
	t.Helper()
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	authorizedKey := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH)))
	signerPriv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	signerPeerID, err := peer.IDFromPrivateKey(signerPriv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := discovery.NamespaceDiscoveryContext{ClusterID: "cluster-solar", NamespaceID: "default", KeyID: "nsdk_opaque", Secret: bytes.Repeat([]byte{0x77}, 32)}
	service, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, SubjectPeerID: signerPeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionAnnounce}, ExpiresAt: time.Now().UTC().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	membershipBytes, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceID: service.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(service.PublicKey), PublisherPeerID: signerPeerID.String(), RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "opaque-nonce"}, service.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "lmstudio", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaseBytes, err := json.Marshal(leaseArtifacts.Lease)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := json.Marshal(leaseArtifacts.ServiceClaim)
	if err != nil {
		t.Fatal(err)
	}
	ann, err := discovery.NewAnnouncementV3(ctx, signerPeerID, 30*time.Second, discovery.AnnouncementV3Payload{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceName: "lmstudio", ServiceKind: "http", ServiceID: service.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(service.PublicKey), ConnectPolicy: "namespace_members", Addresses: []string{"/ip4/127.0.0.1/tcp/40123/p2p/" + signerPeerID.String()}, MembershipCapability: membershipBytes, PublishLease: leaseBytes, ServiceClaim: claimBytes, RegisteredAt: time.Now().UTC().Add(-time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := ann.Sign(signerPriv); err != nil {
		t.Fatal(err)
	}
	secretPath := filepath.Join(t.TempDir(), "discovery-current.secret")
	if err := os.WriteFile(secretPath, ctx.Secret, 0o600); err != nil {
		t.Fatal(err)
	}
	// A membership_capability_file is required for DiscoveryRuntime() to bind
	// the namespace secret to a runtime; the file need only exist for the test
	// (contents are not verified in the code paths under test here).
	membershipPath := filepath.Join(t.TempDir(), "membership.cap.json")
	if err := os.WriteFile(membershipPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := cfgpkg.Config{
		CurrentCluster:   "solar",
		CurrentNamespace: ctx.NamespaceID,
		Clusters: map[string]cfgpkg.Cluster{
			"solar": {
				ClusterID:                ctx.ClusterID,
				AuthorityPublicKey:       authorizedKey,
				MembershipCapabilityFile: membershipPath,
				Namespaces: map[string]cfgpkg.Namespace{
					ctx.NamespaceID: {
						Discovery:              cfgpkg.NamespaceDiscoveryEnabled,
						DiscoverySecretCurrent: &cfgpkg.ManagedSecretRef{Type: cfgpkg.SecretTypeNamespaceDiscovery, KeyID: ctx.KeyID, File: secretPath, CreatedAt: time.Now().UTC()},
					},
				},
			},
		},
	}
	return opaqueFixture{Cfg: cfg, Announcement: ann, Ctx: ctx, AuthorityPub: authorityPub, SignerPeerID: signerPeerID, ServiceName: "lmstudio"}
}

// TestValidateOpaqueAnnouncementsV3AcceptsSignedRecordWithLocalAuthority is the
// core positive path for #346: a consumer with the correct authority/context
// verifies a relay-forwarded opaque record and surfaces it as a trusted
// service.
func TestValidateOpaqueAnnouncementsV3AcceptsSignedRecordWithLocalAuthority(t *testing.T) {
	fx := buildOpaqueFixture(t)
	services := validateOpaqueAnnouncementsV3(fx.Cfg, []discovery.AnnouncementV3{fx.Announcement})
	if len(services) != 1 {
		t.Fatalf("validated services = %#v, want 1", services)
	}
	got := services[0]
	if got.Name != fx.ServiceName {
		t.Fatalf("service name = %q, want %q", got.Name, fx.ServiceName)
	}
	if got.PeerID != fx.SignerPeerID.String() {
		t.Fatalf("service peer id = %q, want %q", got.PeerID, fx.SignerPeerID.String())
	}
	if got.ClusterID != fx.Ctx.ClusterID || got.NamespaceID != fx.Ctx.NamespaceID {
		t.Fatalf("service scope = %q/%q", got.ClusterID, got.NamespaceID)
	}
}

// TestValidateOpaqueAnnouncementsV3IgnoresRecordsWhenNoLocalAuthority prevents
// a consumer that doesn't have the cluster's authority key from ever showing
// an opaque relay record as a trusted service. This is the security ratchet.
func TestValidateOpaqueAnnouncementsV3IgnoresRecordsWhenNoLocalAuthority(t *testing.T) {
	fx := buildOpaqueFixture(t)
	cfg := fx.Cfg
	cluster := cfg.Clusters["solar"]
	cluster.AuthorityPublicKey = ""
	cfg.Clusters["solar"] = cluster

	services := validateOpaqueAnnouncementsV3(cfg, []discovery.AnnouncementV3{fx.Announcement})
	if len(services) != 0 {
		t.Fatalf("expected no services when local authority is missing; got %#v", services)
	}
}

// TestValidateOpaqueAnnouncementsV3IgnoresTamperedSignature ensures a
// signature-tampered opaque record is silently dropped by the consumer, even
// though the relay accepted it.
func TestValidateOpaqueAnnouncementsV3IgnoresTamperedSignature(t *testing.T) {
	fx := buildOpaqueFixture(t)
	tampered := fx.Announcement
	tampered.Signature = append([]byte(nil), fx.Announcement.Signature...)
	tampered.Signature[0] ^= 0xff
	services := validateOpaqueAnnouncementsV3(fx.Cfg, []discovery.AnnouncementV3{tampered})
	if len(services) != 0 {
		t.Fatalf("expected no services from tampered announcement; got %#v", services)
	}
}

// TestValidateOpaqueAnnouncementsV3IgnoresWrongAuthority guards against a
// consumer that has a valid cluster context but for the wrong cluster / wrong
// authority accepting cross-cluster records.
func TestValidateOpaqueAnnouncementsV3IgnoresWrongAuthority(t *testing.T) {
	fx := buildOpaqueFixture(t)
	otherPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherSSH, err := ssh.NewPublicKey(otherPub)
	if err != nil {
		t.Fatal(err)
	}
	cfg := fx.Cfg
	cluster := cfg.Clusters["solar"]
	cluster.AuthorityPublicKey = strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherSSH)))
	cfg.Clusters["solar"] = cluster

	services := validateOpaqueAnnouncementsV3(cfg, []discovery.AnnouncementV3{fx.Announcement})
	if len(services) != 0 {
		t.Fatalf("expected no services with wrong authority; got %#v", services)
	}
}

// TestMergeOpaqueServicesDoesNotShadowPrimary confirms that a primary record
// from the relay's validated cache is preserved when an opaque duplicate is
// present.
func TestMergeOpaqueServicesDoesNotShadowPrimary(t *testing.T) {
	primary := []Service{{Name: "lmstudio", ServiceID: "svc-abc", PeerID: "peer-1", Status: "online"}}
	opaque := []Service{{Name: "lmstudio", ServiceID: "svc-abc", PeerID: "peer-1", Status: "online"}}
	merged := mergeOpaqueServices(primary, opaque)
	if len(merged) != 1 {
		t.Fatalf("merged len = %d, want 1", len(merged))
	}
	if merged[0].Status != "online" {
		t.Fatalf("primary status must survive merge: %#v", merged[0])
	}
}

// TestMergeOpaqueServicesAppendsUniqueRecords ensures new opaque records
// extend the visible service list.
func TestMergeOpaqueServicesAppendsUniqueRecords(t *testing.T) {
	primary := []Service{{Name: "testapi", ServiceID: "svc-1", PeerID: "peer-1"}}
	opaque := []Service{{Name: "lmstudio", ServiceID: "svc-2", PeerID: "peer-2"}}
	merged := mergeOpaqueServices(primary, opaque)
	if len(merged) != 2 {
		t.Fatalf("merged len = %d, want 2", len(merged))
	}
}
