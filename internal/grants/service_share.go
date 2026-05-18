package grants

import (
	"crypto/ed25519"
	"crypto/rand"
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
	ShareInviteTokenPrefix        = "tubo-share-invite-v1."
	LegacyServiceShareTokenPrefix = "tubo-service-share-v1."
	ShareInviteKind               = "share-invite"
	ShareInviteVersion            = "v1"
	ShareInviteDefaultTTL         = time.Hour
	MaxShareInviteTTL             = 24 * time.Hour

	ServiceShareTokenPrefix = ShareInviteTokenPrefix
	ServiceShareKind        = ShareInviteKind
	ServiceShareVersion     = ShareInviteVersion
	ServiceShareDefaultTTL  = ShareInviteDefaultTTL
	MaxServiceShareTTL      = MaxShareInviteTTL
)

type GrantServiceEndpoint struct {
	Protocol string   `json:"protocol,omitempty"`
	Peers    []string `json:"peers,omitempty"`
}

type ServiceSharePayload struct {
	Version            string                       `json:"version"`
	Kind               string                       `json:"kind"`
	JTI                string                       `json:"jti"`
	ClusterName        string                       `json:"cluster_name"`
	ClusterID          string                       `json:"cluster_id"`
	AuthorityPublicKey string                       `json:"authority_public_key"`
	Namespace          string                       `json:"namespace"`
	NamespaceID        string                       `json:"namespace_id"`
	ServiceName        string                       `json:"service_name,omitempty"`
	DisplayNameHint    string                       `json:"display_name_hint,omitempty"`
	ServiceID          string                       `json:"service_id,omitempty"`
	TargetServiceID    string                       `json:"target_service_id,omitempty"`
	Grant              capability.ConnectCapability `json:"grant"` // legacy bearer fallback for old tokens/bridges
	GrantService       GrantServiceEndpoint         `json:"grant_service,omitempty"`
	IssuedAt           time.Time                    `json:"issued_at"`
	ExpiresAt          time.Time                    `json:"expires_at"`
}

type ServiceShareArtifacts struct {
	Payload ServiceSharePayload
	Token   string
}

func IsServiceShareToken(token string) bool {
	token = strings.TrimSpace(token)
	return strings.HasPrefix(token, ServiceShareTokenPrefix) || strings.HasPrefix(token, LegacyServiceShareTokenPrefix)
}

func SignServiceShareToken(payload ServiceSharePayload, priv ed25519.PrivateKey) (string, error) {
	if len(priv) == 0 {
		return "", errors.New("private key is required")
	}
	if payload.JTI == "" {
		jti, err := newShareInviteJTI()
		if err != nil {
			return "", err
		}
		payload.JTI = jti
	}
	payload.Version = ShareInviteVersion
	payload.Kind = ShareInviteKind
	normalizeShareInvitePayload(&payload)
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payloadBytes)
	return ShareInviteTokenPrefix + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
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

func BuildShareInviteArtifactsFromLease(priv ed25519.PrivateKey, clusterName string, lease PublishLease, displayName string, shareTTL time.Duration) (ServiceShareArtifacts, error) {
	payload, err := buildShareInvitePayloadFromLease(priv, clusterName, lease, displayName, shareTTL)
	if err != nil {
		return ServiceShareArtifacts{}, err
	}
	token, err := SignServiceShareToken(payload, priv)
	if err != nil {
		return ServiceShareArtifacts{}, err
	}
	return ServiceShareArtifacts{Payload: payload, Token: token}, nil
}

func BuildShareInviteArtifactsFromLeaseWithGrantService(priv ed25519.PrivateKey, clusterName string, lease PublishLease, displayName string, shareTTL time.Duration, grantPeers []string) (ServiceShareArtifacts, error) {
	payload, err := buildShareInvitePayloadFromLease(priv, clusterName, lease, displayName, shareTTL)
	if err != nil {
		return ServiceShareArtifacts{}, err
	}
	if len(grantPeers) > 0 {
		payload.GrantService = GrantServiceEndpoint{Protocol: ProtocolID, Peers: append([]string(nil), grantPeers...)}
	}
	token, err := SignServiceShareToken(payload, priv)
	if err != nil {
		return ServiceShareArtifacts{}, err
	}
	return ServiceShareArtifacts{Payload: payload, Token: token}, nil
}

func BuildShareInviteTokenFromLease(priv ed25519.PrivateKey, clusterName string, lease PublishLease, displayName string, shareTTL time.Duration) (string, error) {
	artifacts, err := BuildShareInviteArtifactsFromLease(priv, clusterName, lease, displayName, shareTTL)
	if err != nil {
		return "", err
	}
	return artifacts.Token, nil
}

