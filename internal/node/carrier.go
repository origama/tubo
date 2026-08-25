package node

// This file is the one-way libp2p → Carrier integration adapter at the
// host/L1 edge (A0 exit criterion 4, issue #28). Session consumes the neutral
// Carrier contract; only this package knows core.Stream. The host-integration
// adapter intentionally imports neither runtime, authority, discovery, store,
// nor CLI packages.

import (
	"io"
	"time"

	core "github.com/libp2p/go-libp2p/core/network"
)

// Carrier is Session's raw full-duplex byte path. Implementations must expose
// bounded setup deadlines, full close/reset, and exact CloseWrite half-close.
// Carrier facts are transport facts, never authorization evidence.
type Carrier interface {
	io.Reader
	io.Writer
	Close() error
	Reset() error
	CloseWrite() error
	SetDeadline(time.Time) error
}

// libp2pCarrier adapts one libp2p stream to the neutral Carrier contract.
type libp2pCarrier struct{ stream core.Stream }

func (c *libp2pCarrier) Read(p []byte) (int, error)    { return c.stream.Read(p) }
func (c *libp2pCarrier) Write(p []byte) (int, error)   { return c.stream.Write(p) }
func (c *libp2pCarrier) Close() error                  { return c.stream.Close() }
func (c *libp2pCarrier) Reset() error                  { return c.stream.Reset() }
func (c *libp2pCarrier) CloseWrite() error             { return c.stream.CloseWrite() }
func (c *libp2pCarrier) SetDeadline(t time.Time) error { return c.stream.SetDeadline(t) }

// WrapStream adapts an already-acquired authorized tunnel stream to Carrier at
// the receiving boundary. The caller owns stream authentication/authorization
// ordering before invoking this.
func WrapStream(stream core.Stream) Carrier {
	if stream == nil {
		return nil
	}
	return &libp2pCarrier{stream: stream}
}
