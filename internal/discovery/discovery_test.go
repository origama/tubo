package discovery_test

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"p2p-api-tunnel/internal/discovery"
)

// --- Announcement signing and verification ---

func TestAnnouncementSignAndVerify(t *testing.T) {
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pid, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		t.Fatal(err)
	}

	ann := &discovery.Announcement{
		ServiceName: "my-api",
		PeerID:      pid,
		Addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
		TTL:         30 * time.Second,
	}

	err = ann.Sign(privKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	pubKey := privKey.GetPublic()
	ok, err := ann.Verify(pubKey)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !ok {
		t.Error("Signature verification failed for valid announcement")
	}
}

func TestAnnouncementVerifyWrongKey(t *testing.T) {
	privKey1, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	pid1, _ := peer.IDFromPrivateKey(privKey1)

	ann := &discovery.Announcement{
		ServiceName: "my-api",
		PeerID:      pid1,
		Addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
		TTL:         30 * time.Second,
	}
	err = ann.Sign(privKey1)
	if err != nil {
		t.Fatal(err)
	}

	// Generate a different key and try to verify
	privKey2, _, _ := crypto.GenerateEd25519Key(nil)
	ok, err := ann.Verify(privKey2.GetPublic())
	if err == nil && ok {
		t.Error("Should have failed verification with wrong key")
	}
}

func TestAnnouncementVerifyTampered(t *testing.T) {
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := peer.IDFromPrivateKey(privKey)

	ann := &discovery.Announcement{
		ServiceName: "my-api",
		PeerID:      pid,
		Addresses:   []string{"/ip4/127.0.0.1/tcp/8080"},
		TTL:         30 * time.Second,
	}
	err = ann.Sign(privKey)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with the service name after signing
	ann.ServiceName = "hacked-api"
	ok, err := ann.Verify(privKey.GetPublic())
	if err == nil && ok {
		t.Error("Should have failed verification for tampered announcement")
	}
}

func TestAnnouncementMarshalUnmarshal(t *testing.T) {
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := peer.IDFromPrivateKey(privKey)

	ann := &discovery.Announcement{
		ServiceName: "my-api",
		PeerID:      pid,
		Addresses:   []string{"/ip4/127.0.0.1/tcp/8080", "/ip6::1/tcp/9090"},
		TTL:         30 * time.Second,
	}
	err = ann.Sign(privKey)
	if err != nil {
		t.Fatal(err)
	}

	// Marshal to bytes and back
	data, err := ann.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := &discovery.Announcement{}
	err = got.Unmarshal(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ServiceName != ann.ServiceName {
		t.Errorf("ServiceName: got %q, want %q", got.ServiceName, ann.ServiceName)
	}
	if got.PeerID != ann.PeerID {
		t.Errorf("PeerID: got %s, want %s", got.PeerID, ann.PeerID)
	}
	if len(got.Addresses) != len(ann.Addresses) {
		t.Errorf("Addresses length: got %d, want %d", len(got.Addresses), len(ann.Addresses))
	}
	ok, err := got.Verify(privKey.GetPublic())
	if err != nil || !ok {
		t.Error("Unmarshaled announcement should verify correctly")
	}
}

// --- Discovery Cache tests ---

func TestCacheAddAndResolve(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, 10*time.Minute)
	pid := peer.ID("test-peer-123")

	err := cache.Add(pid, "my-api", []string{"/ip4/1.2.3.4/tcp/8080"})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	entry, ok := cache.Resolve("my-api")
	if !ok {
		t.Fatal("Resolve should find the service")
	}
	if entry.PeerID != pid {
		t.Errorf("PeerID: got %s, want %s", entry.PeerID, pid)
	}
	if len(entry.Addresses) != 1 || entry.Addresses[0] != "/ip4/1.2.3.4/tcp/8080" {
		t.Errorf("Addresses: got %+v", entry.Addresses)
	}
}

func TestCacheExpiry(t *testing.T) {
	cache := discovery.NewCache(50*time.Millisecond, 1*time.Second)
	pid := peer.ID("expiring-peer")

	err := cache.Add(pid, "short-lived", []string{"/ip4/1.2.3.4/tcp/8080"})
	if err != nil {
		t.Fatal(err)
	}

	// Should exist now
	_, ok := cache.Resolve("short-lived")
	if !ok {
		t.Fatal("Should find service before expiry")
	}

	// Wait for TTL to expire + cleanup interval
	time.Sleep(200 * time.Millisecond)

	_, ok = cache.Resolve("short-lived")
	if ok {
		t.Error("Service should have expired")
	}
}

func TestCacheUpdate(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, 10*time.Minute)
	pid := peer.ID("test-peer")

	err := cache.Add(pid, "my-api", []string{"/ip4/1.2.3.4/tcp/8080"})
	if err != nil {
		t.Fatal(err)
	}

	// Update with new address
	err = cache.Add(pid, "my-api", []string{"/ip4/5.6.7.8/tcp/9090"})
	if err != nil {
		t.Fatal(err)
	}

	entry, ok := cache.Resolve("my-api")
	if !ok {
		t.Fatal("Should still find service after update")
	}
	if entry.Addresses[0] != "/ip4/5.6.7.8/tcp/9090" {
		t.Errorf("Address should be updated: got %v", entry.Addresses)
	}
}

func TestCacheMultipleServices(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, 10*time.Minute)
	peerA := peer.ID("peer-a")
	peerB := peer.ID("peer-b")

	err := cache.Add(peerA, "service-a", []string{"/ip4/1.1.1.1/tcp/80"})
	if err != nil {
		t.Fatal(err)
	}
	err = cache.Add(peerB, "service-b", []string{"/ip4/2.2.2.2/tcp/80"})
	if err != nil {
		t.Fatal(err)
	}

	entryA, ok := cache.Resolve("service-a")
	if !ok || entryA.PeerID != peerA {
		t.Errorf("Should resolve service-a correctly: found=%v, gotPeer=%s, wantPeer=%s", ok, func() string { if entryA != nil { return entryA.PeerID.String() }; return "nil" }(), peerA.String())
	}

	entryB, ok := cache.Resolve("service-b")
	if !ok || entryB.PeerID != peerB {
		t.Errorf("Should resolve service-b correctly: found=%v, gotPeer=%s, wantPeer=%s", ok, func() string { if entryB != nil { return entryB.PeerID.String() }; return "nil" }(), peerB.String())
	}
}

func TestCacheResolveUnknown(t *testing.T) {
	cache := discovery.NewCache(30*time.Second, 10*time.Minute)
	_, ok := cache.Resolve("nonexistent")
	if ok {
		t.Error("Should not find unknown service")
	}
}

// --- Heartbeat tests ---

func TestHeartbeatRenewal(t *testing.T) {
	cache := discovery.NewCache(200*time.Millisecond, 500*time.Millisecond)
	pid := peer.ID("heartbeat-peer")

	err := cache.Add(pid, "hb-service", []string{"/ip4/1.2.3.4/tcp/8080"})
	if err != nil {
		t.Fatal(err)
	}

	// Wait a bit, then renew (simulating heartbeat)
	time.Sleep(60 * time.Millisecond)
	err = cache.Add(pid, "hb-service", []string{"/ip4/1.2.3.4/tcp/8080"})
	if err != nil {
		t.Fatal(err)
	}

	// Wait past original TTL — should still exist because we renewed
	time.Sleep(60 * time.Millisecond)

	_, ok := cache.Resolve("hb-service")
	if !ok {
		t.Error("Service should still be alive after heartbeat renewal")
	}
}