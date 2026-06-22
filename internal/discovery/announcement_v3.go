package discovery

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	capability "github.com/origama/tubo/internal/capability"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/hkdf"
)

const (
	AnnouncementVersionV3            = "tubo-discovery-v3"
	namespaceDiscoverySecretByteSize = 32
	topicDerivationLabelV3           = "tubo discovery topic v1"
	payloadDerivationLabelV3         = "tubo discovery payload v1"
)

type NamespaceDiscoveryContext struct {
	ClusterID   string
	NamespaceID string
	KeyID       string
	Secret      []byte
}

type AnnouncementV3 struct {
	Version    string        `json:"version"`
	PeerID     peer.ID       `json:"peer_id"`
	TTL        time.Duration `json:"ttl"`
	KeyID      string        `json:"key_id"`
	Nonce      []byte        `json:"nonce"`
	Ciphertext []byte        `json:"ciphertext"`
	Signature  []byte        `json:"signature,omitempty"`
}

type AnnouncementV3Payload struct {
	ClusterID            string                          `json:"cluster_id"`
	NamespaceID          string                          `json:"namespace_id"`
	Kind                 string                          `json:"kind,omitempty"`
	ServiceName          string                          `json:"service_name"`
	ServiceKind          string                          `json:"service_kind,omitempty"`
	ServiceID            string                          `json:"service_id,omitempty"`
	ServicePublicKey     string                          `json:"service_public_key,omitempty"`
	ConnectPolicy        string                          `json:"connect_policy,omitempty"`
	GrantService         *grantspkg.GrantServiceEndpoint `json:"grant_service,omitempty"`
	Addresses            []string                        `json:"addresses"`
	MembershipCapability []byte                          `json:"membership_capability,omitempty"`
	PublishLease         []byte                          `json:"publish_lease,omitempty"`
	ServiceClaim         []byte                          `json:"service_claim,omitempty"`
	Capabilities         []string                        `json:"capabilities,omitempty"`
	RegisteredAt         time.Time                       `json:"registered_at,omitempty"`
}

type announcementV3EnvelopeBody struct {
	Version string        `json:"version"`
	PeerID  string        `json:"peer_id"`
	TTL     time.Duration `json:"ttl"`
	KeyID   string        `json:"key_id"`
}

type announcementV3SigBody struct {
	Version    string        `json:"version"`
	PeerID     string        `json:"peer_id"`
	TTL        time.Duration `json:"ttl"`
	KeyID      string        `json:"key_id"`
	Nonce      []byte        `json:"nonce"`
	Ciphertext []byte        `json:"ciphertext"`
}

func DeriveNamespaceTopicV3(ctx NamespaceDiscoveryContext) (string, error) {
	derived, err := deriveV3Bytes(ctx, topicDerivationLabelV3, 32)
	if err != nil {
		return "", err
	}
	return encodeOpaqueTopic("/discovery/v3/", derived), nil
}

func DeriveAnnouncementV3PayloadKey(ctx NamespaceDiscoveryContext) ([]byte, error) {
	return deriveV3Bytes(ctx, payloadDerivationLabelV3, 32)
}

func NewAnnouncementV3(ctx NamespaceDiscoveryContext, peerID peer.ID, ttl time.Duration, payload AnnouncementV3Payload) (AnnouncementV3, error) {
	if err := validateNamespaceDiscoveryContext(ctx); err != nil {
		return AnnouncementV3{}, err
	}
	ann := AnnouncementV3{Version: AnnouncementVersionV3, PeerID: peerID, TTL: ttl, KeyID: ctx.KeyID}
	nonce, ciphertext, err := encryptAnnouncementV3Payload(ctx, ann.envelopeAAD(), payload)
	if err != nil {
		return AnnouncementV3{}, err
	}
	ann.Nonce = nonce
	ann.Ciphertext = ciphertext
	return ann, nil
}

func (a *AnnouncementV3) Sign(privKey crypto.PrivKey) error {
	sig, err := a.computeSig()
	if err != nil {
		return fmt.Errorf("compute signature: %w", err)
	}
	a.Signature, err = privKey.Sign(sig)
	return err
}

func (a *AnnouncementV3) Verify(pubKey crypto.PubKey) (bool, error) {
	expectedSig, err := a.computeSig()
	if err != nil {
		return false, fmt.Errorf("compute expected sig: %w", err)
	}
	return pubKey.Verify(expectedSig, a.Signature)
}

func (a *AnnouncementV3) Marshal() ([]byte, error) {
	return json.Marshal(a)
}

