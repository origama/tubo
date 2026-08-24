package runtime

import (
	"context"
	"errors"
	"sync"
)

var (
	errRuntimeClosed      = errors.New("runtime closed")
	errDuplicateSessionID = errors.New("duplicate session ID")
	errInvalidSessionID   = errors.New("invalid session ID")
)

// NodeRuntime owns PipeRuntime children for one process-local runtime tree.
// It deliberately does not define host or carrier ownership.
type NodeRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool
	pipes  map[*PipeRuntime]struct{}
	done   chan struct{}
}

func NewNodeRuntime(parent context.Context) *NodeRuntime {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	n := &NodeRuntime{ctx: ctx, cancel: cancel, pipes: make(map[*PipeRuntime]struct{}), done: make(chan struct{})}
	go func() {
		<-ctx.Done()
		n.Close()
	}()
	return n
}

func (n *NodeRuntime) NewPipeRuntime() (*PipeRuntime, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || n.ctx.Err() != nil {
		return nil, errRuntimeClosed
	}
	ctx, cancel := context.WithCancel(n.ctx)
	p := &PipeRuntime{node: n, ctx: ctx, cancel: cancel, registry: newSessionRegistry(), done: make(chan struct{})}
	n.pipes[p] = struct{}{}
	return p, nil
}

func (n *NodeRuntime) removePipe(p *PipeRuntime) {
	n.mu.Lock()
	delete(n.pipes, p)
	n.mu.Unlock()
}

// Close cancels and closes every owned PipeRuntime. It is idempotent.
func (n *NodeRuntime) Close() {
	n.mu.Lock()
	if n.closed {
		done := n.done
		n.mu.Unlock()
		<-done
		return
	}
	n.closed = true
	n.cancel()
	pipes := make([]*PipeRuntime, 0, len(n.pipes))
	for p := range n.pipes {
		pipes = append(pipes, p)
	}
	n.mu.Unlock()
	for _, p := range pipes {
		p.Close()
	}
	close(n.done)
}

// PipeRuntime owns one isolated SessionRegistry. It deliberately makes no
// claim about whether sibling runtimes share a libp2p host.
type PipeRuntime struct {
	node   *NodeRuntime
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	closed   bool
	registry *sessionRegistry
	done     chan struct{}
}

func (p *PipeRuntime) NewSession(cfg sessionConfig) (*sessionPump, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.ctx.Err() != nil {
		return nil, errRuntimeClosed
	}
	return newSessionPump(p.ctx, p.registry, cfg)
}

// Close cancels owned sessions and rejects future registration. It does not
// decide host or carrier ownership.
func (p *PipeRuntime) Close() {
	p.mu.Lock()
	if p.closed {
		done := p.done
		p.mu.Unlock()
		<-done
		return
	}
	p.closed = true
	p.cancel()
	sessions := p.registry.close()
	p.mu.Unlock()
	for _, session := range sessions {
		session.Stop()
	}
	p.node.removePipe(p)
	close(p.done)
}
