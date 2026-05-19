package grants

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/serviceidentity"
)

const (
	PublishLeaseVersion     = "v1"
	PublishLeaseKind        = "publish-lease"
	PublishLeaseRequestKind = "publish-lease-request"
)

type PublishLeaseRequest struct {
	Version                    string   `json:"version"`
	Kind                       string   `json:"kind"`
	ClusterID                  string   `json:"cluster_id"`
	NamespaceID                string   `json:"namespace_id"`
	ServiceID                  string   `json:"service_id"`
	ServicePublicKey           string   `json:"service_public_key"`
	PublisherPeerID            string   `json:"publisher_peer_id"`
	PublisherInstancePublicKey string   `json:"publisher_instance_public_key,omitempty"`
	RequestedCapabilities      []string `json:"requested_capabilities"`
	Nonce                      string   `json:"nonce"`
	ServiceOwnerSignature      []byte   `json:"service_owner_signature"`
}

type PublishLease struct {
	Version                    string                  `json:"version"`
	Kind                       string                  `json:"kind"`
	ClusterID                  string                  `json:"cluster_id"`
	NamespaceID                string                  `json:"namespace_id"`
	ServiceID                  string                  `json:"service_id"`
	ServicePublicKey           string                  `json:"service_public_key"`
	PublisherPeerID            string                  `json:"publisher_peer_id"`
	PublisherInstancePublicKey string                  `json:"publisher_instance_public_key,omitempty"`
	RequestedCapabilities      []string                `json:"requested_capabilities"`
	Nonce                      string                  `json:"nonce"`
	PublishEpoch               int64                   `json:"publish_epoch,omitempty"`
	IssuedAt                   time.Time               `json:"issued_at"`
	ExpiresAt                  time.Time               `json:"expires_at"`
	ServiceClaim               capability.ServiceClaim `json:"service_claim"`
	ServiceOwnerSignature      []byte                  `json:"service_owner_signature,omitempty"`
	Signature                  []byte                  `json:"signature"`
}

type PublishLeaseArtifacts struct {
	Lease        PublishLease
	ServiceClaim capability.ServiceClaim
}

func SignPublishLeaseRequest(req PublishLeaseRequest, ownerPriv ed25519.PrivateKey) (PublishLeaseRequest, error) {
	req.Version = PublishLeaseVersion
	req.Kind = PublishLeaseRequestKind
	payload, err := canonicalPublishLeaseRequest(req)
	if err != nil {
		return PublishLeaseRequest{}, err
	}
	req.ServiceOwnerSignature = ed25519.Sign(ownerPriv, payload)
	return req, nil
}

func VerifyPublishLeaseRequest(req PublishLeaseRequest) error {
	if req.Version != PublishLeaseVersion {
		return fmt.Errorf("unsupported publish lease request version %q", req.Version)
	}
	if req.Kind != PublishLeaseRequestKind {
		return fmt.Errorf("unsupported publish lease request kind %q", req.Kind)
	}
	if req.ClusterID == "" || req.NamespaceID == "" || req.ServiceID == "" || req.ServicePublicKey == "" || req.PublisherPeerID == "" || req.Nonce == "" {
		return errors.New("publish lease request is missing required fields")
	}
	if !validPublishLeaseCapabilities(req.RequestedCapabilities) {
		return errors.New("requested capabilities must be limited to publish/attach/announce/share.mint")
	}
	pub, err := serviceidentity.DecodePublicKey(req.ServicePublicKey)
	if err != nil {
		return fmt.Errorf("decode service public key: %w", err)
	}
	if err := serviceidentity.MatchServiceID(pub, req.ServiceID); err != nil {
		return err
	}
	payload, err := canonicalPublishLeaseRequest(req)
	if err != nil {
		return err
	}
	if len(req.ServiceOwnerSignature) == 0 {
		return errors.New("service owner signature is required")
	}
	if !ed25519.Verify(pub, payload, req.ServiceOwnerSignature) {
		return errors.New("invalid service owner signature")
	}
	return nil
}

func BuildPublishLeaseArtifacts(priv ed25519.PrivateKey, req PublishLeaseRequest, serviceName string, claimTTL, leaseTTL time.Duration) (PublishLeaseArtifacts, error) {
	return BuildPublishLeaseArtifactsWithEpoch(priv, req, serviceName, claimTTL, leaseTTL, 0)
}

