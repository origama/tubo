package p2p

import (
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
)

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
	if err := protocol.VerifyConnectProofSignature(*proof, ed25519.PublicKey(raw)); err != nil {
		return err
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

func hexOfProofCapability(b []byte) string {
	h := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(h[:])
}
