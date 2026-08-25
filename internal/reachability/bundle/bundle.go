// Package bundle defines and verifies the bounded, signed public-bootstrap
// bundle contract (A0 issue #44).
//
// A bootstrap bundle is L1 Reachability provider metadata and nothing more: it
// identifies a relay/bootstrap node by peer identity and dial addresses, states
// its validity window, and references the dedicated signing root that vouched
// for it. It never carries Scope membership, lease, or authorization semantics,
// and it never embeds secrets or service/target metadata.
//
// Trust-root model (no TOFU): the verifying party pins every accepted root;
// a bundle may reference a root by key ID but can never introduce a new public
// trust root, and a root's declared role must be the Reachability bootstrap
// role. This deliberately keeps L1 bootstrap trust separate from any L3
// Exact/Scope Authority trust: both the historical tubo bundle-signing key and
// the A0 contract forbid reusing an authority key as a bootstrap root (and vice
// versa).
//
// The wire format is a fixed deterministic big-endian binary encoding with
// explicit per-field bounds. Decoding validates every length against the
// (bounded) input before allocation and rejects malformed, oversized, trailing,
// duplicate, or identity/address-inconsistent payloads fail closed.
package bundle

import (
	"bytes"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// KindBootstrap is the only accepted bundle kind.
	KindBootstrap = "bootstrap"

	// RoleBootstrap is the dedicated L1 Reachability signing-key role. A
	// pinned root whose role differs (e.g. an authority role) is rejected so a
	// Reachability bundle can never borrow or elevate an authority trust root.
	RoleBootstrap = "tubo.bootstrap.v1"

	// Magic is the fixed leading marker of the wire encoding.
	Magic = "TUBN"

	// MinVersion and MaxVersion bound the supported bundle version. Version 1
	// is the only accepted version this slice understands; anything below the
	// floor is a downgrade and anything above is unknown.
	MinVersion = 1
	MaxVersion = 1

	// SignatureLen is the fixed Ed25519 signature length.
	SignatureLen = ed25519.SignatureSize

	// MaxBundleSize bounds the whole encoded bundle (including signature)
	// before any field is parsed. Together with per-field bounds below this
	// guarantees the decoder never allocates without a bound.
	MaxBundleSize = 16 << 10

	// Per-field decode bounds. Lengths are validated against the remaining
	// buffer and against these upper bounds before any slice.
	MaxKindLen   = 64
	MaxKeyIDLen  = 128
	MaxPeerIDLen = 128
	MaxAddrLen   = 4096
	MaxAddrs     = 64
)

// Sentinel decoder/verifier errors. Callers may classify fail-closed outcomes
// by matching these with errors.Is; each maps to exactly one strict check.
var (
	ErrMalformed        = errors.New("malformed bootstrap bundle")
	ErrOversized        = errors.New("bootstrap bundle exceeds size bound")
	ErrUnknownVersion   = errors.New("bootstrap bundle version not supported")
	ErrDowngrade        = errors.New("bootstrap bundle version is below the minimum supported")
	ErrUnpinnedRoot     = errors.New("bootstrap bundle references an unpinned or wrong-role trust root")
	ErrDuplicateAddrs   = errors.New("bootstrap bundle repeats an address")
	ErrIdentityMismatch = errors.New("bootstrap bundle address does not match its peer identity")
	ErrInvalidSignature = errors.New("bootstrap bundle signature is invalid")
	ErrNotYetValid      = errors.New("bootstrap bundle is not yet within its validity window")
	ErrExpired          = errors.New("bootstrap bundle validity window has expired")
	ErrEmptyAddress     = errors.New("bootstrap bundle has no addresses")
	ErrInvalidValidity  = errors.New("bootstrap bundle validity window is invalid")
)

// Root is a preinstalled verification root. Roots are pinned by the verifying
// party; a bundle can reference a RootKeyID but can never introduce a root.
type Root struct {
	KeyID  string
	Role   string
	Public ed25519.PublicKey
}

// Validity is a strict validity window with no clock-skew grace.
type Validity struct {
	NotBefore time.Time
	NotAfter  time.Time
}

// Bundle is the decoded, semantically validated reachability bundle content.
type Bundle struct {
	Version   uint32
	Kind      string
	RootKeyID string
	PeerID    string
	Addresses []string
	NotBefore time.Time
	NotAfter  time.Time
}

