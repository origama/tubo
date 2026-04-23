package protocol

import (
	"encoding/binary"
	"fmt"
	"io"

	"github.com/multiformats/go-varint"
)

// EncodeFrame writes a frame to w with varint length prefix + type byte + payload.
func EncodeFrame(w io.Writer, msg any) error {
	var ft byte
	var payload []byte
	var err error

	switch m := msg.(type) {
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
	default:
		return fmt.Errorf("unknown frame type: %T", msg)
	}
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}

	// Write varint length prefix + type byte + payload
	lenBytes := varint.ToUvarint(uint64(len(payload)))
	if _, err := w.Write(lenBytes); err != nil {
		return fmt.Errorf("write length: %w", err)
	}
	if err := binary.Write(w, binary.BigEndian, ft); err != nil {
		return fmt.Errorf("write type: %w", err)
	}
	_, err = w.Write(payload)
	return err
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

func decodeString(r io.Reader) (string, error) {
	lenVal, err := readVarint(r)
	if err != nil {
		return "", fmt.Errorf("decode string length: %w", err)
	}
	b := make([]byte, lenVal)
	_, err = io.ReadFull(r, b)
	if err != nil {
		return "", fmt.Errorf("read string data: %w", err)
	}
	return string(b), nil
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

func decodeHeaders(r io.Reader) (map[string][]string, error) {
	count, err := readVarint(r)
	if err != nil {
		return nil, fmt.Errorf("decode headers count: %w", err)
	}

	headers := make(map[string][]string, count)
	for i := uint64(0); i < count; i++ {
		name, err := decodeString(r)
		if err != nil {
			return nil, fmt.Errorf("decode header name: %w", err)
		}
		valCount, err := readVarint(r)
		if err != nil {
			return nil, fmt.Errorf("decode values count for %q: %w", name, err)
		}
		values := make([]string, valCount)
		for j := uint64(0); j < valCount; j++ {
			v, err := decodeString(r)
			if err != nil {
				return nil, fmt.Errorf("decode value for %q: %w", name, err)
			}
			values[j] = v
		}
		headers[name] = values
	}
	return headers, nil
}

// --- Frame type encoders ---

func encodeRequestHeader(m *RequestHeader) ([]byte, error) {
	result := make([]byte, 0, 128)
	result = append(result, encodeString(m.Method)...)
	result = append(result, encodeString(m.Path)...)
	result = append(result, encodeString(m.Query)...)
	result = append(result, encodeHeaders(m.Headers)...)

	// ContentLengthHint as signed varint (zigzag encoding)
	clh := uint64(m.ContentLengthHint)
	if m.ContentLengthHint < 0 {
		clh = uint64((-m.ContentLengthHint-1)<<1) | 1
	} else {
		clh = uint64(m.ContentLengthHint) << 1
	}
	result = append(result, varint.ToUvarint(clh)...)
	return result, nil
}

func decodeRequestHeader(r io.Reader) (*RequestHeader, error) {
	method, err := decodeString(r)
	if err != nil {
		return nil, fmt.Errorf("decode method: %w", err)
	}
	path, err := decodeString(r)
	if err != nil {
		return nil, fmt.Errorf("decode path: %w", err)
	}
	query, err := decodeString(r)
	if err != nil {
		return nil, fmt.Errorf("decode query: %w", err)
	}
	headers, err := decodeHeaders(r)
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

func decodeResponseHeader(r io.Reader) (*ResponseHeader, error) {
	statusBytes := make([]byte, 2)
	_, err := io.ReadFull(r, statusBytes)
	if err != nil {
		return nil, fmt.Errorf("decode status_code: %w", err)
	}
	statusCode := int(binary.BigEndian.Uint16(statusBytes))

	statusText, err := decodeString(r)
	if err != nil {
		return nil, fmt.Errorf("decode status_text: %w", err)
	}
	headers, err := decodeHeaders(r)
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

func decodeBodyChunk(r io.Reader) (*BodyChunk, error) {
	// We need to know the payload size. The caller passes a LimitedReader.
	data := make([]byte, 16384) // read in chunks if needed
	totalRead := 0

	for {
		n, err := r.Read(data[totalRead:])
		totalRead += n
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read body data: %w", err)
		}
		if totalRead >= len(data) {
			// Shouldn't happen with LimitedReader, but handle gracefully
			newBuf := make([]byte, len(data)*2)
			copy(newBuf, data)
			data = newBuf
		}
	}

	data = data[:totalRead]
	isFinal := false
	if len(data) > 0 {
		lastByte := data[len(data)-1]
		switch lastByte {
		case 0x01:
			isFinal = true
			data = data[:len(data)-1]
		case 0x00:
			data = data[:len(data)-1]
		default:
			return nil, fmt.Errorf("invalid is_final byte: 0x%02x", lastByte)
		}
	}

	return &BodyChunk{Data: data, IsFinal: isFinal}, nil
}

func encodeError(m *Error) ([]byte, error) {
	result := make([]byte, 0, 64)
	statusBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(statusBytes, uint16(m.Code))
	result = append(result, statusBytes...)
	result = append(result, encodeString(m.Message)...)
	return result, nil
}

func decodeError(r io.Reader) (*Error, error) {
	statusBytes := make([]byte, 2)
	_, err := io.ReadFull(r, statusBytes)
	if err != nil {
		return nil, fmt.Errorf("decode code: %w", err)
	}
	code := int(binary.BigEndian.Uint16(statusBytes))

	message, err := decodeString(r)
	if err != nil {
		return nil, fmt.Errorf("decode message: %w", err)
	}

	return &Error{Code: code, Message: message}, nil
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
