package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/multiformats/go-varint"
)

func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}

// EncodeFrame writes a frame to w with varint length prefix + type byte + payload.
func EncodeFrame(w io.Writer, msg any) error {
	var ft byte
	var payload []byte
	var err error

	switch m := msg.(type) {
	case *Hello:
		ft = FrameTypeHello
		payload, err = encodeHello(m)
	case *RequestHeader:
		ft = FrameTypeRequestHeader
		payload, err = encodeRequestHeader(m)
	case *ResponseHeader:
		ft = FrameTypeResponseHeader
		payload, err = encodeResponseHeader(m)
	case *BodyChunk:
		ft = FrameTypeBodyChunk
		payload, err = encodeBodyChunk(m)
	case *Error:
		ft = FrameTypeError
		payload, err = encodeError(m)
	case *ConnectProof:
		ft = FrameTypeConnectProof
		payload, err = encodeConnectProof(m)
	case *TunnelRequest:
		ft = FrameTypeTunnelRequest
		payload, err = encodeTunnelRequest(m)
	case *TunnelReady:
		ft = FrameTypeTunnelReady
		payload, err = encodeTunnelReady(m)
	default:
		return fmt.Errorf("unknown frame type: %T", msg)
	}
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	limits := DefaultDecoderLimits()
	if size, max := uint64(len(payload)), limits.frameLimit(ft); size > max {
		return decodeLimitError("frame payload", size, max)
	}

	// Write varint length prefix + type byte + payload
	lenBytes := varint.ToUvarint(uint64(len(payload)))
	if err := writeFull(w, lenBytes); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if err := binary.Write(w, binary.BigEndian, ft); err != nil {
		return fmt.Errorf("write type: %w", err)
	}
	if err := writeFull(w, payload); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}
	return nil
}

// readVarint reads a varint from any io.Reader (works with io.LimitedReader).
// Reads one byte at a time to avoid consuming extra data.
func readVarint(r io.Reader) (uint64, error) {
	buf := make([]byte, 10) // max varint size is 9 for uint63
	n := 0
	for n < 10 {
		b := make([]byte, 1)
		if _, err := io.ReadFull(r, b); err != nil {
			return 0, err
		}
		buf[n] = b[0]
		n++
		// Varint terminates when high bit is 0
		if buf[n-1]&0x80 == 0 {
			break
		}
	}

	val, _, err := varint.FromUvarint(buf[:n])
	if err != nil {
		return 0, fmt.Errorf("invalid varint: %w", err)
	}
	return val, nil
}

// --- String encoding helpers ---

func encodeString(s string) []byte {
	b := []byte(s)
	hdr := varint.ToUvarint(uint64(len(b)))
	return append(hdr, b...)
}

