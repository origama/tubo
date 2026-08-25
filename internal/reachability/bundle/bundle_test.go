package bundle

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// testRoot builds a dedicated bootstrap-role trust root with a fresh key.
func testRoot(t *testing.T, role string) (Root, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Root{KeyID: "test-root-a", Role: role, Public: pub}, priv
}

// testID returns a fresh libp2p peer ID for bundle payloads.
func testID(t *testing.T) (string, string) {
	t.Helper()
	priv, _, err := libcrypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	id, err := peer.IDFromPublicKey(priv.GetPublic())
	if err != nil {
		t.Fatal(err)
	}
	return id.String(), ""
}

// validRelayAddr builds a multiaddr carrying the given peer identity.
func validRelayAddr(t *testing.T, id string) string {
	t.Helper()
	if _, err := peer.Decode(id); err != nil {
		t.Fatal(err)
	}
	return "/ip4/127.0.0.1/tcp/41000/p2p/" + id
}

func nowUTC() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) }

// write8/write16 mirror the production canonical field encoding for use in
// verify-path tests that must inspect decode/verify behavior that Sign() itself
// would reject (downgraded/unknown version, duplicate or identity-mismatched
// address).
func write8(out *bytes.Buffer, value string) {
	_ = binary.Write(out, binary.BigEndian, uint8(len(value)))
	out.WriteString(value)
}
func write16(out *bytes.Buffer, value string) {
	_ = binary.Write(out, binary.BigEndian, uint16(len(value)))
	out.WriteString(value)
}
func rawUnsigned(version uint32, kind, keyID, peerID string, addrs []string, notBefore, notAfter time.Time) []byte {
	var out bytes.Buffer
	out.WriteString(Magic)
	_ = binary.Write(&out, binary.BigEndian, version)
	write8(&out, kind)
	write8(&out, keyID)
	write8(&out, peerID)
	_ = binary.Write(&out, binary.BigEndian, uint16(len(addrs)))
	for _, a := range addrs {
		write16(&out, a)
	}
	_ = binary.Write(&out, binary.BigEndian, notBefore.UnixNano())
	_ = binary.Write(&out, binary.BigEndian, notAfter.UnixNano())
	return out.Bytes()
}
func appendSigned(unsigned []byte, priv ed25519.PrivateKey) []byte {
	sig := ed25519.Sign(priv, unsigned)
	var out bytes.Buffer
	out.Write(unsigned)
	_ = binary.Write(&out, binary.BigEndian, uint8(len(sig)))
	out.Write(sig)
	return out.Bytes()
}

