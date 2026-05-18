package p2p

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	capability "github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/protocol"
	"golang.org/x/crypto/ssh"
)

const connectAccessLeaseKind = "connect-access-lease"

const defaultConnectProofReplayCacheSize = 1024

// ConnectProofReplayCache tracks recently seen connect proof nonces and rejects replays.
type ConnectProofReplayCache struct {
	mu   sync.Mutex
	seen map[string]time.Time
	max  int
}

func NewConnectProofReplayCache(max int) *ConnectProofReplayCache {
	if max <= 0 {
		max = defaultConnectProofReplayCacheSize
	}
	return &ConnectProofReplayCache{seen: make(map[string]time.Time), max: max}
}

func (c *ConnectProofReplayCache) Seen(key string, expiresAt time.Time) bool {
	now := time.Now().UTC()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purgeLocked(now)
	if existing, ok := c.seen[key]; ok && now.Before(existing) {
		return true
	}
	if len(c.seen) >= c.max {
		c.evictOldestLocked()
	}
	c.seen[key] = expiresAt.UTC()
	return false
}

func (c *ConnectProofReplayCache) purgeLocked(now time.Time) {
	for key, expiry := range c.seen {
		if !now.Before(expiry) {
			delete(c.seen, key)
		}
	}
}

func (c *ConnectProofReplayCache) evictOldestLocked() {
	if len(c.seen) == 0 {
		return
	}
	keys := make([]string, 0, len(c.seen))
	for key := range c.seen {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return c.seen[keys[i]].Before(c.seen[keys[j]]) })
	delete(c.seen, keys[0])
}

type ConnectProofValidation struct {
	Require            bool
	AuthorityPublicKey ed25519.PublicKey
	ClusterID          string
	NamespaceID        string
	ServiceID          string
	Replay             *ConnectProofReplayCache
}