func (a *AnnouncementV3) Unmarshal(data []byte) error {
	return json.Unmarshal(data, a)
}

func (a *AnnouncementV3) Payload(ctx NamespaceDiscoveryContext) (AnnouncementV3Payload, error) {
	if a.Version != AnnouncementVersionV3 {
		return AnnouncementV3Payload{}, fmt.Errorf("unsupported announcement version %q", a.Version)
	}
	if err := validateNamespaceDiscoveryContext(ctx); err != nil {
		return AnnouncementV3Payload{}, err
	}
	if strings.TrimSpace(ctx.KeyID) != strings.TrimSpace(a.KeyID) {
		return AnnouncementV3Payload{}, fmt.Errorf("key id mismatch: got %q want %q", a.KeyID, ctx.KeyID)
	}
	payload, err := decryptAnnouncementV3Payload(ctx, a.envelopeAAD(), a.Nonce, a.Ciphertext)
	if err != nil {
		return AnnouncementV3Payload{}, err
	}
	if payload.ClusterID != ctx.ClusterID {
		return AnnouncementV3Payload{}, fmt.Errorf("cluster id mismatch: got %q want %q", payload.ClusterID, ctx.ClusterID)
	}
	if payload.NamespaceID != ctx.NamespaceID {
		return AnnouncementV3Payload{}, fmt.Errorf("namespace id mismatch: got %q want %q", payload.NamespaceID, ctx.NamespaceID)
	}
	return payload, nil
}

// ValidatedAnnouncementV3 is a cache-ready namespace discovery record.
type ValidatedAnnouncementV3 struct {
	PeerID           peer.ID
	Kind             string
	ClusterID        string
	NamespaceID      string
	ServiceID        string
	ServiceName      string
	ServiceKind      string
	ServicePublicKey string
	ConnectPolicy    string
	GrantService     *grantspkg.GrantServiceEndpoint
	Addresses        []string
	Capabilities     []string
	TTL              time.Duration
	ExpiresAt        time.Time
}

// AnnouncementV3ExpiryBounds makes the cache lifetime rule explicit and testable.
type AnnouncementV3ExpiryBounds struct {
	Announcement time.Time
	Membership   time.Time
	PublishLease time.Time
	ServiceClaim time.Time
}

// EffectiveAnnouncementV3Expiry returns the earliest non-zero expiry.
func EffectiveAnnouncementV3Expiry(bounds AnnouncementV3ExpiryBounds) time.Time {
	expiresAt := bounds.Announcement.UTC()
	for _, candidate := range []time.Time{bounds.Membership, bounds.PublishLease, bounds.ServiceClaim} {
		if candidate.IsZero() {
			continue
		}
		candidate = candidate.UTC()
		if expiresAt.IsZero() || candidate.Before(expiresAt) {
			expiresAt = candidate
		}
	}
	return expiresAt.UTC()
}

// ValidateAnnouncementV3 verifies a signed announcement against one discovery
// context, returning a cache-ready record and effective TTL.
func ValidateAnnouncementV3(ctx NamespaceDiscoveryContext, ann AnnouncementV3, signerPub crypto.PubKey, authorityPub ed25519.PublicKey, observedPeerID peer.ID) (ValidatedAnnouncementV3, error) {
	return ValidateAnnouncementV3AcrossContexts(ann, signerPub, authorityPub, observedPeerID, ctx)
}

// ValidateAnnouncementV3AcrossContexts validates a signed announcement against
// one or more discovery contexts, returning the first cache-ready match.
func ValidateAnnouncementV3AcrossContexts(ann AnnouncementV3, signerPub crypto.PubKey, authorityPub ed25519.PublicKey, observedPeerID peer.ID, contexts ...NamespaceDiscoveryContext) (ValidatedAnnouncementV3, error) {
	if strings.TrimSpace(ann.Version) != AnnouncementVersionV3 {
		return ValidatedAnnouncementV3{}, fmt.Errorf("unsupported announcement version %q", ann.Version)
	}
	if observedPeerID != "" && observedPeerID != ann.PeerID {
		return ValidatedAnnouncementV3{}, fmt.Errorf("peer id mismatch: got %q want %q", observedPeerID, ann.PeerID)
	}
	if len(authorityPub) == 0 {
		return ValidatedAnnouncementV3{}, errors.New("authority public key is required")
	}
	if signerPub == nil {
		return ValidatedAnnouncementV3{}, errors.New("announcement signer public key is required")
	}
	ok, err := ann.Verify(signerPub)
	if err != nil {
		return ValidatedAnnouncementV3{}, fmt.Errorf("verify announcement signature: %w", err)
	}
	if !ok {
		return ValidatedAnnouncementV3{}, errors.New("announcement signature invalid")
	}
	if len(contexts) == 0 {
		return ValidatedAnnouncementV3{}, errors.New("no discovery context available")
	}
	var lastErr error
	for _, ctx := range contexts {
		validated, err := validateAnnouncementV3ForContext(ctx, ann, authorityPub)
		if err == nil {
			return validated, nil
		}
		lastErr = err
	}
	if lastErr != nil {
		return ValidatedAnnouncementV3{}, lastErr
	}
	return ValidatedAnnouncementV3{}, errors.New("announcement rejected")
}

