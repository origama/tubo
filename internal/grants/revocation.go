package grants

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

const (
	RevocationStateVersion = "v1"

	RevocationKindInvite        = "invite"
	RevocationKindSession       = "session"
	RevocationKindServiceAccess = "service-access"
	RevocationKindPublish       = "publish"
)

type RevocationRecord struct {
	Kind      string    `json:"kind"`
	ID        string    `json:"id,omitempty"`
	ServiceID string    `json:"service_id,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	RevokedAt time.Time `json:"revoked_at"`
}

type RevocationEpochs struct {
	AccessEpoch  int64 `json:"access_epoch,omitempty"`
	PublishEpoch int64 `json:"publish_epoch,omitempty"`
}

type RevocationState struct {
	Version             string                      `json:"version"`
	RevokedInvites      map[string]RevocationRecord `json:"revoked_invites,omitempty"`
	RevokedSessions     map[string]RevocationRecord `json:"revoked_sessions,omitempty"`
	ServiceAccessEpochs map[string]int64            `json:"service_access_epochs,omitempty"`
	PublishEpochs       map[string]int64            `json:"publish_epochs,omitempty"`
	RevokedPublish      map[string]RevocationRecord `json:"revoked_publish,omitempty"`
}

type RevocationStore struct {
	path string
	now  func() time.Time
}

func NewRevocationStore(path string) *RevocationStore {
	return &RevocationStore{path: path, now: func() time.Time { return time.Now().UTC() }}
}

func DefaultRevocationStorePath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "tubo", "grants", "revocations.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "grants", "revocations.json")
	}
	return filepath.Join(home, ".local", "share", "tubo", "grants", "revocations.json")
}

func (s *RevocationStore) Path() string { return s.path }

func (s *RevocationStore) RevokeInvite(jti, reason string) (RevocationRecord, error) {
	if jti == "" {
		return RevocationRecord{}, errors.New("invite id is required")
	}
	state, err := s.load()
	if err != nil {
		return RevocationRecord{}, err
	}
	rec := RevocationRecord{Kind: RevocationKindInvite, ID: jti, Reason: reason, RevokedAt: s.now().UTC()}
	state.RevokedInvites[jti] = rec
	return rec, s.save(state)
}

func (s *RevocationStore) IsInviteRevoked(jti string) (bool, RevocationRecord, error) {
	state, err := s.load()
	if err != nil {
		return false, RevocationRecord{}, err
	}
	rec, ok := state.RevokedInvites[jti]
	return ok, rec, nil
}

func (s *RevocationStore) RevokeSession(sessionID, reason string) (RevocationRecord, error) {
	if sessionID == "" {
		return RevocationRecord{}, errors.New("session id is required")
	}
	state, err := s.load()
	if err != nil {
		return RevocationRecord{}, err
	}
	rec := RevocationRecord{Kind: RevocationKindSession, ID: sessionID, Reason: reason, RevokedAt: s.now().UTC()}
	state.RevokedSessions[sessionID] = rec
	return rec, s.save(state)
}

func (s *RevocationStore) IsSessionRevoked(sessionID string) (bool, RevocationRecord, error) {
	state, err := s.load()
	if err != nil {
		return false, RevocationRecord{}, err
	}
	rec, ok := state.RevokedSessions[sessionID]
	return ok, rec, nil
}

func (s *RevocationStore) RevokeServiceAccess(serviceID, reason string) (int64, error) {
	if serviceID == "" {
		return 0, errors.New("service id is required")
	}
	state, err := s.load()
	if err != nil {
		return 0, err
	}
	state.ServiceAccessEpochs[serviceID]++
	return state.ServiceAccessEpochs[serviceID], s.save(state)
}

func (s *RevocationStore) ServiceAccessEpoch(serviceID string) (int64, error) {
	state, err := s.load()
	if err != nil {
		return 0, err
	}
	return state.ServiceAccessEpochs[serviceID], nil
}

func (s *RevocationStore) RevokePublish(serviceID, reason string) (int64, error) {
	if serviceID == "" {
		return 0, errors.New("service id is required")
	}
	state, err := s.load()
	if err != nil {
		return 0, err
	}
	state.PublishEpochs[serviceID]++
	state.RevokedPublish[serviceID] = RevocationRecord{Kind: RevocationKindPublish, ServiceID: serviceID, Reason: reason, RevokedAt: s.now().UTC()}
	return state.PublishEpochs[serviceID], s.save(state)
}

func (s *RevocationStore) IsPublishRevoked(serviceID string) (bool, RevocationRecord, error) {
	state, err := s.load()
	if err != nil {
		return false, RevocationRecord{}, err
	}
	rec, ok := state.RevokedPublish[serviceID]
	return ok, rec, nil
}

func (s *RevocationStore) PublishEpoch(serviceID string) (int64, error) {
	state, err := s.load()
	if err != nil {
		return 0, err
	}
	return state.PublishEpochs[serviceID], nil
}

func (s *RevocationStore) EpochsForService(serviceID string) (RevocationEpochs, error) {
	state, err := s.load()
	if err != nil {
		return RevocationEpochs{}, err
	}
	return RevocationEpochs{AccessEpoch: state.ServiceAccessEpochs[serviceID], PublishEpoch: state.PublishEpochs[serviceID]}, nil
}

func (s *RevocationStore) load() (RevocationState, error) {
	state := RevocationState{Version: RevocationStateVersion}
	state.ensureMaps()
	if s == nil || s.path == "" {
		return state, nil
	}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return RevocationState{}, err
	}
	if len(b) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return RevocationState{}, err
	}
	if state.Version == "" {
		state.Version = RevocationStateVersion
	}
	state.ensureMaps()
	return state, nil
}

func (s *RevocationStore) save(state RevocationState) error {
	if s == nil || s.path == "" {
		return nil
	}
	state.Version = RevocationStateVersion
	state.ensureMaps()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".revocations-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func (s *RevocationState) ensureMaps() {
	if s.RevokedInvites == nil {
		s.RevokedInvites = map[string]RevocationRecord{}
	}
	if s.RevokedSessions == nil {
		s.RevokedSessions = map[string]RevocationRecord{}
	}
	if s.ServiceAccessEpochs == nil {
		s.ServiceAccessEpochs = map[string]int64{}
	}
	if s.PublishEpochs == nil {
		s.PublishEpochs = map[string]int64{}
	}
	if s.RevokedPublish == nil {
		s.RevokedPublish = map[string]RevocationRecord{}
	}
}
