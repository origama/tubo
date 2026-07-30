package protocol

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	TunnelID            = "/tubo/tunnel/2.0"
	RecordsID           = "/tubo/records/2.0"
	AuthorityID         = "/tubo/authority/2.0"
	AuthoritySnapshotID = "/tubo/authority/2.1"
	TunnelResumeID      = "/tubo/tunnel-resume/2.1"
	MaxFrame            = 64 << 10
	MaxResumeFrame      = 4 << 10

	// Stream roles for TunnelResumeID
	StreamRoleControl = "control"
	StreamRoleResume  = "resume"

	// Capability for session resume
	CapSessionResumeV1 = "session-resume-v1"
)

func WriteFrame(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > MaxFrame {
		return errors.New("frame exceeds 64 KiB")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(data)))
	if _, err := w.Write(size[:]); err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func ReadFrame(r io.Reader, value any) error {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > MaxFrame {
		return fmt.Errorf("invalid frame size %d", n)
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(r, data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bufio.NewReaderSize(bytesReader(data), len(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("trailing frame payload")
	}
	return nil
}

type byteReader struct {
	data   []byte
	offset int
}

func bytesReader(data []byte) *byteReader { return &byteReader{data: data} }
func (r *byteReader) Read(p []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.offset:])
	r.offset += n
	return n, nil
}

type AuthorityRequest struct {
	Operation    string          `json:"operation"`
	Token        string          `json:"token"`
	PipeID       string          `json:"pipe_id,omitempty"`
	ClientPublic string          `json:"client_public"`
	Nonce        string          `json:"nonce"`
	Proof        string          `json:"proof"`
	Record       json.RawMessage `json:"record,omitempty"`
	Subjects     []string        `json:"subjects,omitempty"`
	AccessLease  string          `json:"access_lease,omitempty"`
}
type AuthorityResponse struct {
	OK           bool              `json:"ok"`
	Error        string            `json:"error,omitempty"`
	Lease        string            `json:"lease,omitempty"`
	RefreshLease string            `json:"refresh_lease,omitempty"`
	Grant        string            `json:"grant,omitempty"`
	Invite       string            `json:"invite,omitempty"`
	Records      []json.RawMessage `json:"records,omitempty"`
	Revocations  []Revocation      `json:"revocations,omitempty"`
}
type Revocation struct {
	SubjectType string `json:"subject_type"`
	SubjectJTI  string `json:"subject_jti"`
	Epoch       uint64 `json:"epoch"`
}

// AuthoritySnapshotRequest is exclusive to /tubo/authority/2.1 records.list.
// It deliberately has no operation field: this protocol serves one bounded,
// complete snapshot operation only.
type AuthoritySnapshotRequest struct {
	Token        string `json:"token"`
	ClientPublic string `json:"client_public"`
	Nonce        string `json:"nonce"`
	Proof        string `json:"proof"`
}

// AuthoritySnapshotResponse carries authority-global ordering metadata. Both
// counters are issued by pinned authority; record sequences are never used as
// snapshot ordering metadata.
type AuthoritySnapshotResponse struct {
	OK         bool              `json:"ok"`
	Error      string            `json:"error,omitempty"`
	Generation uint64            `json:"generation,omitempty"`
	HighWater  uint64            `json:"high_water,omitempty"`
	Records    []json.RawMessage `json:"records,omitempty"`
}

type ClientHello struct {
	Version      string   `json:"version"`
	ClientPublic string   `json:"client_public"`
	Capabilities []string `json:"capabilities"`
}
type ServerHello struct {
	Version      string   `json:"version"`
	PipeID       string   `json:"pipe_id"`
	Challenge    string   `json:"challenge"`
	Capabilities []string `json:"capabilities"`
}
type OpenPipe struct {
	PipeID         string   `json:"pipe_id"`
	ChallengeProof string   `json:"challenge_proof"`
	AccessLease    string   `json:"access_lease"`
	VisitedPipeIDs []string `json:"visited_pipe_ids"`
	HopLimit       int      `json:"hop_limit"`
}
type OpenResult struct {
	OK          bool         `json:"ok"`
	SessionID   string       `json:"session_id,omitempty"`
	Protocol    string       `json:"protocol,omitempty"`
	ResumeOffer *ResumeOffer `json:"resume_offer,omitempty"`
	ErrorCode   string       `json:"error_code,omitempty"`
	Message     string       `json:"message,omitempty"`
	RetryClass  string       `json:"retry_class,omitempty"`
}

// ResumeOffer is sent in OpenOK to negotiate resume capability.
type ResumeOffer struct {
	ControlProtocol   string `json:"control_protocol"`
	ResumeProtocol    string `json:"resume_protocol"`
	MaxInflightPerDir int    `json:"max_inflight_per_dir"`
	AckBytes          int    `json:"ack_bytes"`
	AckIntervalMs     int    `json:"ack_interval_ms"`
	DetachedGraceMs   int    `json:"detached_grace_ms"`
	ResumeSecret      string `json:"resume_secret"` // base64, 32 bytes
}

// ResumeSecretSize is the byte length of the resume secret.
const ResumeSecretSize = 32

// StreamRoleDeclaration is the first message on a TunnelResumeID stream.
type StreamRoleDeclaration struct {
	Role      string `json:"role"`       // "control" or "resume"
	SessionID string `json:"session_id"` // session this stream belongs to
}

// ResumeHello is the first message in a resume candidate handshake.
type ResumeHello struct {
	Version   string `json:"version"`
	PipeID    string `json:"pipe_id"`
	SessionID string `json:"session_id"`
	NextEpoch uint64 `json:"next_epoch"`
	Nonce     string `json:"nonce"` // 32 bytes base64
	Proof     string `json:"proof"` // HMAC-SHA256 base64
}

// ResumeChallenge is sent by server in response to ResumeHello.
type ResumeChallenge struct {
	SessionID string `json:"session_id"`
	NextEpoch uint64 `json:"next_epoch"`
	Nonce     string `json:"nonce"` // 32 bytes base64, server nonce
	Proof     string `json:"proof"` // HMAC-SHA256 base64
}

// ResumeCommit carries the sender's observed offsets.
type ResumeCommit struct {
	SessionID       string `json:"session_id"`
	NextEpoch       uint64 `json:"next_epoch"`
	ClientNonce     string `json:"client_nonce"`
	ServerNonce     string `json:"server_nonce"`
	C2SOldSent      uint64 `json:"c2s_old_sent"`  // highest offset sent on old carrier
	S2CDelivered    uint64 `json:"s2c_delivered"` // committed offset from server->client
	C2SFinSeen      bool   `json:"c2s_fin_seen"`
	S2CFinDelivered bool   `json:"s2c_fin_delivered"`
	Proof           string `json:"proof"`
}

// ResumeResult is the server's final offset negotiation.
type ResumeResult struct {
	OK              bool   `json:"ok"`
	SessionID       string `json:"session_id"`
	Epoch           uint64 `json:"epoch"`
	C2SDelivered    uint64 `json:"c2s_delivered"`
	S2COldSent      uint64 `json:"s2c_old_sent"`
	C2SResumeAt     uint64 `json:"c2s_resume_at"`
	S2CResumeAt     uint64 `json:"s2c_resume_at"`
	C2SFinDelivered bool   `json:"c2s_fin_delivered"`
	S2CFinSeen      bool   `json:"s2c_fin_seen"`
	ErrorCode       string `json:"error_code,omitempty"`
	Proof           string `json:"proof"`
}

// ResumeActivate is sent by the client to activate the new carrier.
type ResumeActivate struct {
	SessionID        string `json:"session_id"`
	Epoch            uint64 `json:"epoch"`
	TranscriptDigest string `json:"transcript_digest"`
	Proof            string `json:"proof"`
}

// ResumeActivated is sent by the server confirming activation.
type ResumeActivated struct {
	SessionID string `json:"session_id"`
	Epoch     uint64 `json:"epoch"`
	Proof     string `json:"proof"`
}

// Ack is a continuous ACK on the control stream.
type Ack struct {
	SessionID       string `json:"session_id"`
	Epoch           uint64 `json:"epoch"`
	ControlSeq      uint64 `json:"control_seq"`
	Direction       string `json:"direction"` // "c2s" or "s2c"
	DeliveredOffset uint64 `json:"delivered_offset"`
	FinDelivered    bool   `json:"fin_delivered"`
	Proof           string `json:"proof"`
}

// SwitchPrepare announces intent to switch.
type SwitchPrepare struct {
	SessionID      string `json:"session_id"`
	FromEpoch      uint64 `json:"from_epoch"`
	ToEpoch        uint64 `json:"to_epoch"`
	CandidateNonce string `json:"candidate_nonce"`
	Reason         string `json:"reason"` // "planned" or "recovery"
	Proof          string `json:"proof"`
}

// Barrier begins quiescing one direction.
type Barrier struct {
	SessionID string `json:"session_id"`
	ToEpoch   uint64 `json:"to_epoch"`
	Direction string `json:"direction"` // "c2s" or "s2c"
	CutOffset uint64 `json:"cut_offset"`
	FinSeen   bool   `json:"fin_seen"`
	Proof     string `json:"proof"`
}

// BarrierAck acknowledges barrier completion.
type BarrierAck struct {
	SessionID       string `json:"session_id"`
	ToEpoch         uint64 `json:"to_epoch"`
	Direction       string `json:"direction"`
	DeliveredOffset uint64 `json:"delivered_offset"`
	FinDelivered    bool   `json:"fin_delivered"`
	Proof           string `json:"proof"`
}

// SwitchAbort cancels a planned switch.
type SwitchAbort struct {
	SessionID string `json:"session_id"`
	ToEpoch   uint64 `json:"to_epoch"`
	Reason    string `json:"reason"`
	Proof     string `json:"proof"`
}

// SwitchSuggested is sent by server to hint a switch.
type SwitchSuggested struct {
	SessionID string `json:"session_id"`
	ToEpoch   uint64 `json:"to_epoch"`
	Reason    string `json:"reason"`
}
