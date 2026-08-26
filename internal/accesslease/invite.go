package accesslease

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ExactInvite is the redesigned, recipient-unbound, scope-independent exact-Pipe
// redemption credential (issue #49/#52). It is NEVER an Access Lease: it is the
// bootstrap credential a recipient redeems to obtain a short-lived
// AccessLease. The recipient is bound only by future redemption/runtime wiring
// (step 6B2, out of scope here) — the value itself carries no Subject.
//
// The invite composes the immutable OwnerBinding ("binding"): the owner-signed
// bonded exact Authority for the exact Pipe is embedded as a bounded base64url
// reference. Verifying an invite therefore proves the dedicated Authority key
// (invite.Issuer) is exactly the single key the Pipe owner signed for that
// exact Pipe — a remote/ambient key cannot mint a redeemable invite for
// another owner's Pipe.
type ExactInvite struct {
	Version        int       `json:"version"`
	Type           string    `json:"type"`
	Issuer         string    `json:"issuer"`
	AudiencePipeID string    `json:"audience_pipe_id"`
	Binding        string    `json:"binding"`
	JTI            string    `json:"jti"`
	IssuedAt       time.Time `json:"issued_at"`
	NotBefore      time.Time `json:"not_before"`
	ExpiresAt      time.Time `json:"expires_at"`
	AccessEpoch    uint64    `json:"access_epoch"`
	OneTime        bool      `json:"one_time"`
	Signature      string    `json:"signature"`
}

func inviteAllowed() map[string]int {
	return map[string]int{
		"audience_pipe_id": MaxPipeIDLen,
		"binding":          MaxBytes,
		"version":          MaxFieldLen,
		"type":             MaxFieldLen,
		"issuer":           MaxFieldLen,
		"jti":              MaxFieldLen,
		"issued_at":        MaxFieldLen,
		"not_before":       MaxFieldLen,
		"expires_at":       MaxFieldLen,
		"access_epoch":     MaxFieldLen,
		"one_time":         MaxFieldLen,
		"signature":        MaxFieldLen,
	}
}

// inviteSigning is the canonical signature-less wire form used for signing
// and verification: the signature member is omitted entirely rather than
// emitted as an empty string placeholder.
type inviteSigning struct {
	Version        int       `json:"version"`
	Type           string    `json:"type"`
	Issuer         string    `json:"issuer"`
	AudiencePipeID string    `json:"audience_pipe_id"`
	Binding        string    `json:"binding"`
	JTI            string    `json:"jti"`
	IssuedAt       time.Time `json:"issued_at"`
	NotBefore      time.Time `json:"not_before"`
	ExpiresAt      time.Time `json:"expires_at"`
	AccessEpoch    uint64    `json:"access_epoch"`
	OneTime        bool      `json:"one_time"`
}

// NewExactInvite builds a fresh one-time exact redemption credential for the
// exact Pipe audience, composing the given owner-bonded binding. The invite
// issuer must be the same dedicated Authority key the owner bound, and the
// binding must be the owner's binding for the exact Pipe. lifetime must be
// short (bounded by MaxTTL). The invite is recipient-UNBOUND by design.
func NewExactInvite(private ed25519.PrivateKey, bindingOwnerBinding OwnerBinding, pipeID string, now time.Time, lifetime time.Duration) (ExactInvite, error) {
	authority := private.Public().(ed25519.PublicKey)
	bound, err := bindingOwnerBinding.VerifyOwnerAuthority(PublicString(authority))
	if err != nil {
		return ExactInvite{}, err
	}
	if !validString(pipeID) || len(pipeID) > MaxPipeIDLen || bound.PipeID != pipeID {
		return ExactInvite{}, errors.New("accesslease: binding pipe mismatch")
	}
	bindingEncoded, err := EncodeOwnerBinding(bindingOwnerBinding)
	if err != nil {
		return ExactInvite{}, err
	}
	if lifetime <= 0 || lifetime > MaxTTL {
		return ExactInvite{}, errors.New("accesslease: invite lifetime outside allowed window")
	}
	jti, err := NewJTI()
	if err != nil {
		return ExactInvite{}, err
	}
	now = now.UTC()
	invite := ExactInvite{Version: Version, Type: InviteType, AudiencePipeID: pipeID, Binding: bindingEncoded, JTI: jti, IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(lifetime), OneTime: true}
	if err := invite.Sign(private); err != nil {
		return ExactInvite{}, err
	}
	return invite, nil
}

func (i ExactInvite) signingBody() ([]byte, error) {
	body := inviteSigning{
		Version:        i.Version,
		Type:           i.Type,
		Issuer:         i.Issuer,
		AudiencePipeID: i.AudiencePipeID,
		Binding:        i.Binding,
		JTI:            i.JTI,
		IssuedAt:       i.IssuedAt,
		NotBefore:      i.NotBefore,
		ExpiresAt:      i.ExpiresAt,
		AccessEpoch:    i.AccessEpoch,
		OneTime:        i.OneTime,
	}
	canonical, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return domainBytes(ExactInviteSigningDomain, canonical), nil
}

