package clusterinvite

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
)

const (
	TokenPrefix            = "tubo-invite-v1."
	Kind                   = "cluster-invite"
	MembershipGrantKind    = "cluster-membership-grant"
	Version                = "v1"
	RoleMember             = "member"
	RoleViewer             = "viewer"
	RoleGrantRequester     = "grant-requester"
	GrantRequestPermission = "grant:request"
)

type Grant struct {
	Role        string   `json:"role"`
	Permissions []string `json:"permissions"`
}

type GrantService struct {
	Protocol string   `json:"protocol"`
	Peers    []string `json:"peers"`
}

type NamespaceDiscoveryEntry struct {
	Version   string    `json:"version,omitempty"`
	Type      string    `json:"type"`
	KeyID     string    `json:"key_id"`
	Secret    string    `json:"secret"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type Payload struct {
	Version            string                   `json:"version"`
	Kind               string                   `json:"kind"`
	JTI                string                   `json:"jti"`
	ClusterName        string                   `json:"cluster_name"`
	ClusterID          string                   `json:"cluster_id"`
	AuthorityPublicKey string                   `json:"authority_public_key"`
	Namespace          string                   `json:"namespace"`
	Discovery          *NamespaceDiscoveryEntry `json:"discovery,omitempty"`
	MembershipToken    string                   `json:"membership_token,omitempty"`
	Grant              Grant                    `json:"grant"`
	GrantService       GrantService             `json:"grant_service,omitempty"`
	IssuedAt           time.Time                `json:"issued_at"`
	ExpiresAt          time.Time                `json:"expires_at"`
}

func GrantForRole(role string) (Grant, error) {
	switch strings.TrimSpace(strings.ToLower(role)) {
	case "", RoleMember:
		return Grant{Role: RoleMember, Permissions: []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}}, nil
	case RoleViewer:
		return Grant{Role: RoleViewer, Permissions: []string{capability.PermissionSubscribe, capability.PermissionList}}, nil
	case RoleGrantRequester:
		return Grant{Role: RoleGrantRequester, Permissions: []string{GrantRequestPermission}}, nil
	default:
		return Grant{}, fmt.Errorf("unsupported cluster invitation permission %q", role)
	}
}

func SignToken(payload Payload, priv ed25519.PrivateKey) (string, error) {
	if err := validateSignedPayload(payload); err != nil {
		return "", err
	}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payloadBytes)
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(payloadBytes) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func ParseAndVerifyToken(token string) (Payload, error) {
	return parseAndVerifyToken(token, validateSignedPayload)
}

func ParseAndVerifyClusterInviteToken(token string) (Payload, error) {
	return parseAndVerifyToken(token, ValidatePayload)
}

func ParseAndVerifyMembershipGrantToken(token string) (Payload, error) {
	return parseAndVerifyToken(token, ValidateMembershipGrantPayload)
}

func parseAndVerifyToken(token string, validate func(Payload) error) (Payload, error) {
	if !IsToken(token) {
		return Payload{}, fmt.Errorf("invalid cluster invite token")
	}
	encoded := strings.TrimPrefix(token, TokenPrefix)
	parts := strings.Split(encoded, ".")
	if len(parts) != 2 {
		return Payload{}, fmt.Errorf("invalid cluster invite token")
	}
	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Payload{}, fmt.Errorf("decode cluster invite payload: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Payload{}, fmt.Errorf("decode cluster invite signature: %w", err)
	}
	var payload Payload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		return Payload{}, fmt.Errorf("decode cluster invite payload json: %w", err)
	}
	if err := validate(payload); err != nil {
		return Payload{}, err
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(payload.AuthorityPublicKey))
	if err != nil {
		return Payload{}, fmt.Errorf("parse cluster invite authority public key: %w", err)
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		return Payload{}, errors.New("cluster invite authority key does not expose a crypto public key")
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return Payload{}, fmt.Errorf("cluster invite authority key is not ed25519: %T", cryptoPub.CryptoPublicKey())
	}
	if !ed25519.Verify(edPub, payloadBytes, sig) {
		return Payload{}, errors.New("invalid cluster invite signature")
	}
	return payload, nil
}

func ValidatePayload(payload Payload) error {
	if err := validateCommonPayload(payload, Kind); err != nil {
		return err
	}
	if payload.Discovery == nil {
		return errors.New("cluster invite is missing namespace discovery entry")
	}
	if err := ValidateNamespaceDiscoveryEntry(*payload.Discovery); err != nil {
		return fmt.Errorf("cluster invite discovery: %w", err)
	}
	return validateGrant(payload, "cluster invite")
}

func ValidateMembershipGrantPayload(payload Payload) error {
	if err := validateCommonPayload(payload, MembershipGrantKind); err != nil {
		return err
	}
	if payload.Discovery != nil {
		return errors.New("cluster membership grant must not contain namespace discovery entry")
	}
	if strings.TrimSpace(payload.MembershipToken) != "" {
		return errors.New("cluster membership grant must not contain nested membership token")
	}
	return validateGrant(payload, "cluster membership grant")
}

func MembershipGrantPayloadFromInvite(invite Payload) (Payload, error) {
	if err := ValidatePayload(invite); err != nil {
		return Payload{}, err
	}
	payload := invite
	payload.Kind = MembershipGrantKind
	payload.Discovery = nil
	payload.MembershipToken = ""
	if err := ValidateMembershipGrantPayload(payload); err != nil {
		return Payload{}, err
	}
	return payload, nil
}

func validateSignedPayload(payload Payload) error {
	switch payload.Kind {
	case Kind:
		return ValidatePayload(payload)
	case MembershipGrantKind:
		return ValidateMembershipGrantPayload(payload)
	default:
		return fmt.Errorf("unsupported cluster invite kind %q", payload.Kind)
	}
}

func validateCommonPayload(payload Payload, kind string) error {
	if payload.Version != Version {
		return fmt.Errorf("unsupported cluster invite version %q", payload.Version)
	}
	if payload.Kind != kind {
		return fmt.Errorf("unsupported cluster invite kind %q", payload.Kind)
	}
	if payload.ClusterName == "" || payload.ClusterID == "" || payload.AuthorityPublicKey == "" || payload.Namespace == "" || payload.JTI == "" {
		return errors.New("cluster invite is missing required fields")
	}
	if time.Now().UTC().After(payload.ExpiresAt.UTC()) {
		return errors.New("cluster invite expired")
	}
	if !payload.IssuedAt.IsZero() && payload.ExpiresAt.Before(payload.IssuedAt) {
		return errors.New("cluster invite expires before it was issued")
	}
	return nil
}

func validateGrant(payload Payload, label string) error {
	switch payload.Grant.Role {
	case RoleMember:
		if !stringSliceEqualSet(payload.Grant.Permissions, []string{capability.PermissionSubscribe, capability.PermissionList, capability.PermissionPublish, capability.PermissionConnect}) {
			return fmt.Errorf("%s member grant has invalid permissions", label)
		}
	case RoleViewer:
		if !stringSliceEqualSet(payload.Grant.Permissions, []string{capability.PermissionSubscribe, capability.PermissionList}) {
			return fmt.Errorf("%s viewer grant has invalid permissions", label)
		}
	case RoleGrantRequester:
		if !stringSliceEqualSet(payload.Grant.Permissions, []string{GrantRequestPermission}) {
			return fmt.Errorf("%s grant-requester has invalid permissions", label)
		}
		if payload.GrantService.Protocol != grantspkg.ProtocolID || len(payload.GrantService.Peers) == 0 {
			return fmt.Errorf("%s grant-requester is missing grant service metadata", label)
		}
		for _, peer := range payload.GrantService.Peers {
			if !strings.Contains(peer, "/p2p/") {
				return fmt.Errorf("cluster invite grant service peer %q is invalid", peer)
			}
		}
	default:
		return fmt.Errorf("%s is missing grant role", label)
	}
	return nil
}

func IsToken(token string) bool {
	return strings.HasPrefix(token, TokenPrefix)
}

func ValidateNamespaceDiscoveryEntry(entry NamespaceDiscoveryEntry) error {
	if strings.TrimSpace(entry.Type) != cfgpkg.SecretTypeNamespaceDiscovery {
		return fmt.Errorf("unsupported discovery entry type %q", entry.Type)
	}
	if strings.TrimSpace(entry.KeyID) == "" {
		return errors.New("discovery entry key id is required")
	}
	if strings.TrimSpace(entry.Secret) == "" {
		return errors.New("discovery entry secret is required")
	}
	secretBytes, err := base64.RawURLEncoding.DecodeString(entry.Secret)
	if err != nil {
		return fmt.Errorf("decode discovery entry secret: %w", err)
	}
	if len(secretBytes) != cfgpkg.NamespaceDiscoverySecretLength {
		return fmt.Errorf("discovery entry secret must be %d bytes", cfgpkg.NamespaceDiscoverySecretLength)
	}
	return nil
}

func AllowsPermissions(grant cfgpkg.ClusterMembershipGrant, clusterName, clusterID, namespace string, required ...string) bool {
	if grant.ClusterName != clusterName || grant.ClusterID != clusterID || grant.Namespace != namespace {
		return false
	}
	if grant.ExpiresAt.IsZero() || time.Now().UTC().After(grant.ExpiresAt.UTC()) {
		return false
	}
	return containsAll(grant.Permissions, required)
}

func MatchesAuthority(payload Payload, authorityPub ed25519.PublicKey) bool {
	if len(authorityPub) == 0 {
		return false
	}
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(payload.AuthorityPublicKey))
	if err != nil {
		return false
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		return false
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return false
	}
	return string(edPub) == string(authorityPub)
}

func containsAll(have []string, required []string) bool {
	if len(required) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, perm := range have {
		set[perm] = struct{}{}
	}
	for _, perm := range required {
		if _, ok := set[perm]; !ok {
			return false
		}
	}
	return true
}

func stringSliceEqualSet(have, want []string) bool {
	if len(have) != len(want) {
		return false
	}
	seen := make(map[string]int, len(have))
	for _, v := range have {
		seen[v]++
	}
	for _, v := range want {
		seen[v]--
		if seen[v] < 0 {
			return false
		}
	}
	return true
}
