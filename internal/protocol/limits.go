package protocol

import (
	"errors"
	"fmt"
)

const (
	// DefaultMaxFrameBytes is the absolute payload limit for every frame.
	DefaultMaxFrameBytes uint64 = 1 << 20

	// Frame-specific limits keep pre-authentication and control messages small
	// while allowing normal HTTP metadata and streamed body chunks.
	DefaultMaxHelloFrameBytes        uint64 = 64 << 10
	DefaultMaxHeaderFrameBytes       uint64 = 512 << 10
	DefaultMaxConnectProofFrameBytes uint64 = 512 << 10
	DefaultMaxBodyChunkBytes         uint64 = 256 << 10
	DefaultMaxControlFrameBytes      uint64 = 64 << 10

	DefaultMaxStringBytes    uint64 = 256 << 10
	DefaultMaxByteFieldBytes uint64 = 512 << 10
	DefaultMaxHeaders        uint64 = 256
	DefaultMaxHeaderValues   uint64 = 256
	DefaultMaxCapabilities   uint64 = 128
)

// ErrDecodeLimitExceeded marks protocol input rejected before an oversized
// allocation or loop. Callers should close or reset the affected stream.
var ErrDecodeLimitExceeded = errors.New("protocol decode limit exceeded")

// DecoderLimits bounds remote-controlled frame payloads, nested fields, and
// collection counts. Zero values are replaced by the defaults.
type DecoderLimits struct {
	MaxFrameBytes        uint64
	MaxHelloFrameBytes   uint64
	MaxHeaderFrameBytes  uint64
	MaxConnectProofBytes uint64
	MaxBodyChunkBytes    uint64
	MaxControlFrameBytes uint64
	MaxStringBytes       uint64
	MaxByteFieldBytes    uint64
	MaxHeaders           uint64
	MaxHeaderValues      uint64
	MaxCapabilities      uint64
}

// DefaultDecoderLimits returns the limits used by NewStreamReader.
func DefaultDecoderLimits() DecoderLimits {
	return DecoderLimits{
		MaxFrameBytes:        DefaultMaxFrameBytes,
		MaxHelloFrameBytes:   DefaultMaxHelloFrameBytes,
		MaxHeaderFrameBytes:  DefaultMaxHeaderFrameBytes,
		MaxConnectProofBytes: DefaultMaxConnectProofFrameBytes,
		MaxBodyChunkBytes:    DefaultMaxBodyChunkBytes,
		MaxControlFrameBytes: DefaultMaxControlFrameBytes,
		MaxStringBytes:       DefaultMaxStringBytes,
		MaxByteFieldBytes:    DefaultMaxByteFieldBytes,
		MaxHeaders:           DefaultMaxHeaders,
		MaxHeaderValues:      DefaultMaxHeaderValues,
		MaxCapabilities:      DefaultMaxCapabilities,
	}
}

func (l DecoderLimits) normalized() DecoderLimits {
	d := DefaultDecoderLimits()
	if l.MaxFrameBytes == 0 {
		l.MaxFrameBytes = d.MaxFrameBytes
	}
	if l.MaxHelloFrameBytes == 0 {
		l.MaxHelloFrameBytes = d.MaxHelloFrameBytes
	}
	if l.MaxHeaderFrameBytes == 0 {
		l.MaxHeaderFrameBytes = d.MaxHeaderFrameBytes
	}
	if l.MaxConnectProofBytes == 0 {
		l.MaxConnectProofBytes = d.MaxConnectProofBytes
	}
	if l.MaxBodyChunkBytes == 0 {
		l.MaxBodyChunkBytes = d.MaxBodyChunkBytes
	}
	if l.MaxControlFrameBytes == 0 {
		l.MaxControlFrameBytes = d.MaxControlFrameBytes
	}
	if l.MaxStringBytes == 0 {
		l.MaxStringBytes = d.MaxStringBytes
	}
	if l.MaxByteFieldBytes == 0 {
		l.MaxByteFieldBytes = d.MaxByteFieldBytes
	}
	if l.MaxHeaders == 0 {
		l.MaxHeaders = d.MaxHeaders
	}
	if l.MaxHeaderValues == 0 {
		l.MaxHeaderValues = d.MaxHeaderValues
	}
	if l.MaxCapabilities == 0 {
		l.MaxCapabilities = d.MaxCapabilities
	}

	// io.LimitedReader uses int64 and allocation capacities use int. Keep
	// custom limits representable before any conversion.
	const maxInt64Value uint64 = 1<<63 - 1
	maxIntValue := uint64(^uint(0) >> 1)
	l.MaxFrameBytes = minLimit(l.MaxFrameBytes, maxInt64Value)
	l.MaxHelloFrameBytes = minLimit(l.MaxHelloFrameBytes, l.MaxFrameBytes)
	l.MaxHeaderFrameBytes = minLimit(l.MaxHeaderFrameBytes, l.MaxFrameBytes)
	l.MaxConnectProofBytes = minLimit(l.MaxConnectProofBytes, l.MaxFrameBytes)
	l.MaxBodyChunkBytes = minLimit(l.MaxBodyChunkBytes, l.MaxFrameBytes)
	l.MaxControlFrameBytes = minLimit(l.MaxControlFrameBytes, l.MaxFrameBytes)
	l.MaxStringBytes = minLimit(l.MaxStringBytes, minLimit(l.MaxFrameBytes, maxIntValue))
	l.MaxByteFieldBytes = minLimit(l.MaxByteFieldBytes, minLimit(l.MaxFrameBytes, maxIntValue))
	l.MaxHeaders = minLimit(l.MaxHeaders, maxIntValue)
	l.MaxHeaderValues = minLimit(l.MaxHeaderValues, maxIntValue)
	l.MaxCapabilities = minLimit(l.MaxCapabilities, maxIntValue)
	return l
}

func minLimit(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func (l DecoderLimits) frameLimit(frameType byte) uint64 {
	l = l.normalized()
	var specific uint64
	switch frameType {
	case FrameTypeHello:
		specific = l.MaxHelloFrameBytes
	case FrameTypeRequestHeader, FrameTypeResponseHeader:
		specific = l.MaxHeaderFrameBytes
	case FrameTypeConnectProof:
		specific = l.MaxConnectProofBytes
	case FrameTypeBodyChunk:
		// BodyChunk payload contains data followed by one IsFinal byte.
		specific = l.MaxBodyChunkBytes + 1
	case FrameTypeError, FrameTypeTunnelRequest, FrameTypeTunnelReady:
		specific = l.MaxControlFrameBytes
	default:
		specific = l.MaxFrameBytes
	}
	if specific > l.MaxFrameBytes {
		return l.MaxFrameBytes
	}
	return specific
}

func decodeLimitError(field string, got, max uint64) error {
	return fmt.Errorf("%w: %s %d exceeds maximum %d", ErrDecodeLimitExceeded, field, got, max)
}
