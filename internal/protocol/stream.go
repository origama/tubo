package protocol

import (
	"fmt"
	"io"
)

// StreamReader reads frames from an io.Reader.
type StreamReader struct {
	r io.Reader
}

// NewStreamReader creates a new StreamReader.
func NewStreamReader(r io.Reader) *StreamReader {
	return &StreamReader{r: r}
}

// readFrameHeader reads the varint length prefix and frame type byte.
// Returns (payload_length, frame_type).
func (s *StreamReader) readFrameHeader() (uint64, byte, error) {
	length, err := readVarint(s.r)
	if err != nil {
		if err == io.EOF {
			return 0, 0, io.EOF
		}
		return 0, 0, fmt.Errorf("read length prefix: %w", err)
	}

	// Read frame type byte
	typeBuf := make([]byte, 1)
	_, err = io.ReadFull(s.r, typeBuf)
	if err != nil {
		return 0, 0, fmt.Errorf("read frame type: %w", err)
	}

	return length, typeBuf[0], nil
}

// ReadRequestHeader reads a RequestHeader frame. Returns io.EOF if no more data.
func (s *StreamReader) ReadRequestHeader() (*RequestHeader, error) {
	length, ft, err := s.readFrameHeader()
	if err != nil {
		return nil, err
	}
	if ft != FrameTypeRequestHeader {
		return nil, fmt.Errorf("expected RequestHeader (0x%02x), got frame type 0x%02x", FrameTypeRequestHeader, ft)
	}

	r := &io.LimitedReader{R: s.r, N: int64(length)}
	return decodeRequestHeader(r)
}

// ReadResponseHeader reads a ResponseHeader frame. Returns io.EOF if no more data.
func (s *StreamReader) ReadResponseHeader() (*ResponseHeader, error) {
	length, ft, err := s.readFrameHeader()
	if err != nil {
		return nil, err
	}
	if ft != FrameTypeResponseHeader {
		return nil, fmt.Errorf("expected ResponseHeader (0x%02x), got frame type 0x%02x", FrameTypeResponseHeader, ft)
	}

	r := &io.LimitedReader{R: s.r, N: int64(length)}
	return decodeResponseHeader(r)
}

// ReadBodyChunk reads a BodyChunk frame. Returns io.EOF if no more data.
func (s *StreamReader) ReadBodyChunk() (*BodyChunk, error) {
	length, ft, err := s.readFrameHeader()
	if err != nil {
		return nil, err
	}
	if ft != FrameTypeBodyChunk {
		return nil, fmt.Errorf("expected BodyChunk (0x%02x), got frame type 0x%02x", FrameTypeBodyChunk, ft)
	}

	r := &io.LimitedReader{R: s.r, N: int64(length)}
	return decodeBodyChunk(r)
}

// ReadError reads an Error frame. Returns io.EOF if no more data.
func (s *StreamReader) ReadError() (*Error, error) {
	length, ft, err := s.readFrameHeader()
	if err != nil {
		return nil, err
	}
	if ft != FrameTypeError {
		return nil, fmt.Errorf("expected Error (0x%02x), got frame type 0x%02x", FrameTypeError, ft)
	}

	r := &io.LimitedReader{R: s.r, N: int64(length)}
	return decodeError(r)
}

// BodyReader returns an io.ReadCloser that reads body data from consecutive BodyChunk frames.
// It stops when it encounters a non-BodyChunk frame or EOF. The returned reader wraps the
// underlying stream and should be consumed fully before reading further frames.
func (s *StreamReader) BodyReader() io.ReadCloser {
	return &bodyReader{reader: s}
}

type bodyReader struct {
	reader    *StreamReader
	chunk     []byte
	offset    int
	lastChunk bool
	closed    bool
	exhausted bool
}

func (br *bodyReader) Read(p []byte) (int, error) {
	if br.exhausted || br.closed {
		return 0, io.EOF
	}

	// If we've fully consumed the final chunk, terminate cleanly.
	if br.lastChunk && br.offset >= len(br.chunk) {
		br.exhausted = true
		return 0, io.EOF
	}

	for len(br.chunk)-br.offset == 0 {
		chunk, err := br.reader.ReadBodyChunk()
		if err != nil {
			if err == io.EOF {
				br.exhausted = true
				return 0, io.EOF
			}
			return 0, err
		}
		br.chunk = chunk.Data
		br.offset = 0
		br.lastChunk = chunk.IsFinal
		if len(br.chunk) == 0 && chunk.IsFinal {
			br.exhausted = true
			return 0, io.EOF
		}
		// Skip empty non-final chunks, read the next one.
		if len(br.chunk) == 0 {
			continue
		}
		break
	}

	n := copy(p, br.chunk[br.offset:])
	br.offset += n
	return n, nil
}

func (br *bodyReader) Close() error {
	br.closed = true
	br.exhausted = true
	return nil
}

// --- StreamWriter ---

// StreamWriter writes frames to an io.Writer.
type StreamWriter struct {
	w io.Writer
}

// NewStreamWriter creates a new StreamWriter.
func NewStreamWriter(w io.Writer) *StreamWriter {
	return &StreamWriter{w: w}
}

// WriteRequestHeader encodes and writes a RequestHeader frame.
func (s *StreamWriter) WriteRequestHeader(m *RequestHeader) error {
	return EncodeFrame(s.w, m)
}

// WriteResponseHeader encodes and writes a ResponseHeader frame.
func (s *StreamWriter) WriteResponseHeader(m *ResponseHeader) error {
	return EncodeFrame(s.w, m)
}

// WriteBodyChunk encodes and writes a BodyChunk frame.
func (s *StreamWriter) WriteBodyChunk(m *BodyChunk) error {
	return EncodeFrame(s.w, m)
}

// WriteError encodes and writes an Error frame.
func (s *StreamWriter) WriteError(m *Error) error {
	return EncodeFrame(s.w, m)
}
