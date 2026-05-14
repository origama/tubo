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
)

func TestPubSubSubscriberV2AcceptsValidAnnouncement(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName: "myapi",
		addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
	})

	subscriber.handleMessageV2(msg)
	assertV2Accepted(t, subscriber, 1, "myapi")
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
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsExpiredServiceClaim(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:           "myapi",
		addresses:             []string{"/ip4/127.0.0.1/tcp/8080"},
		serviceClaimExpiresAt: time.Now().Add(-time.Minute),
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
	})

	subscriber.handleMessageV2(msg)
	assertV2Rejected(t, subscriber)
}

func TestPubSubSubscriberV2RejectsServiceClaimForDifferentServiceID(t *testing.T) {
	subscriber, msg := testV2SubscriberAndMessage(t, testV2Payload{
		serviceName:           "myapi",
		addresses:             []string{"/ip4/127.0.0.1/tcp/8080"},
		serviceClaimServiceID: "other-service",
		serviceClaimPerms:     []string{capability.PermissionAttach, capability.PermissionAnnounce},
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
	serviceName              string
	serviceID                string
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
}

type testV2Harness struct {
	PubSubSubscriber
	testPrivKey crypto.PrivKey
	topic       string
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
	serviceID := payload.serviceID
	if serviceID == "" {
		serviceID = "service-myapi"
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
	membershipBytes, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	ann, err := NewAnnouncementV2("cluster-123", "tenant-a", pid, payload.ttl, AnnouncementV2Payload{
		ServiceName:          payload.serviceName,
		ServiceID:            serviceID,
		Addresses:            payload.addresses,
		MembershipCapability: membershipBytes,
		ServiceClaim:         serviceClaim,
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
		testPrivKey: priv,
		topic:       topic,
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
