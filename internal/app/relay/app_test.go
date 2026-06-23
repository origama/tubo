package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/crypto/ssh"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
)

func TestRelayDiscoveryQueryServesCachedServices(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "relay-query-seed", EnableDiscoveryPubSub: true})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if app.cache == nil {
		t.Fatal("expected relay cache")
	}
	if err := app.cache.Add(app.host.ID(), "myapi", p2p.PeerAddrs(app.host), 30*time.Second); err != nil {
		t.Fatal(err)
	}
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "relay-query-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(app.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := discoveryquery.ListServices(ctx, client, info)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "relay" || len(resp.Services) != 1 || resp.Services[0].Name != "myapi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestRelayLimitFromConfigTreatsZeroDataAsUnlimited(t *testing.T) {
	limit := relayLimitFromConfig(5*time.Minute, 0)
	if limit == nil {
		t.Fatal("expected duration-limited relay limit")
	}
	if limit.Duration != 5*time.Minute {
		t.Fatalf("duration = %s", limit.Duration)
	}
	if limit.Data != relayUnlimitedDataBytes {
		t.Fatalf("data = %d, want unlimited sentinel %d", limit.Data, relayUnlimitedDataBytes)
	}
}

func TestRelayLimitFromConfigCanDisableAllLimits(t *testing.T) {
	if limit := relayLimitFromConfig(0, 0); limit != nil {
		t.Fatalf("limit = %#v, want nil", limit)
	}
}

func TestLoadConfigFromEnvDefaultsMatchRelayConfigDefaults(t *testing.T) {
	cfg, err := LoadConfigFromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BufferSize != 65536 {
		t.Fatalf("BufferSize = %d, want 65536", cfg.BufferSize)
	}
	if cfg.LimitDataBytes != 0 {
		t.Fatalf("LimitDataBytes = %d, want 0/unlimited", cfg.LimitDataBytes)
	}
}

func TestLoadConfigFromEnvAcceptsExplicitDataLimit(t *testing.T) {
	cfg, err := LoadConfigFromEnv(func(key string) string {
		if key == "RELAY_LIMIT_DATA_BYTES" {
			return "12345"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LimitDataBytes != 12345 {
		t.Fatalf("LimitDataBytes = %d", cfg.LimitDataBytes)
	}
}

func TestRelayDiscoveryQueryAcceptsAnnouncementV3(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "relay-query-v3-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	signerPriv := client.Peerstore().PrivKey(client.ID())
	if signerPriv == nil {
		t.Fatal("missing client private key")
	}
	fixture := buildRelayV3Fixture(t, client.ID(), signerPriv)
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: "relay-query-v3-seed", EnableDiscoveryPubSub: true, AuthorityPublicKey: fixture.authorityAuthorized, DiscoveryContext: &fixture.discoveryContext})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if app.cache == nil {
		t.Fatal("expected relay cache for v3")
	}
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(app.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := discoveryquery.AnnounceAnnouncementV3(ctx, client, info, fixture.announcement)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("unexpected announce_v3 error: %#v", resp)
	}
	if got := app.cache.Count(); got != 1 {
		t.Fatalf("cache count = %d, want 1", got)
	}
	entry, ok := app.cache.Resolve(fixture.announcementPayload.ServiceName)
	if !ok || entry.ServiceID != fixture.announcementPayload.ServiceID {
		t.Fatalf("unexpected cached entry: %#v", entry)
	}
}

type relayV3Fixture struct {
	authorityAuthorized string
	discoveryContext    discovery.NamespaceDiscoveryContext
	announcement        discovery.AnnouncementV3
	announcementPayload discovery.AnnouncementV3Payload
}

func buildRelayV3Fixture(t *testing.T, signerPeerID peer.ID, signerPriv crypto.PrivKey) relayV3Fixture {
	t.Helper()
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySigner, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	ctx := discovery.NamespaceDiscoveryContext{ClusterID: "cluster-123", NamespaceID: "default", KeyID: "nsdk_relay_v3", Secret: bytes.Repeat([]byte{0x42}, 32)}
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
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceID: service.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(service.PublicKey), PublisherPeerID: signerPeerID.String(), RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "relay-query-v3-nonce"}, service.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "myapi", time.Hour, time.Hour)
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
	payload := discovery.AnnouncementV3Payload{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceName: "myapi", ServiceKind: "http", ServiceID: service.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(service.PublicKey), ConnectPolicy: "namespace_members", Addresses: []string{"/ip4/127.0.0.1/tcp/40123/p2p/" + signerPeerID.String()}, MembershipCapability: membershipBytes, PublishLease: leaseBytes, ServiceClaim: claimBytes, RegisteredAt: time.Now().UTC().Add(-time.Second)}
	ann, err := discovery.NewAnnouncementV3(ctx, signerPeerID, 30*time.Second, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := ann.Sign(signerPriv); err != nil {
		t.Fatal(err)
	}
	return relayV3Fixture{authorityAuthorized: string(ssh.MarshalAuthorizedKey(authoritySigner)), discoveryContext: ctx, announcement: ann, announcementPayload: payload}
}
