package authority

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/localkey"
)

const (
	MaxTokenSize    = 48 << 10
	AccessLeaseTTL  = 10 * time.Minute
	RefreshLeaseTTL = 48 * time.Hour
)

// PipeEndpoint carries only published pipe routing material. It must never
// carry a local service target.
type PipeEndpoint struct {
	PeerID    string   `json:"peer_id"`
	Addresses []string `json:"addresses"`
	Protocol  string   `json:"protocol"`
}

type Token struct {
	Version          int           `json:"version"`
	Type             string        `json:"type"`
	Issuer           string        `json:"issuer"`
	Subject          string        `json:"subject"`
	Audience         string        `json:"audience,omitempty"`
	Scope            domain.Scope  `json:"scope"`
	Role             string        `json:"role,omitempty"`
	JTI              string        `json:"jti"`
	IssuedAt         time.Time     `json:"issued_at"`
	ExpiresAt        time.Time     `json:"expires_at"`
	AuthorityAddress string        `json:"authority_address,omitempty"`
	NamespaceKey     string        `json:"namespace_key,omitempty"`
	OneTime          bool          `json:"one_time,omitempty"`
	PipeEndpoint     *PipeEndpoint `json:"pipe_endpoint,omitempty"`
	DisplayNameHint  string        `json:"display_name_hint,omitempty"`
	AccessEpoch      uint64        `json:"access_epoch,omitempty"`
	PublishEpoch     uint64        `json:"publish_epoch,omitempty"`
	Signature        string        `json:"signature"`
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value), nil
}

func NewToken(kind string, scope domain.Scope, subject, audience string, lifetime time.Duration) (Token, error) {
	jti, err := randomID("jti_")
	if err != nil {
		return Token{}, err
	}
	now := time.Now().UTC()
	return Token{Version: 2, Type: kind, Scope: scope, Subject: subject, Audience: audience, JTI: jti, IssuedAt: now, ExpiresAt: now.Add(lifetime)}, nil
}

func (t Token) signingBytes() ([]byte, error) {
	t.Signature = ""
	return json.Marshal(t)
}

func (t *Token) Sign(private ed25519.PrivateKey) error {
	public := private.Public().(ed25519.PublicKey)
	t.Issuer = base64.RawURLEncoding.EncodeToString(public)
	message, err := t.signingBytes()
	if err != nil {
		return err
	}
	t.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, message))
	return nil
}

func (t Token) Verify(public ed25519.PublicKey, now time.Time) error {
	if t.Version != 2 {
		return fmt.Errorf("unsupported token version %d", t.Version)
	}
	if len(t.Subject) == 0 || len(t.Subject) > 8192 || len(t.JTI) == 0 || len(t.JTI) > 8192 || len(t.Audience) > 8192 || len(t.DisplayNameHint) > 8192 {
		return errors.New("invalid token metadata")
	}
	if t.PipeEndpoint != nil {
		if t.PipeEndpoint.PeerID == "" || t.PipeEndpoint.Protocol == "" || len(t.PipeEndpoint.PeerID) > 8192 || len(t.PipeEndpoint.Protocol) > 8192 || len(t.PipeEndpoint.Addresses) == 0 || len(t.PipeEndpoint.Addresses) > 64 {
			return errors.New("invalid pipe endpoint")
		}
		for _, address := range t.PipeEndpoint.Addresses {
			if len(address) == 0 || len(address) > 8192 {
				return errors.New("invalid pipe endpoint")
			}
		}
	}
	if t.Type == "share.invite" && (!t.OneTime || t.Audience == "" || t.PipeEndpoint == nil) {
		return errors.New("invalid share invite")
	}
	if t.Type == "connect.refresh" && t.Audience == "" {
		return errors.New("invalid refresh lease")
	}
	if err := t.Scope.Validate(); err != nil {
		return err
	}
	if t.IssuedAt.IsZero() || !t.ExpiresAt.After(t.IssuedAt) {
		return errors.New("invalid token lifetime")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !now.Before(t.ExpiresAt) {
		return errors.New("token expired")
	}
	if t.IssuedAt.After(now.Add(30 * time.Second)) {
		return errors.New("token issued in future")
	}
	wantIssuer := base64.RawURLEncoding.EncodeToString(public)
	if t.Issuer != wantIssuer {
		return errors.New("token issuer does not match pinned authority")
	}
	signature, err := base64.RawURLEncoding.DecodeString(t.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid token signature encoding")
	}
	message, err := t.signingBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(public, message, signature) {
		return errors.New("invalid token signature")
	}
	return nil
}

func Encode(t Token) (string, error) {
	data, err := json.Marshal(t)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	if len(encoded) > MaxTokenSize {
		return "", errors.New("token exceeds 48 KiB encoded field")
	}
	return encoded, nil
}

func Decode(encoded string) (Token, error) {
	if len(encoded) > MaxTokenSize {
		return Token{}, errors.New("token exceeds encoded limit")
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return Token{}, errors.New("invalid token encoding")
	}
	if len(data) > MaxTokenSize {
		return Token{}, errors.New("token exceeds 48 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var value Token
	if err := decoder.Decode(&value); err != nil {
		return Token{}, fmt.Errorf("invalid token: %w", err)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return Token{}, errors.New("trailing token payload")
	}
	return value, nil
}

func PublicString(public ed25519.PublicKey) string {
	return base64.RawURLEncoding.EncodeToString(public)
}

func PublicFromString(value string) (ed25519.PublicKey, error) {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(data), nil
}

func ReadPrivate(path string) (ed25519.PrivateKey, error) {
	return localkey.Load(path)
}

func GeneratePrivate(path string) (ed25519.PrivateKey, error) {
	return localkey.Provision(path)
}

func PipeID(public ed25519.PublicKey) string {
	sum := sha256.Sum256(public)
	return "pip_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:20]))
}

func SessionID() (string, error) { return randomID("ses_") }

func SignProof(private ed25519.PrivateKey, purpose string, parts ...string) string {
	message := purpose + "\x00" + strings.Join(parts, "\x00")
	return base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(message)))
}

func VerifyProof(public ed25519.PublicKey, signature, purpose string, parts ...string) bool {
	value, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return false
	}
	message := purpose + "\x00" + strings.Join(parts, "\x00")
	return ed25519.Verify(public, []byte(message), value)
}