func TestCanonicalBytesDeterministic(t *testing.T) {
	root, priv := testRoot(t, RoleBootstrap)
	id, _ := testID(t)
	b := &Bundle{
		Version:   1,
		Kind:      KindBootstrap,
		RootKeyID: root.KeyID,
		PeerID:    id,
		Addresses: []string{validRelayAddr(t, id)},
		NotBefore: nowUTC(),
		NotAfter:  nowUTC().Add(24 * time.Hour),
	}
	blob, err := Sign(b, priv)
	if err != nil {
		t.Fatal(err)
	}
	// Determinism: an equivalent bundle produces byte-identical signed bytes.
	again, err := Sign(&Bundle{Version: b.Version, Kind: b.Kind, RootKeyID: b.RootKeyID, PeerID: b.PeerID, Addresses: append([]string(nil), b.Addresses...), NotBefore: b.NotBefore, NotAfter: b.NotAfter}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(blob, again) {
		t.Fatal("canonical signed bytes are not deterministic")
	}
	got, err := ParseVerify(blob, []Root{root}, nowUTC())
	if err != nil {
		t.Fatal(err)
	}
	if got.PeerID != id || got.RootKeyID != root.KeyID || len(got.Addresses) != 1 || got.NotBefore != b.NotBefore || got.NotAfter != b.NotAfter {
		t.Fatalf("verified bundle did not round-trip: %#v", got)
	}
}

func TestValidBundleAccepts(t *testing.T) {
	root, priv := testRoot(t, RoleBootstrap)
	id, _ := testID(t)
	blob, err := Sign(&Bundle{
		Version:   1,
		Kind:      KindBootstrap,
		RootKeyID: root.KeyID,
		PeerID:    id,
		Addresses: []string{validRelayAddr(t, id)},
		NotBefore: nowUTC().Add(-time.Hour),
		NotAfter:  nowUTC().Add(time.Hour),
	}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseVerify(blob, []Root{root}, nowUTC()); err != nil {
		t.Fatalf("valid bundle rejected: %v", err)
	}
}

func TestRejectsWrongRootAndUnpinnedRoot(t *testing.T) {
	rootA, privA := testRoot(t, RoleBootstrap)
	rootA.KeyID = "root-a"
	_, privB := testRoot(t, RoleBootstrap)
	id, _ := testID(t)
	mk := func(rootKeyID string, priv ed25519.PrivateKey) []byte {
		t.Helper()
		blob, err := Sign(&Bundle{Version: 1, Kind: KindBootstrap, RootKeyID: rootKeyID, PeerID: id, Addresses: []string{validRelayAddr(t, id)}, NotBefore: nowUTC().Add(-time.Hour), NotAfter: nowUTC().Add(time.Hour)}, priv)
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}
	// Signed by B but claims root A: pinned A's key fails the signature.
	if _, err := ParseVerify(mk("root-a", privB), []Root{rootA}, nowUTC()); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("wrong-issuer bundle accepted: %v", err)
	}
	// References an unpinned root: rejected before signature (no TOFU).
	if _, err := ParseVerify(mk("root-unknown", privA), []Root{rootA}, nowUTC()); !errors.Is(err, ErrUnpinnedRoot) {
		t.Fatalf("unpinned root accepted: %v", err)
	}
}

func TestRejectsWrongRole(t *testing.T) {
	authRoot, priv := testRoot(t, "tubo.authority.v1") // authority-role root
	id, _ := testID(t)
	blob, err := Sign(&Bundle{Version: 1, Kind: KindBootstrap, RootKeyID: authRoot.KeyID, PeerID: id, Addresses: []string{validRelayAddr(t, id)}, NotBefore: nowUTC().Add(-time.Hour), NotAfter: nowUTC().Add(time.Hour)}, priv)
	if err != nil {
		t.Fatal(err)
	}
	// The bundle references a pinned key, but that key's role is not the
	// bootstrap role, so it must never be accepted as reachability trust.
	if _, err := ParseVerify(blob, []Root{authRoot}, nowUTC()); !errors.Is(err, ErrUnpinnedRoot) {
		t.Fatalf("authority-role root accepted for reachability: %v", err)
	}
}

func TestRejectsVersionUnknownAndDowngrade(t *testing.T) {
	root, priv := testRoot(t, RoleBootstrap)
	id, _ := testID(t)
	addr := validRelayAddr(t, id)
	window := struct{ n, a time.Time }{nowUTC(), nowUTC().Add(time.Hour)}
	// Unknown (higher) version is rejected, even when the signature is valid.
	unknown := appendSigned(rawUnsigned(2, KindBootstrap, root.KeyID, id, []string{addr}, window.n, window.a), priv)
	if _, err := ParseVerify(unknown, []Root{root}, nowUTC()); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("unknown version accepted: %v", err)
	}
	// Downgrade below the minimum floor is rejected without an unsigned fallback.
	downgraded := appendSigned(rawUnsigned(0, KindBootstrap, root.KeyID, id, []string{addr}, window.n, window.a), priv)
	if _, err := ParseVerify(downgraded, []Root{root}, nowUTC()); !errors.Is(err, ErrDowngrade) {
		t.Fatalf("downgraded version accepted: %v", err)
	}
}

