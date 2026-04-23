package discovery

import (
	"context"
	"sync"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
)

// Resolver resolves a service name to a peer ID and its addresses.
type Resolver interface {
	Resolve(serviceName string) (*ServiceEntry, bool)
}

// PubSubSubscriber listens for announcements on the discovery pubsub topic,
// verifies signatures, and updates the local cache accordingly.
type PubSubSubscriber struct {
	topic  *pubsub.Topic
	cache  *Cache
	pubKey map[peer.ID]crypto.PubKey // known public keys (populated by caller)
	mu     sync.RWMutex
}

// NewPubSubSubscriber creates a subscriber that listens on the discovery topic.
func NewPubSubSubscriber(topic *pubsub.Topic, cache *Cache) *PubSubSubscriber {
	return &PubSubSubscriber{
		topic:  topic,
		cache:  cache,
		pubKey: make(map[peer.ID]crypto.PubKey),
	}
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
	s.mu.RLock()
	pk, known := s.pubKey[ann.PeerID]
	s.mu.RUnlock()

	if !known {
		return // unknown peer, skip
	}

	ok, err := ann.Verify(pk)
	if err != nil || !ok {
		return // invalid signature, skip
	}

	// Valid announcement — update cache
	_ = s.cache.Add(ann.PeerID, ann.ServiceName, ann.Addresses)
}