func validateAnnouncementV3ForContext(ctx NamespaceDiscoveryContext, ann AnnouncementV3, authorityPub ed25519.PublicKey) (ValidatedAnnouncementV3, error) {
	payload, err := ann.Payload(ctx)
	if err != nil {
		return ValidatedAnnouncementV3{}, err
	}
	if payload.ServiceName == "" || payload.RegisteredAt.IsZero() || ann.TTL <= 0 {
		return ValidatedAnnouncementV3{}, errors.New("announcement missing required payload fields")
	}
	if len(payload.MembershipCapability) == 0 {
		return ValidatedAnnouncementV3{}, errors.New("membership capability missing")
	}
	var membership capability.MembershipCapability
	if err := json.Unmarshal(payload.MembershipCapability, &membership); err != nil {
		return ValidatedAnnouncementV3{}, fmt.Errorf("decode membership capability: %w", err)
	}
	if err := verifyAnnouncementMembership(membership, authorityPub, ctx.ClusterID, ctx.NamespaceID, ann.PeerID.String()); err != nil {
		return ValidatedAnnouncementV3{}, err
	}
	if payload.ServiceID == "" || payload.ServicePublicKey == "" {
		return ValidatedAnnouncementV3{}, errors.New("service identity is incomplete")
	}
	servicePub, err := serviceidentity.DecodePublicKey(payload.ServicePublicKey)
	if err != nil {
		return ValidatedAnnouncementV3{}, fmt.Errorf("decode service public key: %w", err)
	}
	if err := serviceidentity.MatchServiceID(servicePub, payload.ServiceID); err != nil {
		return ValidatedAnnouncementV3{}, err
	}
	membershipExpiresAt := membership.ExpiresAt.UTC()
	expiresAt := EffectiveAnnouncementV3Expiry(AnnouncementV3ExpiryBounds{Announcement: payload.RegisteredAt.UTC().Add(ann.TTL), Membership: membershipExpiresAt})
	if len(payload.PublishLease) > 0 {
		lease, err := grantspkg.ParseAndVerifyPublishLeaseBytes(payload.PublishLease, authorityPub, ctx.ClusterID, ctx.NamespaceID, payload.ServiceID, ann.PeerID.String())
		if err != nil {
			return ValidatedAnnouncementV3{}, err
		}
		if lease.ServicePublicKey != payload.ServicePublicKey {
			return ValidatedAnnouncementV3{}, fmt.Errorf("publish lease service public key mismatch")
		}
		expiresAt = EffectiveAnnouncementV3Expiry(AnnouncementV3ExpiryBounds{Announcement: expiresAt, PublishLease: lease.ExpiresAt.UTC()})
	} else {
		if len(payload.ServiceClaim) == 0 {
			return ValidatedAnnouncementV3{}, errors.New("service claim missing")
		}
		var claim capability.ServiceClaim
		if err := json.Unmarshal(payload.ServiceClaim, &claim); err != nil {
			return ValidatedAnnouncementV3{}, fmt.Errorf("decode service claim: %w", err)
		}
		if err := capability.VerifyServiceClaim(claim, authorityPub, ctx.ClusterID, ctx.NamespaceID, payload.ServiceID, ann.PeerID.String()); err != nil {
			return ValidatedAnnouncementV3{}, err
		}
		expiresAt = EffectiveAnnouncementV3Expiry(AnnouncementV3ExpiryBounds{Announcement: expiresAt, ServiceClaim: claim.ExpiresAt.UTC()})
	}
	if len(payload.ServiceClaim) > 0 && len(payload.PublishLease) > 0 {
		var claim capability.ServiceClaim
		if err := json.Unmarshal(payload.ServiceClaim, &claim); err != nil {
			return ValidatedAnnouncementV3{}, fmt.Errorf("decode service claim: %w", err)
		}
		if err := capability.VerifyServiceClaim(claim, authorityPub, ctx.ClusterID, ctx.NamespaceID, payload.ServiceID, ann.PeerID.String()); err != nil {
			return ValidatedAnnouncementV3{}, err
		}
		expiresAt = EffectiveAnnouncementV3Expiry(AnnouncementV3ExpiryBounds{Announcement: expiresAt, ServiceClaim: claim.ExpiresAt.UTC()})
	}
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return ValidatedAnnouncementV3{}, errors.New("announcement expired")
	}
	kind := strings.TrimSpace(payload.Kind)
	if kind == "" {
		kind = ResourceKindService
	}
	return ValidatedAnnouncementV3{
		PeerID:           ann.PeerID,
		Kind:             kind,
		ClusterID:        ctx.ClusterID,
		NamespaceID:      ctx.NamespaceID,
		ServiceID:        payload.ServiceID,
		ServiceName:      payload.ServiceName,
		ServiceKind:      payload.ServiceKind,
		ServicePublicKey: payload.ServicePublicKey,
		ConnectPolicy:    payload.ConnectPolicy,
		GrantService:     grantspkg.SanitizeGrantServiceEndpoint(payload.GrantService),
		Addresses:        append([]string(nil), payload.Addresses...),
		Capabilities:     append([]string(nil), payload.Capabilities...),
		TTL:              ttl,
		ExpiresAt:        expiresAt,
	}, nil
}

