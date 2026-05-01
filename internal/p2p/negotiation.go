package p2p

import (
	"sync"
	"time"

	"p2p-api-tunnel/internal/protocol"
)

type NegotiationEvent struct {
	Timestamp             time.Time `json:"timestamp"`
	LocalRole             string    `json:"local_role"`
	RemoteRole            string    `json:"remote_role"`
	StreamProtocolID      string    `json:"stream_protocol_id"`
	LocalProtocolVersion  string    `json:"local_protocol_version"`
	RemoteProtocolVersion string    `json:"remote_protocol_version"`
	Capabilities          []string  `json:"capabilities"`
}

var negotiationState = struct {
	sync.Mutex
	recent []NegotiationEvent
}{}

const maxRecentNegotiations = 32

func RecordNegotiation(localRole, remoteRole, streamProtocolID, remoteProtocolVersion string, capabilities []string) {
	negotiationState.Lock()
	defer negotiationState.Unlock()
	copiedCaps := append([]string(nil), capabilities...)
	event := NegotiationEvent{
		Timestamp:             time.Now().UTC(),
		LocalRole:             localRole,
		RemoteRole:            remoteRole,
		StreamProtocolID:      streamProtocolID,
		LocalProtocolVersion:  protocol.ProtocolVersion,
		RemoteProtocolVersion: remoteProtocolVersion,
		Capabilities:          copiedCaps,
	}
	negotiationState.recent = append([]NegotiationEvent{event}, negotiationState.recent...)
	if len(negotiationState.recent) > maxRecentNegotiations {
		negotiationState.recent = negotiationState.recent[:maxRecentNegotiations]
	}
}

func RecentNegotiations() []NegotiationEvent {
	negotiationState.Lock()
	defer negotiationState.Unlock()
	out := make([]NegotiationEvent, len(negotiationState.recent))
	copy(out, negotiationState.recent)
	return out
}
