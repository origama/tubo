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
)

// ErrMissing is returned by read-only load when no key material exists; it
// fails closed rather than silently creating anything.
var ErrMissing = errors.New("exactauthority: no owner authority key material")

// Key is a provisioned, owner-bound dedicated Exact Authority key. The private
// key lives in a single deterministic local file under the owner's secret
// path. OwnerProof binds the key to the owner's self-certifying Pipe identity.
type Key struct {
	private    ed25519.PrivateKey
	owner      ed25519.PublicKey
	secretPath string
}

// Public returns the derived dedicated Exact Authority public key.
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
// Ed25519 signature over accesslease.OwnerKeyProofDomain of the canonical
// owner + authority-key bytes, proving the owner produced this dedicated key.
type keyMetadata struct {
	Format          string `json:"format"`
	Version         int    `json:"version"`
	Owner           string `json:"owner"`
	AuthoritySecret string `json:"authority_secret"`
	AuthorityPublic string `json:"authority_public"`
	Proof           string `json:"proof"`
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

// Provision creates the dedicated authority key owned by owner at the
// deterministic secret path, or loads the pre-existing one. It is the "create
// once / load existing once" primitive: the path depends only on the owner, so
// a second Provision for the same owner loads the existing key rather than
// over-writing it. owner is the Pipe owner's identity secret used to author
// the mandatory owner proof. If key material already exists under the path it
// is loaded and verified against the provided owner identity.
func Provision(dir string, owner ed25519.PrivateKey) (*Key, error) {
	if owner == nil || len(owner) != ed25519.PrivateKeySize {
		return nil, errors.New("exactauthority: owner identity required")
	}
	ownerPub := owner.Public().(ed25519.PublicKey)
	path := secretPath(dir, ownerPub)
	if _, err := os.Stat(path); err == nil {
		return Load(dir, ownerPub)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("exactauthority: stat secret: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), secretDirMode); err != nil {
		return nil, fmt.Errorf("exactauthority: mkdir: %w", err)
	}
	_ = os.Chmod(filepath.Dir(path), secretDirMode)
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
	if err := os.WriteFile(path, payload, secretFileMode); err != nil {
		return nil, fmt.Errorf("exactauthority: write secret: %w", err)
	}
	_ = os.Chmod(path, secretFileMode)
	return &Key{private: priv, owner: ownerPub, secretPath: path}, nil
}

// Load read-only loads the existing, owner-bound key. It creates no filesystem
// entry: missing material returns ErrMissing and corrupt or owner-mismatched
// material fails closed.
func Load(dir string, owner ed25519.PublicKey) (*Key, error) {
	if owner == nil || len(owner) != ed25519.PublicKeySize {
		return nil, errors.New("exactauthority: owner identity required")
	}
	path := secretPath(dir, owner)
	buf, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrMissing
		}
		return nil, fmt.Errorf("exactauthority: read secret: %w", err)
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
	// signature over owner+auth bytes under accesslease.OwnerKeyProofDomain.
	// Without it the material is not trusted for local share preparation.
	if !verifyOwnerProof(envelope.Proof, owner, authPub) {
		return nil, errors.New("exactauthority: owner proof invalid")
	}
	return &Key{private: priv, owner: owner, secretPath: path}, nil
}

// NewOwnerBindingForShare builds, for trusted local share preparation, the
// immutable owner-signed OwnerBinding binding this key as the single dedicated
// Exact Authority key for the exact Pipe owned by this owner. owner is the
// owner's identity secret; it must match the owner this key was provisioned
// for, else it fails closed.
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
