package networkbundle

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"

	"golang.org/x/crypto/ssh"
)

func Verify(bundle *Bundle, trustedKeys map[string]string) ([]byte, string, error) {
	if bundle == nil {
		return nil, "", errors.New("nil bundle")
	}
	if bundle.Kind != "tubo.network.bundle" {
		return nil, "", fmt.Errorf("unsupported bundle kind %q", bundle.Kind)
	}
	if bundle.Version != 1 {
		return nil, "", fmt.Errorf("unsupported bundle version %d", bundle.Version)
	}
	if bundle.PayloadEncoding != "base64url" {
		return nil, "", fmt.Errorf("unsupported payload encoding %q", bundle.PayloadEncoding)
	}
	if bundle.Signature.Alg != "ed25519" {
		return nil, "", fmt.Errorf("unsupported signature alg %q", bundle.Signature.Alg)
	}
	trustedKey, ok := trustedKeys[bundle.Signature.KeyID]
	if !ok {
		return nil, "", fmt.Errorf("unknown bundle key_id %q", bundle.Signature.KeyID)
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(bundle.Payload)
	if err != nil {
		return nil, "", fmt.Errorf("decode payload: %w", err)
	}
	sigBytes, err := base64.RawURLEncoding.DecodeString(bundle.Signature.Value)
	if err != nil {
		return nil, "", fmt.Errorf("decode signature: %w", err)
	}
	pub, err := parseTrustedEd25519Key(trustedKey)
	if err != nil {
		return nil, "", err
	}
	if !ed25519.Verify(pub, payloadBytes, sigBytes) {
		return nil, "", errors.New("invalid bundle signature")
	}
	return payloadBytes, bundle.Signature.KeyID, nil
}

func parseTrustedEd25519Key(authorizedKey string) (ed25519.PublicKey, error) {
	key, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		return nil, fmt.Errorf("parse trusted public key: %w", err)
	}
	cryptoPub, ok := key.(ssh.CryptoPublicKey)
	if !ok {
		return nil, errors.New("trusted key is not a crypto public key")
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("trusted key is not ed25519")
	}
	return edPub, nil
}
