// Package exactauthority is the L3 owner-bound Exact Pipe Authority foundation
// for A0 step 6B1 (issue #52). It hosts only the deterministic, owner-local
// dedicated Exact Authority key lifecycle primitive used for trusted local
// share preparation (the Authority signing key for exact-Pipe Access Leases
// and Exact Invites). It is pure and stdlib-only: no network/runtime handlers,
// no CLI, no persistence schema, no Scope/membership/discovery, no OpenAuthorizer
// wiring, and no reachability/libp2p/store/CLI imports.
//
// Ownership model: the Pipe owner owns the Exact Authority for its Pipe. A
// dedicated Exact Authority key is created once under a deterministic secret
// path that is a pure function of the owner identity, and the owner's identity
// key embeds a mandatory owner proof over the key material. Load is read-only
// and strictly fails closed on missing, corrupt, or owner-mismatched material.
// The owner then signs exactly one accesslease.OwnerBinding binding this key
// to an exact Pipe (share preparation); possession of that binding is what
// later (6B2) resolves as issuer trust. Nothing here asserts authority by
// itself.
//
// Provisioning safety: the secret is written to a restrictive-mode temp file
// in the authority directory, fully written and fsynced, then atomically
// installed with a no-clobber hard link so a concurrent first provision has
// exactly one persisted winner and every losing creator reloads that winner.
// Symlinked authority directories or key files and group/other-writable
// permissions fail closed; chmod/permission errors are never ignored.
package exactauthority

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
)

const (
	keyFormat      = "exact_owner_authority_key_v1"
	keyVersion     = 1
	secretFileMode = 0o600
	secretDirMode  = 0o700
	// keyEnvelopeMax bounds the whole on-disk secret envelope. Checked before
	// the read buffers any bytes and re-checked after decoding so no
	// unbounded allocation can occur.
	keyEnvelopeMax = 16 << 10
)

// ErrMissing is returned by read-only load when no key material exists; it
// fails closed rather than silently creating anything.
var ErrMissing = errors.New("exactauthority: no owner authority key material")

// ErrSymlink is returned when a symlink would redirect the authoritative
// secret read/write; the lifecycle fails closed rather than following it.
var ErrSymlink = errors.New("exactauthority: secret path resolves through a symlink")

// ErrExists is returned when atomic no-clobber provisioning observes that
// another creator already installed the secret; the caller reloads the
// persisted winner.
var ErrExists = errors.New("exactauthority: secret already provisioned")

// Key is a provisioned, owner-bound dedicated authority key. The private key
// lives in a single deterministic local file under the owner's secret path.
// OwnerProof binds the key to the owner's self-certifying Pipe identity.
type Key struct {
	private    ed25519.PrivateKey
	owner      ed25519.PublicKey
	secretPath string
}

// Public returns the derived dedicated authority public key.
func (k *Key) Public() ed25519.PublicKey {
	if k == nil {
		return nil
	}
	return k.private.Public().(ed25519.PublicKey)
}

// OwnerPublic returns the owning participant's public identity key.
func (k *Key) OwnerPublic() ed25519.PublicKey {
	if k == nil {
		return nil
	}
	return k.owner
}

// SecretPath returns the deterministic secret file path.
func (k *Key) SecretPath() string {
	if k == nil {
		return ""
	}
	return k.secretPath
}

// keyMetadata is the deterministic on-disk envelope. proof is the owner's
// Ed25519 signature over ownerPublic + authorityPublic under
// accesslease.OwnerKeyProofDomain, proving the owner produced this key.
type keyMetadata struct {
	Format          string `json:"format"`
	Version         int    `json:"version"`
	Owner           string `json:"owner"`
	AuthoritySecret string `json:"authority_secret"`
	AuthorityPublic string `json:"authority_public"`
	Proof           string `json:"proof"`
}

// keyFileAllowed names every accepted member of the strict key envelope and
// bounds each string member before any typed unmarshal or base64 decode.
func keyFileAllowed() map[string]int {
	return map[string]int{
		"format":           accesslease.MaxFieldLen,
		"version":          accesslease.MaxFieldLen,
		"owner":            accesslease.MaxFieldLen,
		"authority_secret": accesslease.MaxFieldLen,
		"authority_public": accesslease.MaxFieldLen,
		"proof":            accesslease.MaxFieldLen,
	}
}

// secretPath is the deterministic secret file path for the given owner. The
// same owner always resolves to the same path, so provisioning and loading
// alias to one canonical key (create-or-load existing once).
func secretPath(dir string, owner ed25519.PublicKey) string {
	sum := sha256.Sum256(owner)
	id := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:16]))
	return filepath.Join(dir, "exact-authority", "ea-"+id+".key.json")
}

