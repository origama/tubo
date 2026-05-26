package grants

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const shareRedemptionStateVersion = "v1"

var ErrShareInviteAlreadyRedeemed = errors.New("share invite already redeemed")

type ShareRedemptionRecord struct {
	JTI                 string    `json:"jti"`
	ClusterID           string    `json:"cluster_id"`
	NamespaceID         string    `json:"namespace_id"`
	ServiceID           string    `json:"service_id"`
	RedeemedByPeerID    string    `json:"redeemed_by_peer_id,omitempty"`
	ClientKeyThumbprint string    `json:"client_key_thumbprint,omitempty"`
	SessionID           string    `json:"session_id,omitempty"`
	RedeemedAt          time.Time `json:"redeemed_at"`
	TokenExpiresAt      time.Time `json:"token_expires_at"`
}

type shareRedemptionState struct {
	Version string                  `json:"version"`
	Items   []ShareRedemptionRecord `json:"items,omitempty"`
}

type ShareRedemptionStore struct {
	path string
	now  func() time.Time
	mu   sync.Mutex
}

func NewShareRedemptionStore(path string) *ShareRedemptionStore {
	return &ShareRedemptionStore{path: path, now: func() time.Time { return time.Now().UTC() }}
}

func DefaultShareRedemptionStorePath() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "tubo", "grants", "share-redemptions.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "grants", "share-redemptions.json")
	}
	return filepath.Join(home, ".local", "share", "tubo", "grants", "share-redemptions.json")
}

func (s *ShareRedemptionStore) Path() string { return s.path }

func (s *ShareRedemptionStore) TryConsume(record ShareRedemptionRecord) error {
	if record.JTI == "" || record.ClusterID == "" || record.NamespaceID == "" || record.ServiceID == "" {
		return errors.New("share redemption record is missing required fields")
	}
	if record.TokenExpiresAt.IsZero() {
		return errors.New("share redemption record is missing token expiry")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return err
	}
	now := s.now().UTC()
	state.pruneExpired(now)
	for _, item := range state.Items {
		if item.JTI == record.JTI {
			return ErrShareInviteAlreadyRedeemed
		}
	}
	if record.RedeemedAt.IsZero() {
		record.RedeemedAt = now
	}
	state.Version = shareRedemptionStateVersion
	state.Items = append(state.Items, record)
	state.sort()
	return s.save(state)
}

func (s *ShareRedemptionStore) List() ([]ShareRedemptionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	if state.pruneExpired(s.now().UTC()) {
		if err := s.save(state); err != nil {
			return nil, err
		}
	}
	return append([]ShareRedemptionRecord(nil), state.Items...), nil
}

func (s *ShareRedemptionStore) load() (shareRedemptionState, error) {
	b, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return shareRedemptionState{Version: shareRedemptionStateVersion}, nil
	}
	if err != nil {
		return shareRedemptionState{}, err
	}
	var state shareRedemptionState
	if err := json.Unmarshal(b, &state); err != nil {
		return shareRedemptionState{}, fmt.Errorf("decode share redemption store %s: %w", s.path, err)
	}
	if state.Version == "" {
		state.Version = shareRedemptionStateVersion
	}
	state.sort()
	return state, nil
}

func (s *ShareRedemptionStore) save(state shareRedemptionState) error {
	state.sort()
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".share-redemptions-*.tmp")
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

func (s *shareRedemptionState) pruneExpired(now time.Time) bool {
	if len(s.Items) == 0 {
		return false
	}
	keep := s.Items[:0]
	changed := false
	for _, item := range s.Items {
		if !item.TokenExpiresAt.IsZero() && now.After(item.TokenExpiresAt.UTC()) {
			changed = true
			continue
		}
		keep = append(keep, item)
	}
	s.Items = keep
	return changed
}

func (s *shareRedemptionState) sort() {
	sort.SliceStable(s.Items, func(i, j int) bool { return s.Items[i].RedeemedAt.Before(s.Items[j].RedeemedAt) })
}
