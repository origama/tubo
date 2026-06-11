package peers

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Alias is a local, operator-managed label for a peer ID.
// It is purely a UX hint and carries no cryptographic meaning.
type Alias struct {
	PeerID    string    `json:"peer_id"`
	Name      string    `json:"name"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type fileState struct {
	Aliases []Alias `json:"aliases"`
}

type Store struct {
	path string
	now  func() time.Time
}

func NewStore(path string) *Store {
	return &Store{path: path, now: func() time.Time { return time.Now().UTC() }}
}

func DefaultStorePath() string {
	if override := strings.TrimSpace(os.Getenv("TUBO_PEER_ALIAS_STORE")); override != "" {
		return override
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "tubo", "peers", "aliases.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", "peers", "aliases.json")
	}
	return filepath.Join(home, ".local", "share", "tubo", "peers", "aliases.json")
}

func (s *Store) Path() string { return s.path }

func (s *Store) Upsert(peerID, name, note string) (Alias, error) {
	peerID = strings.TrimSpace(peerID)
	name = strings.TrimSpace(name)
	note = strings.TrimSpace(note)
	if peerID == "" {
		return Alias{}, errors.New("peer id is required")
	}
	if name == "" {
		return Alias{}, errors.New("alias name is required")
	}
	state, err := s.load()
	if err != nil {
		return Alias{}, err
	}
	alias := Alias{PeerID: peerID, Name: name, Note: note, UpdatedAt: s.now().UTC()}
	updated := false
	for i := range state.Aliases {
		if state.Aliases[i].PeerID == peerID {
			state.Aliases[i] = alias
			updated = true
			break
		}
	}
	if !updated {
		state.Aliases = append(state.Aliases, alias)
	}
	if err := s.save(state); err != nil {
		return Alias{}, err
	}
	return alias, nil
}

func (s *Store) Lookup(peerID string) (Alias, bool, error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return Alias{}, false, nil
	}
	state, err := s.load()
	if err != nil {
		return Alias{}, false, err
	}
	for _, alias := range state.Aliases {
		if alias.PeerID == peerID {
			return alias, true, nil
		}
	}
	return Alias{}, false, nil
}

func (s *Store) List() ([]Alias, error) {
	state, err := s.load()
	if err != nil {
		return nil, err
	}
	out := append([]Alias(nil), state.Aliases...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Name < out[j].Name
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out, nil
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
		return fileState{}, fmt.Errorf("decode peer alias store %s: %w", s.path, err)
	}
	sort.SliceStable(state.Aliases, func(i, j int) bool {
		if state.Aliases[i].UpdatedAt.Equal(state.Aliases[j].UpdatedAt) {
			return state.Aliases[i].Name < state.Aliases[j].Name
		}
		return state.Aliases[i].UpdatedAt.Before(state.Aliases[j].UpdatedAt)
	})
	return state, nil
}

func (s *Store) save(state fileState) error {
	sort.SliceStable(state.Aliases, func(i, j int) bool {
		if state.Aliases[i].UpdatedAt.Equal(state.Aliases[j].UpdatedAt) {
			return state.Aliases[i].Name < state.Aliases[j].Name
		}
		return state.Aliases[i].UpdatedAt.Before(state.Aliases[j].UpdatedAt)
	})
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".aliases-*.tmp")
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
