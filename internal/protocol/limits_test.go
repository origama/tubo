package protocol_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/multiformats/go-varint"

	"github.com/origama/tubo/internal/protocol"
)

func rawFrame(frameType byte, payload []byte) []byte {
	out := append([]byte(nil), varint.ToUvarint(uint64(len(payload)))...)
	out = append(out, frameType)
	return append(out, payload...)
}

func declaredFrame(frameType byte, length uint64) []byte {
	out := append([]byte(nil), varint.ToUvarint(length)...)
	return append(out, frameType)
}

func appendPrefixedString(dst []byte, value string) []byte {
	dst = append(dst, varint.ToUvarint(uint64(len(value)))...)
	return append(dst, value...)
}

func TestStreamReaderRejectsAbsoluteFrameLimitBeforePayloadRead(t *testing.T) {
	frame := declaredFrame(protocol.FrameTypeHello, protocol.DefaultMaxFrameBytes+1)
	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadHello()
	if !errors.Is(err, protocol.ErrDecodeLimitExceeded) {
		t.Fatalf("ReadHello error = %v, want ErrDecodeLimitExceeded", err)
	}
}

func TestStreamReaderRejectsVarintAboveMaxInt64(t *testing.T) {
	// Canonical uint64 max varint. go-multiformats limits protocol varints to
	// uint63, so this must fail before any uint64-to-int64 conversion.
	frame := append(bytes.Repeat([]byte{0xff}, 9), 0x01, protocol.FrameTypeHello)
	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadHello()
	if err == nil || !strings.Contains(err.Error(), "invalid varint") {
		t.Fatalf("ReadHello error = %v, want invalid varint", err)
	}
}

func TestStreamReaderRejectsOversizedNestedHelloString(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x00, 0x01}
	payload = append(payload, varint.ToUvarint(protocol.DefaultMaxStringBytes+1)...)
	frame := rawFrame(protocol.FrameTypeHello, payload)

	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadHello()
	if !errors.Is(err, protocol.ErrDecodeLimitExceeded) {
		t.Fatalf("ReadHello error = %v, want ErrDecodeLimitExceeded", err)
	}
}

func TestStreamReaderRejectsOversizedConnectProofByteField(t *testing.T) {
	var payload []byte
	for _, value := range []string{"cluster-a", "default", "svc-a", "12D3KooWsubject", "2026-07-23T20:00:00Z"} {
		payload = appendPrefixedString(payload, value)
	}
	payload = append(payload, varint.ToUvarint(protocol.DefaultMaxByteFieldBytes+1)...)
	frame := rawFrame(protocol.FrameTypeConnectProof, payload)

	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadConnectProof()
	if !errors.Is(err, protocol.ErrDecodeLimitExceeded) {
		t.Fatalf("ReadConnectProof error = %v, want ErrDecodeLimitExceeded", err)
	}
}

func TestStreamReaderRejectsCapabilityCountLimit(t *testing.T) {
	payload := []byte{0x00, 0x01, 0x00, 0x01, 0x00}
	payload = append(payload, varint.ToUvarint(protocol.DefaultMaxCapabilities+1)...)
	frame := rawFrame(protocol.FrameTypeHello, payload)

	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadHello()
	if !errors.Is(err, protocol.ErrDecodeLimitExceeded) {
		t.Fatalf("ReadHello error = %v, want ErrDecodeLimitExceeded", err)
	}
}

func TestStreamReaderRejectsHeaderCountLimit(t *testing.T) {
	// Empty method/path/query, then an oversized header count.
	payload := []byte{0x00, 0x00, 0x00}
	payload = append(payload, varint.ToUvarint(protocol.DefaultMaxHeaders+1)...)
	frame := rawFrame(protocol.FrameTypeRequestHeader, payload)

	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadRequestHeader()
	if !errors.Is(err, protocol.ErrDecodeLimitExceeded) {
		t.Fatalf("ReadRequestHeader error = %v, want ErrDecodeLimitExceeded", err)
	}
}

