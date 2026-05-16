package grants

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	capability "github.com/origama/tubo/internal/capability"
	"golang.org/x/crypto/ssh"
)

const (
	ServiceShareTokenPrefix = "tubo-service-share-v1."
	ServiceShareKind        = "service-share"
	ServiceShareVersion     = "v1"
	ServiceShareDefaultTTL  = time.Hour
	MaxServiceShareTTL      = 24 * time.Hour
)

type ServiceSharePayload struct {
	Version            string                       `json:"version"`
	Kind               string                       `json:"kind"`
	ClusterName        string                       `json:"cluster_name"`
	ClusterID          string                       `json:"cluster_id"`
	AuthorityPublicKey string                       `json:"authority_public_key"`
	Namespace          string                       `json:"namespace"`
	NamespaceID        string                       `json:"namespace_id"`
	ServiceName        string                       `json:"service_name"`
	ServiceID          string                       `json:"service_id"`
	Grant              capability.ConnectCapability `json:"grant"`
	IssuedAt           time.Time                    `json:"issued_at"`
	ExpiresAt          time.Time                    `json:"expires_at"`
}

type ServiceShareArtifacts struct {
	Payload ServiceSharePayload
	Token   string
}

func IsServiceShareToken(token string) bool {
	return strings.HasPrefix(strings.TrimSpace(token), ServiceShareTokenPrefix)
}

func SignServiceShareToken(payload ServiceSharePayload, priv ed25519.PrivateKey) (string, error) {
	if len(priv) == 0 {
		return "", errors.New("private key is required")
	}
	payload.Version = ServiceShareVersion
	payload.Kind = ServiceShareKind
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payloadBytes)
	return ServiceShareTokenPrefix + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func BuildServiceShareArtifacts(priv ed25519.PrivateKey, clusterName, clusterID, namespaceID, serviceName, serviceID string, shareTTL time.Duration) (ServiceShareArtifacts, error) {
	payload, err := buildServiceSharePayload(priv, clusterName, clusterID, namespaceID, serviceName, serviceID, shareTTL)
	if err != nil {
		return ServiceShareArtifacts{}, err
	}
	token, err := SignServiceShareToken(payload, priv)
	if err != nil {
		return ServiceShareArtifacts{}, err
	}
	return ServiceShareArtifacts{Payload: payload, Token: token}, nil
}

func BuildServiceShareToken(priv ed25519.PrivateKey, clusterName, clusterID, namespaceID, serviceName, serviceID string, shareTTL time.Duration) (string, error) {
	artifacts, err := BuildServiceShareArtifacts(priv, clusterName, clusterID, namespaceID, serviceName, serviceID, shareTTL)
	if err != nil {
		return "", err
	}
	return artifacts.Token, nil
}

func ParseAndVerifyServiceShareToken(token string) (ServiceSharePayload, error) {
	if !IsServiceShareToken(token) {
		return ServiceSharePayload{}, fmt.Errorf("invalid service share token")
	}
	encoded := strings.TrimPrefix(strings.TrimSpace(token), ServiceShareTokenPrefix)
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return ServiceSharePayload{}, fmt.Errorf("invalid service share token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return ServiceSharePayload{}, fmt.Errorf("decode service share payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ServiceSharePayload{}, fmt.Errorf("decode service share signature: %w", err)
	}
	var payload ServiceSharePayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return ServiceSharePayload{}, fmt.Errorf("decode service share payload json: %w", err)
	}
	if payload.Version != ServiceShareVersion {
		return ServiceSharePayload{}, fmt.Errorf("unsupported service share version %q", payload.Version)
	}
	if payload.Kind != ServiceShareKind {
		return ServiceSharePayload{}, fmt.Errorf("unsupported service share kind %q", payload.Kind)
	}
	if payload.ClusterName == "" || payload.ClusterID == "" || payload.AuthorityPublicKey == "" || payload.Namespace == "" || payload.NamespaceID == "" || payload.ServiceName == "" || payload.ServiceID == "" {
		return ServiceSharePayload{}, errors.New("service share is missing required fields")
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(payload.AuthorityPublicKey))
	if err != nil {
		return ServiceSharePayload{}, fmt.Errorf("parse service share authority public key: %w", err)
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		return ServiceSharePayload{}, errors.New("service share authority key does not expose a crypto public key")
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return ServiceSharePayload{}, fmt.Errorf("service share authority key is not ed25519: %T", cryptoPub.CryptoPublicKey())
	}
	if !ed25519.Verify(edPub, payloadBytes, sig) {
		return ServiceSharePayload{}, errors.New("invalid service share signature")
	}
	if time.Now().UTC().After(payload.ExpiresAt.UTC()) {
		return ServiceSharePayload{}, errors.New("service share expired")
	}
	if !payload.IssuedAt.IsZero() && payload.ExpiresAt.Before(payload.IssuedAt) {
		return ServiceSharePayload{}, errors.New("service share expires before it was issued")
	}
	if err := capability.VerifyConnectCapability(payload.Grant, edPub, payload.ClusterID, payload.NamespaceID, payload.ServiceID, ""); err != nil {
		return ServiceSharePayload{}, err
	}
	if !payload.Grant.ExpiresAt.UTC().Equal(payload.ExpiresAt.UTC()) {
		return ServiceSharePayload{}, errors.New("service share expiry mismatch")
	}
	if len(payload.Grant.Permissions) != 1 || payload.Grant.Permissions[0] != capability.PermissionConnect {
		return ServiceSharePayload{}, errors.New("service share must be connect-only")
	}
	if payload.Grant.ClusterID != payload.ClusterID || payload.Grant.NamespaceID != payload.NamespaceID || payload.Grant.ServiceID != payload.ServiceID {
		return ServiceSharePayload{}, errors.New("service share grant scope mismatch")
	}
	return payload, nil
}

