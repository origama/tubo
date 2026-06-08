package protocol

import (
	"time"

	iversion "github.com/origama/tubo/internal/version"
)

// Protocol version identifier
const (
	ProtocolVersion = "1.1"
	ProtocolID      = "/p2p-tunnel/1.1"
	ProtocolMajor   = iversion.ProtocolMajor
	ProtocolMinor   = 1
)

// Frame types
const (
	FrameTypeHello          byte = 0x00
	FrameTypeRequestHeader  byte = 0x01
	FrameTypeResponseHeader byte = 0x02
	FrameTypeBodyChunk      byte = 0x03
	FrameTypeError          byte = 0x04
	FrameTypeConnectProof   byte = 0x05
	FrameTypeTunnelRequest  byte = 0x06
	FrameTypeTunnelReady    byte = 0x07
)

type Hello struct {
	ProtocolMajor uint16
	ProtocolMinor uint16
	Role          string
	Capabilities  []string
}

const (
	CapabilityHelloV1        = "hello-v1"
	CapabilityConnectProofV1 = "connect-proof-v1"
	CapabilityRawTCPV1       = "raw-tcp-v1"
)

func SupportedCapabilities() []string {
	return []string{CapabilityHelloV1, CapabilityConnectProofV1, CapabilityRawTCPV1}
}

func NegotiateCapabilities(remote []string) []string {
	supported := make(map[string]struct{}, len(SupportedCapabilities()))
	for _, cap := range SupportedCapabilities() {
		supported[cap] = struct{}{}
	}
	common := make([]string, 0, len(remote))
	for _, cap := range remote {
		if _, ok := supported[cap]; ok {
			common = append(common, cap)
		}
	}
	sortStrings(common)
	return common
}

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

// ConnectProof carries a signed connect authorization proof from the client peer.
type TunnelRequest struct {
	Kind string
}

type TunnelReady struct {
	Kind string
}

type ConnectProof struct {
	ClusterID       string    `json:"cluster_id"`
	NamespaceID     string    `json:"namespace_id"`
	ServiceID       string    `json:"service_id"`
	SubjectPeerID   string    `json:"subject_peer_id"`
	IssuedAt        time.Time `json:"issued_at,omitempty"`
	ExpiresAt       time.Time `json:"expires_at"`
	Nonce           []byte    `json:"nonce"`
	JTI             string    `json:"jti,omitempty"`
	Capability      []byte    `json:"capability"`
	AccessLeaseHash []byte    `json:"access_lease_hash,omitempty"`
	Signature       []byte    `json:"signature,omitempty"`
}
