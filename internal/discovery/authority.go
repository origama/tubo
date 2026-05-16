package discovery

import (
	"crypto/ed25519"
	"fmt"

	"golang.org/x/crypto/ssh"
)

// ParseAuthorityPublicKey parses an SSH authorized key string into a raw
// ed25519 public key that can verify capability signatures.
func ParseAuthorityPublicKey(authorized string) (ed25519.PublicKey, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorized))
	if err != nil {
		return nil, err
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		return nil, fmt.Errorf("authority key does not expose a crypto public key")
	}
	rawPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok || len(rawPub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("authority key is not ed25519")
	}
	return append(ed25519.PublicKey(nil), rawPub...), nil
}
