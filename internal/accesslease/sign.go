// Package accesslease defines the pure, Scope-independent, owner-bound exact
// networking credentials for A0 step 6B1 (issue #52): the canonical exact-Pipe
// Access Lease, the immutable owner-signed Exact Owner Bond Authority binding,
// and the redesigned recipient-unbound Exact Invite that composes that binding.
//
// These are value/contract packages only: no network handlers, no persistence,
// no CLI, no OpenAuthorizer runtime wiring, no Scope/membership/discovery, and
// no import of any reachability/libp2p/store/CLI package. Decoding is strict
// and bounded before any allocation beyond the bounded struct, unknown or
// duplicate keys are rejected, trailing payload is rejected, and arbitrary
// nested metadata maps and concrete multiaddrs are never representable.
//
// Ownership model: a PipeID is self-certifying — it is the SHA-256 digest of
// the owning participant's Ed25519 public key. The Pipe owner owns the Exact
// Authority for that Pipe. The owner signs an Owner Binding that binds exactly
// one dedicated Exact Authority key (plus bounded neutral redemption peer
// addresses/protocol and an authorization epoch) to that exact Pipe. The Exact
// Invite composes that binding; the Access Lease is signed by the dedicated
// Authority key. Distinct NUL-delimited signing-domain prefixes keep every
// credential's signed bytes from being re-used or mistaken across kinds.
package accesslease

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

const (
	// Version is the canonical version accepted for all credentials here.
	Version = 1
	// AccessLeaseType is the fixed Access Lease discriminant.
	AccessLeaseType = "pipe.open.access"
	// InviteType is the fixed Exact Invite discriminant.
	InviteType = "pipe.open.invite"
	// OwnerBindingType is the fixed owner-bond discriminant.
	OwnerBindingType = "exact.owner.binding"

	// MaxBytes bounds any serialized base64url credential field on the wire.
	MaxBytes = 48 << 10
	// MaxFieldLen bounds every scalar string field.
	MaxFieldLen = 4096
	// MaxRedemptionAddrLen bounds the full comma-joined redemption address list.
	MaxRedemptionAddrLen = 1024
	// MaxRedemptionProtocolLen bounds the neutral redemption protocol string.
	MaxRedemptionProtocolLen = 128
	// MaxPipeIDLen bounds the audience Pipe identifier.
	MaxPipeIDLen = 256
	// MaxClockSkew bounds how far in the future issuance may claim to be.
	MaxClockSkew = 30 * time.Second
	// MaxTTL is the maximum accepted issued-to-expiry lifetime for the
	// short-lived exact-Pipe lease and invite.
	MaxTTL = 10 * time.Minute
)

var errStrict = errors.New("accesslease: invalid encoding")

// Distinct NUL-terminated signing-domain prefixes. Signing and verification
// always prepend the credential's own domain prefix, and the prefix is the
// only NUL-delimited framing, so a signature produced for one credential kind
// cannot satisfy another kind or version.
const (
	AccessLeaseSigningDomain    = "tubo://access-lease/v1\x00"
	OwnerBindingSigningDomain   = "tubo://exact-owner-binding/v1\x00"
	ExactInviteSigningDomain    = "tubo://exact-invite/v1\x00"
	OwnerKeyProofDomain         = "tubo://exact-owner-key-proof/v1\x00"
	OpenProofPurpose            = "tubo-open-v2"
	RedeemProofPurpose          = "tubo-exact-redeem-v2"
	MaxRedemptionAddresses      = 8
	OwnerBindingMaxAddressBytes = MaxRedemptionAddrLen
)

// PublicString renders an Ed25519 public key in canonical base64url form.
func PublicString(public ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(public)
}

// PublicFromString parses a canonical base64url Ed25519 public key.
func PublicFromString(value string) (ed25519.PublicKey, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) != ed25519.PublicKeySize {
		return nil, errors.New("accesslease: invalid Ed25519 public key")
	}
	return ed25519.PublicKey(data), nil
}

// PipeIDFromOwner derives the self-certifying Pipe identifier from the owning
// participant's Ed25519 public key. The PipeID is exactly the SHA-256 digest of
// the owner public key; possession of the matching secret is the only way to
// produce it. This is the foundation of "the Pipe owner owns its exact
// Authority".
func PipeIDFromOwner(owner ed25519.PublicKey) string {
	sum := sha256.Sum256(owner)
	return "pip_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:20]))
}

// VerifyPipeOwner reports whether pipeID is exactly the self-deriving PipeID
// of the given owner public key. An empty or malformed pipeID, or a wrong
// owner, fails closed (returns false).
func VerifyPipeOwner(pipeID string, owner ed25519.PublicKey) bool {
	if pipeID == "" || len(owner) != ed25519.PublicKeySize {
		return false
	}
	return pipeID == PipeIDFromOwner(owner)
}

func validString(value string) bool {
	return len(value) > 0 && len(value) <= MaxFieldLen
}

func validNonEmpty(value string, max int) bool {
	return len(value) > 0 && len(value) <= max
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value), nil
}

// NewJTI returns a fresh credential identifier used across lease, invite, and
// binding JTI/JTI namespaces.
func NewJTI() (string, error) { return randomID("jti_") }

// domainBytes returns the exact framing bytes: the NUL-delimited domain ring
// prefix plus the canonical payload bytes.
func domainBytes(domain string, payload []byte) []byte {
	out := make([]byte, 0, len(domain)+len(payload))
	out = append(out, domain...)
	out = append(out, payload...)
	return out
}

// SignProof signs a purpose-delimited possession message with a fresh domain
// prefix so a possession proof cannot be confused across purposes.
func SignProof(private ed25519.PrivateKey, purpose string, parts ...string) string {
	message := purpose + "\x00" + strings.Join(parts, "\x00")
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(message)))
}

// VerifyProof verifies a purpose-delimited possession message.
func VerifyProof(public ed25519.PublicKey, signature, purpose string, parts ...string) bool {
	value, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	message := purpose + "\x00" + strings.Join(parts, "\x00")
	return ed25519.Verify(public, []byte(message), value)
}

func decodeSignature(signature string) ([]byte, error) {
	value, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil || len(value) != ed25519.SignatureSize {
		return nil, errors.New("accesslease: invalid signature encoding")
	}
	return value, nil
}

func base64RawURLEncode(data []byte) string {
	return base64.RawURLEncoding.EncodeToString(data)
}