func ParseAndVerifyServiceShareToken(token string) (ServiceSharePayload, error) {
	trimmed := strings.TrimSpace(token)
	prefix := ""
	switch {
	case strings.HasPrefix(trimmed, ServiceShareTokenPrefix):
		prefix = ServiceShareTokenPrefix
	case strings.HasPrefix(trimmed, LegacyServiceShareTokenPrefix):
		prefix = LegacyServiceShareTokenPrefix
	default:
		return ServiceSharePayload{}, fmt.Errorf("invalid service share token")
	}
	encoded := strings.TrimPrefix(trimmed, prefix)
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
	normalizeShareInvitePayload(&payload)
	if payload.Version != ShareInviteVersion {
		return ServiceSharePayload{}, fmt.Errorf("unsupported service share version %q", payload.Version)
	}
	if payload.Kind != ShareInviteKind && payload.Kind != ServiceShareKind {
		return ServiceSharePayload{}, fmt.Errorf("unsupported service share kind %q", payload.Kind)
	}
	if payload.ClusterName == "" || payload.ClusterID == "" || payload.AuthorityPublicKey == "" || payload.Namespace == "" || payload.NamespaceID == "" || payload.JTI == "" || payload.ServiceName == "" || payload.DisplayNameHint == "" || payload.ServiceID == "" || payload.TargetServiceID == "" {
		return ServiceSharePayload{}, errors.New("service share is missing required fields")
	}
	if payload.ServiceID != payload.TargetServiceID {
		return ServiceSharePayload{}, errors.New("service share target service id mismatch")
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
	if err := capability.VerifyConnectCapability(payload.Grant, edPub, payload.ClusterID, payload.NamespaceID, payload.TargetServiceID, ""); err != nil {
		return ServiceSharePayload{}, err
	}
	if !payload.Grant.ExpiresAt.UTC().Equal(payload.ExpiresAt.UTC()) {
		return ServiceSharePayload{}, errors.New("service share expiry mismatch")
	}
	if len(payload.Grant.Permissions) != 1 || payload.Grant.Permissions[0] != capability.PermissionConnect {
		return ServiceSharePayload{}, errors.New("service share must be connect-only")
	}
	if payload.Grant.ClusterID != payload.ClusterID || payload.Grant.NamespaceID != payload.NamespaceID || payload.Grant.ServiceID != payload.TargetServiceID {
		return ServiceSharePayload{}, errors.New("service share grant scope mismatch")
	}
	return payload, nil
}

type ApprovalArtifacts struct {
	ServiceClaim         capability.ServiceClaim
	PublishLease         PublishLease
	MembershipCapability capability.MembershipCapability
	ServiceShareToken    string
}

func BuildApprovalArtifacts(priv ed25519.PrivateKey, clusterName, clusterID, namespaceID, serviceName, serviceID, servicePeerID string, claimTTL, shareTTL time.Duration, requestedCapabilities []string, servicePublicKey, requestNonce string, ownerSignature []byte) (ApprovalArtifacts, error) {
	return BuildApprovalArtifactsWithGrantService(priv, clusterName, clusterID, namespaceID, serviceName, serviceID, servicePeerID, claimTTL, shareTTL, requestedCapabilities, servicePublicKey, requestNonce, ownerSignature, nil)
}

func BuildApprovalArtifactsWithGrantService(priv ed25519.PrivateKey, clusterName, clusterID, namespaceID, serviceName, serviceID, servicePeerID string, claimTTL, shareTTL time.Duration, requestedCapabilities []string, servicePublicKey, requestNonce string, ownerSignature []byte, grantPeers []string) (ApprovalArtifacts, error) {
	if claimTTL <= 0 {
		claimTTL = ServiceShareDefaultTTL
	}
	if shareTTL <= 0 {
		shareTTL = ServiceShareDefaultTTL
	}
	if shareTTL > claimTTL {
		shareTTL = claimTTL
	}
	requestedCapabilities = append([]string(nil), requestedCapabilities...)
	if len(requestedCapabilities) == 0 {
		requestedCapabilities = []string{capability.PermissionAttach, capability.PermissionAnnounce}
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(priv, PublishLeaseRequest{
		Version:               PublishLeaseVersion,
		Kind:                  PublishLeaseRequestKind,
		ClusterID:             clusterID,
		NamespaceID:           namespaceID,
		ServiceID:             serviceID,
		ServicePublicKey:      servicePublicKey,
		PublisherPeerID:       servicePeerID,
		RequestedCapabilities: requestedCapabilities,
		Nonce:                 requestNonce,
		ServiceOwnerSignature: ownerSignature,
	}, serviceName, claimTTL, shareTTL)
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
		ExpiresAt: leaseArtifacts.ServiceClaim.ExpiresAt,
	}, priv)
	if err != nil {
		return ApprovalArtifacts{}, err
	}
	shareArtifacts, err := BuildShareInviteArtifactsFromLeaseWithGrantService(priv, clusterName, leaseArtifacts.Lease, serviceName, shareTTL, grantPeers)
	if err != nil {
		shareArtifacts, err = BuildServiceShareArtifacts(priv, clusterName, clusterID, namespaceID, serviceName, serviceID, shareTTL)
		if err != nil {
			return ApprovalArtifacts{}, err
		}
	}
	return ApprovalArtifacts{ServiceClaim: leaseArtifacts.ServiceClaim, PublishLease: leaseArtifacts.Lease, MembershipCapability: membership, ServiceShareToken: shareArtifacts.Token}, nil
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
	return buildShareInvitePayload(clusterName, clusterID, namespaceID, serviceName, serviceID, pubAuthorized, priv, shareTTL)
}

