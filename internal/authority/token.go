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
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

const MaxTokenSize = 48 << 10

type Token struct {
	Version          int          `json:"version"`
	Type             string       `json:"type"`
	Issuer           string       `json:"issuer"`
	Subject          string       `json:"subject"`
	Audience         string       `json:"audience,omitempty"`
	Scope            domain.Scope `json:"scope"`
	Role             string       `json:"role,omitempty"`
	JTI              string       `json:"jti"`
	IssuedAt         time.Time    `json:"issued_at"`
	ExpiresAt        time.Time    `json:"expires_at"`
	AuthorityAddress string       `json:"authority_address,omitempty"`
	NamespaceKey     string       `json:"namespace_key,omitempty"`
	OneTime          bool         `json:"one_time,omitempty"`
	Signature        string       `json:"signature"`
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
	if len(t.Subject) == 0 || len(t.Subject) > 8192 || len(t.JTI) == 0 || len(t.JTI) > 8192 {
		return errors.New("invalid token metadata")
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key file")
	}
	return ed25519.PrivateKey(decoded), nil
}

func GeneratePrivate(path string) (ed25519.PrivateKey, error) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	data := []byte(base64.RawURLEncoding.EncodeToString(private))
	if _, err := file.Write(data); err != nil {
		file.Close()
		os.Remove(path)
		return nil, err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		os.Remove(path)
		return nil, err
	}
	if err := file.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return private, nil
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
