package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
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

const SchemaVersion = 14

const maxScopeSyncLeaseTTL = 5 * time.Minute

var (
	ErrScopeSyncLeaseLost             = errors.New("scope sync lease lost")
	errAuthoritativeSnapshotRollback  = errors.New("rollback authoritative snapshot generation")
	errAuthoritativeSnapshotAmbiguous = errors.New("ambiguous authoritative snapshot generation")
)

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

const (
	SnapshotSourcePinnedAuthority = "pinned_authority"
	MaxSnapshotRecords            = 256
)

type Record struct {
	Scope      string
	PipeID     string
	Name       string
	Payload    []byte
	ExpiresAt  time.Time
	Sequence   uint64
	Discovered bool
}

type ScopeSyncLease struct {
	Scope           string    `json:"scope"`
	HolderRef       string    `json:"holder_ref,omitempty"`
	ProcessIdentity string    `json:"-"`
	Token           string    `json:"-"`
	AcquiredAt      time.Time `json:"acquired_at,omitempty"`
	RenewedAt       time.Time `json:"renewed_at,omitempty"`
	ExpiresAt       time.Time `json:"expires_at,omitempty"`
	Active          bool      `json:"active"`
}

type ScopeFreshness struct {
	Scope              string     `json:"scope"`
	LastAttempt        *time.Time `json:"last_attempt,omitempty"`
	LastSuccess        *time.Time `json:"last_success,omitempty"`
	Source             string     `json:"source,omitempty"`
	Complete           bool       `json:"complete"`
	ErrorClass         string     `json:"error_class,omitempty"`
	NextRetry          *time.Time `json:"next_retry,omitempty"`
	SnapshotGeneration uint64     `json:"snapshot_generation"`
	SnapshotHighWater  uint64     `json:"snapshot_high_water"`
	SnapshotDigest     string     `json:"-"`
}

type AuthoritativeSnapshot struct {
	Source     string
	Complete   bool
	Generation uint64 // authority-global, trusted deletion ordering
	HighWater  uint64 // authority mutation watermark; never deletion ordering
	Digest     string // canonical complete snapshot digest
	Records    []Record
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
	CurrentTransportPath string    `json:"current_transport_path,omitempty"`
	StartedAt            time.Time `json:"started_at"`
	BytesIn              int64     `json:"bytes_in"`
	BytesOut             int64     `json:"bytes_out"`
}

// PathEvent records a session transport path transition.
type PathEvent struct {
	SessionID    string    `json:"session_id"`
	Epoch        uint64    `json:"epoch"`
	FromPath     string    `json:"from_path"`
	ToPath       string    `json:"to_path"`
	Reason       string    `json:"reason"`
	C2SCommitted uint64    `json:"c2s_committed"`
	S2CCommitted uint64    `json:"s2c_committed"`
	CommittedAt  time.Time `json:"committed_at"`
}

type Revocation struct {
	Scope       string    `json:"scope"`
	SubjectType string    `json:"subject_type"`
	SubjectJTI  string    `json:"subject_jti"`
	RevokedAt   time.Time `json:"revoked_at"`
	Epoch       uint64    `json:"epoch"`
}

var ErrInviteRedeemed = errors.New("invite already redeemed by another client")

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
	return openWithDriver(ctx, "sqlite")
}

