package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

const SchemaVersion = 4

type Store struct {
	db       *sql.DB
	home     string
	configMu sync.Mutex
}

type Scope struct {
	Name             string `json:"name"`
	AuthorityPublic  string `json:"authority_public"`
	AuthorityKeyFile string `json:"authority_key_file,omitempty"`
	NamespaceKeyFile string `json:"namespace_key_file,omitempty"`
}

type Membership struct {
	Scope            string `json:"scope"`
	Role             string `json:"role"`
	Token            string `json:"token"`
	AuthorityPublic  string `json:"authority_public"`
	AuthorityAddress string `json:"authority_address"`
	ClientKeyFile    string `json:"client_key_file"`
}

type Record struct {
	Scope      string
	PipeID     string
	Name       string
	Payload    []byte
	ExpiresAt  time.Time
	Sequence   uint64
	Discovered bool
}

type Run struct {
	PrimaryRef      string    `json:"primary_ref"`
	Kind            string    `json:"kind"`
	PID             int       `json:"pid"`
	ProcessIdentity string    `json:"-"`
	Source          string    `json:"source"`
	Status          string    `json:"status"`
	PeerID          string    `json:"peer_id,omitempty"`
	Address         string    `json:"address,omitempty"`
	StartedAt       time.Time `json:"started_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type Supervisor struct {
	PrimaryRef  string    `json:"primary_ref"`
	Manager     string    `json:"manager"`
	Label       string    `json:"label"`
	UnitPath    string    `json:"unit_path"`
	InstalledAt time.Time `json:"installed_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Session struct {
	ID                   string    `json:"id"`
	PipeRef              string    `json:"pipe_ref"`
	InitialTransportPath string    `json:"initial_transport_path"`
	StartedAt            time.Time `json:"started_at"`
	BytesIn              int64     `json:"bytes_in"`
	BytesOut             int64     `json:"bytes_out"`
}