func BuildPublishLeaseArtifactsWithEpoch(priv ed25519.PrivateKey, req PublishLeaseRequest, serviceName string, claimTTL, leaseTTL time.Duration, publishEpoch int64) (PublishLeaseArtifacts, error) {
	if err := VerifyPublishLeaseRequest(req); err != nil {
		return PublishLeaseArtifacts{}, err
	}
	if claimTTL <= 0 {
		claimTTL = ServiceShareDefaultTTL
	}
	if leaseTTL <= 0 || leaseTTL > claimTTL {
		leaseTTL = claimTTL
	}
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{
		ClusterID:     req.ClusterID,
		NamespaceID:   req.NamespaceID,
		ServiceID:     req.ServiceID,
		SubjectPeerID: req.PublisherPeerID,
		Permissions:   []string{capability.PermissionAttach, capability.PermissionAnnounce},
		ExpiresAt:     time.Now().UTC().Add(claimTTL),
	}, priv)
	if err != nil {
		return PublishLeaseArtifacts{}, err
	}
	lease := PublishLease{
		Version:                    PublishLeaseVersion,
		Kind:                       PublishLeaseKind,
		ClusterID:                  req.ClusterID,
		NamespaceID:                req.NamespaceID,
		ServiceID:                  req.ServiceID,
		ServicePublicKey:           req.ServicePublicKey,
		PublisherPeerID:            req.PublisherPeerID,
		PublisherInstancePublicKey: req.PublisherInstancePublicKey,
		RequestedCapabilities:      append([]string(nil), req.RequestedCapabilities...),
		Nonce:                      req.Nonce,
		PublishEpoch:               publishEpoch,
		IssuedAt:                   time.Now().UTC(),
		ExpiresAt:                  time.Now().UTC().Add(leaseTTL),
		ServiceClaim:               claim,
		ServiceOwnerSignature:      append([]byte(nil), req.ServiceOwnerSignature...),
	}
	if serviceName != "" {
		// serviceName is intentionally not part of the binding; kept for compatibility only.
	}
	payload, err := canonicalPublishLease(lease)
	if err != nil {
		return PublishLeaseArtifacts{}, err
	}
	lease.Signature = ed25519.Sign(priv, payload)
	return PublishLeaseArtifacts{Lease: lease, ServiceClaim: claim}, nil
}

