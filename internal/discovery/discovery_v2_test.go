package discovery

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	capability "github.com/origama/tubo/internal/capability"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/serviceidentity"
)

func TestPubSubSubscriberV2AcceptsValidAnnouncement(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName: "myapi",
		addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
	})

	subscriber.handleMessageV2(msg)
	assertV2Accepted(t, subscriber, 1, "myapi")
}

func TestPubSubSubscriberV2AcceptsConnectMetadata(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:   "myapi",
		connectPolicy: "namespace_members",
		grantService:  &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/9.8.7.6/tcp/4001/p2p/12D3KooWGrant"}},
		addresses:     []string{"/ip4/127.0.0.1/tcp/8080"},
	})

	subscriber.handleMessageV2(msg)
	assertV2Accepted(t, subscriber, 1, "myapi")
	entry, ok := subscriber.cache.Resolve("myapi")
	if !ok {
		t.Fatal("expected cache entry")
	}
	if entry.ConnectPolicy != "namespace_members" {
		t.Fatalf("connect policy = %q", entry.ConnectPolicy)
	}
	if entry.GrantService == nil || len(entry.GrantService.Peers) != 1 || entry.GrantService.Peers[0] != "/ip4/9.8.7.6/tcp/4001/p2p/12D3KooWGrant" {
		t.Fatalf("grant service = %#v", entry.GrantService)
	}
}

func TestPubSubSubscriberV2FiltersLocalOnlyGrantPeers(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:   "myapi",
		connectPolicy: "namespace_members",
		grantService:  &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWGrant", "/ip4/5.6.7.8/tcp/4001/p2p/12D3KooWGrant"}},
		addresses:     []string{"/ip4/127.0.0.1/tcp/8080"},
	})

	subscriber.handleMessageV2(msg)
	assertV2Accepted(t, subscriber, 1, "myapi")
	entry, ok := subscriber.cache.Resolve("myapi")
	if !ok {
		t.Fatal("expected cache entry")
	}
	if entry.GrantService == nil || len(entry.GrantService.Peers) != 1 || entry.GrantService.Peers[0] != "/ip4/5.6.7.8/tcp/4001/p2p/12D3KooWGrant" {
		t.Fatalf("grant service peers = %#v", entry.GrantService)
	}
}

func TestPubSubSubscriberV2AcceptsNamespaceMembershipWithServiceClaim(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:       "myapi",
		addresses:         []string{"/ip4/127.0.0.1/tcp/8080"},
		membershipSubject: "cluster-123",
	})

	subscriber.handleMessageV2(msg)
	assertV2Accepted(t, subscriber, 1, "myapi")
}

func TestPubSubSubscriberV2RejectsReplay(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName: "myapi",
		addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
	})

	subscriber.handleMessageV2(msg)
	subscriber.handleMessageV2(msg)
	assertV2Accepted(t, subscriber, 1, "myapi")
}

