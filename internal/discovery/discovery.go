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
type subscriberScope struct {
	topic       *pubsub.Topic
	expected    string
	mode        Mode
	clusterID   string
	namespaceID string
	context     *NamespaceDiscoveryContext
}

type PubSubSubscriber struct {
	scopes             map[string]subscriberScope
	expectedTopic      string
	mode               Mode
	clusterID          string
	namespaceID        string
	cache              *Cache
	authorityPublicKey ed25519.PublicKey
	pubKey             map[peer.ID]crypto.PubKey // known public keys (populated by caller)
	replay             *announcementReplayCache
	events             chan DiscoveryEvent
	mu                 sync.Mutex
}

func (s *PubSubSubscriber) HasAuthorityPublicKey() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.authorityPublicKey) > 0
}

func (s *PubSubSubscriber) ScopeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.scopes)
}

// NewPubSubSubscriber creates a subscriber that listens on the discovery topic.
func NewPubSubSubscriber(topic *pubsub.Topic, cache *Cache) *PubSubSubscriber {
	return NewPubSubSubscriberWithMode(topic, cache, ModeLegacyV1, "", "")
}

func NewPubSubSubscriberWithMode(topic *pubsub.Topic, cache *Cache, mode Mode, clusterID, namespaceID string) *PubSubSubscriber {
	s := &PubSubSubscriber{
		scopes:      map[string]subscriberScope{},
		mode:        mode,
		clusterID:   clusterID,
		namespaceID: namespaceID,
		cache:       cache,
		pubKey:      make(map[peer.ID]crypto.PubKey),
		replay:      newAnnouncementReplayCache(1024),
		events:      make(chan DiscoveryEvent, 64),
	}
	if topic != nil {
		s.expectedTopic = topic.String()
		s.scopes[topic.String()] = subscriberScope{topic: topic, expected: topic.String(), mode: mode, clusterID: clusterID, namespaceID: namespaceID}
	}
	s.wireExpiredCallback()
	return s
}