func TestRejectsExpiryAndFuture(t *testing.T) {
	root, priv := testRoot(t, RoleBootstrap)
	id, _ := testID(t)
	expired, err := Sign(&Bundle{Version: 1, Kind: KindBootstrap, RootKeyID: root.KeyID, PeerID: id, Addresses: []string{validRelayAddr(t, id)}, NotBefore: nowUTC().Add(-2 * time.Hour), NotAfter: nowUTC().Add(-time.Hour)}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseVerify(expired, []Root{root}, nowUTC()); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired bundle accepted: %v", err)
	}
	future, err := Sign(&Bundle{Version: 1, Kind: KindBootstrap, RootKeyID: root.KeyID, PeerID: id, Addresses: []string{validRelayAddr(t, id)}, NotBefore: nowUTC().Add(time.Hour), NotAfter: nowUTC().Add(2 * time.Hour)}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseVerify(future, []Root{root}, nowUTC()); !errors.Is(err, ErrNotYetValid) {
		t.Fatalf("not-yet-valid bundle accepted: %v", err)
	}
	// No clock-skew grace: a window whose not_after equals now fails closed as
	// expired with no allowance.
	edge, err := Sign(&Bundle{Version: 1, Kind: KindBootstrap, RootKeyID: root.KeyID, PeerID: id, Addresses: []string{validRelayAddr(t, id)}, NotBefore: nowUTC().Add(-time.Hour), NotAfter: nowUTC()}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseVerify(edge, []Root{root}, nowUTC()); !errors.Is(err, ErrExpired) {
		t.Fatalf("boundary-expired bundle accepted: %v", err)
	}
}

func TestRejectsMalformedAndTrailingAndWrongMagic(t *testing.T) {
	root, priv := testRoot(t, RoleBootstrap)
	id, _ := testID(t)
	blob, err := Sign(&Bundle{Version: 1, Kind: KindBootstrap, RootKeyID: root.KeyID, PeerID: id, Addresses: []string{validRelayAddr(t, id)}, NotBefore: nowUTC().Add(-time.Hour), NotAfter: nowUTC().Add(time.Hour)}, priv)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseVerify(append(append([]byte(nil), blob...), []byte("trailing")...), []Root{root}, nowUTC()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("trailing bytes accepted: %v", err)
	}
	tampered := append([]byte(nil), blob...)
	tampered[0] ^= 0xff
	if _, err := ParseVerify(tampered, []Root{root}, nowUTC()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("wrong magic accepted: %v", err)
	}
	if _, err := ParseVerify(blob[:8], []Root{root}, nowUTC()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("truncated bundle accepted: %v", err)
	}
}

func TestRejectsOversizedAndDuplicateAndIdentityMismatch(t *testing.T) {
	root, priv := testRoot(t, RoleBootstrap)
	id, _ := testID(t)
	other, _ := testID(t)
	window := struct{ n, a time.Time }{nowUTC().Add(-time.Hour), nowUTC().Add(time.Hour)}
	// Duplicate addresses are rejected at decode strictly before trust use.
	dup := appendSigned(rawUnsigned(1, KindBootstrap, root.KeyID, id, []string{validRelayAddr(t, id), validRelayAddr(t, id)}, window.n, window.a), priv)
	if _, err := ParseVerify(dup, []Root{root}, nowUTC()); !errors.Is(err, ErrDuplicateAddrs) {
		t.Fatalf("duplicate address accepted: %v", err)
	}
	// An address embedding a different peer identity is an identity mismatch.
	mismatch := appendSigned(rawUnsigned(1, KindBootstrap, root.KeyID, id, []string{validRelayAddr(t, other)}, window.n, window.a), priv)
	if _, err := ParseVerify(mismatch, []Root{root}, nowUTC()); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("identity/address mismatch accepted: %v", err)
	}
	// Address missing a /p2p/ component.
	noP2P := appendSigned(rawUnsigned(1, KindBootstrap, root.KeyID, id, []string{"/dns4/relay.invalid/tcp/4001"}, window.n, window.a), priv)
	if _, err := ParseVerify(noP2P, []Root{root}, nowUTC()); !errors.Is(err, ErrMalformed) {
		t.Fatalf("address without peer identity accepted: %v", err)
	}
	// oversized overall blob
	if _, err := ParseVerify(bytes.Repeat([]byte{'x'}, MaxBundleSize+1), []Root{root}, nowUTC()); !errors.Is(err, ErrOversized) {
		t.Fatalf("oversized blob accepted: %v", err)
	}
}

func TestRejectsEmptyAddressAtSign(t *testing.T) {
	root, priv := testRoot(t, RoleBootstrap)
	id, _ := testID(t)
	if _, err := Sign(&Bundle{Version: 1, Kind: KindBootstrap, RootKeyID: root.KeyID, PeerID: id, Addresses: []string{}, NotBefore: nowUTC().Add(-time.Hour), NotAfter: nowUTC().Add(time.Hour)}, priv); !errors.Is(err, ErrEmptyAddress) {
		t.Fatalf("empty-address bundle sign accepted: %v", err)
	}
	if _, err := Sign(&Bundle{Version: 1, Kind: KindBootstrap, RootKeyID: root.KeyID, PeerID: id, Addresses: []string{"/dns4/relay.invalid/tcp/4001"}, NotBefore: nowUTC().Add(-time.Hour), NotAfter: nowUTC().Add(time.Hour)}, priv); err == nil {
		t.Fatal("address without peer identity accepted at sign")
	}
}