type PeerAlias struct {
	PeerID    string    `json:"peer_id"`
	Name      string    `json:"name"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type RuntimeStats struct {
	RunRef          string    `json:"run_ref"`
	BytesIn         int64     `json:"bytes_in"`
	BytesOut        int64     `json:"bytes_out"`
	RateIn          float64   `json:"rate_in"`
	RateOut         float64   `json:"rate_out"`
	ConnectedPeers  int       `json:"connected_peers"`
	OpenConnections int       `json:"open_connections"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type RuntimePeer struct {
	RunRef    string    `json:"run_ref"`
	PeerID    string    `json:"peer_id"`
	Addresses []string  `json:"addresses,omitempty"`
	LastSeen  time.Time `json:"last_seen"`
}

type ConfigView struct {
	Version           int      `json:"version" yaml:"version"`
	CurrentContext    string   `json:"current_context" yaml:"current_context"`
	CurrentScope      string   `json:"current_scope,omitempty" yaml:"current_scope,omitempty"`
	Relays            []string `json:"relays" yaml:"relays"`
	SwarmKeyFile      string   `json:"swarm_key_file,omitempty" yaml:"swarm_key_file,omitempty"`
	ForceReachability string   `json:"force_reachability" yaml:"force_reachability"`
}

func Home() (string, error) {
	if value := os.Getenv("TUBO_HOME"); value != "" {
		return filepath.Abs(value)
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(config, "tubo"), nil
}

func Open(ctx context.Context) (*Store, error) {
	home, err := Home()
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{home, filepath.Join(home, "secrets"), filepath.Join(home, "logs"), filepath.Join(home, "run")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, fmt.Errorf("chmod %s: %w", dir, err)
		}
	}
	dsn := "file:" + filepath.Join(home, "state.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	s := &Store{db: db, home: home}
	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, err
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := filepath.Join(home, "state.db") + suffix
		if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
			db.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }
func (s *Store) Home() string { return s.home }
func (s *Store) SecretPath(parts ...string) string {
	all := append([]string{s.home, "secrets"}, parts...)
	return filepath.Join(all...)
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS services(ref TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, spec BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS pipes(ref TEXT PRIMARY KEY, name TEXT NOT NULL, pipe_id TEXT NOT NULL UNIQUE, spec BLOB NOT NULL, owner_key_file TEXT NOT NULL, publish_grant TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS scopes(name TEXT PRIMARY KEY, authority_public TEXT NOT NULL, authority_key_file TEXT NOT NULL, namespace_key_file TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS memberships(scope TEXT PRIMARY KEY, role TEXT NOT NULL, token TEXT NOT NULL, authority_public TEXT NOT NULL, authority_address TEXT NOT NULL, client_key_file TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS records(scope TEXT NOT NULL, pipe_id TEXT NOT NULL, name TEXT NOT NULL, payload BLOB NOT NULL, expires_at TEXT NOT NULL, sequence INTEGER NOT NULL, discovered INTEGER NOT NULL, PRIMARY KEY(scope,pipe_id))`,
		`CREATE TABLE IF NOT EXISTS runs(primary_ref TEXT PRIMARY KEY, kind TEXT NOT NULL, pid INTEGER NOT NULL, process_identity TEXT NOT NULL, source TEXT NOT NULL DEFAULT 'foreground', status TEXT NOT NULL, peer_id TEXT NOT NULL, address TEXT NOT NULL, started_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS supervisors(primary_ref TEXT PRIMARY KEY, manager TEXT NOT NULL, label TEXT NOT NULL UNIQUE, unit_path TEXT NOT NULL, installed_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, pipe_ref TEXT NOT NULL, initial_transport_path TEXT NOT NULL, started_at TEXT NOT NULL, bytes_in INTEGER NOT NULL DEFAULT 0, bytes_out INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS peer_aliases(peer_id TEXT PRIMARY KEY, name TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS runtime_stats(run_ref TEXT PRIMARY KEY REFERENCES runs(primary_ref) ON DELETE CASCADE, bytes_in INTEGER NOT NULL, bytes_out INTEGER NOT NULL, rate_in REAL NOT NULL, rate_out REAL NOT NULL, connected_peers INTEGER NOT NULL, open_connections INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS runtime_peers(run_ref TEXT NOT NULL REFERENCES runs(primary_ref) ON DELETE CASCADE, peer_id TEXT NOT NULL, addresses BLOB NOT NULL, last_seen TEXT NOT NULL, PRIMARY KEY(run_ref,peer_id))`,
	}
	if _, err := tx.ExecContext(ctx, statements[0]); err != nil {
		return fmt.Errorf("schema: %w", err)
	}
	var existing sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&existing); err != nil {
		return err
	}
	if existing.Valid && existing.Int64 > SchemaVersion {
		return fmt.Errorf("state schema %d is newer than supported schema %d", existing.Int64, SchemaVersion)
	}
	for _, statement := range statements[1:] {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	columns, err := tx.QueryContext(ctx, `PRAGMA table_info(runs)`)
	if err != nil {
		return fmt.Errorf("schema runs columns: %w", err)
	}
	hasSource := false
	for columns.Next() {
		var cid int
		var name, valueType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := columns.Scan(&cid, &name, &valueType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = columns.Close()
			return fmt.Errorf("schema runs columns: %w", err)
		}
		if name == "source" {
			hasSource = true
		}
	}
	if err := columns.Close(); err != nil {
		return fmt.Errorf("schema runs columns: %w", err)
	}
	if !hasSource {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE runs ADD COLUMN source TEXT NOT NULL DEFAULT 'foreground'`); err != nil {
			return fmt.Errorf("schema 4 runs source: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO schema_migrations(version,applied_at) VALUES(?,?)`, SchemaVersion, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	return tx.Commit()
}

func encode(value any) ([]byte, error) { return json.Marshal(value) }

func (s *Store) PutService(ctx context.Context, spec domain.ServiceSpec) (bool, error) {
	if err := spec.Validate(); err != nil {
		return false, err
	}
	data, err := encode(spec)
	if err != nil {
		return false, err
	}
	var existing []byte
	err = s.db.QueryRowContext(ctx, `SELECT spec FROM services WHERE ref=?`, spec.Ref.String()).Scan(&existing)
	if err == nil {
		if string(existing) == string(data) {
			return false, nil
		}
		return false, fmt.Errorf("conflict: %s already exists with different specification", spec.Ref)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO services(ref,name,spec) VALUES(?,?,?)`, spec.Ref.String(), spec.Ref.Name, data)
	return true, err
}

func (s *Store) Service(ctx context.Context, ref domain.Ref) (domain.ServiceSpec, error) {
	var data []byte
	if err := s.db.QueryRowContext(ctx, `SELECT spec FROM services WHERE ref=?`, ref.String()).Scan(&data); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ServiceSpec{}, fmt.Errorf("%w: %s", domain.ErrNotFound, ref)
		}
		return domain.ServiceSpec{}, err
	}
	var value domain.ServiceSpec
	return value, json.Unmarshal(data, &value)
}

func (s *Store) Services(ctx context.Context) ([]domain.ServiceSpec, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec FROM services ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []domain.ServiceSpec
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var value domain.ServiceSpec
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) PutPipe(ctx context.Context, spec domain.PipeSpec, ownerKey, publishGrant string) (bool, error) {
	if err := spec.Validate(); err != nil {
		return false, err
	}
	data, err := encode(spec)
	if err != nil {
		return false, err
	}
	var existing []byte
	err = s.db.QueryRowContext(ctx, `SELECT spec FROM pipes WHERE ref=?`, spec.Ref.String()).Scan(&existing)
	if err == nil {
		if string(existing) == string(data) {
			if publishGrant != "" {
				_, err = s.db.ExecContext(ctx, `UPDATE pipes SET publish_grant=? WHERE ref=?`, publishGrant, spec.Ref.String())
			}
			return false, err
		}
		return false, fmt.Errorf("conflict: %s already exists with different specification", spec.Ref)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO pipes(ref,name,pipe_id,spec,owner_key_file,publish_grant) VALUES(?,?,?,?,?,?)`, spec.Ref.String(), spec.Ref.Name, spec.PipeID, data, ownerKey, publishGrant)
	return true, err
}

func (s *Store) SetPipePublishGrant(ctx context.Context, ref, grant string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE pipes SET publish_grant=? WHERE ref=?`, grant, ref)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("%w: %s", domain.ErrNotFound, ref)
	}
	return nil
}

func (s *Store) Pipe(ctx context.Context, identifier string) (domain.PipeSpec, string, string, error) {
	query := `SELECT spec,owner_key_file,publish_grant FROM pipes WHERE ref=? OR pipe_id=?`
	var data []byte
	var key, grant string
	if err := s.db.QueryRowContext(ctx, query, identifier, identifier).Scan(&data, &key, &grant); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.PipeSpec{}, "", "", fmt.Errorf("%w: pipe %s", domain.ErrNotFound, identifier)
		}
		return domain.PipeSpec{}, "", "", err
	}
	var value domain.PipeSpec
	return value, key, grant, json.Unmarshal(data, &value)
}

func (s *Store) Pipes(ctx context.Context) ([]domain.PipeSpec, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT spec FROM pipes ORDER BY name,pipe_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []domain.PipeSpec
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var value domain.PipeSpec
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) DeletePipe(ctx context.Context, ref string) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var key string
	if err := tx.QueryRowContext(ctx, `SELECT owner_key_file FROM pipes WHERE ref=?`, ref).Scan(&key); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%w: %s", domain.ErrNotFound, ref)
		}
		return "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT ref,spec FROM pipes WHERE ref<>?`, ref)
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var other string
		var data []byte
		if err := rows.Scan(&other, &data); err != nil {
			rows.Close()
			return "", err
		}
		var spec domain.PipeSpec
		if err := json.Unmarshal(data, &spec); err != nil {
			rows.Close()
			return "", err
		}
		if spec.Target.String() == ref {
			rows.Close()
			return "", fmt.Errorf("conflict: %s is targeted by %s", ref, other)
		}
	}
	if err := rows.Close(); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE pipe_ref=?`, ref); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pipes WHERE ref=?`, ref); err != nil {
		return "", err
	}
	return key, tx.Commit()
}

func (s *Store) DeleteService(ctx context.Context, ref string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM services WHERE ref=?`, ref).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: %s", domain.ErrNotFound, ref)
		}
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT ref,spec FROM pipes`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var pipeRef string
		var data []byte
		if err := rows.Scan(&pipeRef, &data); err != nil {
			rows.Close()
			return err
		}
		var spec domain.PipeSpec
		if err := json.Unmarshal(data, &spec); err != nil {
			rows.Close()
			return err
		}
		if spec.Target.String() == ref {
			rows.Close()
			return fmt.Errorf("conflict: %s is targeted by %s", ref, pipeRef)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM services WHERE ref=?`, ref); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PutScope(ctx context.Context, value Scope) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO scopes(name,authority_public,authority_key_file,namespace_key_file) VALUES(?,?,?,?)`, value.Name, value.AuthorityPublic, value.AuthorityKeyFile, value.NamespaceKeyFile)
	return err
}
func (s *Store) Scope(ctx context.Context, name string) (Scope, error) {
	var v Scope
	err := s.db.QueryRowContext(ctx, `SELECT name,authority_public,authority_key_file,namespace_key_file FROM scopes WHERE name=?`, name).Scan(&v.Name, &v.AuthorityPublic, &v.AuthorityKeyFile, &v.NamespaceKeyFile)
	if errors.Is(err, sql.ErrNoRows) {
		err = fmt.Errorf("%w: scope %s", domain.ErrNotFound, name)
	}
	return v, err
}
func (s *Store) Scopes(ctx context.Context) ([]Scope, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,authority_public,authority_key_file,namespace_key_file FROM scopes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []Scope
	for rows.Next() {
		var v Scope
		if err := rows.Scan(&v.Name, &v.AuthorityPublic, &v.AuthorityKeyFile, &v.NamespaceKeyFile); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, rows.Err()
}
func (s *Store) FirstScope(ctx context.Context) (Scope, error) {
	var name string
	if err := s.db.QueryRowContext(ctx, `SELECT name FROM scopes ORDER BY name LIMIT 1`).Scan(&name); err != nil {
		return Scope{}, err
	}
	return s.Scope(ctx, name)
}
func (s *Store) PutMembership(ctx context.Context, v Membership) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO memberships(scope,role,token,authority_public,authority_address,client_key_file) VALUES(?,?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET role=excluded.role,token=excluded.token,authority_public=excluded.authority_public,authority_address=excluded.authority_address,client_key_file=excluded.client_key_file`, v.Scope, v.Role, v.Token, v.AuthorityPublic, v.AuthorityAddress, v.ClientKeyFile)
	return err
}
func (s *Store) Membership(ctx context.Context, scope string) (Membership, error) {
	var v Membership
	err := s.db.QueryRowContext(ctx, `SELECT scope,role,token,authority_public,authority_address,client_key_file FROM memberships WHERE scope=?`, scope).Scan(&v.Scope, &v.Role, &v.Token, &v.AuthorityPublic, &v.AuthorityAddress, &v.ClientKeyFile)
	if errors.Is(err, sql.ErrNoRows) {
		err = fmt.Errorf("%w: membership %s", domain.ErrNotFound, scope)
	}
	return v, err
}
func (s *Store) Memberships(ctx context.Context) ([]Membership, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope,role,token,authority_public,authority_address,client_key_file FROM memberships ORDER BY scope`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Membership
	for rows.Next() {
		var v Membership
		if err := rows.Scan(&v.Scope, &v.Role, &v.Token, &v.AuthorityPublic, &v.AuthorityAddress, &v.ClientKeyFile); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

type staticConfig struct {
	Version        int                      `yaml:"version"`
	CurrentContext string                   `yaml:"current_context"`
	CurrentScope   string                   `yaml:"current_scope,omitempty"`
	Contexts       map[string]staticContext `yaml:"contexts"`
}
type staticContext struct {
	Overlay staticOverlay `yaml:"overlay"`
}
type staticOverlay struct {
	Relays            []string `yaml:"relays"`
	SwarmKeyFile      string   `yaml:"swarm_key_file"`
	ForceReachability string   `yaml:"force_reachability"`
}

func (s *Store) PutOverlay(_ context.Context, relay, key, reachability string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	config := staticConfig{Version: 1, CurrentContext: "default", Contexts: map[string]staticContext{}}
	if existing, err := s.readConfig(); err == nil {
		config = existing
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if config.Contexts == nil {
		config.Contexts = map[string]staticContext{}
	}
	config.Contexts[config.CurrentContext] = staticContext{Overlay: staticOverlay{Relays: []string{relay}, SwarmKeyFile: key, ForceReachability: reachability}}
	return s.writeConfig(config)
}

func (s *Store) writeConfig(config staticConfig) error {
	data, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	path := filepath.Join(s.home, "config.yaml")
	temporary, err := os.CreateTemp(s.home, ".config-*.yaml")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(s.home)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
func (s *Store) readConfig() (staticConfig, error) {
	data, err := os.ReadFile(filepath.Join(s.home, "config.yaml"))
	if err != nil {
		return staticConfig{}, err
	}
	if len(data) > 1<<20 {
		return staticConfig{}, errors.New("config exceeds 1 MiB")
	}
	var config staticConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&config); err != nil {
		return config, err
	}
	if config.Version != 1 || config.CurrentContext == "" {
		return config, errors.New("invalid static config")
	}
	return config, nil
}
func (s *Store) Overlay(_ context.Context) (relay, key, reachability string, err error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	config, err := s.readConfig()
	if err != nil {
		return "", "", "", fmt.Errorf("%w: overlay config: %v", domain.ErrNotFound, err)
	}
	value, ok := config.Contexts[config.CurrentContext]
	if !ok || len(value.Overlay.Relays) == 0 || value.Overlay.Relays[0] == "" || value.Overlay.SwarmKeyFile == "" {
		return "", "", "", fmt.Errorf("%w: overlay", domain.ErrNotFound)
	}
	return value.Overlay.Relays[0], value.Overlay.SwarmKeyFile, value.Overlay.ForceReachability, nil
}

func (s *Store) Config() (ConfigView, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	config, err := s.readConfig()
	if err != nil {
		return ConfigView{}, err
	}
	value, ok := config.Contexts[config.CurrentContext]
	if !ok {
		return ConfigView{}, errors.New("invalid static config: current context missing")
	}
	return ConfigView{Version: config.Version, CurrentContext: config.CurrentContext, CurrentScope: config.CurrentScope, Relays: append([]string(nil), value.Overlay.Relays...), SwarmKeyFile: value.Overlay.SwarmKeyFile, ForceReachability: value.Overlay.ForceReachability}, nil
}

func (s *Store) ValidateConfig() (ConfigView, error) {
	value, err := s.Config()
	if err != nil {
		return value, err
	}
	if len(value.Relays) == 0 || strings.TrimSpace(value.Relays[0]) == "" {
		return value, errors.New("config relay is required")
	}
	if value.ForceReachability != "private" && value.ForceReachability != "public" {
		return value, errors.New("config force_reachability must be private or public")
	}
	info, err := os.Stat(value.SwarmKeyFile)
	if err != nil {
		return value, fmt.Errorf("swarm key: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return value, errors.New("swarm key permissions must not allow group or other access")
	}
	return value, nil
}

func (s *Store) UseScope(ctx context.Context, scope string) error {
	if _, err := s.Scope(ctx, scope); err != nil {
		if _, membershipErr := s.Membership(ctx, scope); membershipErr != nil {
			return fmt.Errorf("%w: scope %s", domain.ErrNotFound, scope)
		}
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	config, err := s.readConfig()
	if err != nil {
		return err
	}
	config.CurrentScope = scope
	return s.writeConfig(config)
}

func (s *Store) CurrentScope(ctx context.Context) (string, error) {
	s.configMu.Lock()
	config, err := s.readConfig()
	s.configMu.Unlock()
	if err != nil {
		return "", err
	}
	if config.CurrentScope != "" {
		if _, err := s.Scope(ctx, config.CurrentScope); err == nil {
			return config.CurrentScope, nil
		}
		if _, err := s.Membership(ctx, config.CurrentScope); err == nil {
			return config.CurrentScope, nil
		}
		return "", fmt.Errorf("%w: current scope %s", domain.ErrNotFound, config.CurrentScope)
	}
	if scope, err := s.FirstScope(ctx); err == nil {
		return scope.Name, nil
	}
	memberships, err := s.Memberships(ctx)
	if err == nil && len(memberships) > 0 {
		return memberships[0].Scope, nil
	}
	return "", fmt.Errorf("%w: no scope", domain.ErrNotFound)
}

func (s *Store) PutRecord(ctx context.Context, v Record) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sequence uint64
	var payload []byte
	err = tx.QueryRowContext(ctx, `SELECT sequence,payload FROM records WHERE scope=? AND pipe_id=?`, v.Scope, v.PipeID).Scan(&sequence, &payload)
	switch {
	case err == nil && v.Sequence < sequence:
		return errors.New("replayed record sequence")
	case err == nil && v.Sequence == sequence:
		if string(payload) != string(v.Payload) {
			return errors.New("conflicting record at same sequence")
		}
		return nil
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO records(scope,pipe_id,name,payload,expires_at,sequence,discovered) VALUES(?,?,?,?,?,?,?)`, v.Scope, v.PipeID, v.Name, v.Payload, v.ExpiresAt.UTC().Format(time.RFC3339Nano), v.Sequence, v.Discovered)
	default:
		_, err = tx.ExecContext(ctx, `UPDATE records SET name=?,payload=?,expires_at=?,sequence=?,discovered=? WHERE scope=? AND pipe_id=?`, v.Name, v.Payload, v.ExpiresAt.UTC().Format(time.RFC3339Nano), v.Sequence, v.Discovered, v.Scope, v.PipeID)
	}
	if err != nil {
		return err
	}
	return tx.Commit()
}
func (s *Store) Records(ctx context.Context, scope string) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope,pipe_id,name,payload,expires_at,sequence,discovered FROM records WHERE scope=? AND expires_at>? ORDER BY name,pipe_id`, scope, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var v Record
		var expires string
		if err := rows.Scan(&v.Scope, &v.PipeID, &v.Name, &v.Payload, &expires, &v.Sequence, &v.Discovered); err != nil {
			return nil, err
		}
		v.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) Record(ctx context.Context, pipeID string) (Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT scope,pipe_id,name,payload,expires_at,sequence,discovered FROM records WHERE pipe_id=? AND expires_at>? ORDER BY scope`, pipeID, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return Record{}, err
	}
	defer rows.Close()
	var matches []Record
	for rows.Next() {
		var value Record
		var expires string
		if err := rows.Scan(&value.Scope, &value.PipeID, &value.Name, &value.Payload, &expires, &value.Sequence, &value.Discovered); err != nil {
			return Record{}, err
		}
		value.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return Record{}, err
		}
		matches = append(matches, value)
	}
	if err := rows.Err(); err != nil {
		return Record{}, err
	}
	if len(matches) == 0 {
		return Record{}, fmt.Errorf("%w: pipe/%s", domain.ErrNotFound, pipeID)
	}
	if len(matches) > 1 {
		return Record{}, fmt.Errorf("conflict: pipe/%s exists in multiple scopes; specify scope", pipeID)
	}
	return matches[0], nil
}
func (s *Store) RecordSequence(ctx context.Context, scope, pipeID string) (uint64, error) {
	var sequence uint64
	err := s.db.QueryRowContext(ctx, `SELECT sequence FROM records WHERE scope=? AND pipe_id=?`, scope, pipeID).Scan(&sequence)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return sequence, err
}
func (s *Store) RecordInScope(ctx context.Context, scope, pipeID string) (Record, error) {
	var value Record
	var expires string
	err := s.db.QueryRowContext(ctx, `SELECT scope,pipe_id,name,payload,expires_at,sequence,discovered FROM records WHERE scope=? AND pipe_id=? AND expires_at>?`, scope, pipeID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(&value.Scope, &value.PipeID, &value.Name, &value.Payload, &expires, &value.Sequence, &value.Discovered)
	if errors.Is(err, sql.ErrNoRows) {
		return value, fmt.Errorf("%w: pipe/%s in %s", domain.ErrNotFound, pipeID, scope)
	}
	if err == nil {
		value.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
	}
	return value, err
}

func ProcessIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", errors.New("invalid pid")
	}
	output, err := exec.Command("ps", "-o", "lstart=", "-o", "command=", "-p", fmt.Sprint(pid)).Output()
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(output))
	if identity == "" {
		return "", errors.New("process not found")
	}
	return fmt.Sprintf("%d:%s", pid, identity), nil
}

func (s *Store) ReserveRun(ctx context.Context, ref, kind string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs(primary_ref,kind,pid,process_identity,source,status,peer_id,address,started_at,updated_at) VALUES(?,?,0,'','foreground','starting','','',?,?)`, ref, kind, now, now)
	if err != nil {
		return fmt.Errorf("conflict: run already exists for %s: %w", ref, err)
	}
	return nil
}

func (s *Store) PutRun(ctx context.Context, v Run) error {
	if v.Source == "" {
		v.Source = "foreground"
	}
	if v.Source != "foreground" && v.Source != "detached" && v.Source != "launchd" && v.Source != "systemd" {
		return fmt.Errorf("invalid run source %q", v.Source)
	}
	if v.PID > 0 && v.ProcessIdentity == "" {
		identity, err := ProcessIdentity(v.PID)
		if err != nil {
			return fmt.Errorf("capture process identity: %w", err)
		}
		v.ProcessIdentity = identity
	}
	now := time.Now().UTC()
	if v.StartedAt.IsZero() {
		v.StartedAt = now
	}
	v.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs(primary_ref,kind,pid,process_identity,source,status,peer_id,address,started_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(primary_ref) DO UPDATE SET kind=excluded.kind,pid=excluded.pid,process_identity=excluded.process_identity,source=excluded.source,status=excluded.status,peer_id=excluded.peer_id,address=excluded.address,started_at=excluded.started_at,updated_at=excluded.updated_at`, v.PrimaryRef, v.Kind, v.PID, v.ProcessIdentity, v.Source, v.Status, v.PeerID, v.Address, v.StartedAt.Format(time.RFC3339Nano), v.UpdatedAt.Format(time.RFC3339Nano))
	return err
}
func (s *Store) Run(ctx context.Context, ref string) (Run, error) {
	var v Run
	var started, updated string
	err := s.db.QueryRowContext(ctx, `SELECT primary_ref,kind,pid,process_identity,source,status,peer_id,address,started_at,updated_at FROM runs WHERE primary_ref=?`, ref).Scan(&v.PrimaryRef, &v.Kind, &v.PID, &v.ProcessIdentity, &v.Source, &v.Status, &v.PeerID, &v.Address, &started, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("%w: %s", domain.ErrNotFound, ref)
	}
	if err == nil {
		v.StartedAt, _ = time.Parse(time.RFC3339Nano, started)
		v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	}
	return v, err
}
func (s *Store) Runs(ctx context.Context) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT primary_ref,kind,pid,process_identity,source,status,peer_id,address,started_at,updated_at FROM runs ORDER BY primary_ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var v Run
		var a, b string
		if err := rows.Scan(&v.PrimaryRef, &v.Kind, &v.PID, &v.ProcessIdentity, &v.Source, &v.Status, &v.PeerID, &v.Address, &a, &b); err != nil {
			return nil, err
		}
		v.StartedAt, _ = time.Parse(time.RFC3339Nano, a)
		v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, b)
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) DeleteRun(ctx context.Context, ref string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runs WHERE primary_ref=?`, ref)
	return err
}

func (s *Store) PutSupervisor(ctx context.Context, value Supervisor) error {
	if value.PrimaryRef == "" || value.Manager == "" || value.Label == "" || value.UnitPath == "" {
		return errors.New("supervisor fields are required")
	}
	if value.Manager != "launchd" && value.Manager != "systemd" {
		return fmt.Errorf("unsupported supervisor manager %q", value.Manager)
	}
	now := time.Now().UTC()
	if value.InstalledAt.IsZero() {
		value.InstalledAt = now
	}
	value.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO supervisors(primary_ref,manager,label,unit_path,installed_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(primary_ref) DO UPDATE SET manager=excluded.manager,label=excluded.label,unit_path=excluded.unit_path,updated_at=excluded.updated_at`, value.PrimaryRef, value.Manager, value.Label, value.UnitPath, value.InstalledAt.Format(time.RFC3339Nano), value.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) Supervisor(ctx context.Context, ref string) (Supervisor, error) {
	var value Supervisor
	var installed, updated string
	err := s.db.QueryRowContext(ctx, `SELECT primary_ref,manager,label,unit_path,installed_at,updated_at FROM supervisors WHERE primary_ref=?`, ref).Scan(&value.PrimaryRef, &value.Manager, &value.Label, &value.UnitPath, &installed, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return value, fmt.Errorf("%w: supervisor for %s", domain.ErrNotFound, ref)
	}
	if err != nil {
		return value, err
	}
	value.InstalledAt, _ = time.Parse(time.RFC3339Nano, installed)
	value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return value, nil
}

func (s *Store) DeleteSupervisor(ctx context.Context, ref string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM supervisors WHERE primary_ref=?`, ref)
	return err
}

func (s *Store) PutRuntimeTelemetry(ctx context.Context, stats RuntimeStats, peers []RuntimePeer) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stats.UpdatedAt = time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO runtime_stats(run_ref,bytes_in,bytes_out,rate_in,rate_out,connected_peers,open_connections,updated_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(run_ref) DO UPDATE SET bytes_in=excluded.bytes_in,bytes_out=excluded.bytes_out,rate_in=excluded.rate_in,rate_out=excluded.rate_out,connected_peers=excluded.connected_peers,open_connections=excluded.open_connections,updated_at=excluded.updated_at`, stats.RunRef, stats.BytesIn, stats.BytesOut, stats.RateIn, stats.RateOut, stats.ConnectedPeers, stats.OpenConnections, stats.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM runtime_peers WHERE run_ref=?`, stats.RunRef); err != nil {
		return err
	}
	for _, value := range peers {
		addresses, err := json.Marshal(value.Addresses)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO runtime_peers(run_ref,peer_id,addresses,last_seen) VALUES(?,?,?,?)`, stats.RunRef, value.PeerID, addresses, stats.UpdatedAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) RuntimeStats(ctx context.Context, ref string) (RuntimeStats, error) {
	var value RuntimeStats
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT run_ref,bytes_in,bytes_out,rate_in,rate_out,connected_peers,open_connections,updated_at FROM runtime_stats WHERE run_ref=?`, ref).Scan(&value.RunRef, &value.BytesIn, &value.BytesOut, &value.RateIn, &value.RateOut, &value.ConnectedPeers, &value.OpenConnections, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return value, fmt.Errorf("%w: runtime stats %s", domain.ErrNotFound, ref)
	}
	if err == nil {
		value.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	}
	return value, err
}

func (s *Store) RuntimePeers(ctx context.Context) ([]RuntimePeer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_ref,peer_id,addresses,last_seen FROM runtime_peers ORDER BY peer_id,run_ref`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RuntimePeer
	for rows.Next() {
		var value RuntimePeer
		var addresses []byte
		var seen string
		if err := rows.Scan(&value.RunRef, &value.PeerID, &addresses, &seen); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(addresses, &value.Addresses); err != nil {
			return nil, err
		}
		value.LastSeen, _ = time.Parse(time.RFC3339Nano, seen)
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) ClearRuntimePeers(ctx context.Context, ref string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM runtime_peers WHERE run_ref=?`, ref)
	return err
}

func (s *Store) PutSession(ctx context.Context, v Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id,pipe_ref,initial_transport_path,started_at,bytes_in,bytes_out) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET bytes_in=excluded.bytes_in,bytes_out=excluded.bytes_out`, v.ID, v.PipeRef, v.InitialTransportPath, v.StartedAt.UTC().Format(time.RFC3339Nano), v.BytesIn, v.BytesOut)
	return err
}
func (s *Store) Sessions(ctx context.Context, ref string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,pipe_ref,initial_transport_path,started_at,bytes_in,bytes_out FROM sessions WHERE pipe_ref=? ORDER BY started_at DESC LIMIT 100`, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var v Session
		var at string
		if err := rows.Scan(&v.ID, &v.PipeRef, &v.InitialTransportPath, &at, &v.BytesIn, &v.BytesOut); err != nil {
			return nil, err
		}
		v.StartedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Session(ctx context.Context, id string) (Session, error) {
	var v Session
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT id,pipe_ref,initial_transport_path,started_at,bytes_in,bytes_out FROM sessions WHERE id=?`, id).Scan(&v.ID, &v.PipeRef, &v.InitialTransportPath, &at, &v.BytesIn, &v.BytesOut)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("%w: session/%s", domain.ErrNotFound, id)
	}
	if err == nil {
		v.StartedAt, _ = time.Parse(time.RFC3339Nano, at)
	}
	return v, err
}

func (s *Store) PutPeerAlias(ctx context.Context, value PeerAlias) error {
	value.PeerID = strings.TrimSpace(value.PeerID)
	value.Name = strings.TrimSpace(value.Name)
	value.Note = strings.TrimSpace(value.Note)
	if value.PeerID == "" || value.Name == "" {
		return errors.New("peer ID and alias name are required")
	}
	value.UpdatedAt = time.Now().UTC()
	_, err := s.db.ExecContext(ctx, `INSERT INTO peer_aliases(peer_id,name,note,updated_at) VALUES(?,?,?,?) ON CONFLICT(peer_id) DO UPDATE SET name=excluded.name,note=excluded.note,updated_at=excluded.updated_at`, value.PeerID, value.Name, value.Note, value.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *Store) PeerAlias(ctx context.Context, peerID string) (PeerAlias, error) {
	var v PeerAlias
	var at string
	err := s.db.QueryRowContext(ctx, `SELECT peer_id,name,note,updated_at FROM peer_aliases WHERE peer_id=?`, peerID).Scan(&v.PeerID, &v.Name, &v.Note, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("%w: peer/%s", domain.ErrNotFound, peerID)
	}
	if err == nil {
		v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, at)
	}
	return v, err
}

func (s *Store) PeerAliases(ctx context.Context) ([]PeerAlias, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT peer_id,name,note,updated_at FROM peer_aliases ORDER BY name,peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PeerAlias
	for rows.Next() {
		var v PeerAlias
		var at string
		if err := rows.Scan(&v.PeerID, &v.Name, &v.Note, &at); err != nil {
			return nil, err
		}
		v.UpdatedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, v)
	}
	return out, rows.Err()
}
