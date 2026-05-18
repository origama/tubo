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
	ClusterID       string    `json:"cluster_id"`
	NamespaceID     string    `json:"namespace_id"`
	ServiceID       string    `json:"service_id"`
	SubjectPeerID   string    `json:"subject_peer_id"`
	IssuedAt        time.Time `json:"issued_at"`
	ExpiresAt       time.Time `json:"expires_at"`
	Nonce           []byte    `json:"nonce"`
	JTI             string    `json:"jti"`
	Capability      []byte    `json:"capability"`
	AccessLeaseHash []byte    `json:"access_lease_hash,omitempty"`
}

type legacyConnectProofSigBody struct {
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

// NewConnectProofWithPayload constructs and signs a PoP proof from a bound access payload.
func NewConnectProofWithPayload(clusterID, namespaceID, serviceID string, expiresAt time.Time, capabilityBytes, accessLeaseHash []byte, subjectPeerID string, privateKey ed25519.PrivateKey) (ConnectProof, error) {
	proof := ConnectProof{
		ClusterID:       clusterID,
		NamespaceID:     namespaceID,
		ServiceID:       serviceID,
		SubjectPeerID:   subjectPeerID,
		IssuedAt:        time.Now().UTC(),
		ExpiresAt:       expiresAt.UTC(),
		Nonce:           make([]byte, 32),
		Capability:      append([]byte(nil), capabilityBytes...),
		AccessLeaseHash: append([]byte(nil), accessLeaseHash...),
	}
	if _, err := rand.Read(proof.Nonce); err != nil {
		return ConnectProof{}, fmt.Errorf("generate connect proof nonce: %w", err)
	}
	jti, err := newConnectProofJTI()
	if err != nil {
		return ConnectProof{}, err
	}
	proof.JTI = jti
	return SignConnectProof(proof, privateKey)
}

// SignConnectProof signs a proof using the local peer private key.
func SignConnectProof(proof ConnectProof, privateKey ed25519.PrivateKey) (ConnectProof, error) {
	payload, err := connectProofPayloadForSigning(proof)
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
	payload, err := connectProofPayloadForSigning(proof)
	if err != nil {
		return err
	}
	if len(publicKey) == 0 {
		return fmt.Errorf("public key is required")
	}
	if len(proof.Signature) == 0 {
		return fmt.Errorf("signature is required")
	}
	if ed25519.Verify(publicKey, payload, proof.Signature) {
		return nil
	}
	if proof.IssuedAt.IsZero() && proof.JTI == "" && len(proof.AccessLeaseHash) == 0 {
		legacyPayload, legacyErr := legacyConnectProofSignaturePayload(proof)
		if legacyErr == nil && ed25519.Verify(publicKey, legacyPayload, proof.Signature) {
			return nil
		}
	}
	return fmt.Errorf("invalid connect proof signature")
}

func connectProofPayloadForSigning(proof ConnectProof) ([]byte, error) {
	if proof.IssuedAt.IsZero() && proof.JTI == "" && len(proof.AccessLeaseHash) == 0 {
		return legacyConnectProofSignaturePayload(proof)
	}
	return connectProofSignaturePayload(proof)
}

func connectProofSignaturePayload(proof ConnectProof) ([]byte, error) {
	body := connectProofSigBody{
		ClusterID:       proof.ClusterID,
		NamespaceID:     proof.NamespaceID,
		ServiceID:       proof.ServiceID,
		SubjectPeerID:   proof.SubjectPeerID,
		IssuedAt:        proof.IssuedAt.UTC(),
		ExpiresAt:       proof.ExpiresAt.UTC(),
		Nonce:           append([]byte(nil), proof.Nonce...),
		JTI:             proof.JTI,
		Capability:      append([]byte(nil), proof.Capability...),
		AccessLeaseHash: append([]byte(nil), proof.AccessLeaseHash...),
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return nil, fmt.Errorf("encode connect proof payload: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func legacyConnectProofSignaturePayload(proof ConnectProof) ([]byte, error) {
	body := legacyConnectProofSigBody{
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
		return nil, fmt.Errorf("encode legacy connect proof payload: %w", err)
	}
	return bytes.TrimSpace(buf.Bytes()), nil
}

func newConnectProofJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate connect proof jti: %w", err)
	}
	return fmt.Sprintf("cp_%x", b[:]), nil
}
