package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	capability "github.com/origama/tubo/internal/capability"
)

type connectProofSigBody struct {
	ClusterID     string    `json:"cluster_id"`
	NamespaceID   string    `json:"namespace_id"`
	ServiceID     string    `json:"service_id"`
	SubjectPeerID string    `json:"subject_peer_id"`
	ExpiresAt     time.Time `json:"expires_at"`
	Nonce         []byte    `json:"nonce"`
	Capability    []byte    `json:"capability"`
}

// NewConnectProof constructs and signs a connect proof from a connect capability grant.
func NewConnectProof(grant capability.ConnectCapability, subjectPeerID string, privateKey ed25519.PrivateKey) (ConnectProof, error) {
	capBytes, err := json.Marshal(grant)
	if err != nil {
		return ConnectProof{}, fmt.Errorf("marshal connect capability: %w", err)
	}
	proof := ConnectProof{
		ClusterID:     grant.ClusterID,
		NamespaceID:   grant.NamespaceID,
		ServiceID:     grant.ServiceID,
		SubjectPeerID: subjectPeerID,
		ExpiresAt:     grant.ExpiresAt.UTC(),
		Nonce:         make([]byte, 32),
		Capability:    capBytes,
	}
	if _, err := rand.Read(proof.Nonce); err != nil {
		return ConnectProof{}, fmt.Errorf("generate connect proof nonce: %w", err)
	}
	return SignConnectProof(proof, privateKey)
}

// SignConnectProof signs a proof using the local peer private key.
func SignConnectProof(proof ConnectProof, privateKey ed25519.PrivateKey) (ConnectProof, error) {
	payload, err := connectProofSignaturePayload(proof)
	if err != nil {
		return ConnectProof{}, err
	}
	if len(privateKey) == 0 {
		return ConnectProof{}, fmt.Errorf("private key is required")
	}
	proof.Signature = ed25519.Sign(privateKey, payload)
	return proof, nil
}

// VerifyConnectProofSignature verifies the proof signature against the remote peer public key.
func VerifyConnectProofSignature(proof ConnectProof, publicKey ed25519.PublicKey) error {
	payload, err := connectProofSignaturePayload(proof)
	if err != nil {
		return err
	}
	if len(publicKey) == 0 {
		return fmt.Errorf("public key is required")
	}
	if len(proof.Signature) == 0 {
		return fmt.Errorf("signature is required")
	}
	if !ed25519.Verify(publicKey, payload, proof.Signature) {
		return fmt.Errorf("invalid connect proof signature")
	}
	return nil
}

func connectProofSignaturePayload(proof ConnectProof) ([]byte, error) {
	body := connectProofSigBody{
		ClusterID:     proof.ClusterID,
		NamespaceID:   proof.NamespaceID,
		ServiceID:     proof.ServiceID,
		SubjectPeerID: proof.SubjectPeerID,
		ExpiresAt:     proof.ExpiresAt.UTC(),
		Nonce:         append([]byte(nil), proof.Nonce...),
		Capability:    append([]byte(nil), proof.Capability...),
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode connect proof payload: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}
