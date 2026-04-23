package p2p

import "p2p-api-tunnel/internal/protocol"

// ProtocolID is the libp2p protocol identifier for tunnel streams.
const ProtocolID = protocol.ProtocolID

// ProtocolVersion is the wire protocol version.
const ProtocolVersion = protocol.ProtocolVersion

// This file is kept for backward compatibility references only.
// All wire protocol types have moved to the internal/protocol package:
//   - RequestMessage → protocol.RequestHeader
//   - ResponseMessage → protocol.ResponseHeader + BodyChunk frames
//
// The legacy JSON protocol had critical bugs (multi-value header truncation, no streaming).
// New code should use the binary framing protocol in internal/protocol.
