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

func leaseAllowed() map[string]int {
	return map[string]int{
		// audience_pipe_id is the one member with a tighter string bound than
		// the shared MaxFieldLen default.
		"audience_pipe_id": MaxPipeIDLen,
		"version":          MaxFieldLen,
		"type":             MaxFieldLen,
		"issuer":           MaxFieldLen,
		"subject":          MaxFieldLen,
		"jti":              MaxFieldLen,
		"issued_at":        MaxFieldLen,
		"not_before":       MaxFieldLen,
		"expires_at":       MaxFieldLen,
		"access_epoch":     MaxFieldLen,
		"signature":        MaxFieldLen,
	}
}

// accessLeaseSigning is the canonical signature-less wire form used for
// signing and verification. It deliberately declares no Signature member, so
// the signed bytes are exactly the body the protocol document specifies
// (signature field omitted) rather than a `"signature":""` placeholder.
type accessLeaseSigning struct {
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
}

// NewAccessLease builds a fresh exact-Pipe Access Lease for subject and
// pipe the issuer controls. subject MUST be the canonical base64url Ed25519
// public key of the recipient; arbitrary strings are rejected. lifetime must
// be positive and bounded by MaxTTL; accessEpoch is the issuing authority's
// observed pipe-access authorization epoch for this exact Pipe; Now is forced
// to UTC.
func NewAccessLease(private ed25519.PrivateKey, subject, pipeID string, now time.Time, lifetime time.Duration, accessEpoch uint64) (AccessLease, error) {
	if !validString(subject) || !validString(pipeID) || len(pipeID) > MaxPipeIDLen {
		return AccessLease{}, errors.New("accesslease: invalid subject or pipe")
	}
	if _, err := PublicFromString(subject); err != nil {
		return AccessLease{}, errors.New("accesslease: subject is not a canonical Ed25519 public key")
	}
	if lifetime <= 0 || lifetime > MaxTTL {
		return AccessLease{}, errors.New("accesslease: lifetime outside allowed window")
	}
	jti, err := NewJTI()
	if err != nil {
		return AccessLease{}, err
	}
	now = now.UTC()
	lease := AccessLease{Version: Version, Type: AccessLeaseType, Subject: subject, AudiencePipeID: pipeID, JTI: jti, IssuedAt: now, NotBefore: now, ExpiresAt: now.Add(lifetime), AccessEpoch: accessEpoch}
	if err := lease.Sign(private); err != nil {
		return AccessLease{}, err
	}
	return lease, nil
}

func (l AccessLease) signingBody() ([]byte, error) {
	body := accessLeaseSigning{
		Version:        l.Version,
		Type:           l.Type,
		Issuer:         l.Issuer,
		Subject:        l.Subject,
		AudiencePipeID: l.AudiencePipeID,
		JTI:            l.JTI,
		IssuedAt:       l.IssuedAt,
		NotBefore:      l.NotBefore,
		ExpiresAt:      l.ExpiresAt,
		AccessEpoch:    l.AccessEpoch,
	}
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
	// Subject must be a canonical Ed25519 public key, matching how it is
	// minted; arbitrary strings are denied.
	if _, err := PublicFromString(l.Subject); err != nil {
		return errors.New("accesslease: subject is not a canonical Ed25519 public key")
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
	// NotBefore gets the SAME bounded clock-skew tolerance as IssuedAt:
	// a mildly ahead issuer (≤ MaxClockSkew) that can pass the IssuedAt
	// guard must also pass this one; otherwise one check accepts the
	// issuer's clock and the next rejects the same credential. ExpiresAt
	// remains STRICT: skew never extends validity past expiry.
	if l.NotBefore.After(now.Add(MaxClockSkew)) {
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