func (v ConnectProofValidation) Validate(remotePeer peer.ID, remotePub crypto.PubKey, proof *protocol.ConnectProof) error {
	if !v.Require {
		return nil
	}
	if proof == nil {
		return fmt.Errorf("connect proof required")
	}
	if proof.ClusterID != v.ClusterID {
		return fmt.Errorf("connect proof cluster id mismatch: got %q want %q", proof.ClusterID, v.ClusterID)
	}
	if proof.NamespaceID != v.NamespaceID {
		return fmt.Errorf("connect proof namespace id mismatch: got %q want %q", proof.NamespaceID, v.NamespaceID)
	}
	if proof.ServiceID != v.ServiceID {
		return fmt.Errorf("connect proof service id mismatch: got %q want %q", proof.ServiceID, v.ServiceID)
	}
	if proof.SubjectPeerID != remotePeer.String() {
		return fmt.Errorf("connect proof subject peer id mismatch: got %q want %q", proof.SubjectPeerID, remotePeer.String())
	}
	if proof.ExpiresAt.IsZero() {
		return fmt.Errorf("connect proof expires_at is required")
	}
	if time.Now().UTC().After(proof.ExpiresAt.UTC()) {
		return fmt.Errorf("connect proof expired")
	}
	if len(proof.Nonce) == 0 {
		return fmt.Errorf("connect proof nonce is required")
	}
	if remotePub == nil {
		return fmt.Errorf("missing remote public key")
	}
	raw, err := remotePub.Raw()
	if err != nil {
		return fmt.Errorf("raw remote public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("remote public key is not ed25519")
	}
	remoteEdPub := ed25519.PublicKey(raw)
	if err := protocol.VerifyConnectProofSignature(*proof, remoteEdPub); err != nil {
		return err
	}
	if isConnectAccessLeasePayload(proof.Capability) {
		return v.validateConnectAccessLeaseProof(remotePeer, remoteEdPub, proof)
	}
	var grant capability.ConnectCapability
	if err := json.Unmarshal(proof.Capability, &grant); err != nil {
		return fmt.Errorf("decode connect capability: %w", err)
	}
	if err := capability.VerifyConnectCapability(grant, v.AuthorityPublicKey, v.ClusterID, v.NamespaceID, v.ServiceID, ""); err != nil {
		return err
	}
	if !grant.ExpiresAt.UTC().Equal(proof.ExpiresAt.UTC()) {
		return fmt.Errorf("connect proof expiry mismatch")
	}
	if grant.SubjectPeerID != "" {
		return fmt.Errorf("connect capability must remain bearer")
	}
	if v.Replay != nil {
		key := strings.Join([]string{v.ClusterID, v.NamespaceID, v.ServiceID, remotePeer.String(), base64.RawURLEncoding.EncodeToString(proof.Nonce), hexOfProofCapability(proof.Capability)}, "|")
		if v.Replay.Seen(key, proof.ExpiresAt) {
			return fmt.Errorf("connect proof replay detected")
		}
	}
	return nil
}

func (v ConnectProofValidation) validateConnectAccessLeaseProof(remotePeer peer.ID, remotePub ed25519.PublicKey, proof *protocol.ConnectProof) error {
	if proof.IssuedAt.IsZero() {
		return fmt.Errorf("connect proof issued_at is required")
	}
	if proof.JTI == "" {
		return fmt.Errorf("connect proof jti is required")
	}
	now := time.Now().UTC()
	if proof.IssuedAt.UTC().After(now.Add(2 * time.Minute)) {
		return fmt.Errorf("connect proof issued_at is in the future")
	}
	if now.Sub(proof.IssuedAt.UTC()) > 5*time.Minute {
		return fmt.Errorf("connect proof issued_at is too old")
	}
	if len(proof.AccessLeaseHash) == 0 {
		return fmt.Errorf("connect proof access lease hash is required")
	}
	expectedHash := connectAccessLeaseHashBytes(proof.Capability)
	if !bytes.Equal(proof.AccessLeaseHash, expectedHash) {
		return fmt.Errorf("connect proof access lease hash mismatch")
	}
	var access connectAccessLease
	if err := json.Unmarshal(proof.Capability, &access); err != nil {
		return fmt.Errorf("decode connect access lease: %w", err)
	}
	if err := verifyConnectAccessLease(access, v.AuthorityPublicKey, v.ClusterID, v.NamespaceID, v.ServiceID); err != nil {
		return err
	}
	if proof.ExpiresAt.UTC().After(access.ExpiresAt.UTC()) {
		return fmt.Errorf("connect proof expiry exceeds access lease expiry")
	}
	remoteThumb := remotePublicKeyThumbprint(remotePub)
	if access.ClientKeyThumbprint != remoteThumb {
		return fmt.Errorf("connect access lease client key thumbprint mismatch")
	}
	if v.Replay != nil {
		key := strings.Join([]string{v.ClusterID, v.NamespaceID, v.ServiceID, remotePeer.String(), proof.JTI, base64.RawURLEncoding.EncodeToString(proof.Nonce), hexOfProofCapability(proof.Capability)}, "|")
		if v.Replay.Seen(key, proof.ExpiresAt) {
			return fmt.Errorf("connect proof replay detected")
		}
	}
	return nil
}

type connectAccessLease struct {
	Version             string    `json:"version"`
	Kind                string    `json:"kind"`
	JTI                 string    `json:"jti"`
	SessionID           string    `json:"session_id"`
	ShareInviteJTI      string    `json:"share_invite_jti,omitempty"`
	ClusterID           string    `json:"cluster_id"`
	NamespaceID         string    `json:"namespace_id"`
	ServiceID           string    `json:"service_id"`
	ClientPublicKey     string    `json:"client_public_key"`
	ClientKeyThumbprint string    `json:"client_key_thumbprint"`
	Permissions         []string  `json:"permissions"`
	IssuedAt            time.Time `json:"issued_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	Signature           []byte    `json:"signature,omitempty"`
}

type canonicalConnectAccessLease struct {
	Version             string   `json:"version"`
	Kind                string   `json:"kind"`
	JTI                 string   `json:"jti"`
	SessionID           string   `json:"session_id"`
	ShareInviteJTI      string   `json:"share_invite_jti,omitempty"`
	ClusterID           string   `json:"cluster_id"`
	NamespaceID         string   `json:"namespace_id"`
	ServiceID           string   `json:"service_id"`
	ClientPublicKey     string   `json:"client_public_key"`
	ClientKeyThumbprint string   `json:"client_key_thumbprint"`
	Permissions         []string `json:"permissions"`
	IssuedAt            string   `json:"issued_at"`
	ExpiresAt           string   `json:"expires_at"`
}

func verifyConnectAccessLease(lease connectAccessLease, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID string) error {
	if lease.Kind != connectAccessLeaseKind {
		return fmt.Errorf("unsupported connect access lease kind %q", lease.Kind)
	}
	if lease.ClusterID != clusterID {
		return fmt.Errorf("connect lease cluster id mismatch: got %q want %q", lease.ClusterID, clusterID)
	}
	if lease.NamespaceID != namespaceID {
		return fmt.Errorf("connect lease namespace id mismatch: got %q want %q", lease.NamespaceID, namespaceID)
	}
	if lease.ServiceID != serviceID {
		return fmt.Errorf("connect lease service id mismatch: got %q want %q", lease.ServiceID, serviceID)
	}
	if !connectLeaseHasConnectPermission(lease.Permissions) {
		return fmt.Errorf("connect lease missing connect permission")
	}
	if lease.ExpiresAt.IsZero() {
		return fmt.Errorf("connect lease expires_at is required")
	}
	if time.Now().UTC().After(lease.ExpiresAt.UTC()) {
		return fmt.Errorf("connect lease expired")
	}
	thumbprint, err := connectClientKeyThumbprint(lease.ClientPublicKey)
	if err != nil {
		return err
	}
	if lease.ClientKeyThumbprint != thumbprint {
		return fmt.Errorf("connect lease client key thumbprint mismatch")
	}
	payload, err := canonicalConnectAccessLeaseBytes(lease)
	if err != nil {
		return err
	}
	if len(lease.Signature) == 0 {
		return fmt.Errorf("connect lease signature is required")
	}
	if !ed25519.Verify(authorityPub, payload, lease.Signature) {
		return fmt.Errorf("invalid connect lease signature")
	}
	return nil
}

func canonicalConnectAccessLeaseBytes(lease connectAccessLease) ([]byte, error) {
	perms := append([]string(nil), lease.Permissions...)
	sort.Strings(perms)
	return json.Marshal(canonicalConnectAccessLease{
		Version:             lease.Version,
		Kind:                lease.Kind,
		JTI:                 lease.JTI,
		SessionID:           lease.SessionID,
		ShareInviteJTI:      lease.ShareInviteJTI,
		ClusterID:           lease.ClusterID,
		NamespaceID:         lease.NamespaceID,
		ServiceID:           lease.ServiceID,
		ClientPublicKey:     strings.TrimSpace(lease.ClientPublicKey),
		ClientKeyThumbprint: lease.ClientKeyThumbprint,
		Permissions:         perms,
		IssuedAt:            lease.IssuedAt.UTC().Format(time.RFC3339Nano),
		ExpiresAt:           lease.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
}

func connectClientKeyThumbprint(clientPublicKey string) (string, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(clientPublicKey)))
	if err != nil {
		return "", fmt.Errorf("parse connect client public key: %w", err)
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		return "", fmt.Errorf("connect client key does not expose a crypto public key")
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return "", fmt.Errorf("connect client key is not ed25519: %T", cryptoPub.CryptoPublicKey())
	}
	h := sha256.Sum256(edPub)
	return base64.RawURLEncoding.EncodeToString(h[:]), nil
}

func connectLeaseHasConnectPermission(perms []string) bool {
	for _, perm := range perms {
		if perm == capability.PermissionConnect {
			return true
		}
	}
	return false
}

func connectAccessLeaseHashBytes(b []byte) []byte {
	h := sha256.Sum256(bytes.TrimSpace(b))
	return h[:]
}

func isConnectAccessLeasePayload(b []byte) bool {
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(b, &header); err != nil {
		return false
	}
	return header.Kind == connectAccessLeaseKind
}

func remotePublicKeyThumbprint(pub ed25519.PublicKey) string {
	h := sha256.Sum256(pub)
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func hexOfProofCapability(b []byte) string {
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}
