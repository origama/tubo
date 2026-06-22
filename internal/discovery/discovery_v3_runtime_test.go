package discovery

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	pb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	capability "github.com/origama/tubo/internal/capability"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
)

type testV3RuntimeHarness struct {
	PubSubSubscriber
	testPrivKey      crypto.PrivKey
	testAuthorityKey ed25519.PrivateKey
	topic            string
	context          NamespaceDiscoveryContext
}

func testV3RuntimeContext(secretByte byte, keyID string) NamespaceDiscoveryContext {
	return NamespaceDiscoveryContext{
		ClusterID:   "cluster-123",
		NamespaceID: "tenant-a",
		KeyID:       keyID,
		Secret:      bytesRepeat(secretByte, 32),
	}
}

func bytesRepeat(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

func testV3SubscriberAndMessage(t *testing.T, ctx NamespaceDiscoveryContext, serviceName string, authorityPriv ed25519.PrivateKey) (*testV3RuntimeHarness, *pubsub.Message) {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub := authorityPriv.Public().(ed25519.PublicKey)
	membership := capability.MembershipCapability{
		ClusterID:     ctx.ClusterID,
		NamespaceID:   ctx.NamespaceID,
		SubjectPeerID: pid.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}
	membership, err = capability.SignMembershipCapability(membership, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	membershipBytes, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:             ctx.ClusterID,
		NamespaceID:           ctx.NamespaceID,
		ServiceID:             owner.ServiceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(owner.PublicKey),
		PublisherPeerID:       pid.String(),
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		Nonce:                 "test-v3-lease-nonce",
	}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, serviceName, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leaseBytes, err := json.Marshal(artifacts.Lease)
	if err != nil {
		t.Fatal(err)
	}
	claimBytes, err := json.Marshal(artifacts.Lease.ServiceClaim)
	if err != nil {
		t.Fatal(err)
	}
	payload := AnnouncementV3Payload{
		ClusterID:            ctx.ClusterID,
		NamespaceID:          ctx.NamespaceID,
		ServiceName:          serviceName,
		ServiceKind:          "http",
		ServiceID:            owner.ServiceID,
		ServicePublicKey:     serviceidentity.EncodePublicKey(owner.PublicKey),
		ConnectPolicy:        "namespace_members",
		Addresses:            []string{"/ip4/127.0.0.1/tcp/8080"},
		MembershipCapability: membershipBytes,
		PublishLease:         leaseBytes,
		ServiceClaim:         claimBytes,
		RegisteredAt:         time.Now().UTC().Add(-time.Second),
	}
	ann, err := NewAnnouncementV3(ctx, pid, 30*time.Second, payload)
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
	topic, err := DeriveNamespaceTopicV3(ctx)
	if err != nil {
		t.Fatal(err)
	}
	h := &testV3RuntimeHarness{
		PubSubSubscriber: PubSubSubscriber{
			scopes:             map[string]subscriberScope{topic: {expected: topic, mode: ModeNamespaceV3, clusterID: ctx.ClusterID, namespaceID: ctx.NamespaceID, context: &ctx}},
			cache:              NewCache(30*time.Second, time.Hour),
			authorityPublicKey: authorityPub,
			pubKey:             map[peer.ID]crypto.PubKey{pid: priv.GetPublic()},
			replay:             newAnnouncementReplayCache(16),
			events:             make(chan DiscoveryEvent, 4),
		},
		testPrivKey:      priv,
		testAuthorityKey: authorityPriv,
		topic:            topic,
		context:          ctx,
	}
	h.wireExpiredCallback()
	msg := &pubsub.Message{Message: &pb.Message{Data: data, From: []byte(pid), Topic: &topic, Key: mustPubKeyRaw(t, priv.GetPublic())}}
	return h, msg
}

func testV3AnnouncementFixture(t *testing.T, ctx NamespaceDiscoveryContext, serviceName string, authorityPriv ed25519.PrivateKey) (*testV3RuntimeHarness, AnnouncementV3, AnnouncementV3Payload) {
	t.Helper()
	h, msg := testV3SubscriberAndMessage(t, ctx, serviceName, authorityPriv)
	var ann AnnouncementV3
	if err := json.Unmarshal(msg.Data, &ann); err != nil {
		t.Fatal(err)
	}
	payload, err := ann.Payload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return h, ann, payload
}

func buildV3Announcement(t *testing.T, ctx NamespaceDiscoveryContext, signer crypto.PrivKey, payload AnnouncementV3Payload) AnnouncementV3 {
	t.Helper()
	pid, err := peer.IDFromPrivateKey(signer)
	if err != nil {
		t.Fatal(err)
	}
	ann, err := NewAnnouncementV3(ctx, pid, 30*time.Second, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := ann.Sign(signer); err != nil {
		t.Fatal(err)
	}
	return ann
}

func assertV3Accepted(t *testing.T, subscriber *testV3RuntimeHarness, wantCount int, wantService string) {
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

func assertV3Rejected(t *testing.T, subscriber *testV3RuntimeHarness) {
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

func TestPubSubSubscriberV3RejectsV2AnnouncementOnV3Topic(t *testing.T) {
	hV2, msgV2 := testV2SubscriberAndMessage(t, testV2Payload{serviceName: "legacy-svc", addresses: []string{"/ip4/127.0.0.1/tcp/8080"}})
	ctx := testV3RuntimeContext(0x11, "nsdk_current")
	topic, err := DeriveNamespaceTopicV3(ctx)
	if err != nil {
		t.Fatal(err)
	}
	msgV2.Topic = &topic
	subscriber := &testV3RuntimeHarness{
		PubSubSubscriber: PubSubSubscriber{
			scopes:             map[string]subscriberScope{topic: {expected: topic, mode: ModeNamespaceV3, clusterID: ctx.ClusterID, namespaceID: ctx.NamespaceID, context: &ctx}},
			cache:              NewCache(30*time.Second, time.Hour),
			authorityPublicKey: hV2.testAuthorityKey.Public().(ed25519.PublicKey),
			pubKey:             map[peer.ID]crypto.PubKey{msgV2.GetFrom(): hV2.testPrivKey.GetPublic()},
			replay:             newAnnouncementReplayCache(16),
			events:             make(chan DiscoveryEvent, 4),
		},
		topic:   topic,
		context: ctx,
	}
	subscriber.wireExpiredCallback()
	subscriber.handleMessage(msgV2)
	assertV3Rejected(t, subscriber)
}

func TestPubSubSubscriberV3RejectsWrongContext(t *testing.T) {
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	_ = authorityPub
	goodCtx := testV3RuntimeContext(0x11, "nsdk_current")
	wrongCtx := testV3RuntimeContext(0x22, "nsdk_wrong")
	subscriber, msg := testV3SubscriberAndMessage(t, goodCtx, "svc-current", authorityPriv)
	subscriber.scopes = map[string]subscriberScope{subscriber.topic: {expected: subscriber.topic, mode: ModeNamespaceV3, clusterID: wrongCtx.ClusterID, namespaceID: wrongCtx.NamespaceID, context: &wrongCtx}}
	subscriber.handleMessage(msg)
	assertV3Rejected(t, subscriber)
}

func TestPubSubSubscriberV3AcceptsCurrentAndPreviousTopics(t *testing.T) {
	_, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	currentCtx := testV3RuntimeContext(0x11, "nsdk_current")
	previousCtx := testV3RuntimeContext(0x22, "nsdk_previous")
	currentSub, currentMsg := testV3SubscriberAndMessage(t, currentCtx, "svc-current", authorityPriv)
	previousSub, previousMsg := testV3SubscriberAndMessage(t, previousCtx, "svc-previous", authorityPriv)
	subscriber := &testV3RuntimeHarness{
		PubSubSubscriber: PubSubSubscriber{
			scopes: map[string]subscriberScope{
				currentSub.topic:  {expected: currentSub.topic, mode: ModeNamespaceV3, clusterID: currentCtx.ClusterID, namespaceID: currentCtx.NamespaceID, context: &currentCtx},
				previousSub.topic: {expected: previousSub.topic, mode: ModeNamespaceV3, clusterID: previousCtx.ClusterID, namespaceID: previousCtx.NamespaceID, context: &previousCtx},
			},
			cache:              NewCache(30*time.Second, time.Hour),
			authorityPublicKey: authorityPriv.Public().(ed25519.PublicKey),
			pubKey: map[peer.ID]crypto.PubKey{
				currentMsg.GetFrom():  currentSub.testPrivKey.GetPublic(),
				previousMsg.GetFrom(): previousSub.testPrivKey.GetPublic(),
			},
			replay: newAnnouncementReplayCache(16),
			events: make(chan DiscoveryEvent, 8),
		},
	}
	subscriber.wireExpiredCallback()
	subscriber.handleMessage(currentMsg)
	assertV3Accepted(t, subscriber, 1, "svc-current")
	subscriber.handleMessage(previousMsg)
	assertV3Accepted(t, subscriber, 2, "svc-previous")
}

func TestPublisherPublishV3UsesCurrentTopicOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hPublisher, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "discovery-v3-publisher")
	if err != nil {
		t.Fatal(err)
	}
	defer hPublisher.Close()
	hObserver, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "discovery-v3-observer")
	if err != nil {
		t.Fatal(err)
	}
	defer hObserver.Close()
	info, err := p2p.AddrInfoFromString(p2p.PeerAddrs(hPublisher)[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := hObserver.Connect(ctx, info); err != nil {
		t.Fatal(err)
	}
	psPublisher, err := pubsub.NewGossipSub(ctx, hPublisher, pubsub.WithFloodPublish(true))
	if err != nil {
		t.Fatal(err)
	}
	psObserver, err := pubsub.NewGossipSub(ctx, hObserver, pubsub.WithFloodPublish(true))
	if err != nil {
		t.Fatal(err)
	}
	currentCtx := testV3RuntimeContext(0x11, "nsdk_current")
	previousCtx := testV3RuntimeContext(0x22, "nsdk_previous")
	currentTopicName, err := DeriveNamespaceTopicV3(currentCtx)
	if err != nil {
		t.Fatal(err)
	}
	previousTopicName, err := DeriveNamespaceTopicV3(previousCtx)
	if err != nil {
		t.Fatal(err)
	}
	currentTopicPublisher, err := psPublisher.Join(currentTopicName)
	if err != nil {
		t.Fatal(err)
	}
	currentTopicObserver, err := psObserver.Join(currentTopicName)
	if err != nil {
		t.Fatal(err)
	}
	previousTopicObserver, err := psObserver.Join(previousTopicName)
	if err != nil {
		t.Fatal(err)
	}
	currentSub, err := currentTopicObserver.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer currentSub.Cancel()
	previousSub, err := previousTopicObserver.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer previousSub.Cancel()
	privKey := hPublisher.Peerstore().PrivKey(hPublisher.ID())
	if privKey == nil {
		t.Fatal("missing publisher private key")
	}
	payload := AnnouncementV3Payload{ClusterID: currentCtx.ClusterID, NamespaceID: currentCtx.NamespaceID, ServiceName: "svc-current", ServiceKind: "http", ServiceID: "service-current", ServicePublicKey: "service-pub-current", ConnectPolicy: "namespace_members", Addresses: []string{"/ip4/127.0.0.1/tcp/8080"}, MembershipCapability: []byte("membership"), PublishLease: []byte("lease"), ServiceClaim: []byte("claim"), RegisteredAt: time.Now().UTC()}
	ann, err := NewAnnouncementV3(currentCtx, hPublisher.ID(), 30*time.Second, payload)
	if err != nil {
		t.Fatal(err)
	}
	publisher := NewPublisher(currentTopicPublisher, privKey)
	time.Sleep(300 * time.Millisecond)
	if err := publisher.PublishV3(ctx, ann); err != nil {
		t.Fatal(err)
	}
	if _, err := currentSub.Next(ctx); err != nil {
		t.Fatalf("expected current topic message: %v", err)
	}
	prevCtx, prevCancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer prevCancel()
	if _, err := previousSub.Next(prevCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected no previous-topic message, got %v", err)
	}
}

func TestPubSubSubscriberV3RequiresAuthorityPublicKey(t *testing.T) {
	_, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testV3RuntimeContext(0x11, "nsdk_current")
	subscriber, msg := testV3SubscriberAndMessage(t, ctx, "svc-current", authorityPriv)
	subscriber.authorityPublicKey = nil
	subscriber.handleMessage(msg)
	assertV3Rejected(t, subscriber)
}

func TestValidateAnnouncementV3AcrossContextsAcceptsValidAnnouncement(t *testing.T) {
	_, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testV3RuntimeContext(0x11, "nsdk_current")
	h, ann, _ := testV3AnnouncementFixture(t, ctx, "svc-current", authorityPriv)
	validated, err := ValidateAnnouncementV3(ctx, ann, h.testPrivKey.GetPublic(), authorityPriv.Public().(ed25519.PublicKey), ann.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if validated.ServiceName != "svc-current" || validated.ClusterID != ctx.ClusterID || validated.NamespaceID != ctx.NamespaceID || validated.ServiceID == "" || validated.TTL <= 0 {
		t.Fatalf("unexpected validated announcement: %#v", validated)
	}
}

func TestValidateAnnouncementV3AcrossContextsRejectsAuthorizationFailures(t *testing.T) {
	_, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorityPub := authorityPriv.Public().(ed25519.PublicKey)
	ctx := testV3RuntimeContext(0x11, "nsdk_current")
	h, ann, payload := testV3AnnouncementFixture(t, ctx, "svc-current", authorityPriv)
	basePub := h.testPrivKey.GetPublic()
	build := func(payload AnnouncementV3Payload) AnnouncementV3 {
		return buildV3Announcement(t, ctx, h.testPrivKey, payload)
	}

	t.Run("missing membership", func(t *testing.T) {
		mutated := payload
		mutated.MembershipCapability = nil
		ann := build(mutated)
		if _, err := ValidateAnnouncementV3(ctx, ann, basePub, authorityPub, ann.PeerID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "membership") {
			t.Fatalf("expected membership rejection, got %v", err)
		}
	})

	t.Run("expired membership with valid lease", func(t *testing.T) {
		mutated := payload
		var membership capability.MembershipCapability
		if err := json.Unmarshal(mutated.MembershipCapability, &membership); err != nil {
			t.Fatal(err)
		}
		membership.ExpiresAt = time.Now().Add(-time.Minute)
		signed, err := capability.SignMembershipCapability(membership, authorityPriv)
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(signed)
		if err != nil {
			t.Fatal(err)
		}
		mutated.MembershipCapability = b
		ann := build(mutated)
		if _, err := ValidateAnnouncementV3(ctx, ann, basePub, authorityPub, ann.PeerID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "expired") {
			t.Fatalf("expected expired membership rejection, got %v", err)
		}
	})

	t.Run("invalid publish lease", func(t *testing.T) {
		mutated := payload
		mutated.PublishLease = []byte("bad-lease")
		ann := build(mutated)
		if _, err := ValidateAnnouncementV3(ctx, ann, basePub, authorityPub, ann.PeerID); err == nil {
			t.Fatal("expected publish lease rejection")
		}
	})

	t.Run("wrong service id", func(t *testing.T) {
		mutated := payload
		mutated.ServiceID = "service-other"
		ann := build(mutated)
		if _, err := ValidateAnnouncementV3(ctx, ann, basePub, authorityPub, ann.PeerID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "invalid service_id") {
			t.Fatalf("expected service id rejection, got %v", err)
		}
	})

	t.Run("wrong peer binding", func(t *testing.T) {
		wrongPeer := peer.ID("12D3KooWBadPeer")
		if _, err := ValidateAnnouncementV3(ctx, ann, basePub, authorityPub, wrongPeer); err == nil || !strings.Contains(strings.ToLower(err.Error()), "peer id") {
			t.Fatalf("expected peer binding rejection, got %v", err)
		}
	})

	t.Run("wrong scope", func(t *testing.T) {
		wrongCtx := ctx
		wrongCtx.NamespaceID = "tenant-b"
		wrongCtx.Secret = append([]byte(nil), ctx.Secret...)
		if _, err := ValidateAnnouncementV3(wrongCtx, ann, basePub, authorityPub, ann.PeerID); err == nil {
			t.Fatal("expected wrong scope rejection")
		}
	})

	t.Run("bad signature", func(t *testing.T) {
		mutated := ann
		mutated.Signature = append([]byte(nil), ann.Signature...)
		mutated.Signature[0] ^= 0xff
		if _, err := ValidateAnnouncementV3(ctx, mutated, basePub, authorityPub, ann.PeerID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "signature") {
			t.Fatalf("expected signature rejection, got %v", err)
		}
	})
}

func buildV3ExpiryFixture(t *testing.T, membershipTTL, claimTTL, leaseTTL, announcementTTL time.Duration) (NamespaceDiscoveryContext, AnnouncementV3, crypto.PubKey, ed25519.PublicKey, peer.ID) {
	t.Helper()
	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signerPriv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	signerPeerID, err := peer.IDFromPrivateKey(signerPriv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testV3RuntimeContext(0x33, "nsdk_expiry")
	owner, err := serviceidentity.Generate()
	if err != nil {
		t.Fatal(err)
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, SubjectPeerID: signerPeerID.String(), Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionAnnounce}, ExpiresAt: time.Now().UTC().Add(membershipTTL)}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	membershipBytes, err := json.Marshal(membership)
	if err != nil {
		t.Fatal(err)
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceID: owner.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey), PublisherPeerID: signerPeerID.String(), RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "expiry-fixture-nonce"}, owner.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "svc-expiry", claimTTL, leaseTTL)
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
	ann, err := NewAnnouncementV3(ctx, signerPeerID, announcementTTL, AnnouncementV3Payload{ClusterID: ctx.ClusterID, NamespaceID: ctx.NamespaceID, ServiceName: "svc-expiry", ServiceKind: "http", ServiceID: owner.ServiceID, ServicePublicKey: serviceidentity.EncodePublicKey(owner.PublicKey), ConnectPolicy: "namespace_members", Addresses: []string{"/ip4/127.0.0.1/tcp/8080"}, MembershipCapability: membershipBytes, PublishLease: leaseBytes, ServiceClaim: claimBytes, RegisteredAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if err := ann.Sign(signerPriv); err != nil {
		t.Fatal(err)
	}
	return ctx, ann, signerPriv.GetPublic(), authorityPub, signerPeerID
}

func TestEffectiveAnnouncementV3ExpiryUsesEarliestBound(t *testing.T) {
	now := time.Date(2026, 6, 22, 19, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		bounds AnnouncementV3ExpiryBounds
		want   time.Time
	}{
		{name: "membership", bounds: AnnouncementV3ExpiryBounds{Announcement: now.Add(5 * time.Second), Membership: now.Add(2 * time.Second), PublishLease: now.Add(3 * time.Second), ServiceClaim: now.Add(4 * time.Second)}, want: now.Add(2 * time.Second)},
		{name: "service claim", bounds: AnnouncementV3ExpiryBounds{Announcement: now.Add(5 * time.Second), Membership: now.Add(4 * time.Second), PublishLease: now.Add(3 * time.Second), ServiceClaim: now.Add(1 * time.Second)}, want: now.Add(1 * time.Second)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := EffectiveAnnouncementV3Expiry(tc.bounds)
			if !got.Equal(tc.want) {
				t.Fatalf("expiry = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestValidateAnnouncementV3BoundsTTLByMembershipExpiry(t *testing.T) {
	ctx, ann, signerPub, authorityPub, _ := buildV3ExpiryFixture(t, 200*time.Millisecond, time.Second, time.Second, 5*time.Second)
	validated, err := ValidateAnnouncementV3(ctx, ann, signerPub, authorityPub, ann.PeerID)
	if err != nil {
		t.Fatal(err)
	}
	if validated.TTL > 400*time.Millisecond {
		t.Fatalf("ttl = %s, want membership-bound ttl", validated.TTL)
	}
	cache := NewCache(50*time.Millisecond, 10*time.Millisecond)
	defer cache.Stop()
	if err := cache.AddV2(validated.PeerID, validated.ClusterID, validated.NamespaceID, validated.ServiceID, validated.ServiceName, validated.Kind, validated.ServiceKind, validated.ServicePublicKey, validated.ConnectPolicy, validated.GrantService, validated.Addresses, validated.Capabilities, validated.TTL); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Resolve(validated.ServiceName); !ok {
		t.Fatal("expected service in cache")
	}
	time.Sleep(350 * time.Millisecond)
	if _, ok := cache.Resolve(validated.ServiceName); ok {
		t.Fatal("expected cache entry to expire when membership expires first")
	}
}

func TestValidateAnnouncementV3RejectsExpiredPublishLeaseBeforeMembership(t *testing.T) {
	ctx, ann, signerPub, authorityPub, _ := buildV3ExpiryFixture(t, time.Second, time.Second, 150*time.Millisecond, 5*time.Second)
	time.Sleep(250 * time.Millisecond)
	if _, err := ValidateAnnouncementV3(ctx, ann, signerPub, authorityPub, ann.PeerID); err == nil || !strings.Contains(strings.ToLower(err.Error()), "publish lease expired") {
		t.Fatalf("expected publish lease expiry rejection, got %v", err)
	}
}
