package accesslease

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
)

// OwnerBinding is the immutable, owner-signed Exact Owner Bond Authority
// binding (issue #52). It binds exactly one dedicated Exact Authority key to
// one exact Pipe that the Owner owns, plus bounded neutral redemption peer
// addresses/protocol and an authorization epoch. It never carries a remote
// publisher, member, relay, bootstrap provider, discovery record, endpoint
// target, or any arbitrary metadata map.
//
// Critically, the binding is signed by the OWNER's identity key, not by the
// dedicated authority key. This is what makes the Exact Authority owner-bound
// and owner-local: a remote/ambient key inventing authority for another
// owner's Pipe cannot produce a valid owner signature. The receiver derives
// the trusted issuer by (a) verifying the audience PipeID is the owner's
// self-deriving PipeID, then (b) verifying the owner's signature over the
// binding with exactly one AuthorityKey.
type OwnerBinding struct {
	Version      int    `json:"version"`
	Type         string `json:"type"`
	Owner        string `json:"owner"`
	AuthorityKey string `json:"authority_key"`
	PipeID       string `json:"pipe_id"`
	// RedemptionPeers is a comma-joined, bounded list of neutral redemption
	// peer addresses (deliberately NOT concrete multiaddrs / L1 types). Each is
	// a bounded address string; the whole field is bounded by
	// MaxRedemptionAddrLen.
	RedemptionPeers    string `json:"redemption_peers,omitempty"`
	RedemptionProtocol string `json:"redemption_protocol,omitempty"`
	Epoch              uint64 `json:"epoch"`
	Signature          string `json:"signature"`
}

func ownerBindingAllowed() map[string]bool {
	return map[string]bool{
		"version":             true,
		"type":                true,
		"owner":               true,
		"authority_key":       true,
		"pipe_id":             true,
		"redemption_peers":    true,
		"redemption_protocol": true,
		"epoch":               true,
		"signature":           true,
	}
}

// NewOwnerBinding builds a fresh immutable owner-bond signed by the owner's
// identity key. pipeID must be the owner's self-deriving PipeID (verified by
// VerifyPipeOwner) so the owner cannot bind authority for a foreign Pipe.
// exactly one authorityKey is bound.
func NewOwnerBinding(ownerPrivate ed25519.PrivateKey, authorityKey ed25519.PublicKey, pipeID string, epoch uint64, redemptionPeers, redemptionProtocol string) (OwnerBinding, error) {
	owner := ownerPrivate.Public().(ed25519.PublicKey)
	if len(owner) != ed25519.PublicKeySize {
		return OwnerBinding{}, errors.New("accesslease: invalid owner key")
	}
	if !VerifyPipeOwner(pipeID, owner) {
		return OwnerBinding{}, errors.New("accesslease: owner cannot bind a foreign Pipe")
	}
	if !validString(pipeID) || len(pipeID) > MaxPipeIDLen {
		return OwnerBinding{}, errors.New("accesslease: invalid pipe id")
	}
	if len(authorityKey) != ed25519.PublicKeySize {
		return OwnerBinding{}, errors.New("accesslease: invalid authority key")
	}
	if len(strings.Split(redemptionPeers, ",")) > MaxRedemptionAddresses || len(redemptionPeers) > MaxRedemptionAddrLen {
		return OwnerBinding{}, errors.New("accesslease: redemption peers out of bounds")
	}
	if redemptionProtocol != "" && len(redemptionProtocol) > MaxRedemptionProtocolLen {
		return OwnerBinding{}, errors.New("accesslease: redemption protocol out of bounds")
	}
	binding := OwnerBinding{
		Version:            Version,
		Type:               OwnerBindingType,
		Owner:              PublicString(owner),
		AuthorityKey:       PublicString(authorityKey),
		PipeID:             pipeID,
		RedemptionPeers:    redemptionPeers,
		RedemptionProtocol: redemptionProtocol,
		Epoch:              epoch,
	}
	if err := binding.Sign(ownerPrivate); err != nil {
		return OwnerBinding{}, err
	}
	return binding, nil
}

func (b OwnerBinding) signingBody() ([]byte, error) {
	body := b
	body.Signature = ""
	canonical, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return domainBytes(OwnerBindingSigningDomain, canonical), nil
}

