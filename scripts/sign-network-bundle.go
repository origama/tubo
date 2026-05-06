package main

import (
	"bytes"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

type envelope struct {
	Kind            string    `json:"kind"`
	Version         int       `json:"version"`
	PayloadEncoding string    `json:"payload_encoding"`
	Payload         string    `json:"payload"`
	Signature       signature `json:"signature"`
}

type signature struct {
	Alg   string `json:"alg"`
	KeyID string `json:"key_id"`
	Value string `json:"value"`
}

func main() {
	var payloadPath string
	var privateKeyPath string
	var passphrasePath string
	var outPath string
	var keyID string
	flag.StringVar(&payloadPath, "payload", "", "path to payload JSON")
	flag.StringVar(&privateKeyPath, "private-key", "", "path to OpenSSH private key")
	flag.StringVar(&passphrasePath, "passphrase-file", "", "path to private key passphrase")
	flag.StringVar(&outPath, "out", "", "output bundle path")
	flag.StringVar(&keyID, "key-id", "tubo-root-2026", "bundle signing key id")
	flag.Parse()
	if payloadPath == "" || privateKeyPath == "" || passphrasePath == "" || outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/sign-network-bundle.go --payload <payload.json> --private-key <key> --passphrase-file <file> --out <bundle> [--key-id tubo-root-2026]")
		os.Exit(2)
	}
	payloadBytes, err := os.ReadFile(payloadPath)
	if err != nil {
		fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payloadBytes); err != nil {
		fatal(fmt.Errorf("compact payload json: %w", err))
	}
	priv, err := loadSigner(privateKeyPath, passphrasePath)
	if err != nil {
		fatal(err)
	}
	sigBytes, err := signPayload(priv, compact.Bytes())
	if err != nil {
		fatal(err)
	}
	bundle := envelope{
		Kind:            "tubo.network.bundle",
		Version:         1,
		PayloadEncoding: "base64url",
		Payload:         base64.RawURLEncoding.EncodeToString(compact.Bytes()),
		Signature: signature{
			Alg:   "ed25519",
			KeyID: keyID,
			Value: base64.RawURLEncoding.EncodeToString(sigBytes),
		},
	}
	outBytes, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		fatal(err)
	}
	outBytes = append(outBytes, '\n')
	if err := os.WriteFile(outPath, outBytes, 0644); err != nil {
		fatal(err)
	}
}

func loadSigner(privateKeyPath, passphrasePath string) (any, error) {
	keyBytes, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, err
	}
	passphraseBytes, err := os.ReadFile(passphrasePath)
	if err != nil {
		return nil, err
	}
	passphrase := []byte(strings.TrimSpace(string(passphraseBytes)))
	key, err := ssh.ParseRawPrivateKeyWithPassphrase(keyBytes, passphrase)
	if err != nil {
		return nil, fmt.Errorf("parse private key: %w", err)
	}
	return key, nil
}

func signPayload(key any, payload []byte) ([]byte, error) {
	switch k := key.(type) {
	case ed25519.PrivateKey:
		return ed25519.Sign(k, payload), nil
	case *ed25519.PrivateKey:
		return ed25519.Sign(*k, payload), nil
	case crypto.Signer:
		return k.Sign(rand.Reader, payload, crypto.Hash(0))
	default:
		return nil, fmt.Errorf("unsupported private key type %T", key)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "sign-network-bundle:", err)
	os.Exit(1)
}