func NewPubSubSubscriberV3(topics []*pubsub.Topic, cache *Cache, contexts []NamespaceDiscoveryContext) *PubSubSubscriber {
	s := &PubSubSubscriber{
		scopes: map[string]subscriberScope{},
		mode:   ModeNamespaceV3,
		cache:  cache,
		pubKey: make(map[peer.ID]crypto.PubKey),
		replay: newAnnouncementReplayCache(1024),
		events: make(chan DiscoveryEvent, 64),
	}
	for i, topic := range topics {
		if topic == nil || i >= len(contexts) {
			continue
		}
		ctx := contexts[i]
		s.scopes[topic.String()] = subscriberScope{topic: topic, expected: topic.String(), mode: ModeNamespaceV3, clusterID: ctx.ClusterID, namespaceID: ctx.NamespaceID, context: &ctx}
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
	if len(s.scopes) == 0 {
		close(stopCh)
		return stopCh
	}
	started := false
	for _, scope := range s.scopes {
		sub, err := scope.topic.Subscribe()
		if err != nil {
			continue
		}
		started = true
		go func(sub *pubsub.Subscription) {
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
		}(sub)
	}
	if !started {
		close(stopCh)
	}
	return stopCh
}

// handleMessage processes a single pubsub message as an Announcement.
func (s *PubSubSubscriber) handleMessage(msg *pubsub.Message) {
	scope, ok := s.scopes[msg.GetTopic()]
	if !ok {
		return
	}
	if scope.mode == ModeNamespaceV2 {
		s.handleMessageV2WithScope(msg, scope)
		return
	}
	if scope.mode == ModeNamespaceV3 {
		s.handleMessageV3(msg, scope)
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
	scope := subscriberScope{expected: s.expectedTopic, mode: s.mode, clusterID: s.clusterID, namespaceID: s.namespaceID}
	s.handleMessageV2WithScope(msg, scope)
}

func (s *PubSubSubscriber) handleMessageV2WithScope(msg *pubsub.Message, scope subscriberScope) {
	if scope.expected != "" && msg.GetTopic() != scope.expected {
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
	if scope.clusterID == "" || scope.namespaceID == "" {
		return
	}
	payload, err := ann.Payload(scope.clusterID, scope.namespaceID)
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
	if err := verifyAnnouncementMembership(membership, s.authorityPublicKey, scope.clusterID, scope.namespaceID, ann.PeerID.String()); err != nil {
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
		lease, err := grantspkg.ParseAndVerifyPublishLeaseBytes(payload.PublishLease, s.authorityPublicKey, scope.clusterID, scope.namespaceID, payload.ServiceID, ann.PeerID.String())
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
		if err := capability.VerifyServiceClaim(claim, s.authorityPublicKey, scope.clusterID, scope.namespaceID, payload.ServiceID, ann.PeerID.String()); err != nil {
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
	replayKey := strings.Join([]string{scope.expected, ann.PeerID.String(), hex.EncodeToString(ann.Nonce)}, "|")
	if s.replay != nil && s.replay.Seen(replayKey, expiresAt) {
		return
	}
	kind := strings.TrimSpace(payload.Kind)
	if kind == "" {
		kind = ResourceKindService
	}
	if err := s.cache.AddV2(ann.PeerID, scope.clusterID, scope.namespaceID, payload.ServiceID, payload.ServiceName, kind, payload.ServiceKind, payload.ServicePublicKey, payload.ConnectPolicy, grantspkg.SanitizeGrantServiceEndpoint(payload.GrantService), payload.Addresses, append([]string(nil), payload.Capabilities...), cacheTTL); err != nil {
		return
	}
	log.Printf("discovery v2 announcement accepted service=%q peer=%s namespace=%s/%s addrs=%d ttl=%s", payload.ServiceName, ann.PeerID, scope.clusterID, scope.namespaceID, len(payload.Addresses), ann.TTL)

	s.events <- DiscoveryEvent{Type: "added", ServiceID: payload.ServiceID, ServiceName: payload.ServiceName, PeerID: ann.PeerID}
}

func (s *PubSubSubscriber) handleMessageV3(msg *pubsub.Message, scope subscriberScope) {
	if scope.expected != "" && msg.GetTopic() != scope.expected {
		return
	}
	if scope.context == nil {
		return
	}
	var ann AnnouncementV3
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
	if len(s.authorityPublicKey) == 0 {
		return
	}
	payload, err := ann.Payload(*scope.context)
	if err != nil {
		return
	}
	if payload.ServiceName == "" || payload.RegisteredAt.IsZero() || ann.TTL <= 0 {
		return
	}
	if len(payload.MembershipCapability) == 0 {
		return
	}
	var membership capability.MembershipCapability
	if err := json.Unmarshal(payload.MembershipCapability, &membership); err != nil {
		return
	}
	if err := verifyAnnouncementMembership(membership, s.authorityPublicKey, scope.clusterID, scope.namespaceID, ann.PeerID.String()); err != nil {
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
		lease, err := grantspkg.ParseAndVerifyPublishLeaseBytes(payload.PublishLease, s.authorityPublicKey, scope.clusterID, scope.namespaceID, payload.ServiceID, ann.PeerID.String())
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
		if err := capability.VerifyServiceClaim(claim, s.authorityPublicKey, scope.clusterID, scope.namespaceID, payload.ServiceID, ann.PeerID.String()); err != nil {
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
	replayKey := strings.Join([]string{scope.expected, ann.PeerID.String(), hex.EncodeToString(ann.Nonce)}, "|")
	if s.replay != nil && s.replay.Seen(replayKey, expiresAt) {
		return
	}
	kind := strings.TrimSpace(payload.Kind)
	if kind == "" {
		kind = ResourceKindService
	}
	if err := s.cache.AddV2(ann.PeerID, scope.clusterID, scope.namespaceID, payload.ServiceID, payload.ServiceName, kind, payload.ServiceKind, payload.ServicePublicKey, payload.ConnectPolicy, grantspkg.SanitizeGrantServiceEndpoint(payload.GrantService), payload.Addresses, append([]string(nil), payload.Capabilities...), cacheTTL); err != nil {
		return
	}
	log.Printf("discovery v3 announcement accepted service=%q peer=%s namespace=%s/%s addrs=%d ttl=%s", payload.ServiceName, ann.PeerID, scope.clusterID, scope.namespaceID, len(payload.Addresses), ann.TTL)
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
