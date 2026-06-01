package discovery

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo/internal/capability"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/serviceidentity"
)

const broadNamespaceWildcard = "*"

// DiscoveryEvent is emitted when a service registration changes.
type DiscoveryEvent struct {
	Type        string // "added" or "removed"
	ServiceID   string
	ServiceName string
	PeerID      peer.ID
}

// Resolver resolves a service name to a peer ID and its addresses.
type Resolver interface {
	Resolve(serviceName string) (*ServiceEntry, bool)
}

// PubSubSubscriber listens for announcements on the discovery pubsub topic,
// verifies signatures, updates the local cache, and emits DiscoveryEvents.
type PubSubSubscriber struct {
	topic              *pubsub.Topic
	expectedTopic      string
	cache              *Cache
	mode               Mode
	clusterID          string
	namespaceID        string
	authorityPublicKey ed25519.PublicKey
	pubKey             map[peer.ID]crypto.PubKey // known public keys (populated by caller)
	replay             *announcementReplayCache
	events             chan DiscoveryEvent
	mu                 sync.Mutex
}

// NewPubSubSubscriber creates a subscriber that listens on the discovery topic.
func NewPubSubSubscriber(topic *pubsub.Topic, cache *Cache) *PubSubSubscriber {
	return NewPubSubSubscriberWithMode(topic, cache, ModeLegacyV1, "", "")
}

func NewPubSubSubscriberWithMode(topic *pubsub.Topic, cache *Cache, mode Mode, clusterID, namespaceID string) *PubSubSubscriber {
	expectedTopic := ""
	if topic != nil {
		expectedTopic = topic.String()
	}
	s := &PubSubSubscriber{
		topic:         topic,
		expectedTopic: expectedTopic,
		cache:         cache,
		mode:          mode,
		clusterID:     clusterID,
		namespaceID:   namespaceID,
		pubKey:        make(map[peer.ID]crypto.PubKey),
		replay:        newAnnouncementReplayCache(1024),
		events:        make(chan DiscoveryEvent, 64),
	}
	s.wireExpiredCallback()
	return s
}

// OnEvents returns a receive-only channel of discovery events.
func (s *PubSubSubscriber) OnEvents() <-chan DiscoveryEvent {
	return s.events
}

// wireExpiredCallback registers the subscriber as the cache's expiry handler,
// emitting "removed" events when entries expire from the cache.
func (s *PubSubSubscriber) wireExpiredCallback() {
	s.cache.SetExpiredCallback(func(serviceName string, peerID peer.ID) {
		select {
		case s.events <- DiscoveryEvent{Type: "removed", ServiceName: serviceName, PeerID: peerID}:
		default:
			// drop if channel is full to avoid blocking the cache goroutine
		}
	})
}

// AddPublicKey registers a known public key for signature verification.
func (s *PubSubSubscriber) AddPublicKey(pID peer.ID, pk crypto.PubKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pubKey[pID] = pk
}

// SetAuthorityPublicKey configures the authority key used to validate
// membership/service claims in namespace-scoped Discovery V2 announcements.
func (s *PubSubSubscriber) SetAuthorityPublicKey(raw ed25519.PublicKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorityPublicKey = append(ed25519.PublicKey(nil), raw...)
}

// Start begins listening for announcements on the pubsub topic.
// It returns a stop channel that should be closed to shut down the subscriber.
func (s *PubSubSubscriber) Start(ctx context.Context) chan struct{} {
	stopCh := make(chan struct{})
	sub, err := s.topic.Subscribe()
	if err != nil {
		close(stopCh)
		return stopCh
	}

	go func() {
		defer sub.Cancel()
		for {
			select {
			case <-stopCh:
				return
			default:
				msg, err := sub.Next(ctx)
				if err != nil {
					return
				}
				s.handleMessage(msg)
			}
		}
	}()

	return stopCh
}

// handleMessage processes a single pubsub message as an Announcement.
func (s *PubSubSubscriber) handleMessage(msg *pubsub.Message) {
	if s.mode == ModeNamespaceV2 {
		s.handleMessageV2(msg)
		return
	}
	var ann Announcement
	if err := ann.Unmarshal(msg.Data); err != nil {
		return // malformed message, skip
	}

	// Reject mismatched sender/announcer identity.
	from := msg.GetFrom()
	if from != "" && from != ann.PeerID {
		return
	}

	// Verify signature against known public key.
	// If we don't already have the key, try extracting it from PeerID or pubsub message.
	s.mu.Lock()
	pk, known := s.pubKey[ann.PeerID]
	s.mu.Unlock()

	if !known {
		var err error

		pk, err = ann.PeerID.ExtractPublicKey()
		if err != nil || pk == nil {
			keyBytes := msg.GetKey()
			if len(keyBytes) == 0 {
				return // unknown peer and no key material in message
			}
			pk, err = crypto.UnmarshalPublicKey(keyBytes)
			if err != nil || pk == nil || !ann.PeerID.MatchesPublicKey(pk) {
				return // key in message is invalid or does not match peer ID
			}
		}

		// Cache for next messages.
		s.mu.Lock()
		s.pubKey[ann.PeerID] = pk
		s.mu.Unlock()
	}

	ok, err := ann.Verify(pk)
	if err != nil || !ok {
		return // invalid signature, skip
	}

	// Valid announcement — update cache and emit added event
	_ = s.cache.Add(ann.PeerID, ann.ServiceName, ann.Addresses, ann.TTL)
	log.Printf("discovery announcement accepted service=%q peer=%s addrs=%d ttl=%s", ann.ServiceName, ann.PeerID, len(ann.Addresses), ann.TTL)
	s.events <- DiscoveryEvent{Type: "added", ServiceName: ann.ServiceName, PeerID: ann.PeerID}

	// Ensure key stays cached for future verification.
	s.mu.Lock()
	if _, ok := s.pubKey[ann.PeerID]; !ok {
		s.pubKey[ann.PeerID] = pk
	}
	s.mu.Unlock()
}

