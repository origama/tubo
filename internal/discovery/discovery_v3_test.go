package discovery_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/origama/tubo/internal/discovery"
	grantspkg "github.com/origama/tubo/internal/grants"
)

func testV3Context(secretByte byte) discovery.NamespaceDiscoveryContext {
	secret := bytes.Repeat([]byte{secretByte}, 32)
	return discovery.NamespaceDiscoveryContext{
		ClusterID:   "cluster-123",
		NamespaceID: "tenant-a",
		KeyID:       "nsdk_20260602_abcd1234",
		Secret:      secret,
	}
}

func testV3Payload() discovery.AnnouncementV3Payload {
	return discovery.AnnouncementV3Payload{
		ClusterID:            "cluster-123",
		NamespaceID:          "tenant-a",
		ServiceName:          "my-api",
		ServiceKind:          "http",
		ServiceID:            "service-123",
		ServicePublicKey:     "service-pub-123",
		ConnectPolicy:        "namespace_members",
		GrantService:         &grantspkg.GrantServiceEndpoint{Protocol: grantspkg.ProtocolID, Peers: []string{"/ip4/1.2.3.4/tcp/4001/p2p/12D3KooWGrant"}},
		Addresses:            []string{"/ip4/127.0.0.1/tcp/8080"},
		MembershipCapability: []byte("membership-capability-bytes"),
		PublishLease:         []byte("publish-lease-bytes"),
		ServiceClaim:         []byte("service-claim-bytes"),
		Capabilities:         []string{"raw-tcp-v1"},
		RegisteredAt:         time.Date(2026, 6, 2, 17, 0, 0, 0, time.UTC),
	}
}

func TestNamespaceTopicV3IsOpaqueAndStable(t *testing.T) {
	ctxA := testV3Context(0x11)
	ctxB := testV3Context(0x11)
	ctxKeyChanged := testV3Context(0x11)
	ctxKeyChanged.KeyID = "nsdk_20260602_ffff0000"
	ctxSecretChanged := testV3Context(0x22)

	topicA, err := discovery.DeriveNamespaceTopicV3(ctxA)
	if err != nil {
		t.Fatal(err)
	}
	topicB, err := discovery.DeriveNamespaceTopicV3(ctxB)
	if err != nil {
		t.Fatal(err)
	}
	topicKeyChanged, err := discovery.DeriveNamespaceTopicV3(ctxKeyChanged)
	if err != nil {
		t.Fatal(err)
	}
	topicSecretChanged, err := discovery.DeriveNamespaceTopicV3(ctxSecretChanged)
	if err != nil {
		t.Fatal(err)
	}
	if topicA != topicB {
		t.Fatalf("topic not stable: %q != %q", topicA, topicB)
	}
	if topicA == topicKeyChanged {
		t.Fatalf("topic should change when key id changes: %q", topicA)
	}
	if topicA == topicSecretChanged {
		t.Fatalf("topic should change when secret changes: %q", topicA)
	}
	if !bytes.HasPrefix([]byte(topicA), []byte("/discovery/v3/")) {
		t.Fatalf("unexpected topic prefix: %q", topicA)
	}
	for _, leaked := range []string{"cluster-123", "tenant-a", "nsdk_20260602_abcd1234"} {
		if bytes.Contains([]byte(topicA), []byte(leaked)) {
			t.Fatalf("topic leaks cleartext %q: %q", leaked, topicA)
		}
	}
}

func TestAnnouncementV3PayloadKeyUsesDistinctDerivationLabel(t *testing.T) {
	ctx := testV3Context(0x11)
	topic, err := discovery.DeriveNamespaceTopicV3(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payloadKey, err := discovery.DeriveAnnouncementV3PayloadKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payloadKey, []byte(topic)) || bytes.Equal(payloadKey, []byte(topic)) {
		t.Fatalf("payload key derivation unexpectedly overlaps topic id: %q vs %x", topic, payloadKey)
	}
}

func TestAnnouncementV3SignVerifyAndDecrypt(t *testing.T) {
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pid, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testV3Context(0x11)
	payload := testV3Payload()
	ann, err := discovery.NewAnnouncementV3(ctx, pid, 45*time.Second, payload)
	if err != nil {
		t.Fatalf("new announcement v3: %v", err)
	}
	if err := ann.Sign(privKey); err != nil {
		t.Fatalf("sign: %v", err)
	}
	ok, err := ann.Verify(privKey.GetPublic())
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Fatal("expected valid v3 signature")
	}
	raw, err := ann.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, leaked := range [][]byte{[]byte("cluster-123"), []byte("tenant-a"), []byte("my-api"), []byte("/ip4/127.0.0.1/tcp/8080"), []byte("12D3KooWGrant")} {
		if bytes.Contains(raw, leaked) {
			t.Fatalf("cleartext data leaked in public envelope: %q in %s", leaked, raw)
		}
	}
	var got discovery.AnnouncementV3
	if err := got.Unmarshal(raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	decoded, err := got.Payload(ctx)
	if err != nil {
		t.Fatalf("payload decrypt: %v", err)
	}
	if decoded.ClusterID != payload.ClusterID || decoded.NamespaceID != payload.NamespaceID || decoded.ServiceName != payload.ServiceName {
		t.Fatalf("unexpected decoded payload: %#v", decoded)
	}
	if len(decoded.Addresses) != 1 || decoded.Addresses[0] != payload.Addresses[0] {
		t.Fatalf("addresses = %#v want %#v", decoded.Addresses, payload.Addresses)
	}
	ctxWrong := testV3Context(0x22)
	if _, err := got.Payload(ctxWrong); err == nil {
		t.Fatal("expected mismatched secret context to fail")
	}
}

func TestAnnouncementV3RejectsTampering(t *testing.T) {
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pid, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		t.Fatal(err)
	}
	ctx := testV3Context(0x11)
	ann, err := discovery.NewAnnouncementV3(ctx, pid, 45*time.Second, testV3Payload())
	if err != nil {
		t.Fatal(err)
	}
	if err := ann.Sign(privKey); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		mut  func(*discovery.AnnouncementV3)
	}{
		{name: "ciphertext", mut: func(a *discovery.AnnouncementV3) { a.Ciphertext[0] ^= 0xff }},
		{name: "nonce", mut: func(a *discovery.AnnouncementV3) { a.Nonce[0] ^= 0xff }},
		{name: "key id", mut: func(a *discovery.AnnouncementV3) { a.KeyID = a.KeyID + "-tampered" }},
		{name: "peer id", mut: func(a *discovery.AnnouncementV3) { a.PeerID = peer.ID("12D3KooWBadPeer") }},
		{name: "ttl", mut: func(a *discovery.AnnouncementV3) { a.TTL += time.Second }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mutated := ann
			mutated.Nonce = append([]byte(nil), ann.Nonce...)
			mutated.Ciphertext = append([]byte(nil), ann.Ciphertext...)
			mutated.Signature = append([]byte(nil), ann.Signature...)
			tc.mut(&mutated)
			ok, err := mutated.Verify(privKey.GetPublic())
			if err != nil {
				t.Fatalf("verify error: %v", err)
			}
			if ok {
				t.Fatalf("expected tampered %s announcement to fail verification", tc.name)
			}
		})
	}
}
