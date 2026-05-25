package discovery

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	grantspkg "github.com/origama/tubo/internal/grants"
)

type AnnouncementV2 struct {
	ClusterID   string        `json:"cluster_id"`
	NamespaceID string        `json:"namespace_id"`
	PeerID      peer.ID       `json:"peer_id"`
	TTL         time.Duration `json:"ttl"`
	Nonce       []byte        `json:"nonce"`
	Ciphertext  []byte        `json:"ciphertext"`
	Signature   []byte        `json:"signature,omitempty"`
}

type AnnouncementV2Payload struct {
	ServiceName          string                          `json:"service_name"` // display name only; not an authorization key
	ServiceID            string                          `json:"service_id,omitempty"`
	ServicePublicKey     string                          `json:"service_public_key,omitempty"`
	ConnectPolicy        string                          `json:"connect_policy,omitempty"`
	GrantService         *grantspkg.GrantServiceEndpoint `json:"grant_service,omitempty"`
	Addresses            []string                        `json:"addresses"`
	MembershipCapability []byte                          `json:"membership_capability,omitempty"`
	ServiceClaim         []byte                          `json:"service_claim,omitempty"` // legacy compatibility when publish_lease is absent
	PublishLease         []byte                          `json:"publish_lease,omitempty"`
	RegisteredAt         time.Time                       `json:"registered_at,omitempty"`
}

type announcementV2SigBody struct {
	ClusterID   string        `json:"cluster_id"`
	NamespaceID string        `json:"namespace_id"`
	PeerID      string        `json:"peer_id"`
	TTL         time.Duration `json:"ttl"`
	Nonce       []byte        `json:"nonce"`
	Ciphertext  []byte        `json:"ciphertext"`
}

func NewAnnouncementV2(clusterID, namespaceID string, peerID peer.ID, ttl time.Duration, payload AnnouncementV2Payload) (AnnouncementV2, error) {
	nonce, ciphertext, err := encryptAnnouncementV2Payload(clusterID, namespaceID, payload)
	if err != nil {
		return AnnouncementV2{}, err
	}
	return AnnouncementV2{ClusterID: clusterID, NamespaceID: namespaceID, PeerID: peerID, TTL: ttl, Nonce: nonce, Ciphertext: ciphertext}, nil
}

func (a *AnnouncementV2) Sign(privKey crypto.PrivKey) error {
	sig, err := a.computeSig()
	if err != nil {
		return fmt.Errorf("compute signature: %w", err)
	}
	a.Signature, err = privKey.Sign(sig)
	return err
}

func (a *AnnouncementV2) Verify(pubKey crypto.PubKey) (bool, error) {
	expectedSig, err := a.computeSig()
	if err != nil {
		return false, fmt.Errorf("compute expected sig: %w", err)
	}
	return pubKey.Verify(expectedSig, a.Signature)
}

func (a *AnnouncementV2) Marshal() ([]byte, error) {
	return json.Marshal(a)
}

func (a *AnnouncementV2) Unmarshal(data []byte) error {
	return json.Unmarshal(data, a)
}

func (a *AnnouncementV2) Payload(clusterID, namespaceID string) (AnnouncementV2Payload, error) {
	if clusterID != a.ClusterID {
		return AnnouncementV2Payload{}, fmt.Errorf("cluster id mismatch: got %q want %q", a.ClusterID, clusterID)
	}
	if namespaceID != a.NamespaceID {
		return AnnouncementV2Payload{}, fmt.Errorf("namespace id mismatch: got %q want %q", a.NamespaceID, namespaceID)
	}
	return decryptAnnouncementV2Payload(clusterID, namespaceID, a.Nonce, a.Ciphertext)
}

func (a *AnnouncementV2) computeSig() ([]byte, error) {
	var buf bytes.Buffer
	body := announcementV2SigBody{
		ClusterID:   a.ClusterID,
		NamespaceID: a.NamespaceID,
		PeerID:      a.PeerID.String(),
		TTL:         a.TTL,
		Nonce:       append([]byte(nil), a.Nonce...),
		Ciphertext:  append([]byte(nil), a.Ciphertext...),
	}
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func encryptAnnouncementV2Payload(clusterID, namespaceID string, payload AnnouncementV2Payload) ([]byte, []byte, error) {
	key := deriveAnnouncementV2Key(clusterID, namespaceID)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	ciphertext := gcm.Seal(nil, nonce, plain, nil)
	return nonce, ciphertext, nil
}

func decryptAnnouncementV2Payload(clusterID, namespaceID string, nonce, ciphertext []byte) (AnnouncementV2Payload, error) {
	key := deriveAnnouncementV2Key(clusterID, namespaceID)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return AnnouncementV2Payload{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return AnnouncementV2Payload{}, err
	}
	if len(nonce) != gcm.NonceSize() {
		return AnnouncementV2Payload{}, fmt.Errorf("invalid announcement nonce size %d", len(nonce))
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return AnnouncementV2Payload{}, err
	}
	var payload AnnouncementV2Payload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return AnnouncementV2Payload{}, err
	}
	return payload, nil
}

func deriveAnnouncementV2Key(clusterID, namespaceID string) [32]byte {
	return sha256.Sum256([]byte(clusterID + "\x00" + namespaceID))
}