func decodeString(r io.Reader, limits DecoderLimits) (string, error) {
	lenVal, err := readVarint(r)
	if err != nil {
		return "", fmt.Errorf("decode string length: %w", err)
	}
	b, err := decodeBytesWithLen(r, lenVal, limits.normalized().MaxStringBytes, "string bytes")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func encodeBytes(b []byte) []byte {
	hdr := varint.ToUvarint(uint64(len(b)))
	return append(hdr, b...)
}

func decodeBytes(r io.Reader, limits DecoderLimits) ([]byte, error) {
	lenVal, err := readVarint(r)
	if err != nil {
		return nil, fmt.Errorf("decode bytes length: %w", err)
	}
	return decodeBytesWithLen(r, lenVal, limits.normalized().MaxByteFieldBytes, "byte field")
}

func decodeBytesWithLen(r io.Reader, lenVal, max uint64, field string) ([]byte, error) {
	if lenVal > max {
		return nil, decodeLimitError(field, lenVal, max)
	}
	if remaining, ok := limitedReaderRemainingBytes(r); ok && lenVal > remaining {
		return nil, fmt.Errorf("%s length %d exceeds remaining frame payload %d", field, lenVal, remaining)
	}
	b := make([]byte, int(lenVal))
	_, err := io.ReadFull(r, b)
	if err != nil {
		return nil, fmt.Errorf("read bytes data: %w", err)
	}
	return b, nil
}

// --- Headers encoding (multi-value preserved) ---

func encodeHeaders(headers map[string][]string) []byte {
	result := make([]byte, 0, 64)
	count := uint64(len(headers))
	hdrLenBytes := varint.ToUvarint(count)
	result = append(result, hdrLenBytes...)

	// Sort keys for deterministic encoding
	keys := make([]string, 0, count)
	for k := range headers {
		keys = append(keys, k)
	}
	sortStrings(keys)

	for _, name := range keys {
		values := headers[name]
		result = append(result, encodeString(name)...)
		valCountBytes := varint.ToUvarint(uint64(len(values)))
		result = append(result, valCountBytes...)
		for _, v := range values {
			result = append(result, encodeString(v)...)
		}
	}
	return result
}

func decodeHeaders(r io.Reader, limits DecoderLimits) (map[string][]string, error) {
	limits = limits.normalized()
	count, err := readVarint(r)
	if err != nil {
		return nil, fmt.Errorf("decode headers count: %w", err)
	}
	if count > limits.MaxHeaders {
		return nil, decodeLimitError("header count", count, limits.MaxHeaders)
	}

	headers := make(map[string][]string, int(count))
	for i := uint64(0); i < count; i++ {
		name, err := decodeString(r, limits)
		if err != nil {
			return nil, fmt.Errorf("decode header name: %w", err)
		}
		valCount, err := readVarint(r)
		if err != nil {
			return nil, fmt.Errorf("decode values count for %q: %w", name, err)
		}
		if valCount > limits.MaxHeaderValues {
			return nil, decodeLimitError("header value count", valCount, limits.MaxHeaderValues)
		}
		values := make([]string, int(valCount))
		for j := uint64(0); j < valCount; j++ {
			v, err := decodeString(r, limits)
			if err != nil {
				return nil, fmt.Errorf("decode value for %q: %w", name, err)
			}
			values[int(j)] = v
		}
		headers[name] = values
	}
	return headers, nil
}

// --- Frame type encoders ---

func encodeTunnelRequest(m *TunnelRequest) ([]byte, error) {
	return encodeString(m.Kind), nil
}

func decodeTunnelRequest(r io.Reader, limits DecoderLimits) (*TunnelRequest, error) {
	kind, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode tunnel kind: %w", err)
	}
	return &TunnelRequest{Kind: kind}, nil
}

func encodeTunnelReady(m *TunnelReady) ([]byte, error) {
	return encodeString(m.Kind), nil
}

func decodeTunnelReady(r io.Reader, limits DecoderLimits) (*TunnelReady, error) {
	kind, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode tunnel ready kind: %w", err)
	}
	return &TunnelReady{Kind: kind}, nil
}

func encodeHello(m *Hello) ([]byte, error) {
	result := make([]byte, 0, 64)
	majorBytes := make([]byte, 2)
	minorBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(majorBytes, m.ProtocolMajor)
	binary.BigEndian.PutUint16(minorBytes, m.ProtocolMinor)
	result = append(result, majorBytes...)
	result = append(result, minorBytes...)
	result = append(result, encodeString(m.Role)...)
	capCount := varint.ToUvarint(uint64(len(m.Capabilities)))
	result = append(result, capCount...)
	for _, cap := range m.Capabilities {
		result = append(result, encodeString(cap)...)
	}
	return result, nil
}

func decodeHello(r io.Reader, limits DecoderLimits) (*Hello, error) {
	limits = limits.normalized()
	majorBytes := make([]byte, 2)
	minorBytes := make([]byte, 2)
	if _, err := io.ReadFull(r, majorBytes); err != nil {
		return nil, fmt.Errorf("decode protocol_major: %w", err)
	}
	if _, err := io.ReadFull(r, minorBytes); err != nil {
		return nil, fmt.Errorf("decode protocol_minor: %w", err)
	}
	role, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode role: %w", err)
	}
	capCount, err := readVarint(r)
	if err != nil {
		return nil, fmt.Errorf("decode capabilities count: %w", err)
	}
	if capCount > limits.MaxCapabilities {
		return nil, decodeLimitError("capability count", capCount, limits.MaxCapabilities)
	}
	caps := make([]string, 0, int(capCount))
	for i := uint64(0); i < capCount; i++ {
		capability, err := decodeString(r, limits)
		if err != nil {
			return nil, fmt.Errorf("decode capability: %w", err)
		}
		caps = append(caps, capability)
	}
	return &Hello{ProtocolMajor: binary.BigEndian.Uint16(majorBytes), ProtocolMinor: binary.BigEndian.Uint16(minorBytes), Role: role, Capabilities: caps}, nil
}

