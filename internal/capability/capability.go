package capability

import (
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

const (
	PermissionSubscribe = "subscribe"
	PermissionList      = "list"
	PermissionPublish   = "publish"
	PermissionConnect   = "connect"
	PermissionAttach    = "attach"
	PermissionAnnounce  = "announce"
)

type MembershipCapability struct {
	ClusterID     string
	NamespaceID   string
	SubjectPeerID string
	Permissions   []string
	ExpiresAt     time.Time
	Signature     []byte
}

type ServiceClaim struct {
	ClusterID     string
	NamespaceID   string
	ServiceID     string
	SubjectPeerID string
	Permissions   []string
	ExpiresAt     time.Time
	Signature     []byte
}

type ConnectCapability struct {
	ClusterID     string
	NamespaceID   string
	ServiceID     string
	SubjectPeerID string
	Permissions   []string
	ExpiresAt     time.Time
	Signature     []byte
}

type membershipPayload struct {
	Type          string   `json:"type"`
	ClusterID     string   `json:"cluster_id"`
	NamespaceID   string   `json:"namespace_id"`
	SubjectPeerID string   `json:"subject_peer_id"`
	Permissions   []string `json:"permissions"`
	ExpiresAt     string   `json:"expires_at"`
}

type serviceClaimPayload struct {
	Type          string   `json:"type"`
	ClusterID     string   `json:"cluster_id"`
	NamespaceID   string   `json:"namespace_id"`
	ServiceID     string   `json:"service_id"`
	SubjectPeerID string   `json:"subject_peer_id"`
	Permissions   []string `json:"permissions"`
	ExpiresAt     string   `json:"expires_at"`
}

type connectPayload struct {
	Type          string   `json:"type"`
	ClusterID     string   `json:"cluster_id"`
	NamespaceID   string   `json:"namespace_id"`
	ServiceID     string   `json:"service_id"`
	SubjectPeerID string   `json:"subject_peer_id"`
	Permissions   []string `json:"permissions"`
	ExpiresAt     string   `json:"expires_at"`
}

type canonicalMembership struct {
	payload membershipPayload
}

type canonicalServiceClaim struct {
	payload serviceClaimPayload
}

type canonicalConnectCapability struct {
	payload connectPayload
}

func SignMembershipCapability(cap MembershipCapability, privateKey ed25519.PrivateKey) (MembershipCapability, error) {
	payload := normalizeMembership(cap)
	sig, err := signCanonical(payload, privateKey)
	if err != nil {
		return MembershipCapability{}, err
	}
	cap.Permissions = payload.payload.Permissions
	cap.ExpiresAt = cap.ExpiresAt.UTC()
	cap.Signature = sig
	return cap, nil
}

func VerifyMembershipCapability(cap MembershipCapability, publicKey ed25519.PublicKey, clusterID, namespaceID, subjectPeerID string) error {
	if err := verifyCanonical(normalizeMembership(cap), cap.Signature, publicKey); err != nil {
		return err
	}
	return validateMembershipScope(cap, clusterID, namespaceID, subjectPeerID)
}

func SignServiceClaim(cap ServiceClaim, privateKey ed25519.PrivateKey) (ServiceClaim, error) {
	payload := normalizeServiceClaim(cap)
	sig, err := signCanonical(payload, privateKey)
	if err != nil {
		return ServiceClaim{}, err
	}
	cap.Permissions = payload.payload.Permissions
	cap.ExpiresAt = cap.ExpiresAt.UTC()
	cap.Signature = sig
	return cap, nil
}

func VerifyServiceClaim(cap ServiceClaim, publicKey ed25519.PublicKey, clusterID, namespaceID, serviceID, subjectPeerID string) error {
	if err := verifyCanonical(normalizeServiceClaim(cap), cap.Signature, publicKey); err != nil {
		return err
	}
	return validateServiceClaimScope(cap, clusterID, namespaceID, serviceID, subjectPeerID)
}

func SignConnectCapability(cap ConnectCapability, privateKey ed25519.PrivateKey) (ConnectCapability, error) {
	payload := normalizeConnectCapability(cap)
	sig, err := signCanonical(payload, privateKey)
	if err != nil {
		return ConnectCapability{}, err
	}
	cap.Permissions = payload.payload.Permissions
	cap.ExpiresAt = cap.ExpiresAt.UTC()
	cap.Signature = sig
	return cap, nil
}

func VerifyConnectCapability(cap ConnectCapability, publicKey ed25519.PublicKey, clusterID, namespaceID, serviceID, subjectPeerID string) error {
	if err := verifyCanonical(normalizeConnectCapability(cap), cap.Signature, publicKey); err != nil {
		return err
	}
	return validateConnectScope(cap, clusterID, namespaceID, serviceID, subjectPeerID)
}

func normalizeMembership(cap MembershipCapability) canonicalMembership {
	return canonicalMembership{payload: membershipPayload{
		Type:          "membership",
		ClusterID:     cap.ClusterID,
		NamespaceID:   cap.NamespaceID,
		SubjectPeerID: cap.SubjectPeerID,
		Permissions:   canonicalPermissions(cap.Permissions),
		ExpiresAt:     canonicalTime(cap.ExpiresAt),
	}}
}

func normalizeServiceClaim(cap ServiceClaim) canonicalServiceClaim {
	return canonicalServiceClaim{payload: serviceClaimPayload{
		Type:          "service-claim",
		ClusterID:     cap.ClusterID,
		NamespaceID:   cap.NamespaceID,
		ServiceID:     cap.ServiceID,
		SubjectPeerID: cap.SubjectPeerID,
		Permissions:   canonicalPermissions(cap.Permissions),
		ExpiresAt:     canonicalTime(cap.ExpiresAt),
	}}
}

func normalizeConnectCapability(cap ConnectCapability) canonicalConnectCapability {
	return canonicalConnectCapability{payload: connectPayload{
		Type:          "connect",
		ClusterID:     cap.ClusterID,
		NamespaceID:   cap.NamespaceID,
		ServiceID:     cap.ServiceID,
		SubjectPeerID: cap.SubjectPeerID,
		Permissions:   canonicalPermissions(cap.Permissions),
		ExpiresAt:     canonicalTime(cap.ExpiresAt),
	}}
}

func canonicalPermissions(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := append([]string(nil), in...)
	sort.Strings(out)
	uniq := out[:0]
	var prev string
	for i, perm := range out {
		if i == 0 || perm != prev {
			uniq = append(uniq, perm)
			prev = perm
		}
	}
	return uniq
}

func canonicalTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func signCanonical(v any, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) == 0 {
		return nil, errors.New("private key is required")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(privateKey, b), nil
}

func verifyCanonical(v any, signature []byte, publicKey ed25519.PublicKey) error {
	if len(publicKey) == 0 {
		return errors.New("public key is required")
	}
	if len(signature) == 0 {
		return errors.New("signature is required")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, b, signature) {
		return errors.New("invalid signature")
	}
	return nil
}

func validateMembershipScope(cap MembershipCapability, clusterID, namespaceID, subjectPeerID string) error {
	if err := validateTime(cap.ExpiresAt); err != nil {
		return err
	}
	if cap.ClusterID != clusterID {
		return fmt.Errorf("cluster id mismatch: got %q want %q", cap.ClusterID, clusterID)
	}
	if cap.NamespaceID != namespaceID {
		return fmt.Errorf("namespace id mismatch: got %q want %q", cap.NamespaceID, namespaceID)
	}
	if cap.SubjectPeerID != subjectPeerID {
		return fmt.Errorf("subject peer id mismatch: got %q want %q", cap.SubjectPeerID, subjectPeerID)
	}
	if !hasAllPermissions(cap.Permissions, []string{PermissionSubscribe, PermissionList, PermissionPublish}) {
		return fmt.Errorf("missing required permissions")
	}
	return nil
}

func validateServiceClaimScope(cap ServiceClaim, clusterID, namespaceID, serviceID, subjectPeerID string) error {
	if err := validateTime(cap.ExpiresAt); err != nil {
		return err
	}
	if cap.ClusterID != clusterID {
		return fmt.Errorf("cluster id mismatch: got %q want %q", cap.ClusterID, clusterID)
	}
	if cap.NamespaceID != namespaceID {
		return fmt.Errorf("namespace id mismatch: got %q want %q", cap.NamespaceID, namespaceID)
	}
	if cap.ServiceID != serviceID {
		return fmt.Errorf("service id mismatch: got %q want %q", cap.ServiceID, serviceID)
	}
	if cap.SubjectPeerID != subjectPeerID {
		return fmt.Errorf("subject peer id mismatch: got %q want %q", cap.SubjectPeerID, subjectPeerID)
	}
	if !hasAllPermissions(cap.Permissions, []string{PermissionAttach, PermissionAnnounce}) {
		return fmt.Errorf("missing required permissions")
	}
	return nil
}

func validateConnectScope(cap ConnectCapability, clusterID, namespaceID, serviceID, subjectPeerID string) error {
	if err := validateTime(cap.ExpiresAt); err != nil {
		return err
	}
	if cap.ClusterID != clusterID {
		return fmt.Errorf("cluster id mismatch: got %q want %q", cap.ClusterID, clusterID)
	}
	if cap.NamespaceID != namespaceID {
		return fmt.Errorf("namespace id mismatch: got %q want %q", cap.NamespaceID, namespaceID)
	}
	if cap.ServiceID != serviceID {
		return fmt.Errorf("service id mismatch: got %q want %q", cap.ServiceID, serviceID)
	}
	if cap.SubjectPeerID != subjectPeerID {
		return fmt.Errorf("subject peer id mismatch: got %q want %q", cap.SubjectPeerID, subjectPeerID)
	}
	if !hasAllPermissions(cap.Permissions, []string{PermissionConnect}) {
		return fmt.Errorf("missing required permissions")
	}
	return nil
}

func validateTime(t time.Time) error {
	if t.IsZero() {
		return errors.New("expires_at is required")
	}
	if time.Now().UTC().After(t.UTC()) {
		return errors.New("capability expired")
	}
	return nil
}

func hasAllPermissions(have []string, want []string) bool {
	set := make(map[string]struct{}, len(have))
	for _, perm := range have {
		set[perm] = struct{}{}
	}
	for _, perm := range want {
		if _, ok := set[perm]; !ok {
			return false
		}
	}
	return true
}
