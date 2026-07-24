package grants

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	path        string
	now         func() time.Time
	LockTimeout time.Duration
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
	return withGrantStoreLock(s.path, s.LockTimeout, func() error {
		state, err := s.loadUnlocked()
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
		return s.saveUnlocked(state)
	})
}

func (s *ShareRedemptionStore) List() ([]ShareRedemptionRecord, error) {
	var out []ShareRedemptionRecord
	err := withGrantStoreLock(s.path, s.LockTimeout, func() error {
		state, err := s.loadUnlocked()
		if err != nil {
			return err
		}
		if state.pruneExpired(s.now().UTC()) {
			if err := s.saveUnlocked(state); err != nil {
				return err
			}
		}
		out = append([]ShareRedemptionRecord(nil), state.Items...)
		return nil
	})
	return out, err
}

func (s *ShareRedemptionStore) loadUnlocked() (shareRedemptionState, error) {
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

func (s *ShareRedemptionStore) saveUnlocked(state shareRedemptionState) error {
	state.sort()
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteGrantStore(s.path, append(b, '\n'))
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
