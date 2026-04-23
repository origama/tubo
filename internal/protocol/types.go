package protocol

// Protocol version identifier
const (
	ProtocolVersion = "1.0"
	ProtocolID      = "/p2p-tunnel/1.0"
)

// Frame types
const (
	FrameTypeRequestHeader byte = 0x01
	FrameTypeResponseHeader byte = 0x02
	FrameTypeBodyChunk     byte = 0x03
	FrameTypeError         byte = 0x04
)

// RequestHeader carries HTTP request metadata from Edge Gateway to Connector.
type RequestHeader struct {
	Method            string
	Path              string
	Query             string
	Headers           map[string][]string // Multi-value headers preserved
	ContentLengthHint int64               // -1 if unknown (streaming)
}

// ResponseHeader carries HTTP response metadata from Connector to Edge Gateway.
type ResponseHeader struct {
	StatusCode int
	StatusText string
	Headers    map[string][]string // Multi-value headers preserved
}

// BodyChunk carries a chunk of request/response body data.
type BodyChunk struct {
	Data    []byte
	IsFinal bool // true when this is the last chunk
}

// Error frame signals an error condition on the stream.
type Error struct {
	Code    int
	Message string
}
