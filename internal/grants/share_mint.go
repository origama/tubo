package grants

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/origama/tubo/internal/serviceidentity"
)

const (
	ShareMintRequestVersion = "v1"
	ShareMintRequestKind    = "share-mint-request"
	ShareMintMaxAge         = 15 * time.Minute
	ShareMintMaxClockSkew   = 2 * time.Minute
)

type ShareMintRequest struct {
	Version               string       `json:"version"`
	Kind                  string       `json:"kind"`
	ClusterID             string       `json:"cluster_id"`
	NamespaceID           string       `json:"namespace_id"`
	ServiceID             string       `json:"service_id"`
	PublishLease          PublishLease `json:"publish_lease"`
	ServicePeerID         string       `json:"service_peer_id"`
	ServiceAddresses      []string     `json:"service_addresses"`
	RequestedTTLSeconds   int64        `json:"requested_ttl_seconds"`
	RequestNonce          string       `json:"request_nonce"`
	RequestIssuedAt       time.Time    `json:"request_issued_at"`
	ServiceOwnerSignature []byte       `json:"service_owner_signature"`
}

func SignShareMintRequest(req ShareMintRequest, ownerPriv ed25519.PrivateKey) (ShareMintRequest, error) {
	req.Version = ShareMintRequestVersion
	req.Kind = ShareMintRequestKind
	payload, err := canonicalShareMintRequest(req)
	if err != nil {
		return ShareMintRequest{}, err
	}
	req.ServiceOwnerSignature = ed25519.Sign(ownerPriv, payload)
	return req, nil
}

func VerifyShareMintRequest(req ShareMintRequest) error {
	if req.Version != ShareMintRequestVersion {
		return fmt.Errorf("unsupported share mint request version %q", req.Version)
	}
	if req.Kind != ShareMintRequestKind {
		return fmt.Errorf("unsupported share mint request kind %q", req.Kind)
	}
	if req.ClusterID == "" || req.NamespaceID == "" || req.ServiceID == "" {
		return errors.New("share mint request is missing required scope fields")
	}
	if req.PublishLease.ServiceID == "" {
		return errors.New("share mint request publish lease is required")
	}
	if strings.TrimSpace(req.ServicePeerID) == "" {
		return errors.New("share mint request service peer id is required")
	}
	if len(req.ServiceAddresses) == 0 {
		return errors.New("share mint request service addresses are required")
	}
	if req.RequestedTTLSeconds <= 0 {
		return errors.New("share mint request requested_ttl_seconds is required")
	}
	if strings.TrimSpace(req.RequestNonce) == "" {
		return errors.New("share mint request nonce is required")
	}
	if req.RequestIssuedAt.IsZero() {
		return errors.New("share mint request issued_at is required")
	}
	if len(req.ServiceOwnerSignature) == 0 {
		return errors.New("share mint request service owner signature is required")
	}
	pub, err := serviceidentity.DecodePublicKey(req.PublishLease.ServicePublicKey)
	if err != nil {
		return fmt.Errorf("decode service public key: %w", err)
	}
	if err := serviceidentity.MatchServiceID(pub, req.ServiceID); err != nil {
		return err
	}
	payload, err := canonicalShareMintRequest(req)
	if err != nil {
		return err
	}
	if !ed25519.Verify(pub, payload, req.ServiceOwnerSignature) {
		return errors.New("invalid share mint request service owner signature")
	}
	return nil
}

func ShareMintRequestMatchesFreshness(req ShareMintRequest, now time.Time) error {
	issuedAt := req.RequestIssuedAt.UTC()
	now = now.UTC()
	if issuedAt.After(now.Add(ShareMintMaxClockSkew)) {
		return errors.New("share mint request issued_at is in the future")
	}
	if now.Sub(issuedAt) > ShareMintMaxAge {
		return errors.New("share mint request is too old")
	}
	return nil
}

func canonicalShareMintRequest(req ShareMintRequest) ([]byte, error) {
	leaseHash, err := shareMintLeaseHash(req.PublishLease)
	if err != nil {
		return nil, err
	}
	payload := struct {
		Version             string    `json:"version"`
		Kind                string    `json:"kind"`
		ClusterID           string    `json:"cluster_id"`
		NamespaceID         string    `json:"namespace_id"`
		ServiceID           string    `json:"service_id"`
		PublishLeaseHash    string    `json:"publish_lease_hash"`
		ServicePeerID       string    `json:"service_peer_id"`
		ServiceAddresses    []string  `json:"service_addresses"`
		RequestedTTLSeconds int64     `json:"requested_ttl_seconds"`
		RequestNonce        string    `json:"request_nonce"`
		RequestIssuedAt     time.Time `json:"request_issued_at"`
	}{
		Version:             req.Version,
		Kind:                req.Kind,
		ClusterID:           req.ClusterID,
		NamespaceID:         req.NamespaceID,
		ServiceID:           req.ServiceID,
		PublishLeaseHash:    leaseHash,
		ServicePeerID:       strings.TrimSpace(req.ServicePeerID),
		ServiceAddresses:    canonicalShareMintAddresses(req.ServiceAddresses),
		RequestedTTLSeconds: req.RequestedTTLSeconds,
		RequestNonce:        strings.TrimSpace(req.RequestNonce),
		RequestIssuedAt:     req.RequestIssuedAt.UTC(),
	}
	return json.Marshal(payload)
}

func shareMintLeaseHash(lease PublishLease) (string, error) {
	payload, err := canonicalPublishLease(lease)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

func canonicalShareMintAddresses(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, raw := range in {
		addr := strings.TrimSpace(raw)
		if addr == "" {
			continue
		}
		if _, ok := seen[addr]; ok {
			continue
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}
	return out
}

func validateShareMintServiceEndpoint(servicePeerID string, addrs []string) ([]string, error) {
	servicePeerID = strings.TrimSpace(servicePeerID)
	if servicePeerID == "" {
		return nil, errors.New("share mint request service peer id is required")
	}
	cleaned := canonicalShareMintAddresses(addrs)
	if len(cleaned) == 0 {
		return nil, errors.New("share mint request service addresses are required")
	}
	for _, addr := range cleaned {
		if !IsRemoteDialableGrantServicePeer(addr) {
			return nil, fmt.Errorf("share mint request service endpoint %q is not remote-dialable", addr)
		}
		embeddedPeerID, ok := shareMintEndpointPeerID(addr)
		if !ok {
			return nil, fmt.Errorf("share mint request service endpoint %q must embed /p2p/%s", addr, servicePeerID)
		}
		if embeddedPeerID != servicePeerID {
			return nil, fmt.Errorf("share mint request service endpoint %q embeds peer %q, want %q", addr, embeddedPeerID, servicePeerID)
		}
	}
	return cleaned, nil
}

func shareMintEndpointPeerID(addr string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(addr), "/")
	for i := len(parts) - 2; i >= 0; i-- {
		if parts[i] != "p2p" {
			continue
		}
		peerID := strings.TrimSpace(parts[i+1])
		if peerID == "" {
			return "", false
		}
		return peerID, true
	}
	return "", false
}
