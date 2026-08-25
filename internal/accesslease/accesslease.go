package accesslease

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// AccessLease is the canonical, Scope-independent exact-Pipe Access Lease
// (issue #49/#52, step 6B1 contract foundation). It is the remote-open
// authorization credential eventually evaluated on the L2 receiving boundary
// through the neutral openauth.OpenAuthorizer contract (wiring is step 6B2,
// out of scope here).
//
// Invariants, enforced at signing and at verify:
//   - fixed version=1 and fixed type=pipe.open.access;
//   - issuer is the dedicated Exact Authority public key string;
//   - subject is the recipient identity public key string;
//   - audience_pipe_id is the exact Pipe the lease authorizes opening;
//   - jti is unique, non-empty, jti_-prefixed and length-bounded;
//   - issued_at/not_before/expires_at form a short-lived bounded window;
//   - access_epoch is the issuing authority's observed authorization epoch;
//   - signature is an Ed25519 signature by the authority key over the canonical
//     bytes with the signature field omitted, prefixed by AccessLeaseSigningDomain.
//
// The lease deliberately carries no Scope, PipeEndpoint, service target, relay
// route/address, discovery metadata, namespace, bootstrap key, or arbitrary
// metadata map.
type AccessLease struct {
	Version        int       `json:"version"`
	Type           string    `json:"type"`
	Issuer         string    `json:"issuer"`
	Subject        string    `json:"subject"`
	AudiencePipeID string    `json:"audience_pipe_id"`
	JTI            string    `json:"jti"`
	IssuedAt       time.Time `json:"issued_at"`
	NotBefore      time.Time `json:"not_before"`
	ExpiresAt      time.Time `json:"expires_at"`
	AccessEpoch    uint64    `json:"access_epoch"`
	Signature      string    `json:"signature"`
}

func leaseAllowed() map[string]bool {
	return map[string]bool{
		"version":          true,
		"type":             true,
		"issuer":           true,
		"subject":          true,
		"audience_pipe_id": true,
		"jti":              true,
		"issued_at":        true,
		"not_before":       true,
		"expires_at":       true,
		"access_epoch":     true,
		"signature":        true,
	}
}

// NewAccessLease builds a fresh exact-Pipe Access Lease for subject and
// pipe the issuer controls. lifetime must be positive and bounded by
// MaxTTL; Now is forced to UTC.
func NewAccessLease(private ed25519.PrivateKey, subject, pipeID string, now time.Time, lifetime time.Duration) (AccessLease, error) {
	if !validString(subject) || !validString(pipeID) || len(pipeID) > MaxPipeIDLen {
		return AccessLease{}, errors.New("accesslease: invalid subject or pipe")
	}
	if lifetime <= 0 || lifetime > MaxTTL {
		return AccessLease{}, errors.New("accesslease: lifetime outside allowed window")
	}
	jti, err := NewJTI()
	if err != nil {
		return AccessLease{}, err
	}
	now = now.UTC()
	lease := AccessLease{Version: Version, Type: AccessLeaseType, Subject: subject, AudiencePipeID: pipeID, JTI: jti, IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(lifetime)}
	if err := lease.Sign(private); err != nil {
		return AccessLease{}, err
	}
	return lease, nil
}

func (l AccessLease) signingBody() ([]byte, error) {
	body := l
	body.Signature = ""
	canonical, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return domainBytes(AccessLeaseSigningDomain, canonical), nil
}

// Sign stamps Issuer with the authority key and appends the Ed25519 signature
// over the canonical access-lease domain bytes with the signature omitted.
func (l *AccessLease) Sign(private ed25519.PrivateKey) error {
	public := private.Public().(ed25519.PublicKey)
	l.Issuer = PublicString(public)
	canonical, err := l.signingBody()
	if err != nil {
		return err
	}
	l.Signature = PublicString(ed25519.Sign(private, canonical))
	return nil
}

// Verify validates bounded fields, the short-lived time window, the subject and
// exact-Pipe audience bindings, and the Ed25519 signature of the pinned
// authority key. Signature is verified over AccessLeaseSigningDomain, so
// bytes signed for another credential kind or version fail here.
func (l AccessLease) Verify(public ed25519.PublicKey, subject, pipeID string, now time.Time) error {
	if l.Version != Version || l.Type != AccessLeaseType {
		return errors.New("accesslease: wrong kind or version")
	}
	wantIssuer := PublicString(public)
	if l.Issuer != wantIssuer || !validString(l.Issuer) || !validString(l.Subject) || !validString(l.AudiencePipeID) || !validString(l.JTI) || !strings.HasPrefix(l.JTI, "jti_") {
		return errors.New("accesslease: invalid identity or metadata")
	}
	if len(l.AudiencePipeID) > MaxPipeIDLen {
		return errors.New("accesslease: pipe audience too long")
	}
	if l.IssuedAt.IsZero() || l.NotBefore.IsZero() || !l.ExpiresAt.After(l.IssuedAt) {
		return errors.New("accesslease: invalid lifetime")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	if l.IssuedAt.After(now.Add(MaxClockSkew)) {
		return errors.New("accesslease: issued too far in future")
	}
	if l.NotBefore.After(now) {
		return errors.New("accesslease: not yet valid")
	}
	if !l.ExpiresAt.After(now) {
		return errors.New("accesslease: expired")
	}
	if l.ExpiresAt.Sub(l.IssuedAt) > MaxTTL {
		return errors.New("accesslease: max TTL exceeded")
	}
	if subject != "" && l.Subject != subject {
		return errors.New("accesslease: subject mismatch")
	}
	if pipeID != "" && l.AudiencePipeID != pipeID {
		return errors.New("accesslease: pipe audience mismatch")
	}
	signature, err := decodeSignature(l.Signature)
	if err != nil {
		return err
	}
	canonical, err := l.signingBody()
	if err != nil {
		return err
	}
	if !ed25519.Verify(public, canonical, signature) {
		return errors.New("accesslease: invalid signature")
	}
	return nil
}

// Encode serializes the lease to a bounded canonical base64url form.
func Encode(l AccessLease) (string, error) {
	data, err := json.Marshal(l)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > MaxBytes {
		return "", errors.New("accesslease: exceeds bounded field")
	}
	return encoded, nil
}

// Decode strictly decodes a bounded base64url lease (flat object, allow-listed
// members, no duplicate or nested or trailing bytes) and returns the value.
func Decode(encoded string) (AccessLease, error) {
	data, err := decodeStrictJSON(encoded, leaseAllowed())
	if err != nil {
		return AccessLease{}, err
	}
	var lease AccessLease
	if err := json.Unmarshal(data, &lease); err != nil {
		return AccessLease{}, errStrict
	}
	return lease, nil
}