func encodeRequestHeader(m *RequestHeader) ([]byte, error) {
	result := make([]byte, 0, 128)
	result = append(result, encodeString(m.Method)...)
	result = append(result, encodeString(m.Path)...)
	result = append(result, encodeString(m.Query)...)
	result = append(result, encodeHeaders(m.Headers)...)

	// ContentLengthHint as signed varint (zigzag encoding)
	var clh uint64
	if m.ContentLengthHint < 0 {
		clh = uint64((-m.ContentLengthHint-1)<<1) | 1
	} else {
		clh = uint64(m.ContentLengthHint) << 1
	}
	result = append(result, varint.ToUvarint(clh)...)
	return result, nil
}

func decodeRequestHeader(r io.Reader, limits DecoderLimits) (*RequestHeader, error) {
	method, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode method: %w", err)
	}
	path, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode path: %w", err)
	}
	query, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode query: %w", err)
	}
	headers, err := decodeHeaders(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode headers: %w", err)
	}

	clhVal, err := readVarint(r)
	if err != nil {
		return nil, fmt.Errorf("decode content_length_hint: %w", err)
	}
	contentLengthHint := int64(clhVal >> 1)
	if clhVal&1 == 1 {
		contentLengthHint = -contentLengthHint - 1
	}

	return &RequestHeader{
		Method:            method,
		Path:              path,
		Query:             query,
		Headers:           headers,
		ContentLengthHint: contentLengthHint,
	}, nil
}

func encodeResponseHeader(m *ResponseHeader) ([]byte, error) {
	result := make([]byte, 0, 128)
	statusBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(statusBytes, uint16(m.StatusCode))
	result = append(result, statusBytes...)
	result = append(result, encodeString(m.StatusText)...)
	result = append(result, encodeHeaders(m.Headers)...)
	return result, nil
}

func decodeResponseHeader(r io.Reader, limits DecoderLimits) (*ResponseHeader, error) {
	statusBytes := make([]byte, 2)
	_, err := io.ReadFull(r, statusBytes)
	if err != nil {
		return nil, fmt.Errorf("decode status_code: %w", err)
	}
	statusCode := int(binary.BigEndian.Uint16(statusBytes))

	statusText, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode status_text: %w", err)
	}
	headers, err := decodeHeaders(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode headers: %w", err)
	}

	return &ResponseHeader{
		StatusCode: statusCode,
		StatusText: statusText,
		Headers:    headers,
	}, nil
}

func encodeBodyChunk(m *BodyChunk) ([]byte, error) {
	result := make([]byte, 0, len(m.Data)+1)
	result = append(result, m.Data...)
	if m.IsFinal {
		result = append(result, 0x01)
	} else {
		result = append(result, 0x00)
	}
	return result, nil
}

func decodeBodyChunk(r io.Reader, limits DecoderLimits) (*BodyChunk, error) {
	remaining, ok := limitedReaderRemainingBytes(r)
	if !ok {
		return nil, errors.New("decode body chunk requires a bounded frame reader")
	}
	limits = limits.normalized()
	maxPayload := limits.MaxBodyChunkBytes + 1
	if remaining > maxPayload {
		return nil, decodeLimitError("body chunk payload", remaining, maxPayload)
	}
	data := make([]byte, int(remaining))
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read body data: %w", err)
	}
	if len(data) == 0 {
		return &BodyChunk{}, nil
	}

	lastByte := data[len(data)-1]
	isFinal := false
	switch lastByte {
	case 0x01:
		isFinal = true
	case 0x00:
	default:
		return nil, fmt.Errorf("invalid is_final byte: 0x%02x", lastByte)
	}
	return &BodyChunk{Data: data[:len(data)-1], IsFinal: isFinal}, nil
}

func encodeError(m *Error) ([]byte, error) {
	result := make([]byte, 0, 64)
	statusBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(statusBytes, uint16(m.Code))
	result = append(result, statusBytes...)
	result = append(result, encodeString(m.Message)...)
	return result, nil
}

func decodeError(r io.Reader, limits DecoderLimits) (*Error, error) {
	statusBytes := make([]byte, 2)
	_, err := io.ReadFull(r, statusBytes)
	if err != nil {
		return nil, fmt.Errorf("decode code: %w", err)
	}
	code := int(binary.BigEndian.Uint16(statusBytes))

	message, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}

	return &Error{Code: code, Message: message}, nil
}

