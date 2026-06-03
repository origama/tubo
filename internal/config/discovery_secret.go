package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const (
	SecretTypeNamespaceDiscovery   = "namespace-discovery"
	NamespaceDiscoverySecretLength = 32
)

type ManagedSecretRef struct {
	Type      string    `yaml:"type,omitempty" json:"type,omitempty"`
	KeyID     string    `yaml:"key_id,omitempty" json:"key_id,omitempty"`
	File      string    `yaml:"file,omitempty" json:"file,omitempty"`
	CreatedAt time.Time `yaml:"created_at,omitempty" json:"created_at,omitempty"`
	ExpiresAt time.Time `yaml:"expires_at,omitempty" json:"expires_at,omitempty"`
}

func GenerateSecretBytes(length int) ([]byte, error) {
	if length <= 0 {
		return nil, fmt.Errorf("invalid secret length %d", length)
	}
	buf := make([]byte, length)
	if _, err := io.ReadFull(rand.Reader, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

func GenerateSecretKeyID(prefix string, now time.Time) (string, error) {
	if strings.TrimSpace(prefix) == "" {
		return "", fmt.Errorf("key id prefix is required")
	}
	randSuffix := make([]byte, 4)
	if _, err := io.ReadFull(rand.Reader, randSuffix); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s_%s_%s", prefix, now.UTC().Format("20060102T150405Z"), hex.EncodeToString(randSuffix)), nil
}

func SecretFingerprint(secret []byte) string {
	sum := sha256.Sum256(secret)
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func ReadNamespaceDiscoverySecretFile(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("secret file path is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) != NamespaceDiscoverySecretLength {
		return nil, fmt.Errorf("namespace discovery secret must be %d bytes, got %d", NamespaceDiscoverySecretLength, len(data))
	}
	return data, nil
}

func NamespaceDiscoverySecretFingerprint(ref *ManagedSecretRef) (string, error) {
	if ref == nil {
		return "", fmt.Errorf("secret ref is required")
	}
	secret, err := ReadNamespaceDiscoverySecretFile(ref.File)
	if err != nil {
		return "", err
	}
	return SecretFingerprint(secret), nil
}

func BuildNamespaceDiscoverySecretRef(path string, now time.Time) ([]byte, *ManagedSecretRef, error) {
	secret, err := GenerateSecretBytes(NamespaceDiscoverySecretLength)
	if err != nil {
		return nil, nil, err
	}
	keyID, err := GenerateSecretKeyID("nsdk", now.UTC())
	if err != nil {
		return nil, nil, err
	}
	return BuildNamespaceDiscoverySecretRefFromBytes(path, secret, keyID, now, time.Time{})
}

func BuildNamespaceDiscoverySecretRefFromBytes(path string, secret []byte, keyID string, createdAt, expiresAt time.Time) ([]byte, *ManagedSecretRef, error) {
	if len(secret) != NamespaceDiscoverySecretLength {
		return nil, nil, fmt.Errorf("namespace discovery secret must be %d bytes", NamespaceDiscoverySecretLength)
	}
	if strings.TrimSpace(keyID) == "" {
		return nil, nil, fmt.Errorf("key id is required")
	}
	ref := &ManagedSecretRef{
		Type:      SecretTypeNamespaceDiscovery,
		KeyID:     strings.TrimSpace(keyID),
		File:      strings.TrimSpace(path),
		CreatedAt: createdAt.UTC(),
		ExpiresAt: expiresAt.UTC(),
	}
	if createdAt.IsZero() {
		ref.CreatedAt = time.Time{}
	}
	if expiresAt.IsZero() {
		ref.ExpiresAt = time.Time{}
	}
	return append([]byte(nil), secret...), ref, nil
}