func (s *PubSubSubscriber) handleMessageV2(msg *pubsub.Message) {
	if s.expectedTopic != "" && msg.GetTopic() != s.expectedTopic {
		return
	}
	var ann AnnouncementV2
	if err := ann.Unmarshal(msg.Data); err != nil {
		return
	}
	from := msg.GetFrom()
	if from != "" && from != ann.PeerID {
		return
	}

	s.mu.Lock()
	pk, known := s.pubKey[ann.PeerID]
	s.mu.Unlock()
	if !known {
		var err error
		pk, err = ann.PeerID.ExtractPublicKey()
		if err != nil || pk == nil {
			keyBytes := msg.GetKey()
			if len(keyBytes) == 0 {
				return
			}
			pk, err = crypto.UnmarshalPublicKey(keyBytes)
			if err != nil || pk == nil || !ann.PeerID.MatchesPublicKey(pk) {
				return
			}
		}
		s.mu.Lock()
		s.pubKey[ann.PeerID] = pk
		s.mu.Unlock()
	}
	ok, err := ann.Verify(pk)
	if err != nil || !ok {
		return
	}
	if s.clusterID == "" || s.namespaceID == "" {
		return
	}
	payload, err := ann.Payload(s.clusterID, s.namespaceID)
	if err != nil {
		return
	}
	if payload.ServiceName == "" || payload.RegisteredAt.IsZero() || ann.TTL <= 0 {
		return
	}
	if len(s.authorityPublicKey) == 0 {
		return
	}
	if len(payload.MembershipCapability) == 0 {
		return
	}
	var membership capability.MembershipCapability
	if err := json.Unmarshal(payload.MembershipCapability, &membership); err != nil {
		return
	}
	if err := verifyAnnouncementMembership(membership, s.authorityPublicKey, s.clusterID, s.namespaceID, ann.PeerID.String()); err != nil {
		return
	}
	if payload.ServiceID == "" || payload.ServicePublicKey == "" {
		return
	}
	servicePub, err := serviceidentity.DecodePublicKey(payload.ServicePublicKey)
	if err != nil {
		return
	}
	if err := serviceidentity.MatchServiceID(servicePub, payload.ServiceID); err != nil {
		return
	}
	expiresAt := payload.RegisteredAt.UTC().Add(ann.TTL)
	if len(payload.PublishLease) > 0 {
		lease, err := grantspkg.ParseAndVerifyPublishLeaseBytes(payload.PublishLease, s.authorityPublicKey, s.clusterID, s.namespaceID, payload.ServiceID, ann.PeerID.String())
		if err != nil {
			return
		}
		if lease.ServicePublicKey != payload.ServicePublicKey {
			return
		}
		leaseExpiresAt := lease.ExpiresAt.UTC()
		if leaseExpiresAt.Before(expiresAt) {
			expiresAt = leaseExpiresAt
		}
	} else {
		if len(payload.ServiceClaim) == 0 {
			return
		}
		var claim capability.ServiceClaim
		if err := json.Unmarshal(payload.ServiceClaim, &claim); err != nil {
			return
		}
		if err := capability.VerifyServiceClaim(claim, s.authorityPublicKey, s.clusterID, s.namespaceID, payload.ServiceID, ann.PeerID.String()); err != nil {
			return
		}
		claimExpiresAt := claim.ExpiresAt.UTC()
		if claimExpiresAt.Before(expiresAt) {
			expiresAt = claimExpiresAt
		}
	}
	cacheTTL := time.Until(expiresAt)
	if cacheTTL <= 0 {
		return
	}
	replayKey := strings.Join([]string{s.expectedTopic, ann.PeerID.String(), hex.EncodeToString(ann.Nonce)}, "|")
	if s.replay != nil && s.replay.Seen(replayKey, expiresAt) {
		return
	}
	if err := s.cache.AddV2(ann.PeerID, payload.ServiceID, payload.ServiceName, payload.ServiceKind, payload.ServicePublicKey, payload.ConnectPolicy, grantspkg.SanitizeGrantServiceEndpoint(payload.GrantService), payload.Addresses, append([]string(nil), payload.Capabilities...), cacheTTL); err != nil {
		return
	}
	log.Printf("discovery v2 announcement accepted service=%q peer=%s namespace=%s/%s addrs=%d ttl=%s", payload.ServiceName, ann.PeerID, s.clusterID, s.namespaceID, len(payload.Addresses), ann.TTL)
	s.events <- DiscoveryEvent{Type: "added", ServiceID: payload.ServiceID, ServiceName: payload.ServiceName, PeerID: ann.PeerID}
}

func verifyAnnouncementMembership(membership capability.MembershipCapability, pub ed25519.PublicKey, clusterID, namespaceID, announcerPeerID string) error {
	var lastErr error
	for _, subject := range []string{announcerPeerID, clusterID} {
		candidateNamespaces := []string{namespaceID}
		if membership.NamespaceID == broadNamespaceWildcard {
			candidateNamespaces = append(candidateNamespaces, broadNamespaceWildcard)
		}
		for _, candidateNamespace := range candidateNamespaces {
			if err := capability.VerifyMembershipCapability(membership, pub, clusterID, candidateNamespace, subject); err != nil {
				lastErr = err
				continue
			}
			if membership.NamespaceID == namespaceID || membership.NamespaceID == broadNamespaceWildcard {
				return nil
			}
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return errors.New("membership capability rejected")
}