func VerifyPublishLease(lease PublishLease, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error {
	if lease.Version != PublishLeaseVersion {
		return fmt.Errorf("unsupported publish lease version %q", lease.Version)
	}
	if lease.Kind != PublishLeaseKind {
		return fmt.Errorf("unsupported publish lease kind %q", lease.Kind)
	}
	if lease.ClusterID != clusterID {
		return fmt.Errorf("cluster id mismatch: got %q want %q", lease.ClusterID, clusterID)
	}
	if lease.NamespaceID != namespaceID {
		return fmt.Errorf("namespace id mismatch: got %q want %q", lease.NamespaceID, namespaceID)
	}
	if lease.ServiceID != serviceID {
		return fmt.Errorf("service id mismatch: got %q want %q", lease.ServiceID, serviceID)
	}
	if lease.PublisherPeerID != servicePeerID {
		return fmt.Errorf("publisher peer id mismatch: got %q want %q", lease.PublisherPeerID, servicePeerID)
	}
	if lease.ServicePublicKey == "" {
		return errors.New("service public key is required")
	}
	pub, err := serviceidentity.DecodePublicKey(lease.ServicePublicKey)
	if err != nil {
		return fmt.Errorf("decode service public key: %w", err)
	}
	if err := serviceidentity.MatchServiceID(pub, serviceID); err != nil {
		return err
	}
	if lease.ExpiresAt.IsZero() {
		return errors.New("publish lease expires_at is required")
	}
	if time.Now().UTC().After(lease.ExpiresAt.UTC()) {
		return errors.New("publish lease expired")
	}
	if err := capability.VerifyServiceClaim(lease.ServiceClaim, authorityPub, clusterID, namespaceID, serviceID, servicePeerID); err != nil {
		return err
	}
	payload, err := canonicalPublishLease(lease)
	if err != nil {
		return err
	}
	if len(lease.Signature) == 0 {
		return errors.New("publish lease signature is required")
	}
	if !ed25519.Verify(authorityPub, payload, lease.Signature) {
		return errors.New("invalid publish lease signature")
	}
	return nil
}

func ParseAndVerifyPublishLeaseBytes(b []byte, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) (PublishLease, error) {
	var lease PublishLease
	if err := json.Unmarshal(b, &lease); err != nil {
		return PublishLease{}, err
	}
	if err := VerifyPublishLease(lease, authorityPub, clusterID, namespaceID, serviceID, servicePeerID); err != nil {
		return PublishLease{}, err
	}
	return lease, nil
}

func canonicalPublishLeaseRequest(req PublishLeaseRequest) ([]byte, error) {
	payload := struct {
		Version                    string   `json:"version"`
		Kind                       string   `json:"kind"`
		ClusterID                  string   `json:"cluster_id"`
		NamespaceID                string   `json:"namespace_id"`
		ServiceID                  string   `json:"service_id"`
		ServicePublicKey           string   `json:"service_public_key"`
		PublisherPeerID            string   `json:"publisher_peer_id"`
		PublisherInstancePublicKey string   `json:"publisher_instance_public_key,omitempty"`
		RequestedCapabilities      []string `json:"requested_capabilities"`
		Nonce                      string   `json:"nonce"`
	}{
		Version:                    req.Version,
		Kind:                       req.Kind,
		ClusterID:                  req.ClusterID,
		NamespaceID:                req.NamespaceID,
		ServiceID:                  req.ServiceID,
		ServicePublicKey:           req.ServicePublicKey,
		PublisherPeerID:            req.PublisherPeerID,
		PublisherInstancePublicKey: req.PublisherInstancePublicKey,
		RequestedCapabilities:      canonicalPublishLeaseCapabilities(req.RequestedCapabilities),
		Nonce:                      req.Nonce,
	}
	return json.Marshal(payload)
}

func canonicalPublishLease(lease PublishLease) ([]byte, error) {
	payload := struct {
		Version                    string                  `json:"version"`
		Kind                       string                  `json:"kind"`
		ClusterID                  string                  `json:"cluster_id"`
		NamespaceID                string                  `json:"namespace_id"`
		ServiceID                  string                  `json:"service_id"`
		ServicePublicKey           string                  `json:"service_public_key"`
		PublisherPeerID            string                  `json:"publisher_peer_id"`
		PublisherInstancePublicKey string                  `json:"publisher_instance_public_key,omitempty"`
		RequestedCapabilities      []string                `json:"requested_capabilities"`
		Nonce                      string                  `json:"nonce"`
		PublishEpoch               int64                   `json:"publish_epoch,omitempty"`
		IssuedAt                   time.Time               `json:"issued_at"`
		ExpiresAt                  time.Time               `json:"expires_at"`
		ServiceClaim               capability.ServiceClaim `json:"service_claim"`
		ServiceOwnerSignature      []byte                  `json:"service_owner_signature,omitempty"`
	}{
		Version:                    lease.Version,
		Kind:                       lease.Kind,
		ClusterID:                  lease.ClusterID,
		NamespaceID:                lease.NamespaceID,
		ServiceID:                  lease.ServiceID,
		ServicePublicKey:           lease.ServicePublicKey,
		PublisherPeerID:            lease.PublisherPeerID,
		PublisherInstancePublicKey: lease.PublisherInstancePublicKey,
		RequestedCapabilities:      canonicalPublishLeaseCapabilities(lease.RequestedCapabilities),
		Nonce:                      lease.Nonce,
		PublishEpoch:               lease.PublishEpoch,
		IssuedAt:                   lease.IssuedAt.UTC(),
		ExpiresAt:                  lease.ExpiresAt.UTC(),
		ServiceClaim:               lease.ServiceClaim,
		ServiceOwnerSignature:      lease.ServiceOwnerSignature,
	}
	return json.Marshal(payload)
}

func canonicalPublishLeaseCapabilities(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	uniq := out[:0]
	var prev string
	for i, cap := range out {
		if i == 0 || cap != prev {
			uniq = append(uniq, cap)
			prev = cap
		}
	}
	return uniq
}

func validPublishLeaseCapabilities(perms []string) bool {
	if len(perms) == 0 {
		return false
	}
	for _, perm := range canonicalPublishLeaseCapabilities(perms) {
		switch perm {
		case capability.PermissionPublish, capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint:
		default:
			return false
		}
	}
	return true
}
