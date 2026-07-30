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
	MaxFrame            = 64 << 10
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
	OK         bool   `json:"ok"`
	SessionID  string `json:"session_id,omitempty"`
	Protocol   string `json:"protocol,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
	Message    string `json:"message,omitempty"`
	RetryClass string `json:"retry_class,omitempty"`
}