// Sign stamps Owner with the owner key and appends the owner's Ed25519
// signature over the canonical owner-binding domain bytes.
func (b *OwnerBinding) Sign(owner ed25519.PrivateKey) error {
	public := owner.Public().(ed25519.PublicKey)
	b.Owner = PublicString(public)
	canonical, err := b.signingBody()
	if err != nil {
		return err
	}
	b.Signature = PublicString(ed25519.Sign(owner, canonical))
	return nil
}

// VerifyOwnerBinding validates the owner signature over OwnerBindingSigningDomain
// and returns the owner public key the binding was signed by. It also enforces
// bounds and the fixed kind/version. It does NOT by itself trust the owner for a
// Pipe; callers combine it with VerifyPipeOwner/VerifyPipeAuthority.
func (b OwnerBinding) VerifyOwnerBinding() (ed25519.PublicKey, error) {
	if b.Version != Version || b.Type != OwnerBindingType {
		return nil, errors.New("accesslease: wrong binding kind or version")
	}
	if !validString(b.Owner) || !validString(b.AuthorityKey) || !validString(b.PipeID) || len(b.PipeID) > MaxPipeIDLen {
		return nil, errors.New("accesslease: invalid binding identity or metadata")
	}
	if len(b.RedemptionPeers) > MaxRedemptionAddrLen {
		return nil, errors.New("accesslease: redemption peers out of bounds")
	}
	if b.RedemptionPeers != "" && len(strings.Split(b.RedemptionPeers, ",")) > MaxRedemptionAddresses {
		return nil, errors.New("accesslease: too many redemption peers")
	}
	if b.RedemptionProtocol != "" && len(b.RedemptionProtocol) > MaxRedemptionProtocolLen {
		return nil, errors.New("accesslease: redemption protocol out of bounds")
	}
	ownerPub, err := PublicFromString(b.Owner)
	if err != nil {
		return nil, err
	}
	authorityPub, err := PublicFromString(b.AuthorityKey)
	if err != nil {
		return nil, err
	}
	if len(authorityPub) != ed25519.PublicKeySize {
		return nil, errors.New("accesslease: invalid authority key")
	}
	signature, err := decodeSignature(b.Signature)
	if err != nil {
		return nil, err
	}
	canonical, err := b.signingBody()
	if err != nil {
		return nil, err
	}
	if !ed25519.Verify(ownerPub, canonical, signature) {
		return nil, errors.New("accesslease: invalid owner binding signature")
	}
	return ownerPub, nil
}

// Owner is the owning participant identity verified against the self-deriving
// PipeID. AuthorityKey is the single dedicated Exact Authority key the owner
// bound. This is the derived owner-authority trust fact step 6B2 later
// consumes before any remote Pipe open.
type OwnerAuthority struct {
	Owner        string
	AuthorityKey string
	PipeID       string
	Epoch        uint64
}

// VerifyOwnerAuthority validates that (a) the PipeID is the owner's
// self-deriving identity, (b) the binding is produced by that owner, and (c)
// the binding binds exactly the given authority key to that exact Pipe. It
// fails closed on any mismatch. This is the pure contract boundary that keeps
// ownership local: no ambient key can stand in for the binding.
func (b OwnerBinding) VerifyOwnerAuthority(authorityKey string) (*OwnerAuthority, error) {
	ownerPub, err := b.VerifyOwnerBinding()
	if err != nil {
		return nil, err
	}
	if !VerifyPipeOwner(b.PipeID, ownerPub) {
		return nil, errors.New("accesslease: binding owner is not the Pipe owner")
	}
	if authorityKey != "" && b.AuthorityKey != authorityKey {
		return nil, errors.New("accesslease: authority key does not match binding")
	}
	return &OwnerAuthority{
		Owner:        b.Owner,
		AuthorityKey: b.AuthorityKey,
		PipeID:       b.PipeID,
		Epoch:        b.Epoch,
	}, nil
}

// EncodeOwnerBinding serializes the owner bond to a bounded canonical base64url
// string.
func EncodeOwnerBinding(b OwnerBinding) (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > MaxBytes {
		return "", errors.New("accesslease: owner binding exceeds bounded field")
	}
	return encoded, nil
}

// DecodeOwnerBinding strictly decodes a bounded base64url owner bond.
func DecodeOwnerBinding(encoded string) (OwnerBinding, error) {
	data, err := decodeStrictJSON(encoded, ownerBindingAllowed())
	if err != nil {
		return OwnerBinding{}, err
	}
	var binding OwnerBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		return OwnerBinding{}, errStrict
	}
	return binding, nil
}