func TestPubSubSubscriberV2RejectsWrongTopic(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName: "myapi",
		addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
	})
	subscriber.expectedTopic = "/discovery/v2/other"

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsExpiredAnnouncement(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:  "myapi",
		addresses:    []string{"/ip4/127.0.0.1/tcp/8080"},
		registeredAt: time.Now().Add(-2 * time.Minute),
		ttl:          30 * time.Second,
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsBadMembershipPermissions(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:     "myapi",
		addresses:       []string{"/ip4/127.0.0.1/tcp/8080"},
		membershipPerms: []string{capability.PermissionSubscribe, capability.PermissionList},
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsMissingServiceClaim(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:      "myapi",
		addresses:        []string{"/ip4/127.0.0.1/tcp/8080"},
		omitServiceClaim: true,
		omitPublishLease: true,
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsExpiredServiceClaim(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:           "myapi",
		addresses:             []string{"/ip4/127.0.0.1/tcp/8080"},
		serviceClaimExpiresAt: time.Now().Add(-time.Minute),
		omitPublishLease:      true,
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsServiceClaimSignedByWrongAuthority(t *testing.T) {
	_, wrongAuthorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:              "myapi",
		addresses:                []string{"/ip4/127.0.0.1/tcp/8080"},
		serviceClaimAuthorityKey: wrongAuthorityPriv,
		omitPublishLease:         true,
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsServiceClaimForDifferentPeer(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:           "myapi",
		addresses:             []string{"/ip4/127.0.0.1/tcp/8080"},
		serviceClaimPeerID:    "12D3KooWDifferentPeer",
		serviceClaimPerms:     []string{capability.PermissionAttach, capability.PermissionAnnounce},
		serviceClaimExpiresAt: time.Now().Add(time.Hour),
		omitPublishLease:      true,
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsServiceClaimForDifferentServiceID(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:           "myapi",
		addresses:             []string{"/ip4/127.0.0.1/tcp/8080"},
		serviceClaimServiceID: "service-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		serviceClaimPerms:     []string{capability.PermissionAttach, capability.PermissionAnnounce},
		omitPublishLease:      true,
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2AcceptsDuplicateDisplayNamesWithDifferentServiceIDs(t *testing.T) {
	subscriber, first := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName: "myapi",
		addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
	})
	_, second := testV2SubscriberAndMessage(t, testV2Payload{
		authorityPriv: subscriber.testAuthorityKey,
		serviceName:   "myapi",
		addresses:     []string{"/ip4/127.0.0.1/tcp/8081"},
	})

	subscriber.handleMessageV2(first)
	subscriber.handleMessageV2(second)
	if got := subscriber.cache.Count(); got != 2 {
		t.Fatalf("cache count = %d want 2", got)
	}
}

func TestPubSubSubscriberV2RejectsDuplicateServiceIDFromWrongKey(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:           "myapi",
		addresses:             []string{"/ip4/127.0.0.1/tcp/8080"},
		wrongServicePublicKey: true,
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsPublishLeaseForDifferentServiceID(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:           "myapi",
		addresses:             []string{"/ip4/127.0.0.1/tcp/8080"},
		publishLeaseServiceID: "service-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsPublishLeaseFromUntrustedIssuer(t *testing.T) {
	_, wrongAuthorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:              "myapi",
		addresses:                []string{"/ip4/127.0.0.1/tcp/8080"},
		publishLeaseAuthorityKey: wrongAuthorityPriv,
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsExpiredPublishLease(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:           "myapi",
		addresses:             []string{"/ip4/127.0.0.1/tcp/8080"},
		publishLeaseExpiresAt: time.Now().Add(-time.Minute),
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsCorruptedCiphertext(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName: "myapi",
		addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
	})
	var ann AnnouncementV2
	if err := ann.Unmarshal(msg.Data); err != nil {
		t.Fatal(err)
	}
	ann.Ciphertext = append([]byte(nil), ann.Ciphertext...)
	if len(ann.Ciphertext) == 0 {
		t.Fatal("ciphertext unexpectedly empty")
	}
	ann.Ciphertext[0] ^= 0xFF
	if err := ann.Sign(subscriber.testPrivKey); err != nil {
		t.Fatal(err)
	}
	data, err := ann.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	msg.Data = data

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

type testV2Payload struct {
	authorityPriv            ed25519.PrivateKey
	serviceName              string
	serviceID                string
	connectPolicy            string
	grantService             *grantspkg.GrantServiceEndpoint
	addresses                []string
	registeredAt             time.Time
	ttl                      time.Duration
	membershipPerms          []string
	membershipSubject        string
	omitServiceClaim         bool
	serviceClaimServiceID    string
	serviceClaimPeerID       string
	serviceClaimPerms        []string
	serviceClaimExpiresAt    time.Time
	serviceClaimAuthorityKey ed25519.PrivateKey
	omitPublishLease         bool
	publishLeaseAuthorityKey ed25519.PrivateKey
	publishLeaseServiceID    string
	publishLeaseExpiresAt    time.Time
	wrongServicePublicKey    bool
}

type testV2Harness struct {
	PubSubSubscriber
	testPrivKey      crypto.PrivKey
	testAuthorityKey ed25519.PrivateKey
	topic            string
}

func testV2SubscriberAndMessage(t *testing.T, payload testV2Payload) (*testV2Harness, *pubsub.Message) {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.authorityPriv) > 0 {
		authorityPriv = payload.authorityPriv
		authorityPub = authorityPriv.Public().(ed25519.PublicKey)
	}
	if payload.ttl == 0 {
		payload.ttl = 30 * time.Second
	}
	if payload.registeredAt.IsZero() {
		payload.registeredAt = time.Now().UTC().Add(-time.Second)
	}
	membershipSubject := payload.membershipSubject
	if membershipSubject == "" {
		membershipSubject = pid.String()
	}
	membership := capability.MembershipCapability{
		ClusterID:     "cluster-123",
		NamespaceID:   "tenant-a",
		SubjectPeerID: membershipSubject,
		Permissions:   payload.membershipPerms,
		ExpiresAt:     time.Now().Add(time.Hour),
	}
	if len(membership.Permissions) == 0 {
		membership.Permissions = []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}
	}
	membership, err = capability.SignMembershipCapability(membership, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	ownerPub, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serviceID := payload.serviceID
	if serviceID == "" {
		serviceID = serviceidentity.ServiceIDFromPublicKey(ownerPub)
	}
	servicePublicKey := serviceidentity.EncodePublicKey(ownerPub)
	if payload.wrongServicePublicKey {
		wrongPub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		servicePublicKey = serviceidentity.EncodePublicKey(wrongPub)
	}
	var serviceClaim []byte
	if !payload.omitServiceClaim {
		claimServiceID := payload.serviceClaimServiceID
		if claimServiceID == "" {
			claimServiceID = serviceID
		}
		claimPeerID := payload.serviceClaimPeerID
		if claimPeerID == "" {
			claimPeerID = pid.String()
		}
		claimExpiresAt := payload.serviceClaimExpiresAt
		if claimExpiresAt.IsZero() {
			claimExpiresAt = time.Now().Add(time.Hour)
		}
		claim := capability.ServiceClaim{
			ClusterID:     "cluster-123",
			NamespaceID:   "tenant-a",
			ServiceID:     claimServiceID,
			SubjectPeerID: claimPeerID,
			Permissions:   payload.serviceClaimPerms,
			ExpiresAt:     claimExpiresAt,
		}
		if len(claim.Permissions) == 0 {
			claim.Permissions = []string{capability.PermissionAttach, capability.PermissionAnnounce}
		}
		claimAuthorityKey := authorityPriv
		if len(payload.serviceClaimAuthorityKey) > 0 {
			claimAuthorityKey = payload.serviceClaimAuthorityKey
		}
		claim, err = capability.SignServiceClaim(claim, claimAuthorityKey)
		if err != nil {
			t.Fatal(err)
		}
		serviceClaim, err = json.Marshal(claim)
		if err != nil {
			t.Fatal(err)
		}
	}
	var publishLease []byte
	if !payload.omitPublishLease {
		leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
			ClusterID:             "cluster-123",
			NamespaceID:           "tenant-a",
			ServiceID:             serviceID,
			ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
			PublisherPeerID:       pid.String(),
			RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
			Nonce:                 "test-lease-nonce",
		}, ownerPriv)
		if err != nil {
			t.Fatal(err)
		}
		leaseAuthorityKey := authorityPriv
		if len(payload.publishLeaseAuthorityKey) > 0 {
			leaseAuthorityKey = payload.publishLeaseAuthorityKey
		}
		leaseTTL := time.Hour
		if !payload.publishLeaseExpiresAt.IsZero() {
			leaseTTL = time.Until(payload.publishLeaseExpiresAt)
		}
		artifacts, err := grantspkg.BuildPublishLeaseArtifacts(leaseAuthorityKey, leaseReq, payload.serviceName, time.Hour, leaseTTL)
		if err != nil {
			t.Fatal(err)
		}
		if payload.publishLeaseServiceID != "" {
			artifacts.Lease.ServiceID = payload.publishLeaseServiceID
		}
		if !payload.publishLeaseExpiresAt.IsZero() {
			artifacts.Lease.ExpiresAt = payload.publishLeaseExpiresAt
		}
		publishLease, err = json.Marshal(artifacts.Lease)
		if err != nil {
			t.Fatal(err)
		}
	}
	membershipBytes, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	ann, err := NewAnnouncementV2("cluster-123", "tenant-a", pid, payload.ttl, AnnouncementV2Payload{
		ServiceName:          payload.serviceName,
		ServiceID:            serviceID,
		ServicePublicKey:     servicePublicKey,
		ConnectPolicy:        payload.connectPolicy,
		GrantService:         grantspkg.CloneGrantServiceEndpoint(payload.grantService),
		Addresses:            payload.addresses,
		MembershipCapability: membershipBytes,
		ServiceClaim:         serviceClaim,
		PublishLease:         publishLease,
		RegisteredAt:         payload.registeredAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ann.Sign(priv); err != nil {
		t.Fatal(err)
	}
	data, err := ann.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	topic := NamespaceTopic("cluster-123", "tenant-a")
	h := &testV2Harness{
		PubSubSubscriber: PubSubSubscriber{
			expectedTopic:      topic,
			cache:              NewCache(30*time.Second, time.Hour),
			mode:               ModeNamespaceV2,
			clusterID:          "cluster-123",
			namespaceID:        "tenant-a",
			authorityPublicKey: authorityPub,
			pubKey:             map[peer.ID]crypto.PubKey{pid: priv.GetPublic()},
			replay:             newAnnouncementReplayCache(16),
			events:             make(chan DiscoveryEvent, 4),
		},
		testPrivKey:      priv,
		testAuthorityKey: authorityPriv,
		topic:            topic,
	}
	msg := &pubsub.Message{Message: &pb.Message{Data: data, From: []byte(pid), Topic: &topic, Key: mustPubKeyRaw(t, priv.GetPublic())}}
	return h, msg
}

func mustPubKeyRaw(t *testing.T, pk crypto.PubKey) []byte {
	t.Helper()
	raw, err := pk.Raw()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func assertV2Accepted(t *testing.T, subscriber *testV2Harness, wantCount int, wantService string) {
	t.Helper()
	if got := subscriber.cache.Count(); got != wantCount {
		t.Fatalf("cache count = %d want %d", got, wantCount)
	}
	select {
	case ev := <-subscriber.events:
		if ev.ServiceName != wantService || ev.Type != "added" {
			t.Fatalf("event = %#v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected discovery event")
	}
}

func assertV2Rejected(t *testing.T, subscriber *testV2Harness) {
	t.Helper()
	if got := subscriber.cache.Count(); got != 0 {
		t.Fatalf("cache count = %d want 0", got)
	}
	select {
	case ev := <-subscriber.events:
		t.Fatalf("unexpected event: %#v", ev)
	default:
	}
}
