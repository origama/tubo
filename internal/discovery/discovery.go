package discovery

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
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

	// Verify signature against known public key
	s.mu.Lock()
	pk, known := s.pubKey[ann.PeerID]
	s.mu.Unlock()

	if !known {
		return // unknown peer, skip
	}

	ok, err := ann.Verify(pk)
	if err != nil || !ok {
		return // invalid signature, skip
	}

	// Valid announcement — update cache and emit added event
	_ = s.cache.Add(ann.PeerID, ann.ServiceName, ann.Addresses)
	s.events <- DiscoveryEvent{Type: "added", ServiceName: ann.ServiceName, PeerID: ann.PeerID}

	// Register the peer's public key for future verification
	s.mu.Lock()
	if _, known := s.pubKey[ann.PeerID]; !known {
		s.pubKey[ann.PeerID] = pk
	}
	s.mu.Unlock()
}