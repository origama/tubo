package grants

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/origama/tubo/internal/capability"
)

const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDenied   = "denied"
	StatusExpired  = "expired"
)

type Request struct {
	ID                    string                           `json:"id"`
	ClusterName           string                           `json:"cluster_name"`
	ClusterID             string                           `json:"cluster_id"`
	NamespaceID           string                           `json:"namespace_id"`
	RequesterPeerID       string                           `json:"requester_peer_id"`
	ServiceName           string                           `json:"service_name"`
	ServiceID             string                           `json:"service_id"`
	ServicePublicKey      string                           `json:"service_public_key"`
	ServiceOwnerSignature []byte                           `json:"service_owner_signature,omitempty"`
	RequestNonce          string                           `json:"request_nonce"`
	ServicePeerID         string                           `json:"service_peer_id"`
	RequestedPermissions  []string                         `json:"requested_permissions"`
	RequestedTTLSeconds   int64                            `json:"requested_ttl_seconds,omitempty"`
	Status                string                           `json:"status"`
	RequestedAt           time.Time                        `json:"requested_at"`
	ExpiresAt             time.Time                        `json:"expires_at"`
	DecidedAt             time.Time                        `json:"decided_at,omitempty"`
	DenialReason          string                           `json:"denial_reason,omitempty"`
	ServiceClaim          *capability.ServiceClaim         `json:"service_claim,omitempty"`
	PublishLease          *PublishLease                    `json:"publish_lease,omitempty"`
	MembershipCapability  *capability.MembershipCapability `json:"membership_capability,omitempty"`
	ServiceShareToken     string                           `json:"service_share_token,omitempty"`
}

type fileState struct {
	Requests []Request `json:"requests"`
}

type Store struct {
	path string
	now  func() time.Time
}

func NewStore(path string) *Store {
	return &Store{path: path, now: func() time.Time { return time.Now().UTC() }}
}

func DefaultStorePath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "tubo", "grants", "requests.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "grants", "requests.json")
	}
	return filepath.Join(home, ".local", "share", "tubo", "grants", "requests.json")
}

func (s *Store) Path() string { return s.path }

func (s *Store) CreatePending(req Request) (Request, error) {
	if err := validateStoreRequest(req); err != nil {
		return Request{}, err
	}
	state, err := s.load()
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	state.expire(now)
	for _, existing := range state.Requests {
		if existing.Status == StatusPending && equivalentActive(existing, req) {
			return existing, nil
		}
	}
	if req.ID == "" {
		id, err := randomID("gr_")
		if err != nil {
			return Request{}, err
		}
		req.ID = id
	}
	if req.RequestedAt.IsZero() {
		req.RequestedAt = now
	}
	if req.ExpiresAt.IsZero() {
		req.ExpiresAt = req.RequestedAt.Add(24 * time.Hour)
	}
	req.Status = StatusPending
	state.Requests = append(state.Requests, req)
	state.sort()
	return req, s.save(state)
}

func (s *Store) ListPending() ([]Request, error) {
	state, changed, err := s.loadAndExpire()
	if err != nil {
		return nil, err
	}
	if changed {
		if err := s.save(state); err != nil {
			return nil, err
		}
	}
	var out []Request
	for _, req := range state.Requests {
		if req.Status == StatusPending {
			out = append(out, req)
		}
	}
	return out, nil
}

func (s *Store) ListAll() ([]Request, error) {
	state, changed, err := s.loadAndExpire()
	if err != nil {
		return nil, err
	}
	if changed {
		if err := s.save(state); err != nil {
			return nil, err
		}
	}
	return append([]Request(nil), state.Requests...), nil
}

func (s *Store) Get(id string) (Request, bool, error) {
	state, changed, err := s.loadAndExpire()
	if err != nil {
		return Request{}, false, err
	}
	if changed {
		if err := s.save(state); err != nil {
			return Request{}, false, err
		}
	}
	for _, req := range state.Requests {
		if req.ID == id {
			return req, true, nil
		}
	}
	return Request{}, false, nil
}

