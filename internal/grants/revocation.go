package grants

import (
	"encoding/json"
	"errors"
	"fmt"
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
	path        string
	now         func() time.Time
	LockTimeout time.Duration
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
	rec := RevocationRecord{Kind: RevocationKindInvite, ID: jti, Reason: reason, RevokedAt: s.now().UTC()}
	err := s.update(func(state *RevocationState) {
		state.RevokedInvites[jti] = rec
	})
	return rec, err
}

func (s *RevocationStore) IsInviteRevoked(jti string) (bool, RevocationRecord, error) {
	var rec RevocationRecord
	var ok bool
	err := s.read(func(state RevocationState) { rec, ok = state.RevokedInvites[jti] })
	return ok, rec, err
}

func (s *RevocationStore) RevokeSession(sessionID, reason string) (RevocationRecord, error) {
	if sessionID == "" {
		return RevocationRecord{}, errors.New("session id is required")
	}
	rec := RevocationRecord{Kind: RevocationKindSession, ID: sessionID, Reason: reason, RevokedAt: s.now().UTC()}
	err := s.update(func(state *RevocationState) {
		state.RevokedSessions[sessionID] = rec
	})
	return rec, err
}

func (s *RevocationStore) IsSessionRevoked(sessionID string) (bool, RevocationRecord, error) {
	var rec RevocationRecord
	var ok bool
	err := s.read(func(state RevocationState) { rec, ok = state.RevokedSessions[sessionID] })
	return ok, rec, err
}

func (s *RevocationStore) RevokeServiceAccess(serviceID, _ string) (int64, error) {
	if serviceID == "" {
		return 0, errors.New("service id is required")
	}
	var epoch int64
	err := s.update(func(state *RevocationState) {
		state.ServiceAccessEpochs[serviceID]++
		epoch = state.ServiceAccessEpochs[serviceID]
	})
	return epoch, err
}

func (s *RevocationStore) ServiceAccessEpoch(serviceID string) (int64, error) {
	var epoch int64
	err := s.read(func(state RevocationState) { epoch = state.ServiceAccessEpochs[serviceID] })
	return epoch, err
}

func (s *RevocationStore) RevokePublish(serviceID, reason string) (int64, error) {
	if serviceID == "" {
		return 0, errors.New("service id is required")
	}
	var epoch int64
	err := s.update(func(state *RevocationState) {
		state.PublishEpochs[serviceID]++
		epoch = state.PublishEpochs[serviceID]
		state.RevokedPublish[serviceID] = RevocationRecord{Kind: RevocationKindPublish, ServiceID: serviceID, Reason: reason, RevokedAt: s.now().UTC()}
	})
	return epoch, err
}

func (s *RevocationStore) IsPublishRevoked(serviceID string) (bool, RevocationRecord, error) {
	var rec RevocationRecord
	var ok bool
	err := s.read(func(state RevocationState) { rec, ok = state.RevokedPublish[serviceID] })
	return ok, rec, err
}

func (s *RevocationStore) PublishEpoch(serviceID string) (int64, error) {
	var epoch int64
	err := s.read(func(state RevocationState) { epoch = state.PublishEpochs[serviceID] })
	return epoch, err
}

func (s *RevocationStore) EpochsForService(serviceID string) (RevocationEpochs, error) {
	var epochs RevocationEpochs
	err := s.read(func(state RevocationState) {
		epochs = RevocationEpochs{AccessEpoch: state.ServiceAccessEpochs[serviceID], PublishEpoch: state.PublishEpochs[serviceID]}
	})
	return epochs, err
}

func (s *RevocationStore) update(mutate func(*RevocationState)) error {
	if s == nil {
		return nil
	}
	return withGrantStoreLock(s.path, s.LockTimeout, func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		mutate(&state)
		return s.saveUnlocked(state)
	})
}

func (s *RevocationStore) read(inspect func(RevocationState)) error {
	if s == nil {
		state := RevocationState{Version: RevocationStateVersion}
		state.ensureMaps()
		inspect(state)
		return nil
	}
	return withGrantStoreLock(s.path, s.LockTimeout, func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		inspect(state)
		return nil
	})
}

func (s *RevocationStore) loadUnlocked() (RevocationState, error) {
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
		return RevocationState{}, fmt.Errorf("decode revocation store %s: %w", s.path, err)
	}
	if state.Version == "" {
		state.Version = RevocationStateVersion
	}
	state.ensureMaps()
	return state, nil
}

func (s *RevocationStore) saveUnlocked(state RevocationState) error {
	if s == nil || s.path == "" {
		return nil
	}
	state.Version = RevocationStateVersion
	state.ensureMaps()
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteGrantStore(s.path, append(b, '\n'))
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
