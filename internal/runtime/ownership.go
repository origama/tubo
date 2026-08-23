package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	core "github.com/libp2p/go-libp2p/core/network"

	wire "github.com/origama/tubo-rebuilt/internal/protocol"
)

var (
	errRuntimeClosed      = errors.New("runtime closed")
	errDuplicateSessionID = errors.New("duplicate session ID")
	errUnknownSession     = errors.New("unknown session")
	errUnsupportedRole    = errors.New("unsupported stream role")
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

// Close cancels owned sessions and rejects future registration and dispatch.
// It does not decide host or carrier ownership.
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

func (p *PipeRuntime) sessionForRole(decl wire.StreamRoleDeclaration) (*sessionPump, error) {
	if decl.SessionID == "" {
		return nil, errInvalidSessionID
	}
	if decl.Role != wire.StreamRoleControl && decl.Role != wire.StreamRoleResume {
		return nil, errUnsupportedRole
	}
	p.mu.Lock()
	closed := p.closed || p.ctx.Err() != nil
	p.mu.Unlock()
	if closed {
		return nil, errRuntimeClosed
	}
	session := p.registry.get(decl.SessionID)
	if session == nil || !session.acceptDispatch() {
		return nil, errUnknownSession
	}
	return session, nil
}

// readRoleDeclaration reads one strict, allocation-bounded initial role frame.
func readRoleDeclaration(r io.Reader, value *wire.StreamRoleDeclaration) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > wire.MaxResumeFrame {
		return fmt.Errorf("invalid resume role frame size %d", n)
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing resume role frame payload")
	}
	return nil
}

// HandleResumeStream consumes one bounded role declaration then dispatches it
// only within this PipeRuntime. Shared-host routing remains intentionally open.
func (p *PipeRuntime) HandleResumeStream(stream core.Stream) {
	var decl wire.StreamRoleDeclaration
	if err := readRoleDeclaration(stream, &decl); err != nil {
		_ = stream.Reset()
		return
	}
	session, err := p.sessionForRole(decl)
	if err != nil || !session.beginDispatch(stream) {
		_ = stream.Reset()
		return
	}
	defer session.endDispatch(stream)
	switch decl.Role {
	case wire.StreamRoleControl:
		if !session.setControlStream(stream) {
			_ = stream.Reset()
			return
		}
		session.handleControlStream(stream)
	case wire.StreamRoleResume:
		session.handleResumeStream(stream)
	default:
		_ = stream.Reset()
	}
}