// SecretPath returns the deterministic secret file path for the given owner
// public key, used to prepare/load the same owner-local dedicated key.
func SecretPath(dir string, owner ed25519.PublicKey) string {
	return secretPath(dir, owner)
}

// secureAuthorityDir verifies the authority directory is a real (non-symlink)
// directory with no group/other-writable permissions. A missing directory is
// allowed (the containing file missing check yields ErrMissing at load time).
// Failures to observe modes are never swallowed.
func secureAuthorityDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("exactauthority: lstat authority dir: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return ErrSymlink
	}
	if !fi.IsDir() {
		return errors.New("exactauthority: authority path is not a directory")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return errors.New("exactauthority: authority dir has unsafe permissions")
	}
	return nil
}

// enforceSecretDir creates the owner-local authority directory with a
// restrictive mode, forcing the mode even under a permissive umask, and never
// ignores a chmod failure.
func enforceSecretDir(dir string) error {
	if err := os.MkdirAll(dir, secretDirMode); err != nil {
		return fmt.Errorf("exactauthority: mkdir: %w", err)
	}
	if err := os.Chmod(dir, secretDirMode); err != nil {
		return fmt.Errorf("exactauthority: chmod secret dir: %w", err)
	}
	return secureAuthorityDir(dir)
}

// syncDir fsyncs a directory so a freshly installed secret becomes durable
// (as appropriate on the host platform).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("exactauthority: open secret dir for sync: %w", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("exactauthority: sync secret dir: %w", err)
	}
	return nil
}

// writeSecretComplete installs payload at path atomically and without
// clobbering an existing file. It writes os.CreateTemp a unique temp in the
// authority directory, enforces 0600, verifies the full write, fsyncs and
// closes, then hard-links the temp to the final name (no-clobber: EEXIST if a
// concurrent creator already installed). The temp is removed in every branch.
// When the no-clobber link observes an existing winner it returns ErrExists so
// the caller reloads the persisted winner instead of racing.
func writeSecretComplete(path string, payload []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ea-*.tmp")
	if err != nil {
		return fmt.Errorf("exactauthority: create secret temp: %w", err)
	}
	tmpName := tmp.Name()
	rollback := func(reason error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return reason
	}
	if err := tmp.Chmod(secretFileMode); err != nil {
		return rollback(fmt.Errorf("exactauthority: chmod secret temp: %w", err))
	}
	n, err := tmp.Write(payload)
	if err != nil || n != len(payload) {
		return rollback(fmt.Errorf("exactauthority: write secret temp (wrote %d/%d): %w", n, len(payload), err))
	}
	if err := tmp.Sync(); err != nil {
		return rollback(fmt.Errorf("exactauthority: sync secret temp: %w", err))
	}
	if err := tmp.Close(); err != nil {
		return rollback(fmt.Errorf("exactauthority: close secret temp: %w", err))
	}
	// Atomic no-clobber install: a concurrent winner leaves an existing final
	// file, Link fails with EEXIST, and we reload that winner.
	if err := os.Link(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		if os.IsExist(err) {
			return ErrExists
		}
		return fmt.Errorf("exactauthority: link secret into place: %w", err)
	}
	if err := os.Remove(tmpName); err != nil {
		// The install succeeded; a leftover temp is cleaned best-effort only
		// (it holds the same payload, removed here).
		_ = err
	}
	if err := syncDir(dir); err != nil {
		return fmt.Errorf("exactauthority: durable secret install: %w", err)
	}
	return nil
}

// Provision creates the dedicated authority key owned by owner at the
// deterministic secret path, or loads the pre-existing one. It is the "create
// once / load existing once" primitive, safe under concurrent first
// provisioning: exactly one persisted winner is written atomically (full
// temp write + fsync + no-clobber hard link) and every losing creator reloads
// that persisted winner. The final bytes are never observed partially. owner
// is the Pipe owner's identity secret used to author the mandatory owner proof.
func Provision(dir string, owner ed25519.PrivateKey) (*Key, error) {
	if owner == nil || len(owner) != ed25519.PrivateKeySize {
		return nil, errors.New("exactauthority: owner identity required")
	}
	ownerPub := owner.Public().(ed25519.PublicKey)
	path := secretPath(dir, ownerPub)
	// Fast path: if key material already exists, load (and strictly validate
	// it). The guarantee that we never race the final write is the atomic
	// no-clobber install below, not this stat.
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, ErrSymlink
		}
		return Load(dir, ownerPub)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("exactauthority: stat secret: %w", err)
	}
	if err := enforceSecretDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		// A concurrent creator won before we created the directory leaves.
		return Load(dir, ownerPub)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	authPub := priv.Public().(ed25519.PublicKey)
	envelope := keyMetadata{
		Format:          keyFormat,
		Version:         keyVersion,
		Owner:           accesslease.PublicString(ownerPub),
		AuthoritySecret: base64.RawURLEncoding.EncodeToString(priv),
		AuthorityPublic: accesslease.PublicString(authPub),
		Proof:           ownerProof(owner, ownerPub, authPub),
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	if err := writeSecretComplete(path, payload); err != nil {
		if errors.Is(err, ErrExists) {
			return Load(dir, ownerPub)
		}
		return nil, err
	}
	return &Key{private: priv, owner: ownerPub, secretPath: path}, nil
}

