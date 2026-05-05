package p2p

import (
	libprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/origama/tubo/internal/protocol"
)

// ProtocolID is the preferred libp2p protocol identifier for tunnel streams.
const ProtocolID = protocol.ProtocolID

// LegacyProtocolID is the previous stream protocol identifier kept for backward compatibility.
const LegacyProtocolID = protocol.LegacyProtocolID

// ProtocolVersion is the wire protocol version.
const ProtocolVersion = protocol.ProtocolVersion

func SupportedProtocolIDs() []libprotocol.ID {
	return []libprotocol.ID{libprotocol.ID(ProtocolID), libprotocol.ID(LegacyProtocolID)}
}

// This file is kept for backward compatibility references only.
// All wire protocol types have moved to the internal/protocol package:
//   - RequestMessage → protocol.RequestHeader
//   - ResponseMessage → protocol.ResponseHeader + BodyChunk frames
//
// The legacy JSON protocol had critical bugs (multi-value header truncation, no streaming).
// New code should use the binary framing protocol in internal/protocol.
