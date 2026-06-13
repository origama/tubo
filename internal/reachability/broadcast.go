package reachability

import (
	"context"
	"sync"
)

type Broadcaster struct {
	mu     sync.Mutex
	subs   []chan Event
	closed bool
}

func NewBroadcaster() *Broadcaster { return &Broadcaster{} }

func (b *Broadcaster) Subscribe(buffer int) <-chan Event {
	if buffer <= 0 {
		buffer = 1
	}
	ch := make(chan Event, buffer)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
		return ch
	}
	b.subs = append(b.subs, ch)
	return ch
}

func (b *Broadcaster) Run(ctx context.Context, src <-chan Event) {
	for {
		select {
		case <-ctx.Done():
			b.Close()
			return
		case ev, ok := <-src:
			if !ok {
				b.Close()
				return
			}
			b.broadcast(ev)
		}
	}
}

func (b *Broadcaster) Close() {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return
	}
	b.closed = true
	subs := b.subs
	b.subs = nil
	b.mu.Unlock()
	for _, ch := range subs {
		close(ch)
	}
}

func (b *Broadcaster) broadcast(ev Event) {
	b.mu.Lock()
	subs := append([]chan Event(nil), b.subs...)
	b.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}