func (a *AnnouncementV3) envelopeAAD() []byte {
	body := announcementV3EnvelopeBody{Version: a.Version, PeerID: a.PeerID.String(), TTL: a.TTL, KeyID: a.KeyID}
	b, _ := json.Marshal(body)
	return b
}

func (a *AnnouncementV3) computeSig() ([]byte, error) {
	var buf bytes.Buffer
	body := announcementV3SigBody{
		Version:    a.Version,
		PeerID:     a.PeerID.String(),
		TTL:        a.TTL,
		KeyID:      a.KeyID,
		Nonce:      append([]byte(nil), a.Nonce...),
		Ciphertext: append([]byte(nil), a.Ciphertext...),
	}
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, err
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func validateNamespaceDiscoveryContext(ctx NamespaceDiscoveryContext) error {
	if strings.TrimSpace(ctx.ClusterID) == "" {
		return fmt.Errorf("cluster id is required")
	}
	if strings.TrimSpace(ctx.NamespaceID) == "" {
		return fmt.Errorf("namespace id is required")
	}
	if strings.TrimSpace(ctx.KeyID) == "" {
		return fmt.Errorf("key id is required")
	}
	if len(ctx.Secret) != namespaceDiscoverySecretByteSize {
		return fmt.Errorf("namespace discovery secret must be %d bytes", namespaceDiscoverySecretByteSize)
	}
	return nil
}

func deriveV3Bytes(ctx NamespaceDiscoveryContext, label string, length int) ([]byte, error) {
	if err := validateNamespaceDiscoveryContext(ctx); err != nil {
		return nil, err
	}
	if strings.TrimSpace(label) == "" {
		return nil, fmt.Errorf("derivation label is required")
	}
	if length <= 0 {
		return nil, fmt.Errorf("derivation length must be positive")
	}
	salt := []byte(ctx.ClusterID + "\x00" + ctx.NamespaceID + "\x00" + ctx.KeyID)
	reader := hkdf.New(sha256.New, ctx.Secret, salt, []byte(label))
	out := make([]byte, length)
	if _, err := io.ReadFull(reader, out); err != nil {
		return nil, err
	}
	return out, nil
}

func encryptAnnouncementV3Payload(ctx NamespaceDiscoveryContext, aad []byte, payload AnnouncementV3Payload) ([]byte, []byte, error) {
	key, err := DeriveAnnouncementV3PayloadKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	block, err := aes.NewCipher(key)
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
	ciphertext := gcm.Seal(nil, nonce, plain, aad)
	return nonce, ciphertext, nil
}

func decryptAnnouncementV3Payload(ctx NamespaceDiscoveryContext, aad, nonce, ciphertext []byte) (AnnouncementV3Payload, error) {
	key, err := DeriveAnnouncementV3PayloadKey(ctx)
	if err != nil {
		return AnnouncementV3Payload{}, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return AnnouncementV3Payload{}, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return AnnouncementV3Payload{}, err
	}
	if len(nonce) != gcm.NonceSize() {
		return AnnouncementV3Payload{}, fmt.Errorf("invalid announcement nonce size %d", len(nonce))
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return AnnouncementV3Payload{}, err
	}
	var payload AnnouncementV3Payload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return AnnouncementV3Payload{}, err
	}
	return payload, nil
}