// openWithDriver keeps Open's lifecycle and migration behavior in one path;
// tests substitute only the database driver to control SQLite writer timing.
func openWithDriver(ctx context.Context, driverName string) (*Store, error) {
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
	dsn := "file:" + filepath.Join(home, "state.db") + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open(driverName, dsn)
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

// beginWriteTx reserves SQLite's writer and rejects cancellation before any
// caller can read state or issue a mutation through the transaction.
func (s *Store) beginWriteTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (s *Store) Home() string { return s.home }
func (s *Store) SecretPath(parts ...string) string {
	all := append([]string{s.home, "secrets"}, parts...)
	return filepath.Join(all...)
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := ctx.Err(); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS schema_migrations(version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS services(ref TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, spec BLOB NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS pipes(ref TEXT PRIMARY KEY, name TEXT NOT NULL, pipe_id TEXT NOT NULL UNIQUE, spec BLOB NOT NULL, owner_key_file TEXT NOT NULL, publish_grant TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS scopes(name TEXT PRIMARY KEY, authority_public TEXT NOT NULL, authority_key_file TEXT NOT NULL, namespace_key_file TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS memberships(scope TEXT PRIMARY KEY, role TEXT NOT NULL, token TEXT NOT NULL, authority_public TEXT NOT NULL, authority_address TEXT NOT NULL, client_key_file TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS records(scope TEXT NOT NULL, pipe_id TEXT NOT NULL, name TEXT NOT NULL, payload BLOB NOT NULL, expires_at TEXT NOT NULL, sequence INTEGER NOT NULL, discovered INTEGER NOT NULL, PRIMARY KEY(scope,pipe_id))`,
		`CREATE TABLE IF NOT EXISTS scope_freshness(scope TEXT PRIMARY KEY, last_attempt TEXT NOT NULL DEFAULT '', last_success TEXT NOT NULL DEFAULT '', source TEXT NOT NULL DEFAULT '', complete INTEGER NOT NULL DEFAULT 0, error_class TEXT NOT NULL DEFAULT '', next_retry TEXT NOT NULL DEFAULT '', snapshot_generation INTEGER NOT NULL DEFAULT 0, snapshot_high_water INTEGER NOT NULL DEFAULT 0, snapshot_digest TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS authority_snapshots(scope TEXT PRIMARY KEY, generation INTEGER NOT NULL, high_water INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scope_sync_leases(scope TEXT PRIMARY KEY, holder_ref TEXT NOT NULL, holder_process_identity TEXT NOT NULL, lease_token TEXT NOT NULL UNIQUE, acquired_at TEXT NOT NULL, renewed_at TEXT NOT NULL, expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS revocations(scope TEXT NOT NULL, subject_type TEXT NOT NULL, subject_jti TEXT NOT NULL, revoked_at TEXT NOT NULL, epoch INTEGER NOT NULL, PRIMARY KEY(scope,subject_type,subject_jti))`,
		`CREATE TABLE IF NOT EXISTS invite_redemptions(jti TEXT NOT NULL, pipe_id TEXT NOT NULL, redeemed_at TEXT NOT NULL, session_id TEXT NOT NULL, client_public TEXT NOT NULL, kind TEXT NOT NULL DEFAULT 'scoped', PRIMARY KEY(jti,pipe_id))`,
		`CREATE TABLE IF NOT EXISTS connect_leases(pipe_id TEXT PRIMARY KEY, scope TEXT NOT NULL, access_lease TEXT NOT NULL, refresh_lease TEXT NOT NULL, expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS remote_pipe_bindings(remote_pipe_id TEXT PRIMARY KEY, authority_public TEXT NOT NULL, owner_binding TEXT NOT NULL, locator TEXT NOT NULL, recipient_public TEXT NOT NULL, source TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS exact_lease_cache(remote_pipe_id TEXT PRIMARY KEY, access_lease TEXT NOT NULL, expires_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS accepted_exact_relationships(pipe_id TEXT NOT NULL, recipient_public TEXT NOT NULL, invite_jti TEXT NOT NULL, access_epoch INTEGER NOT NULL, accepted_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY(pipe_id, recipient_public))`,
		`CREATE TABLE IF NOT EXISTS runs(primary_ref TEXT PRIMARY KEY, kind TEXT NOT NULL, pid INTEGER NOT NULL, process_identity TEXT NOT NULL, source TEXT NOT NULL DEFAULT 'foreground', status TEXT NOT NULL, peer_id TEXT NOT NULL, address TEXT NOT NULL, started_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS supervisors(primary_ref TEXT PRIMARY KEY, manager TEXT NOT NULL, label TEXT NOT NULL UNIQUE, unit_path TEXT NOT NULL, installed_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY, pipe_ref TEXT NOT NULL, initial_transport_path TEXT NOT NULL, started_at TEXT NOT NULL, bytes_in INTEGER NOT NULL DEFAULT 0, bytes_out INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE IF NOT EXISTS peer_aliases(peer_id TEXT PRIMARY KEY, name TEXT NOT NULL, note TEXT NOT NULL DEFAULT '', updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS runtime_stats(run_ref TEXT PRIMARY KEY REFERENCES runs(primary_ref) ON DELETE CASCADE, bytes_in INTEGER NOT NULL, bytes_out INTEGER NOT NULL, rate_in REAL NOT NULL, rate_out REAL NOT NULL, connected_peers INTEGER NOT NULL, open_connections INTEGER NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS runtime_peers(run_ref TEXT NOT NULL REFERENCES runs(primary_ref) ON DELETE CASCADE, peer_id TEXT NOT NULL, addresses BLOB NOT NULL, last_seen TEXT NOT NULL, PRIMARY KEY(run_ref,peer_id))`,
		`CREATE TABLE IF NOT EXISTS path_events(session_id TEXT NOT NULL, epoch INTEGER NOT NULL, from_path TEXT NOT NULL, to_path TEXT NOT NULL, reason TEXT NOT NULL, c2s_committed INTEGER NOT NULL, s2c_committed INTEGER NOT NULL, committed_at TEXT NOT NULL, PRIMARY KEY(session_id,epoch))`,
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
	currentSchema := int64(0)
	if existing.Valid {
		currentSchema = existing.Int64
	}
	for _, statement := range statements[1:] {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("schema: %w", err)
		}
	}
	// Schema 9/10: add current_transport_path to sessions
	sessionColumns, err := tx.QueryContext(ctx, `PRAGMA table_info(sessions)`)
	if err != nil {
		return fmt.Errorf("schema sessions columns: %w", err)
	}
	hasCurrentPath := false
	for sessionColumns.Next() {
		var cid int
		var name, valueType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := sessionColumns.Scan(&cid, &name, &valueType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = sessionColumns.Close()
			return fmt.Errorf("schema sessions columns: %w", err)
		}
		if name == "current_transport_path" {
			hasCurrentPath = true
		}
	}
	if err := sessionColumns.Close(); err != nil {
		return fmt.Errorf("schema sessions columns: %w", err)
	}
	if !hasCurrentPath {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE sessions ADD COLUMN current_transport_path TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("schema 10 sessions current_transport_path: %w", err)
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
	freshnessColumns, err := tx.QueryContext(ctx, `PRAGMA table_info(scope_freshness)`)
	if err != nil {
		return fmt.Errorf("schema freshness columns: %w", err)
	}
	hasDigest := false
	for freshnessColumns.Next() {
		var cid int
		var name, valueType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := freshnessColumns.Scan(&cid, &name, &valueType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = freshnessColumns.Close()
			return fmt.Errorf("schema freshness columns: %w", err)
		}
		hasDigest = hasDigest || name == "snapshot_digest"
	}
	if err := freshnessColumns.Close(); err != nil {
		return fmt.Errorf("schema freshness columns: %w", err)
	}
	if !hasDigest {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE scope_freshness ADD COLUMN snapshot_digest TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("schema 6 snapshot digest: %w", err)
		}
	}
	// Schema 5 metadata was locally derived and cannot prove remote authority
	// ordering. Retain cached records, but force 2.1 to establish provenance.
	if currentSchema == 5 {
		if _, err := tx.ExecContext(ctx, `UPDATE scope_freshness SET source='',complete=0,snapshot_generation=0,snapshot_high_water=0,snapshot_digest=''`); err != nil {
			return fmt.Errorf("schema 6 freshness provenance reset: %w", err)
		}
	}
	// Schema 12 splits the old exact_connections row (remote pipe + locator +
	// lease + local pipe ref) into explicitly separate concepts: durable
	// consumer-side accepted remote-Pipe bindings (remote_pipe_bindings) and an
	// ephemeral AccessLease cache (exact_lease_cache). The old row was coupled
	// to a local ingress and made lease expiry destroy reachability knowledge;
	// the durable binding carries only relationship/reachability state and
	// survives lease expiry and local-Pipe removal. Old exact_connections rows
	// are dropped: they carry no owner binding or recipient identity from which
	// a verifiable durable binding could be reconstructed, so they fail closed.
	// Renewal queries by the accepted recipient, so invite_redemptions gains an
	// index on (pipe_id, client_public).
	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS exact_connections`); err != nil {
		return fmt.Errorf("schema 12 drop exact_connections: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_invite_redemptions_pipe_client ON invite_redemptions(pipe_id, client_public)`); err != nil {
		return fmt.Errorf("schema 12 redemption index: %w", err)
	}
	// Schema 13 makes the accepted relationship explicitly deterministic and
	// epoch-aware. This backfill is a ONE-TIME legacy upgrade (databases created
	// before schema 13 cannot distinguish exact from scoped redemptions after
	// the fact). It must never re-run on later opens: scoped redemptions written
	// after schema 14 carry kind='scoped' and must stay anti-replay history
	// only, never promoted to an accepted exact relationship.
	if currentSchema > 0 && currentSchema < 13 {
		// accepted_exact_relationships is the single authoritative
		// current relationship per (pipe_id, recipient_public); invite_redemptions
		// remains one-time anti-replay history. Deterministic backfill for schema
		// 12 state: every redeemed (pipe, client) pair gets one relationship row
		// from its LATEST redemption (redeemed_at desc, row id tie-break) at the
		// legacy epoch 0. A relationship is thus immediately usable when the
		// current pipe-access epoch is still 0, and fails closed (stale) once any
		// revocation has advanced it.
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO accepted_exact_relationships(pipe_id, recipient_public, invite_jti, access_epoch, accepted_at, updated_at)
			 SELECT pipe_id, client_public, jti, 0, redeemed_at, redeemed_at FROM invite_redemptions r
			 WHERE r.rowid = (SELECT r2.rowid FROM invite_redemptions r2
			  WHERE r2.pipe_id = r.pipe_id AND r2.client_public = r.client_public
			  ORDER BY r2.redeemed_at DESC, r2.rowid DESC LIMIT 1)`); err != nil {
			return fmt.Errorf("schema 13 accepted relationship backfill: %w", err)
		}
	}
	// Schema 14: invite_redemptions gains a kind discriminator so revocation
	// can deterministically classify a JTI as exact (Journey A, kind='exact')
	// vs scoped (legacy share.invite, kind='scoped'). Pre-14 rows default to
	// 'scoped' (no marker exists to distinguish them post-hoc); fresh exact
	// redemptions always mark 'exact'.
	redemptionColumns, err := tx.QueryContext(ctx, `PRAGMA table_info(invite_redemptions)`)
	if err != nil {
		return fmt.Errorf("schema invite_redemptions columns: %w", err)
	}
	hasKind := false
	for redemptionColumns.Next() {
		var cid int
		var name, valueType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := redemptionColumns.Scan(&cid, &name, &valueType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = redemptionColumns.Close()
			return fmt.Errorf("schema invite_redemptions columns: %w", err)
		}
		if name == "kind" {
			hasKind = true
		}
	}
	if err := redemptionColumns.Close(); err != nil {
		return fmt.Errorf("schema invite_redemptions columns: %w", err)
	}
	if !hasKind {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE invite_redemptions ADD COLUMN kind TEXT NOT NULL DEFAULT 'scoped'`); err != nil {
			return fmt.Errorf("schema 14 invite redemption kind: %w", err)
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
	tx, err := s.beginWriteTx(ctx)
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
	tx, err := s.beginWriteTx(ctx)
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

func validateStoredRecord(v Record, now time.Time) error {
	parts := strings.Split(v.Scope, "/")
	if len(parts) != 2 || (domain.Scope{Cluster: parts[0], Namespace: parts[1]}).Validate() != nil || strings.TrimSpace(v.PipeID) == "" || len(v.PipeID) > 8192 || len(v.Name) > 8192 {
		return errors.New("invalid record index")
	}
	if len(v.Payload) == 0 || len(v.Payload) > 64<<10 || v.Sequence == 0 || v.Sequence > uint64(^uint64(0)>>1) {
		return errors.New("invalid record bounds")
	}
	if !v.ExpiresAt.After(now) {
		return errors.New("record expired")
	}
	return nil
}

func putRecordTx(ctx context.Context, tx *sql.Tx, v Record, now time.Time) error {
	if err := validateStoredRecord(v, now); err != nil {
		return err
	}
	var sequence uint64
	var payload []byte
	err := tx.QueryRowContext(ctx, `SELECT sequence,payload FROM records WHERE scope=? AND pipe_id=?`, v.Scope, v.PipeID).Scan(&sequence, &payload)
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
	return err
}

func putDiscoveredRecordTx(ctx context.Context, tx *sql.Tx, v Record, now time.Time) error {
	v.Discovered = true
	var discovered bool
	err := tx.QueryRowContext(ctx, `SELECT discovered FROM records WHERE scope=? AND pipe_id=?`, v.Scope, v.PipeID).Scan(&discovered)
	if err == nil && !discovered {
		// Local and authority rows own this key in local state. A remote cache
		// snapshot must neither replace nor later make them deletion candidates.
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	return putRecordTx(ctx, tx, v, now)
}

func (s *Store) PutRecord(ctx context.Context, v Record) error {
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := putRecordTx(ctx, tx, v, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

func optionalTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func scanFreshness(scan func(...any) error) (ScopeFreshness, error) {
	var value ScopeFreshness
	var attempt, success, retry string
	var complete int
	if err := scan(&value.Scope, &attempt, &success, &value.Source, &complete, &value.ErrorClass, &retry, &value.SnapshotGeneration, &value.SnapshotHighWater, &value.SnapshotDigest); err != nil {
		return value, err
	}
	var err error
	if value.LastAttempt, err = optionalTime(attempt); err != nil {
		return value, err
	}
	if value.LastSuccess, err = optionalTime(success); err != nil {
		return value, err
	}
	if value.NextRetry, err = optionalTime(retry); err != nil {
		return value, err
	}
	value.Complete = complete != 0
	return value, nil
}

func (s *Store) ScopeFreshness(ctx context.Context, scope string) (ScopeFreshness, error) {
	value, err := scanFreshness(func(dest ...any) error {
		return s.db.QueryRowContext(ctx, `SELECT scope,last_attempt,last_success,source,complete,error_class,next_retry,snapshot_generation,snapshot_high_water,snapshot_digest FROM scope_freshness WHERE scope=?`, scope).Scan(dest...)
	})
	if errors.Is(err, sql.ErrNoRows) {
		return value, fmt.Errorf("%w: discovery freshness for %s", domain.ErrNotFound, scope)
	}
	return value, err
}

func (s *Store) EnsureAuthoritySnapshot(ctx context.Context, scope string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO authority_snapshots(scope,generation,high_water) VALUES(?,?,?) ON CONFLICT(scope) DO NOTHING`, scope, 1, 1)
	return err
}

func advanceAuthoritySnapshotTx(ctx context.Context, tx *sql.Tx, scope string) error {
	var generation, highWater uint64
	err := tx.QueryRowContext(ctx, `SELECT generation,high_water FROM authority_snapshots WHERE scope=?`, scope).Scan(&generation, &highWater)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO authority_snapshots(scope,generation,high_water) VALUES(?,?,?)`, scope, 1, 1)
	case err == nil:
		if generation == uint64(^uint64(0)>>1) || highWater == uint64(^uint64(0)>>1) {
			return errors.New("authority snapshot counter exhausted")
		}
		_, err = tx.ExecContext(ctx, `UPDATE authority_snapshots SET generation=?,high_water=? WHERE scope=?`, generation+1, highWater+1, scope)
	}
	return err
}

// AuthoritySnapshot prunes expired authority-owned records, advances metadata
// with that removal, then reads records and metadata in one transaction. More
// records than limit yields complete=false and can never
// authorize deletion on receivers.
func (s *Store) AuthoritySnapshot(ctx context.Context, scope string, limit int) (records []Record, generation, highWater uint64, complete bool, err error) {
	if limit <= 0 || limit > MaxSnapshotRecords {
		return nil, 0, 0, false, errors.New("invalid authority snapshot limit")
	}
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return nil, 0, 0, false, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	pruned, err := tx.ExecContext(ctx, `DELETE FROM records WHERE scope=? AND discovered=0 AND expires_at<=?`, scope, now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, 0, 0, false, err
	}
	if changed, err := pruned.RowsAffected(); err != nil {
		return nil, 0, 0, false, err
	} else if changed > 0 {
		if err := advanceAuthoritySnapshotTx(ctx, tx, scope); err != nil {
			return nil, 0, 0, false, err
		}
	}
	err = tx.QueryRowContext(ctx, `SELECT generation,high_water FROM authority_snapshots WHERE scope=?`, scope).Scan(&generation, &highWater)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, 0, false, errors.New("authority snapshot not initialized")
	}
	if err != nil || generation == 0 || highWater == 0 {
		if err != nil {
			return nil, 0, 0, false, err
		}
		return nil, 0, 0, false, errors.New("invalid authority snapshot metadata")
	}
	rows, err := tx.QueryContext(ctx, `SELECT scope,pipe_id,name,payload,expires_at,sequence,discovered FROM records WHERE scope=? AND discovered=0 AND expires_at>? ORDER BY name,pipe_id LIMIT ?`, scope, now.Format(time.RFC3339Nano), limit+1)
	if err != nil {
		return nil, 0, 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var record Record
		var expires string
		if err := rows.Scan(&record.Scope, &record.PipeID, &record.Name, &record.Payload, &expires, &record.Sequence, &record.Discovered); err != nil {
			return nil, 0, 0, false, err
		}
		record.ExpiresAt, err = time.Parse(time.RFC3339Nano, expires)
		if err != nil {
			return nil, 0, 0, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, 0, false, err
	}
	complete = len(records) <= limit
	if !complete {
		records = records[:limit]
	}
	if err := tx.Commit(); err != nil {
		return nil, 0, 0, false, err
	}
	return records, generation, highWater, complete, nil
}

// PutAuthoritativeRecord commits an authority-owned record with a new
// authority-global snapshot generation and mutation watermark. Per-pipe record
// sequence is not read here and is never global snapshot ordering.
func (s *Store) PutAuthoritativeRecord(ctx context.Context, v Record) error {
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	if err := putRecordTx(ctx, tx, v, now); err != nil {
		return err
	}
	if err := advanceAuthoritySnapshotTx(ctx, tx, v.Scope); err != nil {
		return err
	}
	return tx.Commit()
}

func validScopeSyncLeaseInput(scope, holderRef, processIdentity string, ttl time.Duration) error {
	if strings.TrimSpace(scope) == "" || len(scope) > 255 || strings.TrimSpace(holderRef) == "" || len(holderRef) > 255 || strings.TrimSpace(processIdentity) == "" || len(processIdentity) > 4096 {
		return errors.New("invalid scope sync lease identity")
	}
	if ttl <= 0 || ttl > maxScopeSyncLeaseTTL {
		return errors.New("invalid scope sync lease ttl")
	}
	return nil
}

func newScopeSyncLeaseToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

// AcquireScopeSyncLease commits an expiring, fenced owner before returning it.
// A live owner always wins; callers must wait for expiry before retrying.
func (s *Store) AcquireScopeSyncLease(ctx context.Context, scope, holderRef, processIdentity string, now time.Time, ttl time.Duration) (ScopeSyncLease, bool, error) {
	if err := validScopeSyncLeaseInput(scope, holderRef, processIdentity, ttl); err != nil {
		return ScopeSyncLease{}, false, err
	}
	token, err := newScopeSyncLeaseToken()
	if err != nil {
		return ScopeSyncLease{}, false, err
	}
	now = now.UTC()
	lease := ScopeSyncLease{Scope: scope, HolderRef: holderRef, ProcessIdentity: processIdentity, Token: token, AcquiredAt: now, RenewedAt: now, ExpiresAt: now.Add(ttl), Active: true}
	result, err := s.db.ExecContext(ctx, `INSERT INTO scope_sync_leases(scope,holder_ref,holder_process_identity,lease_token,acquired_at,renewed_at,expires_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET holder_ref=excluded.holder_ref,holder_process_identity=excluded.holder_process_identity,lease_token=excluded.lease_token,acquired_at=excluded.acquired_at,renewed_at=excluded.renewed_at,expires_at=excluded.expires_at WHERE scope_sync_leases.expires_at<=excluded.acquired_at`, scope, holderRef, processIdentity, token, lease.AcquiredAt.Format(time.RFC3339Nano), lease.RenewedAt.Format(time.RFC3339Nano), lease.ExpiresAt.Format(time.RFC3339Nano))
	if err != nil {
		return ScopeSyncLease{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ScopeSyncLease{}, false, err
	}
	if changed != 1 {
		return ScopeSyncLease{}, false, nil
	}
	return lease, true, nil
}

func validLeaseFence(lease ScopeSyncLease) bool {
	return lease.Scope != "" && lease.HolderRef != "" && lease.ProcessIdentity != "" && lease.Token != ""
}

// RenewScopeSyncLease compares every fencing field and rejects expired owners.
func (s *Store) RenewScopeSyncLease(ctx context.Context, lease ScopeSyncLease, now time.Time, ttl time.Duration) (bool, error) {
	if !validLeaseFence(lease) || validScopeSyncLeaseInput(lease.Scope, lease.HolderRef, lease.ProcessIdentity, ttl) != nil {
		return false, errors.New("invalid scope sync lease")
	}
	now = now.UTC()
	expires := now.Add(ttl)
	result, err := s.db.ExecContext(ctx, `UPDATE scope_sync_leases SET renewed_at=?,expires_at=? WHERE scope=? AND holder_ref=? AND holder_process_identity=? AND lease_token=? AND expires_at>?`, now.Format(time.RFC3339Nano), expires.Format(time.RFC3339Nano), lease.Scope, lease.HolderRef, lease.ProcessIdentity, lease.Token, now.Format(time.RFC3339Nano))
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}

// ReleaseScopeSyncLease cannot delete a successor acquired with another token.
func (s *Store) ReleaseScopeSyncLease(ctx context.Context, lease ScopeSyncLease) (bool, error) {
	if !validLeaseFence(lease) {
		return false, errors.New("invalid scope sync lease")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM scope_sync_leases WHERE scope=? AND holder_ref=? AND holder_process_identity=? AND lease_token=?`, lease.Scope, lease.HolderRef, lease.ProcessIdentity, lease.Token)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return changed == 1, nil
}

// ScopeSyncLease returns only a live owner. Its fencing token remains private.
func (s *Store) ScopeSyncLease(ctx context.Context, scope string) (ScopeSyncLease, error) {
	if strings.TrimSpace(scope) == "" || len(scope) > 255 {
		return ScopeSyncLease{}, errors.New("invalid scope sync lease scope")
	}
	var value ScopeSyncLease
	var acquired, renewed, expires string
	err := s.db.QueryRowContext(ctx, `SELECT scope,holder_ref,holder_process_identity,lease_token,acquired_at,renewed_at,expires_at FROM scope_sync_leases WHERE scope=?`, scope).Scan(&value.Scope, &value.HolderRef, &value.ProcessIdentity, &value.Token, &acquired, &renewed, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return ScopeSyncLease{Scope: scope}, nil
	}
	if err != nil {
		return ScopeSyncLease{}, err
	}
	var parseErr error
	if value.AcquiredAt, parseErr = time.Parse(time.RFC3339Nano, acquired); parseErr != nil {
		return ScopeSyncLease{}, parseErr
	}
	if value.RenewedAt, parseErr = time.Parse(time.RFC3339Nano, renewed); parseErr != nil {
		return ScopeSyncLease{}, parseErr
	}
	if value.ExpiresAt, parseErr = time.Parse(time.RFC3339Nano, expires); parseErr != nil {
		return ScopeSyncLease{}, parseErr
	}
	value.Active = value.ExpiresAt.After(time.Now().UTC())
	// Status callers receive holder ref and expiry only. Fencing fields are
	// returned solely by successful acquisition and never by status reads.
	value.ProcessIdentity, value.Token = "", ""
	if !value.Active {
		value.HolderRef = ""
	}
	return value, nil
}

func leaseCurrentTx(ctx context.Context, tx *sql.Tx, lease ScopeSyncLease, now time.Time) error {
	if !validLeaseFence(lease) {
		return ErrScopeSyncLeaseLost
	}
	var expiry string
	err := tx.QueryRowContext(ctx, `SELECT expires_at FROM scope_sync_leases WHERE scope=? AND holder_ref=? AND holder_process_identity=? AND lease_token=?`, lease.Scope, lease.HolderRef, lease.ProcessIdentity, lease.Token).Scan(&expiry)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrScopeSyncLeaseLost
		}
		return err
	}
	parsed, err := time.Parse(time.RFC3339Nano, expiry)
	if err != nil {
		return err
	}
	if !parsed.After(now.UTC()) {
		return ErrScopeSyncLeaseLost
	}
	return nil
}

func (s *Store) recordSnapshotFailure(ctx context.Context, scope, source, errorClass string, nextRetry *time.Time, lease *ScopeSyncLease) error {
	if source != SnapshotSourcePinnedAuthority || errorClass == "" {
		return errors.New("invalid discovery refresh failure")
	}
	if lease == nil {
		now := time.Now().UTC()
		retry := ""
		if nextRetry != nil {
			retry = nextRetry.UTC().Format(time.RFC3339Nano)
		}
		_, err := s.db.ExecContext(ctx, `INSERT INTO scope_freshness(scope,last_attempt,source,complete,error_class,next_retry) VALUES(?,?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET last_attempt=excluded.last_attempt,source=excluded.source,complete=0,error_class=excluded.error_class,next_retry=excluded.next_retry`, scope, now.Format(time.RFC3339Nano), source, false, errorClass, retry)
		return err
	}
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	retry := ""
	if nextRetry != nil {
		retry = nextRetry.UTC().Format(time.RFC3339Nano)
	}
	if err := leaseCurrentTx(ctx, tx, *lease, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scope_freshness(scope,last_attempt,source,complete,error_class,next_retry) VALUES(?,?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET last_attempt=excluded.last_attempt,source=excluded.source,complete=0,error_class=excluded.error_class,next_retry=excluded.next_retry`, scope, now.Format(time.RFC3339Nano), source, false, errorClass, retry); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RecordSnapshotFailure(ctx context.Context, scope, source, errorClass string, nextRetry *time.Time) error {
	return s.recordSnapshotFailure(ctx, scope, source, errorClass, nextRetry, nil)
}

func (s *Store) RecordSnapshotFailureWithLease(ctx context.Context, lease ScopeSyncLease, source, errorClass string, nextRetry *time.Time) error {
	return s.recordSnapshotFailure(ctx, lease.Scope, source, errorClass, nextRetry, &lease)
}

// ReconcileAuthoritativeSnapshot changes cache state only for one complete,
// pinned-authority snapshot. Caller must cryptographically verify every record
// before this method starts its transaction. Store write transactions reserve
// SQLite writer ownership at begin, before reads control cache mutation.
func (s *Store) ReconcileAuthoritativeSnapshot(ctx context.Context, scope string, snapshot AuthoritativeSnapshot) error {
	return s.reconcileAuthoritativeSnapshot(ctx, scope, snapshot, nil)
}

// ReconcileAuthoritativeSnapshotWithLease fences final cache/freshness writes
// against a current, unexpired periodic sync owner.
func (s *Store) ReconcileAuthoritativeSnapshotWithLease(ctx context.Context, lease ScopeSyncLease, snapshot AuthoritativeSnapshot) error {
	return s.reconcileAuthoritativeSnapshot(ctx, lease.Scope, snapshot, &lease)
}

func (s *Store) reconcileAuthoritativeSnapshot(ctx context.Context, scope string, snapshot AuthoritativeSnapshot, lease *ScopeSyncLease) error {
	return s.reconcileAuthoritativeSnapshotOnce(ctx, scope, snapshot, lease)
}

func (s *Store) reconcileAuthoritativeSnapshotOnce(ctx context.Context, scope string, snapshot AuthoritativeSnapshot, lease *ScopeSyncLease) error {
	if snapshot.Source != SnapshotSourcePinnedAuthority || !snapshot.Complete || snapshot.Generation == 0 || snapshot.HighWater == 0 || len(snapshot.Digest) != 64 || len(snapshot.Records) > MaxSnapshotRecords {
		return errors.New("invalid authoritative snapshot metadata")
	}
	seen := make(map[string]struct{}, len(snapshot.Records))
	for _, record := range snapshot.Records {
		if record.Scope != scope {
			return errors.New("invalid authoritative snapshot record")
		}
		if _, duplicate := seen[record.PipeID]; duplicate {
			return errors.New("duplicate record in authoritative snapshot")
		}
		seen[record.PipeID] = struct{}{}
	}
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	for _, record := range snapshot.Records {
		if err := validateStoredRecord(record, now); err != nil {
			return err
		}
	}
	if lease != nil {
		if err := leaseCurrentTx(ctx, tx, *lease, now); err != nil {
			return err
		}
	}
	var currentGeneration, currentHighWater uint64
	var currentDigest string
	err = tx.QueryRowContext(ctx, `SELECT snapshot_generation,snapshot_high_water,snapshot_digest FROM scope_freshness WHERE scope=?`, scope).Scan(&currentGeneration, &currentHighWater, &currentDigest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && (snapshot.Generation < currentGeneration || snapshot.HighWater < currentHighWater) {
		return errAuthoritativeSnapshotRollback
	}
	if err == nil && snapshot.Generation == currentGeneration && currentDigest != "" && (snapshot.Digest != currentDigest || snapshot.HighWater != currentHighWater) {
		return errAuthoritativeSnapshotAmbiguous
	}
	for _, record := range snapshot.Records {
		if err := putDiscoveredRecordTx(ctx, tx, record, now); err != nil {
			return err
		}
	}
	// Only authority-global generation orders deletion. Per-pipe sequence is
	// replay protection for one record, never a scope-wide snapshot watermark.
	if len(snapshot.Records) == 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM records WHERE scope=? AND discovered=1`, scope); err != nil {
			return err
		}
	} else {
		ids := make([]any, 0, len(snapshot.Records)+1)
		ids = append(ids, scope)
		marks := make([]string, 0, len(snapshot.Records))
		for _, record := range snapshot.Records {
			marks = append(marks, "?")
			ids = append(ids, record.PipeID)
		}
		query := `DELETE FROM records WHERE scope=? AND discovered=1 AND pipe_id NOT IN (` + strings.Join(marks, ",") + `)`
		if _, err := tx.ExecContext(ctx, query, ids...); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM records WHERE scope=? AND discovered=1 AND expires_at<=?`, scope, now.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scope_freshness(scope,last_attempt,last_success,source,complete,error_class,next_retry,snapshot_generation,snapshot_high_water,snapshot_digest) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(scope) DO UPDATE SET last_attempt=excluded.last_attempt,last_success=excluded.last_success,source=excluded.source,complete=excluded.complete,error_class='',next_retry='',snapshot_generation=excluded.snapshot_generation,snapshot_high_water=excluded.snapshot_high_water,snapshot_digest=excluded.snapshot_digest`, scope, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), snapshot.Source, true, "", "", snapshot.Generation, snapshot.HighWater, snapshot.Digest); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) PruneExpiredRecords(ctx context.Context, now time.Time) (int64, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM records WHERE discovered=1 AND expires_at<=?`, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (s *Store) Records(ctx context.Context, scope string) ([]Record, error) {
	if _, err := s.PruneExpiredRecords(ctx, time.Now().UTC()); err != nil {
		return nil, err
	}
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
	if _, err := s.PruneExpiredRecords(ctx, time.Now().UTC()); err != nil {
		return Record{}, err
	}
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
	if _, err := s.PruneExpiredRecords(ctx, time.Now().UTC()); err != nil {
		return Record{}, err
	}
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
	tx, err := s.beginWriteTx(ctx)
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

// Revoke persists a scope-bound revocation. Epochs for pipe-access and
// pipe-publish subjects never move backwards, including duplicate retries.
func (s *Store) Revoke(ctx context.Context, scope, subjectType, subjectJTI string, epoch uint64) error {
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(subjectType) == "" || strings.TrimSpace(subjectJTI) == "" || len(scope) > 8192 || len(subjectType) > 128 || len(subjectJTI) > 8192 {
		return errors.New("invalid revocation")
	}
	if epoch == 0 {
		return errors.New("invalid revocation epoch")
	}
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing uint64
	err = tx.QueryRowContext(ctx, `SELECT epoch FROM revocations WHERE scope=? AND subject_type=? AND subject_jti=?`, scope, subjectType, subjectJTI).Scan(&existing)
	if err == nil && existing > epoch {
		return errors.New("revocation epoch rollback")
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// For pipe epoch revocations, reject a lower epoch for same pipe/type even
	// when caller uses a different future subject encoding.
	if subjectType == "pipe-access" || subjectType == "pipe-publish" {
		var maximum sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(epoch) FROM revocations WHERE scope=? AND subject_type=? AND subject_jti=?`, scope, subjectType, subjectJTI).Scan(&maximum); err != nil {
			return err
		}
		if maximum.Valid && uint64(maximum.Int64) > epoch {
			return errors.New("revocation epoch rollback")
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO revocations(scope,subject_type,subject_jti,revoked_at,epoch) VALUES(?,?,?,?,?) ON CONFLICT(scope,subject_type,subject_jti) DO UPDATE SET revoked_at=excluded.revoked_at,epoch=excluded.epoch WHERE excluded.epoch>=revocations.epoch`, scope, subjectType, subjectJTI, time.Now().UTC().Format(time.RFC3339Nano), epoch)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RevocationEpoch(ctx context.Context, scope, subjectType, subjectJTI string) (uint64, error) {
	var epoch uint64
	err := s.db.QueryRowContext(ctx, `SELECT epoch FROM revocations WHERE scope=? AND subject_type=? AND subject_jti=?`, scope, subjectType, subjectJTI).Scan(&epoch)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return epoch, err
}

func (s *Store) IsRevoked(ctx context.Context, scope, subjectType, subjectJTI string) (bool, error) {
	var value int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM revocations WHERE scope=? AND subject_type=? AND subject_jti=?`, scope, subjectType, subjectJTI).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// RedeemInvite atomically consumes a one-time invite. Repeating same JTI and
// client key is idempotent; another client cannot observe success.
func (s *Store) RedeemInvite(ctx context.Context, jti, pipeID, sessionID, clientPublic string) (bool, error) {
	if strings.TrimSpace(jti) == "" || strings.TrimSpace(pipeID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(clientPublic) == "" {
		return false, errors.New("invalid invite redemption")
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO invite_redemptions(jti,pipe_id,redeemed_at,session_id,client_public) VALUES(?,?,?,?,?) ON CONFLICT(jti,pipe_id) DO NOTHING`, jti, pipeID, time.Now().UTC().Format(time.RFC3339Nano), sessionID, clientPublic)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	switch affected {
	case 1:
		return true, nil
	case 0:
		var client string
		if err := s.db.QueryRowContext(ctx, `SELECT client_public FROM invite_redemptions WHERE jti=? AND pipe_id=?`, jti, pipeID).Scan(&client); err != nil {
			return false, err
		}
		if client != clientPublic {
			return false, ErrInviteRedeemed
		}
		return false, nil
	default:
		return false, fmt.Errorf("unexpected invite redemption rows affected: %d", affected)
	}
}

func (s *Store) RedemptionCount(ctx context.Context, jti, pipeID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM invite_redemptions WHERE jti=? AND pipe_id=?`, jti, pipeID).Scan(&count)
	return count, err
}

// RedeemInviteAndAccept consumes a one-time invite atomically with the
// authoritative accepted relationship for (pipe_id, client_public).
//
// Model explicitly separates the two concerns:
//   - invite_redemptions: one-time redemption history / anti-replay evidence
//     (PK invite identity); every invite redeemed exactly once.
//   - accepted_exact_relationships: exactly one current authoritative
//     authorization relationship per (pipe_id, recipient_public), carrying
//     the originating/current invite JTI and the accepted pipe-access epoch.
//
// A first redemption inserts the redemption row AND upserts the relationship
// (a new valid invite for the same Pipe+recipient is an explicit
// re-authorization event that replaces the current relationship). An
// idempotent same-recipient retry of an already-redeemed invite does not
// regress the relationship (a newer invite may have advanced it) and reports
// ok=true so the caller may mint a lease; a different recipient fails closed.
func (s *Store) RedeemInviteAndAccept(ctx context.Context, jti, pipeID, sessionID, clientPublic string, accessEpoch uint64, now time.Time) (bool, error) {
	if strings.TrimSpace(jti) == "" || strings.TrimSpace(pipeID) == "" || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(clientPublic) == "" {
		return false, errors.New("invalid invite redemption")
	}
	now = now.UTC()
	tx, err := s.beginWriteTx(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO invite_redemptions(jti,pipe_id,redeemed_at,session_id,client_public,kind) VALUES(?,?,?,?,?,'exact') ON CONFLICT(jti,pipe_id) DO NOTHING`, jti, pipeID, now.Format(time.RFC3339Nano), sessionID, clientPublic)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	switch affected {
	case 1:
		// First redemption: (re-)accept the authoritative relationship.
		if _, err := tx.ExecContext(ctx, `INSERT INTO accepted_exact_relationships(pipe_id, recipient_public, invite_jti, access_epoch, accepted_at, updated_at) VALUES(?,?,?,?,?,?)
			 ON CONFLICT(pipe_id, recipient_public) DO UPDATE SET invite_jti=excluded.invite_jti, access_epoch=excluded.access_epoch, updated_at=excluded.updated_at`, pipeID, clientPublic, jti, accessEpoch, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
			return false, err
		}
		return true, tx.Commit()
	case 0:
		// Already redeemed: idempotent only for the same recipient. The
		// relationship is intentionally NOT rewritten (a newer invite may
		// have advanced it); the caller may still mint a lease if the epoch
		// checks passed, because the relationship already covers this pipe.
		var client string
		if err := tx.QueryRowContext(ctx, `SELECT client_public FROM invite_redemptions WHERE jti=? AND pipe_id=?`, jti, pipeID).Scan(&client); err != nil {
			return false, err
		}
		if client != clientPublic {
			return false, ErrInviteRedeemed
		}
		return true, tx.Commit()
	default:
		return false, fmt.Errorf("unexpected invite redemption rows affected: %d", affected)
	}
}

// AcceptedRelationship returns the current authoritative accepted
// relationship for this recipient and Pipe: the backing invite JTI and the
// accepted pipe-access epoch. There is exactly one row per
// (pipe_id, recipient_public), so resolution is deterministic — never a LIMIT
// guess over the redemption history. ok=false when the relationship never
// existed; persistence errors fail closed.
func (s *Store) AcceptedRelationship(ctx context.Context, pipeID, clientPublic string) (string, uint64, bool, error) {
	if strings.TrimSpace(pipeID) == "" || strings.TrimSpace(clientPublic) == "" {
		return "", 0, false, errors.New("invalid accepted relationship lookup")
	}
	var jti string
	var accessEpoch uint64
	err := s.db.QueryRowContext(ctx, `SELECT invite_jti, access_epoch FROM accepted_exact_relationships WHERE pipe_id=? AND recipient_public=?`, pipeID, clientPublic).Scan(&jti, &accessEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, false, nil
	}
	if err != nil {
		return "", 0, false, err
	}
	return jti, accessEpoch, true, nil
}

// RedemptionPipeForInvite resolves the pipe an invite JTI was accepted for, or
// ok=false when it was never redeemed. Used by scope-less exact revocation.
func (s *Store) RedemptionPipeForInvite(ctx context.Context, jti string) (string, bool, error) {
	if strings.TrimSpace(jti) == "" {
		return "", false, errors.New("invalid redemption lookup")
	}
	var pipeID string
	err := s.db.QueryRowContext(ctx, `SELECT pipe_id FROM invite_redemptions WHERE jti=? LIMIT 1`, jti).Scan(&pipeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return pipeID, true, nil
}

// ExactRedemptionPipeForInvite resolves the exact Pipe an invite JTI was
// accepted through the EXACT (Journey A) redemption path, or ok=false when
// the JTI is not an exact-redeemed invite. Deterministic discriminator:
// exact redemptions record kind='exact' (RedeemInviteAndAccept), scoped
// share.invite redemptions record kind='scoped' (RedeemInvite) and never
// resolve here — including OBSOLETE exact invites whose accepted relationship
// was later replaced by a newer invite. Persistence errors fail closed.
func (s *Store) ExactRedemptionPipeForInvite(ctx context.Context, jti string) (string, bool, error) {
	if strings.TrimSpace(jti) == "" {
		return "", false, errors.New("invalid exact redemption lookup")
	}
	var pipeID string
	err := s.db.QueryRowContext(ctx, `SELECT pipe_id FROM invite_redemptions WHERE jti=? AND kind='exact' LIMIT 1`, jti).Scan(&pipeID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return pipeID, true, nil
}

// ResolveRevokeInvite deterministically classifies a `revoke invite <jti>`
// into the EXACT invite family (scope = the exact Pipe; the caller revokes
// subject_type exact.invite) or the SCOPED invite family (scope = current
// Scope; the caller revokes subject_type share.invite). Persistence errors
// fail closed: kind is "" and err is non-nil, and the caller must never fall
// back to scoped on a lookup failure.
func (s *Store) ResolveRevokeInvite(ctx context.Context, jti string) (kind, scope string, err error) {
	pipeID, ok, err := s.ExactRedemptionPipeForInvite(ctx, jti)
	if err != nil {
		return "", "", err
	}
	if ok {
		return "exact", pipeID, nil
	}
	return "scoped", "", nil
}

func (s *Store) Revocations(ctx context.Context, scope string, subjectJTIs []string) ([]Revocation, error) {
	if len(subjectJTIs) == 0 {
		return nil, nil
	}
	if len(subjectJTIs) > 256 {
		return nil, errors.New("too many revocation subjects")
	}
	marks, args := make([]string, len(subjectJTIs)), make([]any, 0, len(subjectJTIs)+1)
	args = append(args, scope)
	for i, jti := range subjectJTIs {
		marks[i] = "?"
		args = append(args, jti)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT scope,subject_type,subject_jti,revoked_at,epoch FROM revocations WHERE scope=? AND subject_jti IN (`+strings.Join(marks, ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revocation
	for rows.Next() {
		var v Revocation
		var at string
		if err := rows.Scan(&v.Scope, &v.SubjectType, &v.SubjectJTI, &at, &v.Epoch); err != nil {
			return nil, err
		}
		v.RevokedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) PutConnectLease(ctx context.Context, pipeID, scope, accessLease, refreshLease string, expiresAt time.Time) error {
	if pipeID == "" || scope == "" || accessLease == "" || refreshLease == "" || expiresAt.IsZero() {
		return errors.New("invalid connect lease")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO connect_leases(pipe_id,scope,access_lease,refresh_lease,expires_at) VALUES(?,?,?,?,?) ON CONFLICT(pipe_id) DO UPDATE SET scope=excluded.scope,access_lease=excluded.access_lease,refresh_lease=excluded.refresh_lease,expires_at=excluded.expires_at`, pipeID, scope, accessLease, refreshLease, expiresAt.UTC().Format(time.RFC3339Nano))
	return err
}
func (s *Store) ConnectLease(ctx context.Context, pipeID string) (scope, accessLease, refreshLease string, expiresAt time.Time, err error) {
	var at string
	err = s.db.QueryRowContext(ctx, `SELECT scope,access_lease,refresh_lease,expires_at FROM connect_leases WHERE pipe_id=?`, pipeID).Scan(&scope, &accessLease, &refreshLease, &at)
	if errors.Is(err, sql.ErrNoRows) {
		err = fmt.Errorf("%w: connect lease for %s", domain.ErrNotFound, pipeID)
		return
	}
	if err == nil {
		expiresAt, err = time.Parse(time.RFC3339Nano, at)
	}
	return
}

func (s *Store) PutSession(ctx context.Context, v Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions(id,pipe_ref,initial_transport_path,current_transport_path,started_at,bytes_in,bytes_out) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET bytes_in=excluded.bytes_in,bytes_out=excluded.bytes_out,current_transport_path=COALESCE(excluded.current_transport_path,current_transport_path)`, v.ID, v.PipeRef, v.InitialTransportPath, v.CurrentTransportPath, v.StartedAt.UTC().Format(time.RFC3339Nano), v.BytesIn, v.BytesOut)
	return err
}
func (s *Store) Sessions(ctx context.Context, ref string) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,pipe_ref,initial_transport_path,COALESCE(current_transport_path,''),started_at,bytes_in,bytes_out FROM sessions WHERE pipe_ref=? ORDER BY started_at DESC LIMIT 100`, ref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var v Session
		var at string
		if err := rows.Scan(&v.ID, &v.PipeRef, &v.InitialTransportPath, &v.CurrentTransportPath, &at, &v.BytesIn, &v.BytesOut); err != nil {
			return nil, err
		}
		v.StartedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) Session(ctx context.Context, id string) (Session, error) {
	var v Session
	var at, cur string
	err := s.db.QueryRowContext(ctx, `SELECT id,pipe_ref,initial_transport_path,COALESCE(current_transport_path,''),started_at,bytes_in,bytes_out FROM sessions WHERE id=?`, id).Scan(&v.ID, &v.PipeRef, &v.InitialTransportPath, &cur, &at, &v.BytesIn, &v.BytesOut)
	if errors.Is(err, sql.ErrNoRows) {
		return v, fmt.Errorf("%w: session/%s", domain.ErrNotFound, id)
	}
	if err == nil {
		v.CurrentTransportPath = cur
		v.StartedAt, _ = time.Parse(time.RFC3339Nano, at)
	}
	return v, err
}

func (s *Store) PutPathEvent(ctx context.Context, v PathEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO path_events(session_id,epoch,from_path,to_path,reason,c2s_committed,s2c_committed,committed_at) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(session_id,epoch) DO UPDATE SET from_path=excluded.from_path,to_path=excluded.to_path,reason=excluded.reason,c2s_committed=excluded.c2s_committed,s2c_committed=excluded.s2c_committed,committed_at=excluded.committed_at`, v.SessionID, v.Epoch, v.FromPath, v.ToPath, v.Reason, v.C2SCommitted, v.S2CCommitted, v.CommittedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) SessionPathEvents(ctx context.Context, sessionID string) ([]PathEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id,epoch,from_path,to_path,reason,c2s_committed,s2c_committed,committed_at FROM path_events WHERE session_id=? ORDER BY epoch`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PathEvent
	for rows.Next() {
		var v PathEvent
		var at string
		if err := rows.Scan(&v.SessionID, &v.Epoch, &v.FromPath, &v.ToPath, &v.Reason, &v.C2SCommitted, &v.S2CCommitted, &at); err != nil {
			return nil, err
		}
		v.CommittedAt, _ = time.Parse(time.RFC3339Nano, at)
		out = append(out, v)
	}
	return out, rows.Err()
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