type ApprovalArtifacts struct {
	ServiceClaim         capability.ServiceClaim
	MembershipCapability capability.MembershipCapability
	ServiceShareToken    string
}

func BuildApprovalArtifacts(priv ed25519.PrivateKey, clusterName, clusterID, namespaceID, serviceName, serviceID, servicePeerID string, claimTTL, shareTTL time.Duration) (ApprovalArtifacts, error) {
	if claimTTL <= 0 {
		claimTTL = ServiceShareDefaultTTL
	}
	if shareTTL <= 0 {
		shareTTL = ServiceShareDefaultTTL
	}
	if shareTTL > claimTTL {
		shareTTL = claimTTL
	}
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{
		ClusterID:     clusterID,
		NamespaceID:   namespaceID,
		ServiceID:     serviceID,
		SubjectPeerID: servicePeerID,
		Permissions:   []string{capability.PermissionAttach, capability.PermissionAnnounce},
		ExpiresAt:     time.Now().UTC().Add(claimTTL),
	}, priv)
	if err != nil {
		return ApprovalArtifacts{}, err
	}
	membership, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   namespaceID,
		SubjectPeerID: servicePeerID,
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
		},
		ExpiresAt: claim.ExpiresAt,
	}, priv)
	if err != nil {
		return ApprovalArtifacts{}, err
	}
	shareArtifacts, err := BuildServiceShareArtifacts(priv, clusterName, clusterID, namespaceID, serviceName, serviceID, shareTTL)
	if err != nil {
		return ApprovalArtifacts{}, err
	}
	return ApprovalArtifacts{ServiceClaim: claim, MembershipCapability: membership, ServiceShareToken: shareArtifacts.Token}, nil
}

func buildServiceSharePayload(priv ed25519.PrivateKey, clusterName, clusterID, namespaceID, serviceName, serviceID string, shareTTL time.Duration) (ServiceSharePayload, error) {
	if len(priv) == 0 {
		return ServiceSharePayload{}, errors.New("private key is required")
	}
	if shareTTL <= 0 {
		shareTTL = ServiceShareDefaultTTL
	}
	if shareTTL > MaxServiceShareTTL {
		shareTTL = MaxServiceShareTTL
	}
	pubAuthorized, err := authorityPublicKeyString(priv)
	if err != nil {
		return ServiceSharePayload{}, err
	}
	now := time.Now().UTC()
	grant, err := capability.SignConnectCapability(capability.ConnectCapability{
		ClusterID:     clusterID,
		NamespaceID:   namespaceID,
		ServiceID:     serviceID,
		SubjectPeerID: "",
		Permissions:   []string{capability.PermissionConnect},
		ExpiresAt:     now.Add(shareTTL),
	}, priv)
	if err != nil {
		return ServiceSharePayload{}, err
	}
	return ServiceSharePayload{
		ClusterName:        clusterName,
		ClusterID:          clusterID,
		AuthorityPublicKey: pubAuthorized,
		Namespace:          namespaceID,
		NamespaceID:        namespaceID,
		ServiceName:        serviceName,
		ServiceID:          serviceID,
		Grant:              grant,
		IssuedAt:           now,
		ExpiresAt:          grant.ExpiresAt,
	}, nil
}

func authorityPublicKeyString(priv ed25519.PrivateKey) (string, error) {
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}