func (s *Store) Approve(id string, claim capability.ServiceClaim, lease *PublishLease, membership *capability.MembershipCapability, serviceShareToken string) (Request, error) {
	state, err := s.load()
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	state.expire(now)
	for i := range state.Requests {
		if state.Requests[i].ID != id {
			continue
		}
		if state.Requests[i].Status == StatusExpired || now.After(state.Requests[i].ExpiresAt.UTC()) {
			state.Requests[i].Status = StatusExpired
			_ = s.save(state)
			return Request{}, fmt.Errorf("grant request %q is expired", id)
		}
		if state.Requests[i].Status != StatusPending {
			return Request{}, fmt.Errorf("grant request %q is %s", id, state.Requests[i].Status)
		}
		state.Requests[i].Status = StatusApproved
		state.Requests[i].DecidedAt = now
		state.Requests[i].ServiceClaim = &claim
		state.Requests[i].PublishLease = lease
		state.Requests[i].MembershipCapability = membership
		state.Requests[i].ServiceShareToken = serviceShareToken
		if err := s.save(state); err != nil {
			return Request{}, err
		}
		return state.Requests[i], nil
	}
	return Request{}, fmt.Errorf("grant request %q not found", id)
}

func (s *Store) Deny(id, reason string) (Request, error) {
	state, err := s.load()
	if err != nil {
		return Request{}, err
	}
	now := s.now().UTC()
	state.expire(now)
	for i := range state.Requests {
		if state.Requests[i].ID != id {
			continue
		}
		if state.Requests[i].Status != StatusPending {
			return Request{}, fmt.Errorf("grant request %q is %s", id, state.Requests[i].Status)
		}
		state.Requests[i].Status = StatusDenied
		state.Requests[i].DenialReason = reason
		state.Requests[i].DecidedAt = now
		if err := s.save(state); err != nil {
			return Request{}, err
		}
		return state.Requests[i], nil
	}
	return Request{}, fmt.Errorf("grant request %q not found", id)
}

func (s *Store) ExpirePending() (int, error) {
	state, err := s.load()
	if err != nil {
		return 0, err
	}
	changed := state.expire(s.now().UTC())
	if changed == 0 {
		return 0, nil
	}
	return changed, s.save(state)
}

func (s *Store) loadAndExpire() (fileState, bool, error) {
	state, err := s.load()
	if err != nil {
		return fileState{}, false, err
	}
	changed := state.expire(s.now().UTC()) > 0
	return state, changed, nil
}

func (s *Store) load() (fileState, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return fileState{}, nil
	}
	if err != nil {
		return fileState{}, err
	}
	var state fileState
	if err := json.Unmarshal(b, &state); err != nil {
		return fileState{}, fmt.Errorf("decode grant request store %s: %w", s.path, err)
	}
	state.sort()
	return state, nil
}

func (s *Store) save(state fileState) error {
	state.sort()
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".requests-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func (s *fileState) expire(now time.Time) int {
	changed := 0
	for i := range s.Requests {
		if s.Requests[i].Status == StatusPending && !s.Requests[i].ExpiresAt.IsZero() && now.After(s.Requests[i].ExpiresAt.UTC()) {
			s.Requests[i].Status = StatusExpired
			changed++
		}
	}
	return changed
}

func (s *fileState) sort() {
	sort.SliceStable(s.Requests, func(i, j int) bool { return s.Requests[i].RequestedAt.Before(s.Requests[j].RequestedAt) })
}

func equivalentActive(a, b Request) bool {
	return a.ClusterID == b.ClusterID && a.NamespaceID == b.NamespaceID && a.RequesterPeerID == b.RequesterPeerID && a.ServiceID == b.ServiceID && a.ServicePeerID == b.ServicePeerID && a.RequestNonce == b.RequestNonce
}

func validateStoreRequest(req Request) error {
	if req.ClusterName == "" || req.ClusterID == "" || req.NamespaceID == "" || req.RequesterPeerID == "" || req.ServiceName == "" || req.ServiceID == "" || req.ServicePublicKey == "" || len(req.ServiceOwnerSignature) == 0 || req.RequestNonce == "" || req.ServicePeerID == "" {
		return errors.New("grant request is missing required fields")
	}
	return nil
}

func randomID(prefix string) (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}
