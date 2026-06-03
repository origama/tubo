package discovery

import (
	"context"
	"fmt"

	pubsub "github.com/libp2p/go-libp2p-pubsub"
	"github.com/libp2p/go-libp2p/core/crypto"
)

// Publisher signs and broadcasts service announcements via a GossipSub topic.
type Publisher struct {
	topic   *pubsub.Topic
	privKey crypto.PrivKey
}

// NewPublisher creates a publisher bound to the given pubsub topic, using
// privKey for signing announcements.
func NewPublisher(topic *pubsub.Topic, privKey crypto.PrivKey) *Publisher {
	return &Publisher{
		topic:   topic,
		privKey: privKey,
	}
}

// Publish signs the announcement, marshals it to bytes, and publishes it on
// the GossipSub topic. Returns any error encountered during signing,
// marshaling, or publishing.
func (p *Publisher) Publish(ctx context.Context, ann Announcement) error {
	if err := ann.Sign(p.privKey); err != nil {
		return fmt.Errorf("sign announcement: %w", err)
	}

	data, err := ann.Marshal()
	if err != nil {
		return fmt.Errorf("marshal announcement: %w", err)
	}

	if err := p.topic.Publish(ctx, data); err != nil {
		return fmt.Errorf("publish to topic: %w", err)
	}

	return nil
}

func (p *Publisher) PublishV2(ctx context.Context, ann AnnouncementV2) error {
	if err := ann.Sign(p.privKey); err != nil {
		return fmt.Errorf("sign announcement v2: %w", err)
	}
	data, err := ann.Marshal()
	if err != nil {
		return fmt.Errorf("marshal announcement v2: %w", err)
	}
	if err := p.topic.Publish(ctx, data); err != nil {
		return fmt.Errorf("publish v2 to topic: %w", err)
	}
	return nil
}

func (p *Publisher) PublishV3(ctx context.Context, ann AnnouncementV3) error {
	if err := ann.Sign(p.privKey); err != nil {
		return fmt.Errorf("sign announcement v3: %w", err)
	}
	data, err := ann.Marshal()
	if err != nil {
		return fmt.Errorf("marshal announcement v3: %w", err)
	}
	if err := p.topic.Publish(ctx, data); err != nil {
		return fmt.Errorf("publish v3 to topic: %w", err)
	}
	return nil
}