func buildShareInvitePayloadFromLease(priv ed25519.PrivateKey, clusterName string, lease PublishLease, displayName string, shareTTL time.Duration) (ServiceSharePayload, error) {
	if len(priv) == 0 {
		return ServiceSharePayload{}, errors.New("private key is required")
	}
	if shareTTL <= 0 {
		shareTTL = ShareInviteDefaultTTL
	}
	if shareTTL > MaxShareInviteTTL {
		shareTTL = MaxShareInviteTTL
	}
	pubAuthorized, err := authorityPublicKeyString(priv)
	if err != nil {
		return ServiceSharePayload{}, err
	}
	if err := verifyLeaseCanMintShareInvite(priv, lease); err != nil {
		return ServiceSharePayload{}, err
	}
	if displayName == "" {
		displayName = lease.ServiceID
	}
	return buildShareInvitePayload(clusterName, lease.ClusterID, lease.NamespaceID, displayName, lease.ServiceID, pubAuthorized, priv, shareTTL)
}

func buildShareInvitePayload(clusterName, clusterID, namespaceID, displayName, serviceID, authorityPublicKey string, priv ed25519.PrivateKey, shareTTL time.Duration) (ServiceSharePayload, error) {
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
	jti, err := newShareInviteJTI()
	if err != nil {
		return ServiceSharePayload{}, err
	}
	payload := ServiceSharePayload{
		JTI:                jti,
		ClusterName:        clusterName,
		ClusterID:          clusterID,
		AuthorityPublicKey: authorityPublicKey,
		Namespace:          namespaceID,
		NamespaceID:        namespaceID,
		ServiceName:        displayName,
		DisplayNameHint:    displayName,
		ServiceID:          serviceID,
		TargetServiceID:    serviceID,
		Grant:              grant,
		IssuedAt:           now,
		ExpiresAt:          grant.ExpiresAt,
	}
	normalizeShareInvitePayload(&payload)
	return payload, nil
}

func verifyLeaseCanMintShareInvite(priv ed25519.PrivateKey, lease PublishLease) error {
	if !leaseHasCapability(lease.RequestedCapabilities, capability.PermissionShareMint) {
		return errors.New("publish lease does not allow share invite minting")
	}
	pub := priv.Public().(ed25519.PublicKey)
	if err := VerifyPublishLease(lease, pub, lease.ClusterID, lease.NamespaceID, lease.ServiceID, lease.PublisherPeerID); err != nil {
		return err
	}
	return nil
}

func leaseHasCapability(perms []string, want string) bool {
	for _, perm := range perms {
		if perm == want {
			return true
		}
	}
	return false
}

func normalizeShareInvitePayload(payload *ServiceSharePayload) {
	if payload == nil {
		return
	}
	if payload.TargetServiceID == "" {
		payload.TargetServiceID = payload.ServiceID
	}
	if payload.ServiceID == "" {
		payload.ServiceID = payload.TargetServiceID
	}
	if payload.DisplayNameHint == "" {
		payload.DisplayNameHint = payload.ServiceName
	}
	if payload.ServiceName == "" {
		payload.ServiceName = payload.DisplayNameHint
	}
	if payload.Namespace == "" {
		payload.Namespace = payload.NamespaceID
	}
	if payload.NamespaceID == "" {
		payload.NamespaceID = payload.Namespace
	}
}

func newShareInviteJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "si_" + fmt.Sprintf("%x", b[:]), nil
}

func authorityPublicKeyString(priv ed25519.PrivateKey) (string, error) {
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub))), nil
}

func sameAuthorizedKeyMaterial(a, b string) bool {
	ak, _, _, _, aerr := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(a)))
	bk, _, _, _, berr := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(b)))
	if aerr != nil || berr != nil {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	return string(ak.Marshal()) == string(bk.Marshal())
}