// canonical returns the unsigned canonical bytes that the signature covers.
func (b *Bundle) canonical() ([]byte, error) {
	if b == nil {
		return nil, ErrMalformed
	}
	if b.Kind != KindBootstrap {
		return nil, fmt.Errorf("%w: kind", ErrMalformed)
	}
	if b.Version < MinVersion || b.Version > MaxVersion {
		return nil, fmt.Errorf("%w: version", ErrMalformed)
	}
	if b.RootKeyID == "" || len(b.RootKeyID) > MaxKeyIDLen {
		return nil, fmt.Errorf("%w: root key id", ErrMalformed)
	}
	if len(b.PeerID) == 0 || len(b.PeerID) > MaxPeerIDLen {
		return nil, fmt.Errorf("%w: peer id", ErrMalformed)
	}
	if _, err := peer.Decode(b.PeerID); err != nil {
		return nil, fmt.Errorf("%w: peer id", ErrMalformed)
	}
	if len(b.Addresses) == 0 {
		return nil, ErrEmptyAddress
	}
	if len(b.Addresses) > MaxAddrs {
		return nil, fmt.Errorf("%w: addresses", ErrMalformed)
	}
	if !b.NotAfter.After(b.NotBefore) {
		return nil, ErrInvalidValidity
	}

	var out bytes.Buffer
	out.WriteString(Magic)
	_ = binary.Write(&out, binary.BigEndian, uint32(b.Version))
	writeLen8(&out, b.Kind)
	writeLen8(&out, b.RootKeyID)
	writeLen8(&out, b.PeerID)
	_ = binary.Write(&out, binary.BigEndian, uint16(len(b.Addresses)))
	seen := make(map[string]struct{}, len(b.Addresses))
	for _, addr := range b.Addresses {
		if len(addr) > MaxAddrLen {
			return nil, fmt.Errorf("%w: address length", ErrMalformed)
		}
		if _, dup := seen[addr]; dup {
			return nil, ErrDuplicateAddrs
		}
		seen[addr] = struct{}{}
		if err := validateAddress(addr, b.PeerID); err != nil {
			return nil, err
		}
		writeLen16(&out, addr)
	}
	_ = binary.Write(&out, binary.BigEndian, b.NotBefore.UnixNano())
	_ = binary.Write(&out, binary.BigEndian, b.NotAfter.UnixNano())
	return out.Bytes(), nil
}

// Sign produces a canonical, signature-covered bootstrap blob for the given
// root private key. The root's KeyID and Role are bound into RootKeyID so the
// blob carries together with the message; the verifying side must pin the same
// KeyID and the RoleBootstrap role.
func Sign(b *Bundle, private ed25519.PrivateKey) ([]byte, error) {
	unsigned, err := b.canonical()
	if err != nil {
		return nil, err
	}
	sig := ed25519.Sign(private, unsigned)
	var out bytes.Buffer
	out.Write(unsigned)
	_ = binary.Write(&out, binary.BigEndian, uint8(len(sig)))
	out.Write(sig)
	return out.Bytes(), nil
}

// ParseVerify decodes and verifies a signed bootstrap blob against the pinned
// roots at the given time. It fails closed on malformed, oversized, trailing,
// duplicate, or identity-mismatched payloads, an unsupported or downgraded
// version, an unpinned or wrong-role root, an invalid signature, expiry, or a
// not-yet-valid window. It returns the verified bundle on success.
func ParseVerify(raw []byte, roots []Root, now time.Time) (*Bundle, error) {
	if len(raw) > MaxBundleSize {
		return nil, ErrOversized
	}
	if len(raw) < len(Magic) || string(raw[:len(Magic)]) != Magic {
		return nil, ErrMalformed
	}
	rest := raw[len(Magic):]

	b, err := parseUnsigned(&rest)
	if err != nil {
		return nil, err
	}
	if b.Version < MinVersion {
		return nil, ErrDowngrade
	}
	if b.Version > MaxVersion {
		return nil, ErrUnknownVersion
	}
	if len(rest) < 1 {
		return nil, ErrMalformed
	}
	sigLen := int(rest[0])
	rest = rest[1:]
	if sigLen != SignatureLen || len(rest) < sigLen {
		return nil, ErrMalformed
	}
	sig := rest[:sigLen]
	rest = rest[sigLen:]
	if len(rest) != 0 {
		return nil, ErrMalformed
	}

	root := pinnedRoot(roots, b.RootKeyID)
	if root == nil {
		return nil, fmt.Errorf("%w: %q", ErrUnpinnedRoot, b.RootKeyID)
	}
	if root.Role != RoleBootstrap {
		return nil, fmt.Errorf("%w: root has role %q", ErrUnpinnedRoot, root.Role)
	}
	unsigned, err := b.canonical()
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(root.Public, unsigned, sig) {
		return nil, ErrInvalidSignature
	}
	if now.Before(b.NotBefore) {
		return nil, ErrNotYetValid
	}
	if !now.Before(b.NotAfter) {
		return nil, ErrExpired
	}
	return b, nil
}

