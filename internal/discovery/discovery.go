package discovery

import (
	"context"
	"log"
	"sync"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// DiscoveryEvent is emitted when a service registration changes.
type DiscoveryEvent struct {
	Type        string // "added" or "removed"
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
	topic  *pubsub.Topic
	cache  *Cache
	pubKey map[peer.ID]crypto.PubKey // known public keys (populated by caller)
	events chan DiscoveryEvent
	mu     sync.Mutex
}

// NewPubSubSubscriber creates a subscriber that listens on the discovery topic.
func NewPubSubSubscriber(topic *pubsub.Topic, cache *Cache) *PubSubSubscriber {
	s := &PubSubSubscriber{
		topic:  topic,
		cache:  cache,
		pubKey: make(map[peer.ID]crypto.PubKey),
		events: make(chan DiscoveryEvent, 64),
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
