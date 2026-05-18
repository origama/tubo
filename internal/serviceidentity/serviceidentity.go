package serviceidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var serviceIDPattern = regexp.MustCompile(`^service-[a-f0-9]{16,64}$`)

type Identity struct {
	PrivateKey ed25519.PrivateKey
	PublicKey  ed25519.PublicKey
	ServiceID  string
}

func Generate() (Identity, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return Identity{}, err
	}
	return Identity{PrivateKey: priv, PublicKey: pub, ServiceID: ServiceIDFromPublicKey(pub)}, nil
}

func Ensure(path string) (Identity, bool, error) {
	if path == "" {
		return Identity{}, false, errors.New("service owner key path is required")
	}
	if _, err := os.Stat(path); err == nil {
		id, _, err := Load(path)
		return id, false, err
	} else if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, false, err
	}
	identity, err := Generate()
	if err != nil {
		return Identity{}, false, err
	}
	if err := Save(path, identity.PrivateKey); err != nil {
		return Identity{}, false, err
	}
	return identity, true, nil
}

func Load(path string) (Identity, bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Identity{}, false, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "PRIVATE KEY" {
		return Identity{}, false, fmt.Errorf("decode service owner key: invalid pem")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return Identity{}, false, err
	}
	priv, ok := keyAny.(ed25519.PrivateKey)
	if !ok {
		return Identity{}, false, fmt.Errorf("service owner key is not ed25519")
	}
	pubAny := priv.Public()
	pub, ok := pubAny.(ed25519.PublicKey)
	if !ok {
		return Identity{}, false, fmt.Errorf("service owner public key is not ed25519")
	}
	return Identity{PrivateKey: priv, PublicKey: pub, ServiceID: ServiceIDFromPublicKey(pub)}, false, nil
}

func Save(path string, priv ed25519.PrivateKey) error {
	if path == "" {
		return errors.New("service owner key path is required")
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: encoded}); err != nil {
		return err
	}
	return nil
}

func ServiceIDFromPublicKey(pub ed25519.PublicKey) string {
	sum := sha256.Sum256(pub)
	return "service-" + hex.EncodeToString(sum[:])
}

func ValidateServiceID(serviceID string) error {
	serviceID = strings.TrimSpace(serviceID)
	if !serviceIDPattern.MatchString(serviceID) {
		return fmt.Errorf("invalid service_id %q", serviceID)
	}
	return nil
}

func MatchServiceID(pub ed25519.PublicKey, serviceID string) error {
	if err := ValidateServiceID(serviceID); err != nil {
		return err
	}
	want := ServiceIDFromPublicKey(pub)
	if serviceID != want {
		return fmt.Errorf("service id mismatch: got %q want %q", serviceID, want)
	}
	return nil
}