// Load read-only loads the existing, owner-bound key. It creates no filesystem
// entry: missing material returns ErrMissing, and corrupt, owner-mismatched,
// symlinked, or unsafe-mode material fails closed. The envelope is strictly
// scanned (unknown/duplicate/trailing/nested and overlong members rejected
// before typed unmarshal and before any base64 decode) under a total byte cap.
func Load(dir string, owner ed25519.PublicKey) (*Key, error) {
	if owner == nil || len(owner) != ed25519.PublicKeySize {
		return nil, errors.New("exactauthority: owner identity required")
	}
	path := secretPath(dir, owner)
	if err := secureAuthorityDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMissing
		}
		return nil, fmt.Errorf("exactauthority: stat secret: %w", err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return nil, ErrSymlink
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("exactauthority: secret file has unsafe permissions")
	}
	if fi.Size() > keyEnvelopeMax {
		return nil, errors.New("exactauthority: secret envelope exceeds byte bound")
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("exactauthority: read secret: %w", err)
	}
	if len(buf) > keyEnvelopeMax {
		return nil, errors.New("exactauthority: secret envelope exceeds byte bound")
	}
	// Strict bounded envelope before any typed unmarshal: unknown/duplicate/
	// trailing/nested members and overlong secret/public/proof strings are
	// rejected here, before base64 decode or cryptographic verification.
	if err := accesslease.ScanStrictFlat(buf, keyFileAllowed()); err != nil {
		return nil, fmt.Errorf("exactauthority: invalid secret envelope: %w", err)
	}
	var envelope keyMetadata
	if err := json.Unmarshal(buf, &envelope); err != nil {
		return nil, errors.New("exactauthority: corrupt key envelope")
	}
	if envelope.Format != keyFormat || envelope.Version != keyVersion {
		return nil, errors.New("exactauthority: unsupported key envelope")
	}
	if envelope.Owner != accesslease.PublicString(owner) {
		return nil, errors.New("exactauthority: key owned by a different owner")
	}
	secret, err := base64.RawURLEncoding.DecodeString(envelope.AuthoritySecret)
	if err != nil || len(secret) != ed25519.PrivateKeySize {
		return nil, errors.New("exactauthority: corrupt key secret")
	}
	priv := ed25519.PrivateKey(secret)
	authPub := priv.Public().(ed25519.PublicKey)
	if envelope.AuthorityPublic != accesslease.PublicString(authPub) {
		return nil, errors.New("exactauthority: corrupt key public")
	}
	// Owner proof is mandatory: the embedded proof must be the owner's
	// signature over owner+authority bytes under accesslease.OwnerKeyProofDomain.
	if !verifyOwnerProof(envelope.Proof, owner, authPub) {
		return nil, errors.New("exactauthority: owner proof invalid")
	}
	return &Key{private: priv, owner: owner, secretPath: path}, nil
}

// NewOwnerBindingForShare builds, for trusted local share preparation, the
// immutable owner-signed OwnerBinding binding this key as the single dedicated
// authority key for the exact Pipe owned by this owner. owner is the owner's
// identity secret; it must match the owner this key was provisioned for, else
// it fails closed.
func (k *Key) NewOwnerBindingForShare(owner ed25519.PrivateKey, pipeID string, epoch uint64, peers, protocol string) (accesslease.OwnerBinding, error) {
	if k == nil || k.owner == nil {
		return accesslease.OwnerBinding{}, errors.New("exactauthority: key not provisioned")
	}
	ownerPub := owner.Public().(ed25519.PublicKey)
	if !ownerPub.Equal(k.owner) {
		return accesslease.OwnerBinding{}, errors.New("exactauthority: owner does not match key")
	}
	return accesslease.NewOwnerBinding(owner, k.Public(), pipeID, epoch, peers, protocol)
}

func ownerProof(private ed25519.PrivateKey, ownerPub, authPub ed25519.PublicKey) string {
	return accesslease.SignProof(private, accesslease.OwnerKeyProofDomain, accesslease.PublicString(ownerPub), accesslease.PublicString(authPub))
}

func verifyOwnerProof(proof string, ownerPub, authPub ed25519.PublicKey) bool {
	return accesslease.VerifyProof(ownerPub, proof, accesslease.OwnerKeyProofDomain, accesslease.PublicString(ownerPub), accesslease.PublicString(authPub))
}