// Sign stamps Issuer and appends the dedicated Exact Authority signature over
// the canonical exact-invite domain bytes.
func (i *ExactInvite) Sign(private ed25519.PrivateKey) error {
	public := private.Public().(ed25519.PublicKey)
	i.Issuer = PublicString(public)
	canonical, err := i.signingBody()
	if err != nil {
		return err
	}
	i.Signature = PublicString(ed25519.Sign(private, canonical))
	return nil
}

// Verify validates bounded invariant, short TTL, one-time nature, the invite
// signature by the dedicated Exact key over ExactInviteSigningDomain, and the
// composed owner-signed Binding proving that key is the owner's bound Exact
// Authority for the exact Pipe. pipeID may be empty to skip the audience
// re-check (callers usually pin it).
func (i ExactInvite) Verify(now time.Time, pipeID string) error {
	if i.Version != Version || i.Type != InviteType {
		return errors.New("accesslease: wrong invite kind or version")
	}
	if !validString(i.Issuer) || !validString(i.AudiencePipeID) || !validString(i.JTI) || !strings.HasPrefix(i.JTI, "jti_") {
		return errors.New("accesslease: invalid invite identity or metadata")
	}
	if len(i.AudiencePipeID) > MaxPipeIDLen {
		return errors.New("accesslease: invite pipe audience too long")
	}
	if i.Binding == "" || len(i.Binding) > MaxBytes {
		return errors.New("accesslease: invite missing owner binding")
	}
	if !i.OneTime {
		return errors.New("accesslease: invite must be one-time")
	}
	if i.IssuedAt.IsZero() || i.NotBefore.IsZero() || !i.ExpiresAt.After(i.IssuedAt) {
		return errors.New("accesslease: invalid invite lifetime")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	if i.IssuedAt.After(now.Add(MaxClockSkew)) {
		return errors.New("accesslease: invite issued too far in future")
	}
	if i.NotBefore.After(now) {
		return errors.New("accesslease: invite not yet valid")
	}
	if !i.ExpiresAt.After(now) {
		return errors.New("accesslease: invite expired")
	}
	if i.ExpiresAt.Sub(i.IssuedAt) > MaxTTL {
		return errors.New("accesslease: invite max TTL exceeded")
	}
	if pipeID != "" && i.AudiencePipeID != pipeID {
		return errors.New("accesslease: invite pipe audience mismatch")
	}
	// Compose the owner bond: the embedded binding must be (a) signed by the
	// exact-Pipe owner and (b) bound to the same dedicated Exact key that
	// signed this invite.
	binding, err := DecodeOwnerBinding(i.Binding)
	if err != nil {
		return errors.New("accesslease: invite has invalid owner binding")
	}
	ownerPub, err := binding.VerifyOwnerBinding()
	if err != nil {
		return errors.New("accesslease: invite owner binding invalid")
	}
	if !VerifyPipeOwner(i.AudiencePipeID, ownerPub) {
		return errors.New("accesslease: invite owner is not the Pipe owner")
	}
	if binding.AuthorityKey != i.Issuer {
		return errors.New("accesslease: invite issuer not bound by owner")
	}
	if binding.PipeID != i.AudiencePipeID {
		return errors.New("accesslease: invite binding pipe mismatch")
	}
	signature, err := decodeSignature(i.Signature)
	if err != nil {
		return err
	}
	canonical, err := i.signingBody()
	if err != nil {
		return err
	}
	issuer, err := PublicFromString(i.Issuer)
	if err != nil {
		return err
	}
	if !ed25519.Verify(issuer, canonical, signature) {
		return errors.New("accesslease: invalid invite signature")
	}
	return nil
}

// EncodeInvite serializes the exact invite to a bounded canonical base64url form.
func EncodeInvite(i ExactInvite) (string, error) {
	data, err := json.Marshal(i)
	if err != nil {
		return "", err
	}
	encoded := base64RawURLEncode(data)
	if len(encoded) > MaxBytes {
		return "", errors.New("accesslease: invite exceeds bounded field")
	}
	return encoded, nil
}

// DecodeInvite strictly decodes a bounded base64url exact invite.
func DecodeInvite(encoded string) (ExactInvite, error) {
	data, err := decodeStrictJSON(encoded, inviteAllowed())
	if err != nil {
		return ExactInvite{}, err
	}
	var invite ExactInvite
	if err := json.Unmarshal(data, &invite); err != nil {
		return ExactInvite{}, errStrict
	}
	return invite, nil
}