func TestStreamReaderRejectsHeaderValueCountLimit(t *testing.T) {
	// Empty method/path/query, one header named x, then too many values.
	payload := []byte{0x00, 0x00, 0x00, 0x01, 0x01, 'x'}
	payload = append(payload, varint.ToUvarint(protocol.DefaultMaxHeaderValues+1)...)
	frame := rawFrame(protocol.FrameTypeRequestHeader, payload)

	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadRequestHeader()
	if !errors.Is(err, protocol.ErrDecodeLimitExceeded) {
		t.Fatalf("ReadRequestHeader error = %v, want ErrDecodeLimitExceeded", err)
	}
}

func TestStreamReaderRejectsBodyChunkFrameLimit(t *testing.T) {
	frame := declaredFrame(protocol.FrameTypeBodyChunk, protocol.DefaultMaxBodyChunkBytes+2)
	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadBodyChunk()
	if !errors.Is(err, protocol.ErrDecodeLimitExceeded) {
		t.Fatalf("ReadBodyChunk error = %v, want ErrDecodeLimitExceeded", err)
	}
}

func TestStreamReaderRejectsTrailingFramePayload(t *testing.T) {
	// protocol 1.1, empty role, zero capabilities, one trailing byte.
	payload := []byte{0x00, 0x01, 0x00, 0x01, 0x00, 0x00, 0x42}
	frame := rawFrame(protocol.FrameTypeHello, payload)

	_, err := protocol.NewStreamReader(bytes.NewReader(frame)).ReadHello()
	if err == nil || !strings.Contains(err.Error(), "trailing bytes") {
		t.Fatalf("ReadHello error = %v, want trailing bytes error", err)
	}
}

func TestEncodeFrameRejectsOversizedBodyChunk(t *testing.T) {
	chunk := &protocol.BodyChunk{Data: make([]byte, protocol.DefaultMaxBodyChunkBytes+1), IsFinal: true}
	var buf bytes.Buffer
	err := protocol.EncodeFrame(&buf, chunk)
	if !errors.Is(err, protocol.ErrDecodeLimitExceeded) {
		t.Fatalf("EncodeFrame error = %v, want ErrDecodeLimitExceeded", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("EncodeFrame wrote %d bytes after rejecting payload", buf.Len())
	}
}

func FuzzStreamReaderDoesNotPanic(f *testing.F) {
	f.Add(byte(protocol.FrameTypeHello), []byte{0x00, 0x01, 0x00, 0x01, 0x00, 0x00})
	f.Add(byte(protocol.FrameTypeRequestHeader), []byte{0x00, 0x00, 0x00, 0x00, 0x01})
	f.Add(byte(protocol.FrameTypeBodyChunk), []byte{0x01})
	f.Add(byte(protocol.FrameTypeConnectProof), []byte{0xff, 0xff, 0xff, 0xff, 0x7f})

	f.Fuzz(func(t *testing.T, frameType byte, payload []byte) {
		if len(payload) > int(protocol.DefaultMaxFrameBytes)+1 {
			t.Skip()
		}
		frame := rawFrame(frameType, payload)
		reader := protocol.NewStreamReader(bytes.NewReader(frame))
		switch frameType {
		case protocol.FrameTypeHello:
			_, _ = reader.ReadHello()
		case protocol.FrameTypeRequestHeader:
			_, _ = reader.ReadRequestHeader()
		case protocol.FrameTypeResponseHeader:
			_, _ = reader.ReadResponseHeader()
		case protocol.FrameTypeBodyChunk:
			_, _ = reader.ReadBodyChunk()
		case protocol.FrameTypeError:
			_, _ = reader.ReadError()
		case protocol.FrameTypeConnectProof:
			_, _ = reader.ReadConnectProof()
		case protocol.FrameTypeTunnelRequest:
			_, _ = reader.ReadTunnelRequest()
		case protocol.FrameTypeTunnelReady:
			_, _, _ = reader.ReadTunnelReadyOrError()
		default:
			_, _ = reader.ReadHello()
		}
	})
}
