package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	capability "github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/discovery"
	discoveryquery "github.com/origama/tubo/internal/discovery/query"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/ssh"
)

func mustParseMultiaddrs(t *testing.T, raw ...string) []multiaddr.Multiaddr {
	t.Helper()
	out := make([]multiaddr.Multiaddr, 0, len(raw))
	for _, addr := range raw {
		m, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			t.Fatalf("parse multiaddr %q: %v", addr, err)
		}
		out = append(out, m)
	}
	return out
}

func TestMergeRelayCircuitAddrsAddsRelayPath(t *testing.T) {
	relayID, err := p2p.PeerIDFromSeed("relay-seed-test")
	if err != nil {
		t.Fatal(err)
	}
	serviceID, err := p2p.PeerIDFromSeed("service-seed-test")
	if err != nil {
		t.Fatal(err)
	}
	relayInfo := peer.AddrInfo{ID: relayID, Addrs: mustParseMultiaddrs(t, "/ip4/172.104.128.174/tcp/4001")}
	out := mergeRelayCircuitAddrs([]string{"/ip4/127.0.0.1/tcp/4001/p2p/" + serviceID.String()}, []peer.AddrInfo{relayInfo}, serviceID)
	want := "/ip4/172.104.128.174/tcp/4001/p2p/" + relayID.String() + "/p2p-circuit/p2p/" + serviceID.String()
	found := false
	for _, addr := range out {
		if addr == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("merged addrs missing relay circuit addr %q: %v", want, out)
	}
}

func TestHasRelayReservationUsesTrackedExpiry(t *testing.T) {
	app := &App{reservationReadyUntil: time.Now().Add(30 * time.Second), relayConnected: map[peer.ID]bool{}, relayInfos: []peer.AddrInfo{{ID: peer.ID("12D3KooWRelay")}}}
	if app.hasRelayReservation() {
		t.Fatal("expected no connected relay to suppress tracked reservation")
	}
	app.relayConnected[peer.ID("12D3KooWRelay")] = true
	if !app.hasRelayReservation() {
		t.Fatal("expected tracked reservation to count as ready once relay is connected")
	}
	app.reservationReadyUntil = time.Now().Add(-time.Second)
	if app.hasRelayReservation() {
		t.Fatal("expected expired tracked reservation to be ignored")
	}
}

func writeTestPublishLease(t *testing.T, path string, authorityPriv ed25519.PrivateKey, clusterID, namespaceID, serviceName, serviceSeed string, expiresAt time.Time) string {
	t.Helper()
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		t.Fatal(err)
	}
	req, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:        clusterID,
		NamespaceID:      namespaceID,
		ServiceID:        owner.ServiceID,
		ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:  servicePeerID.String(),
		RequestedCapabilities: []string{
			capability.PermissionAttach,
			capability.PermissionAnnounce,
			capability.PermissionShareMint,
		},
		Nonce: "lease-test-nonce",
	}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := grantspkg.BuildApprovalArtifacts(authorityPriv, "home", clusterID, namespaceID, serviceName, owner.ServiceID, servicePeerID.String(), time.Hour, time.Hour, req.RequestedCapabilities, req.ServicePublicKey, req.Nonce, req.ServiceOwnerSignature)
	if err != nil {
		t.Fatal(err)
	}
	lease := artifacts.PublishLease
	if !expiresAt.IsZero() {
		lease.ExpiresAt = expiresAt
	}
	b, err := json.MarshalIndent(lease, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return owner.ServiceID
}

func TestServiceDiscoveryQueryServesOwnAnnouncement(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-query-seed"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     "cluster-123",
		NamespaceID:   "default",
		SubjectPeerID: servicePeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Time{})
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryMode: discovery.ModeNamespaceV2.String(), DiscoveryTopic: discovery.NamespaceTopic("cluster-123", "default"), DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if app.cache == nil {
		t.Fatal("expected service cache")
	}
	ann, payload, ok := app.currentAnnouncementV2()
	if !ok {
		t.Fatal("expected current announcement")
	}
	if _, err := ann.Payload("cluster-123", "default"); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := app.cache.Add(app.host.ID(), payload.ServiceName, payload.Addresses, 30*time.Second); err != nil {
		t.Fatal(err)
	}
	client, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "service-query-client")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(app.host)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp, err := discoveryquery.GetService(ctx, client, info, "myapi")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Metadata.ServedByRole != "attach" || resp.Service == nil || resp.Service.Name != "myapi" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestServiceDiscoveryQuerySuspendsWithoutValidPublishLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	seed := "service-query-seed-expired"
	servicePeerID, err := p2p.PeerIDFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authoritySSH, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		t.Fatal(err)
	}
	capPath := filepath.Join(t.TempDir(), "membership.cap.json")
	signedCap, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: "cluster-123", NamespaceID: "default", SubjectPeerID: servicePeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish}, ExpiresAt: time.Now().Add(time.Hour)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(signedCap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(capPath, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(t.TempDir(), "publish-lease.json")
	serviceID := writeTestPublishLease(t, leasePath, authorityPriv, "cluster-123", "default", "myapi", seed, time.Now().Add(-time.Minute))
	app, err := New(ctx, Config{Listen: "/ip4/127.0.0.1/tcp/0", Seed: seed, ServiceName: "myapi", ServiceID: serviceID, Target: "http://127.0.0.1:8000", HeartbeatInterval: time.Second, DiscoveryMode: discovery.ModeNamespaceV2.String(), DiscoveryTopic: discovery.NamespaceTopic("cluster-123", "default"), DiscoveryClusterID: "cluster-123", DiscoveryNamespaceID: "default", AuthorityPublicKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authoritySSH))), MembershipCapabilityFile: capPath, ServicePublishLeaseFile: leasePath})
	if err != nil {
		t.Fatal(err)
	}
	defer app.host.Close()
	if _, _, ok := app.currentAnnouncementV2(); ok {
		t.Fatal("expected suspended announcement")
	}
}
