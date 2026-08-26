// Package exactauthority owns the owner-local Exact Pipe Authority key lifecycle
// for A0 step 6B1b. It is L3-local key material plus owner binding only: no
// remote-open runtime wiring, no Scope policy, no reachability, and no Store.
package exactauthority

import (
	"crypto/ed25519"
	"errors"
	"path/filepath"
	"strings"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/localkey"
)

// Authority is the loaded owner-local Exact Authority key plus the verified
// owner binding that proves this dedicated key belongs to exactly one Pipe.
type Authority struct {
	Private ed25519.PrivateKey
	Binding accesslease.OwnerBinding
	Owner   *accesslease.OwnerAuthority
}

// KeyPath returns the deterministic owner-local secret path for the dedicated
// Exact Authority key for pipeID under a Tubo secrets directory. It creates no
// filesystem entries and does not validate or load the key.
func KeyPath(secretsDir, pipeID string) (string, error) {
	if secretsDir == "" || !validPipeIDComponent(pipeID) {
		return "", errors.New("exactauthority: invalid key path")
	}
	return filepath.Join(secretsDir, "exact-authorities", pipeID+".key"), nil
}

// LocalOwnerWorkflow is the trusted local owner-side provisioning capability.
// Remote redemption paths must not receive this value; they use Loader below.
type LocalOwnerWorkflow struct {
	KeyPath            string
	PipeID             string
	Epoch              uint64
	RedemptionPeers    string
	RedemptionProtocol string
}

// Provision creates-or-loads the owner-local dedicated Exact Authority private
// key through the shared localkey lifecycle, then returns an owner-signed
// binding for this exact Pipe. The owner private key is required only in this
// trusted local workflow.
func (w LocalOwnerWorkflow) Provision(ownerPrivate ed25519.PrivateKey) (Authority, error) {
	ownerPublic := ownerPrivate.Public().(ed25519.PublicKey)
	if !accesslease.VerifyPipeOwner(w.PipeID, ownerPublic) {
		return Authority{}, errors.New("exactauthority: owner cannot provision authority for foreign Pipe")
	}
	private, err := localkey.Provision(w.KeyPath)
	if err != nil {
		return Authority{}, err
	}
	binding, err := accesslease.NewOwnerBinding(ownerPrivate, private.Public().(ed25519.PublicKey), w.PipeID, w.Epoch, w.RedemptionPeers, w.RedemptionProtocol)
	if err != nil {
		return Authority{}, err
	}
	return fromBinding(private, binding)
}

// Loader is the read-only Exact Authority key lifecycle surface intended for
// future remote redemption serving. It can load and verify an already-provisioned
// local key against an owner-signed binding; it has no provisioning method and
// requires no owner private key.
type Loader struct {
	KeyPath string
	Binding accesslease.OwnerBinding
}

// Load loads an existing dedicated Exact Authority key and verifies that its
// public key matches the owner-signed binding. Missing, unsafe, corrupt, or
// mismatched local state fails closed and creates no filesystem entries.
func (l Loader) Load() (Authority, error) {
	private, err := localkey.Load(l.KeyPath)
	if err != nil {
		return Authority{}, err
	}
	return fromBinding(private, l.Binding)
}

func fromBinding(private ed25519.PrivateKey, binding accesslease.OwnerBinding) (Authority, error) {
	if len(private) != ed25519.PrivateKeySize {
		return Authority{}, errors.New("exactauthority: invalid private key")
	}
	authorityKey := accesslease.PublicString(private.Public().(ed25519.PublicKey))
	owner, err := binding.VerifyOwnerAuthority(authorityKey)
	if err != nil {
		return Authority{}, err
	}
	if owner.AuthorityKey == owner.Owner {
		return Authority{}, errors.New("exactauthority: authority key must be dedicated and distinct from owner")
	}
	return Authority{Private: private, Binding: binding, Owner: owner}, nil
}

func validPipeIDComponent(pipeID string) bool {
	return strings.HasPrefix(pipeID, "pip_") && !strings.ContainsAny(pipeID, `/\\`) && len(pipeID) <= accesslease.MaxPipeIDLen
}