func encodeConnectProof(m *ConnectProof) ([]byte, error) {
	result := make([]byte, 0, 256)
	result = append(result, encodeString(m.ClusterID)...)
	result = append(result, encodeString(m.NamespaceID)...)
	result = append(result, encodeString(m.ServiceID)...)
	result = append(result, encodeString(m.SubjectPeerID)...)
	result = append(result, encodeString(m.ExpiresAt.UTC().Format(time.RFC3339Nano))...)
	result = append(result, encodeBytes(m.Nonce)...)
	result = append(result, encodeBytes(m.Capability)...)
	result = append(result, encodeBytes(m.Signature)...)
	if !m.IssuedAt.IsZero() || m.JTI != "" || len(m.AccessLeaseHash) > 0 {
		issuedAt := ""
		if !m.IssuedAt.IsZero() {
			issuedAt = m.IssuedAt.UTC().Format(time.RFC3339Nano)
		}
		result = append(result, encodeString(issuedAt)...)
		result = append(result, encodeString(m.JTI)...)
		result = append(result, encodeBytes(m.AccessLeaseHash)...)
	}
	return result, nil
}

// DecodeConnectProof decodes a connect proof payload with default nested-field limits.
func DecodeConnectProof(r io.Reader) (*ConnectProof, error) {
	return decodeConnectProof(r, DefaultDecoderLimits())
}

func decodeConnectProof(r io.Reader, limits DecoderLimits) (*ConnectProof, error) {
	clusterID, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode connect proof cluster_id: %w", err)
	}
	namespaceID, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode connect proof namespace_id: %w", err)
	}
	serviceID, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode connect proof service_id: %w", err)
	}
	subjectPeerID, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode connect proof subject_peer_id: %w", err)
	}
	expiresAtRaw, err := decodeString(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode connect proof expires_at: %w", err)
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, expiresAtRaw)
	if err != nil {
		return nil, fmt.Errorf("parse connect proof expires_at: %w", err)
	}
	nonce, err := decodeBytes(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode connect proof nonce: %w", err)
	}
	capabilityBytes, err := decodeBytes(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode connect proof capability: %w", err)
	}
	signature, err := decodeBytes(r, limits)
	if err != nil {
		return nil, fmt.Errorf("decode connect proof signature: %w", err)
	}
	var issuedAt time.Time
	var jti string
	var accessLeaseHash []byte
	if limitedReaderRemaining(r) > 0 {
		issuedAtRaw, err := decodeString(r, limits)
		if err != nil {
			return nil, fmt.Errorf("decode connect proof issued_at: %w", err)
		}
		if issuedAtRaw != "" {
			issuedAt, err = time.Parse(time.RFC3339Nano, issuedAtRaw)
			if err != nil {
				return nil, fmt.Errorf("parse connect proof issued_at: %w", err)
			}
		}
	}
	if limitedReaderRemaining(r) > 0 {
		jti, err = decodeString(r, limits)
		if err != nil {
			return nil, fmt.Errorf("decode connect proof jti: %w", err)
		}
	}
	if limitedReaderRemaining(r) > 0 {
		accessLeaseHash, err = decodeBytes(r, limits)
		if err != nil {
			return nil, fmt.Errorf("decode connect proof access_lease_hash: %w", err)
		}
	}
	return &ConnectProof{ClusterID: clusterID, NamespaceID: namespaceID, ServiceID: serviceID, SubjectPeerID: subjectPeerID, IssuedAt: issuedAt, ExpiresAt: expiresAt, Nonce: nonce, JTI: jti, Capability: capabilityBytes, AccessLeaseHash: accessLeaseHash, Signature: signature}, nil
}

func limitedReaderRemaining(r io.Reader) int64 {
	if lr, ok := r.(*io.LimitedReader); ok {
		return lr.N
	}
	return 0
}

func limitedReaderRemainingBytes(r io.Reader) (uint64, bool) {
	if lr, ok := r.(*io.LimitedReader); ok {
		if lr.N < 0 {
			return 0, true
		}
		return uint64(lr.N), true
	}
	return 0, false
}

// sortStrings sorts a string slice in place for deterministic encoding.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		key := s[i]
		j := i - 1
		for j >= 0 && s[j] > key {
			s[j+1] = s[j]
			j--
		}
		s[j+1] = key
	}
}