func pinnedRoot(roots []Root, keyID string) *Root {
	for i := range roots {
		if roots[i].KeyID == keyID {
			return &roots[i]
		}
	}
	return nil
}

func parseUnsigned(rest *[]byte) (*Bundle, error) {
	if len(*rest) < 4 {
		return nil, ErrMalformed
	}
	version := binary.BigEndian.Uint32((*rest)[:4])
	*rest = (*rest)[4:]

	b := &Bundle{Version: version}
	var err error
	if b.Kind, err = readLen8(rest, MaxKindLen); err != nil {
		return nil, err
	}
	if b.RootKeyID, err = readLen8(rest, MaxKeyIDLen); err != nil {
		return nil, err
	}
	if b.PeerID, err = readLen8(rest, MaxPeerIDLen); err != nil {
		return nil, err
	}
	if b.Kind != KindBootstrap {
		return nil, fmt.Errorf("%w: kind", ErrMalformed)
	}
	if _, err := peer.Decode(b.PeerID); err != nil {
		return nil, fmt.Errorf("%w: peer id", ErrMalformed)
	}
	if len(*rest) < 2 {
		return nil, ErrMalformed
	}
	count := int(binary.BigEndian.Uint16((*rest)[:2]))
	*rest = (*rest)[2:]
	if count == 0 || count > MaxAddrs {
		return nil, fmt.Errorf("%w: address count", ErrMalformed)
	}
	seen := make(map[string]struct{}, count)
	for i := 0; i < count; i++ {
		var addr string
		if addr, err = readLen16(rest, MaxAddrLen); err != nil {
			return nil, err
		}
		if _, dup := seen[addr]; dup {
			return nil, ErrDuplicateAddrs
		}
		seen[addr] = struct{}{}
		if err := validateAddress(addr, b.PeerID); err != nil {
			return nil, err
		}
		b.Addresses = append(b.Addresses, addr)
	}
	then, err := readTime(rest)
	if err != nil {
		return nil, err
	}
	after, err := readTime(rest)
	if err != nil {
		return nil, err
	}
	if !after.After(then) {
		return nil, ErrInvalidValidity
	}
	b.NotBefore, b.NotAfter = then, after
	return b, nil
}

func writeLen8(out *bytes.Buffer, value string) {
	_ = binary.Write(out, binary.BigEndian, uint8(len(value)))
	out.WriteString(value)
}

func writeLen16(out *bytes.Buffer, value string) {
	_ = binary.Write(out, binary.BigEndian, uint16(len(value)))
	out.WriteString(value)
}

func readLen8(rest *[]byte, max int) (string, error) {
	if len(*rest) < 1 {
		return "", ErrMalformed
	}
	n := int((*rest)[0])
	*rest = (*rest)[1:]
	if n > max {
		return "", ErrOversized
	}
	if len(*rest) < n {
		return "", ErrMalformed
	}
	value := string((*rest)[:n])
	*rest = (*rest)[n:]
	return value, nil
}

func readLen16(rest *[]byte, max int) (string, error) {
	if len(*rest) < 2 {
		return "", ErrMalformed
	}
	n := int(binary.BigEndian.Uint16((*rest)[:2]))
	*rest = (*rest)[2:]
	if n > max {
		return "", ErrOversized
	}
	if len(*rest) < n {
		return "", ErrMalformed
	}
	value := string((*rest)[:n])
	*rest = (*rest)[n:]
	return value, nil
}

func readTime(rest *[]byte) (time.Time, error) {
	if len(*rest) < 8 {
		return time.Time{}, ErrMalformed
	}
	value := int64(binary.BigEndian.Uint64((*rest)[:8]))
	*rest = (*rest)[8:]
	return time.Unix(0, value).UTC(), nil
}

// validateAddress parses addr as a multiaddr and requires the address's
// terminal /p2p/ component to equal the bundle peer identity. An address that
// carries the wrong identity (or no identity) is an identity/address mismatch.
func validateAddress(addr, peerID string) error {
	value, err := ma.NewMultiaddr(addr)
	if err != nil {
		return fmt.Errorf("%w: address", ErrMalformed)
	}
	embedded, err := terminalP2P(value)
	if err != nil {
		return fmt.Errorf("%w: address", ErrMalformed)
	}
	if embedded != peerID {
		return ErrIdentityMismatch
	}
	return nil
}

// terminalP2P returns the terminal /p2p/<peer-id> component value of a
// multiaddr, rejecting addresses without one.
func terminalP2P(value ma.Multiaddr) (string, error) {
	var last string
	ma.ForEach(value, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_P2P {
			last = c.Value()
		}
		return true
	})
	if last == "" {
		return "", errors.New("missing p2p component")
	}
	if _, err := peer.Decode(last); err != nil {
		return "", err
	}
	return last, nil
}
