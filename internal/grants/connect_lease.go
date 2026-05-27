package grants

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/ssh"
)

const (
	ConnectAccessLeaseVersion = "v1"
	ConnectAccessLeaseKind    = "connect-access-lease"
	ConnectRefreshLeaseKind   = "connect-refresh-lease"

	DefaultConnectAccessLeaseTTL  = 10 * time.Minute
	DefaultConnectRefreshLeaseTTL = 48 * time.Hour
)

type ConnectAccessLease struct {
	Version                string    `json:"version"`
	Kind                   string    `json:"kind"`
	JTI                    string    `json:"jti"`
	SessionID              string    `json:"session_id"`
	ShareInviteJTI         string    `json:"share_invite_jti,omitempty"`
	ClusterID              string    `json:"cluster_id"`
	NamespaceID            string    `json:"namespace_id"`
	ServiceID              string    `json:"service_id"`
	ClientPublicKey        string    `json:"client_public_key"`
	ClientKeyThumbprint    string    `json:"client_key_thumbprint"`
	AccessEpoch            int64     `json:"access_epoch,omitempty"`
	Permissions            []string  `json:"permissions"`
	DelegationPublishLease []byte    `json:"delegation_publish_lease,omitempty"`
	IssuedAt               time.Time `json:"issued_at"`
	ExpiresAt              time.Time `json:"expires_at"`
	Signature              []byte    `json:"signature,omitempty"`
}

type ConnectRefreshLease struct {
	Version                string    `json:"version"`
	Kind                   string    `json:"kind"`
	JTI                    string    `json:"jti"`
	SessionID              string    `json:"session_id"`
	ShareInviteJTI         string    `json:"share_invite_jti,omitempty"`
	ClusterID              string    `json:"cluster_id"`
	NamespaceID            string    `json:"namespace_id"`
	ServiceID              string    `json:"service_id"`
	ClientPublicKey        string    `json:"client_public_key"`
	ClientKeyThumbprint    string    `json:"client_key_thumbprint"`
	AccessEpoch            int64     `json:"access_epoch,omitempty"`
	Permissions            []string  `json:"permissions"`
	DelegationPublishLease []byte    `json:"delegation_publish_lease,omitempty"`
	IssuedAt               time.Time `json:"issued_at"`
	ExpiresAt              time.Time `json:"expires_at"`
	Signature              []byte    `json:"signature,omitempty"`
}

type ConnectLeaseArtifacts struct {
	AccessLease  ConnectAccessLease  `json:"access_lease"`
	RefreshLease ConnectRefreshLease `json:"refresh_lease"`
}

type canonicalConnectLease struct {
	Version             string   `json:"version"`
	Kind                string   `json:"kind"`
	JTI                 string   `json:"jti"`
	SessionID           string   `json:"session_id"`
	ShareInviteJTI      string   `json:"share_invite_jti,omitempty"`
	ClusterID           string   `json:"cluster_id"`
	NamespaceID         string   `json:"namespace_id"`
	ServiceID           string   `json:"service_id"`
	ClientPublicKey     string   `json:"client_public_key"`
	ClientKeyThumbprint string   `json:"client_key_thumbprint"`
	AccessEpoch         int64    `json:"access_epoch,omitempty"`
	Permissions         []string `json:"permissions"`
	DelegationHash      string   `json:"delegation_hash,omitempty"`
	IssuedAt            string   `json:"issued_at"`
	ExpiresAt           string   `json:"expires_at"`
}

func BuildConnectLeaseArtifacts(priv ed25519.PrivateKey, invite ServiceSharePayload, clientPublicKey string, accessTTL, refreshTTL time.Duration) (ConnectLeaseArtifacts, error) {
	if len(priv) == 0 {
		return ConnectLeaseArtifacts{}, errors.New("private key is required")
	}
	if accessTTL <= 0 {
		accessTTL = DefaultConnectAccessLeaseTTL
	}
	if refreshTTL <= 0 {
		refreshTTL = DefaultConnectRefreshLeaseTTL
	}
	if refreshTTL < accessTTL {
		accessTTL = refreshTTL
	}
	thumbprint, err := ConnectClientKeyThumbprint(clientPublicKey)
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	now := time.Now().UTC()
	if !invite.ExpiresAt.IsZero() && now.After(invite.ExpiresAt.UTC()) {
		return ConnectLeaseArtifacts{}, errors.New("share invite expired")
	}
	sessionID, err := newConnectLeaseJTI("cs")
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	refreshJTI, err := newConnectLeaseJTI("cr")
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	accessJTI, err := newConnectLeaseJTI("ca")
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	refresh := ConnectRefreshLease{
		Version:             ConnectAccessLeaseVersion,
		Kind:                ConnectRefreshLeaseKind,
		JTI:                 refreshJTI,
		SessionID:           sessionID,
		ShareInviteJTI:      invite.JTI,
		ClusterID:           invite.ClusterID,
		NamespaceID:         invite.NamespaceID,
		ServiceID:           invite.TargetServiceID,
		ClientPublicKey:     strings.TrimSpace(clientPublicKey),
		ClientKeyThumbprint: thumbprint,
		AccessEpoch:         invite.AccessEpoch,
		Permissions:         []string{capability.PermissionConnect},
		IssuedAt:            now,
		ExpiresAt:           now.Add(refreshTTL),
	}
	refresh, err = SignConnectRefreshLease(refresh, priv)
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	access, err := signConnectAccessForRefresh(priv, refresh, accessJTI, accessTTL, now)
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	return ConnectLeaseArtifacts{AccessLease: access, RefreshLease: refresh}, nil
}

func BuildDelegatedConnectLeaseArtifacts(authorityPub ed25519.PublicKey, ownerPriv ed25519.PrivateKey, delegation PublishLease, shareInviteJTI, clientPublicKey string, accessEpoch int64, accessTTL, refreshTTL time.Duration) (ConnectLeaseArtifacts, error) {
	if len(ownerPriv) == 0 {
		return ConnectLeaseArtifacts{}, errors.New("service owner private key is required")
	}
	if err := VerifyPublishLease(delegation, authorityPub, delegation.ClusterID, delegation.NamespaceID, delegation.ServiceID, delegation.PublisherPeerID); err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	if !publishLeaseAllowsDelegatedConnect(delegation.RequestedCapabilities) {
		return ConnectLeaseArtifacts{}, errors.New("publish lease does not allow delegated connect lease minting")
	}
	if accessTTL <= 0 {
		accessTTL = DefaultConnectAccessLeaseTTL
	}
	if refreshTTL <= 0 {
		refreshTTL = DefaultConnectRefreshLeaseTTL
	}
	if refreshTTL < accessTTL {
		accessTTL = refreshTTL
	}
	delegationBytes, err := json.Marshal(delegation)
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	thumbprint, err := ConnectClientKeyThumbprint(clientPublicKey)
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	now := time.Now().UTC()
	if now.After(delegation.ExpiresAt.UTC()) {
		return ConnectLeaseArtifacts{}, errors.New("publish lease expired")
	}
	sessionID, err := newConnectLeaseJTI("cs")
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	refreshJTI, err := newConnectLeaseJTI("cr")
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	accessJTI, err := newConnectLeaseJTI("ca")
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	refresh := ConnectRefreshLease{
		Version:                ConnectAccessLeaseVersion,
		Kind:                   ConnectRefreshLeaseKind,
		JTI:                    refreshJTI,
		SessionID:              sessionID,
		ShareInviteJTI:         shareInviteJTI,
		ClusterID:              delegation.ClusterID,
		NamespaceID:            delegation.NamespaceID,
		ServiceID:              delegation.ServiceID,
		ClientPublicKey:        strings.TrimSpace(clientPublicKey),
		ClientKeyThumbprint:    thumbprint,
		AccessEpoch:            accessEpoch,
		Permissions:            []string{capability.PermissionConnect},
		DelegationPublishLease: append([]byte(nil), delegationBytes...),
		IssuedAt:               now,
		ExpiresAt:              minTime(now.Add(refreshTTL), delegation.ExpiresAt.UTC()),
	}
	refresh, err = SignConnectRefreshLease(refresh, ownerPriv)
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	access, err := signConnectAccessForRefresh(ownerPriv, refresh, accessJTI, accessTTL, now)
	if err != nil {
		return ConnectLeaseArtifacts{}, err
	}
	return ConnectLeaseArtifacts{AccessLease: access, RefreshLease: refresh}, nil
}

func RefreshConnectAccessLease(priv ed25519.PrivateKey, refresh ConnectRefreshLease, accessTTL time.Duration) (ConnectAccessLease, error) {
	if len(priv) == 0 {
		return ConnectAccessLease{}, errors.New("private key is required")
	}
	pub := priv.Public().(ed25519.PublicKey)
	if err := VerifyConnectRefreshLease(refresh, pub, refresh.ClusterID, refresh.NamespaceID, refresh.ServiceID); err != nil {
		return ConnectAccessLease{}, err
	}
	if accessTTL <= 0 {
		accessTTL = DefaultConnectAccessLeaseTTL
	}
	jti, err := newConnectLeaseJTI("ca")
	if err != nil {
		return ConnectAccessLease{}, err
	}
	return signConnectAccessForRefresh(priv, refresh, jti, accessTTL, time.Now().UTC())
}

func RefreshDelegatedConnectAccessLease(authorityPub ed25519.PublicKey, ownerPriv ed25519.PrivateKey, refresh ConnectRefreshLease, accessTTL time.Duration, servicePeerID string) (ConnectAccessLease, error) {
	if len(ownerPriv) == 0 {
		return ConnectAccessLease{}, errors.New("service owner private key is required")
	}
	if err := VerifyDelegatedConnectRefreshLease(refresh, authorityPub, refresh.ClusterID, refresh.NamespaceID, refresh.ServiceID, servicePeerID); err != nil {
		return ConnectAccessLease{}, err
	}
	if accessTTL <= 0 {
		accessTTL = DefaultConnectAccessLeaseTTL
	}
	jti, err := newConnectLeaseJTI("ca")
	if err != nil {
		return ConnectAccessLease{}, err
	}
	return signConnectAccessForRefresh(ownerPriv, refresh, jti, accessTTL, time.Now().UTC())
}

func SignConnectAccessLease(lease ConnectAccessLease, priv ed25519.PrivateKey) (ConnectAccessLease, error) {
	if len(priv) == 0 {
		return ConnectAccessLease{}, errors.New("private key is required")
	}
	lease.Version = ConnectAccessLeaseVersion
	lease.Kind = ConnectAccessLeaseKind
	lease.ClientPublicKey = strings.TrimSpace(lease.ClientPublicKey)
	thumbprint, err := ConnectClientKeyThumbprint(lease.ClientPublicKey)
	if err != nil {
		return ConnectAccessLease{}, err
	}
	lease.ClientKeyThumbprint = thumbprint
	lease.Permissions = canonicalConnectLeasePermissions(lease.Permissions)
	lease.IssuedAt = lease.IssuedAt.UTC()
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	payload, err := canonicalAccessLeaseBytes(lease)
	if err != nil {
		return ConnectAccessLease{}, err
	}
	lease.Signature = ed25519.Sign(priv, payload)
	return lease, nil
}

func SignConnectRefreshLease(lease ConnectRefreshLease, priv ed25519.PrivateKey) (ConnectRefreshLease, error) {
	if len(priv) == 0 {
		return ConnectRefreshLease{}, errors.New("private key is required")
	}
	lease.Version = ConnectAccessLeaseVersion
	lease.Kind = ConnectRefreshLeaseKind
	lease.ClientPublicKey = strings.TrimSpace(lease.ClientPublicKey)
	thumbprint, err := ConnectClientKeyThumbprint(lease.ClientPublicKey)
	if err != nil {
		return ConnectRefreshLease{}, err
	}
	lease.ClientKeyThumbprint = thumbprint
	lease.Permissions = canonicalConnectLeasePermissions(lease.Permissions)
	lease.IssuedAt = lease.IssuedAt.UTC()
	lease.ExpiresAt = lease.ExpiresAt.UTC()
	payload, err := canonicalRefreshLeaseBytes(lease)
	if err != nil {
		return ConnectRefreshLease{}, err
	}
	lease.Signature = ed25519.Sign(priv, payload)
	return lease, nil
}

func VerifyConnectAccessLease(lease ConnectAccessLease, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID string) error {
	if lease.Version != ConnectAccessLeaseVersion {
		return fmt.Errorf("unsupported connect access lease version %q", lease.Version)
	}
	if lease.Kind != ConnectAccessLeaseKind {
		return fmt.Errorf("unsupported connect access lease kind %q", lease.Kind)
	}
	return verifyConnectLease(canonicalAccessLeaseBytes, lease, authorityPub, clusterID, namespaceID, serviceID, delegationHash(lease.DelegationPublishLease))
}

func VerifyDelegatedConnectAccessLease(lease ConnectAccessLease, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error {
	if len(lease.DelegationPublishLease) == 0 {
		return VerifyConnectAccessLease(lease, authorityPub, clusterID, namespaceID, serviceID)
	}
	delegation, err := ParseAndVerifyPublishLeaseBytes(lease.DelegationPublishLease, authorityPub, clusterID, namespaceID, serviceID, servicePeerID)
	if err != nil {
		return err
	}
	if !publishLeaseAllowsDelegatedConnect(delegation.RequestedCapabilities) {
		return errors.New("publish lease does not allow delegated connect lease minting")
	}
	signerPub, err := serviceidentity.DecodePublicKey(delegation.ServicePublicKey)
	if err != nil {
		return err
	}
	return verifyConnectLease(canonicalAccessLeaseBytes, lease, signerPub, clusterID, namespaceID, serviceID, delegationHash(lease.DelegationPublishLease))
}

func VerifyConnectRefreshLease(lease ConnectRefreshLease, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID string) error {
	if lease.Version != ConnectAccessLeaseVersion {
		return fmt.Errorf("unsupported connect refresh lease version %q", lease.Version)
	}
	if lease.Kind != ConnectRefreshLeaseKind {
		return fmt.Errorf("unsupported connect refresh lease kind %q", lease.Kind)
	}
	return verifyConnectLease(canonicalRefreshLeaseBytes, lease, authorityPub, clusterID, namespaceID, serviceID, delegationHash(lease.DelegationPublishLease))
}

func VerifyDelegatedConnectRefreshLease(lease ConnectRefreshLease, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID, servicePeerID string) error {
	if len(lease.DelegationPublishLease) == 0 {
		return VerifyConnectRefreshLease(lease, authorityPub, clusterID, namespaceID, serviceID)
	}
	delegation, err := ParseAndVerifyPublishLeaseBytes(lease.DelegationPublishLease, authorityPub, clusterID, namespaceID, serviceID, servicePeerID)
	if err != nil {
		return err
	}
	if !publishLeaseAllowsDelegatedConnect(delegation.RequestedCapabilities) {
		return errors.New("publish lease does not allow delegated connect lease minting")
	}
	signerPub, err := serviceidentity.DecodePublicKey(delegation.ServicePublicKey)
	if err != nil {
		return err
	}
	return verifyConnectLease(canonicalRefreshLeaseBytes, lease, signerPub, clusterID, namespaceID, serviceID, delegationHash(lease.DelegationPublishLease))
}

func ConnectClientKeyThumbprint(clientPublicKey string) (string, error) {
	pubKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(strings.TrimSpace(clientPublicKey)))
	if err != nil {
		return "", fmt.Errorf("parse connect client public key: %w", err)
	}
	cryptoPub, ok := pubKey.(ssh.CryptoPublicKey)
	if !ok {
		return "", errors.New("connect client key does not expose a crypto public key")
	}
	edPub, ok := cryptoPub.CryptoPublicKey().(ed25519.PublicKey)
	if !ok {
		return "", fmt.Errorf("connect client key is not ed25519: %T", cryptoPub.CryptoPublicKey())
	}
	h := sha256.Sum256(edPub)
	return base64.RawURLEncoding.EncodeToString(h[:]), nil
}

func ConnectAccessLeaseHashBytes(b []byte) []byte {
	h := sha256.Sum256(bytes.TrimSpace(b))
	return h[:]
}

func MarshalConnectAccessLease(lease ConnectAccessLease) ([]byte, error) {
	return json.Marshal(lease)
}

func signConnectAccessForRefresh(priv ed25519.PrivateKey, refresh ConnectRefreshLease, jti string, accessTTL time.Duration, now time.Time) (ConnectAccessLease, error) {
	expiresAt := now.UTC().Add(accessTTL)
	if expiresAt.After(refresh.ExpiresAt.UTC()) {
		expiresAt = refresh.ExpiresAt.UTC()
	}
	if !now.UTC().Before(refresh.ExpiresAt.UTC()) {
		return ConnectAccessLease{}, errors.New("connect refresh lease expired; ask the service owner for a fresh token/invite")
	}
	return SignConnectAccessLease(ConnectAccessLease{
		JTI:                    jti,
		SessionID:              refresh.SessionID,
		ShareInviteJTI:         refresh.ShareInviteJTI,
		ClusterID:              refresh.ClusterID,
		NamespaceID:            refresh.NamespaceID,
		ServiceID:              refresh.ServiceID,
		ClientPublicKey:        refresh.ClientPublicKey,
		ClientKeyThumbprint:    refresh.ClientKeyThumbprint,
		AccessEpoch:            refresh.AccessEpoch,
		Permissions:            []string{capability.PermissionConnect},
		DelegationPublishLease: append([]byte(nil), refresh.DelegationPublishLease...),
		IssuedAt:               now.UTC(),
		ExpiresAt:              expiresAt,
	}, priv)
}

func verifyConnectLease[T ConnectAccessLease | ConnectRefreshLease](canonical func(T) ([]byte, error), lease T, signerPub ed25519.PublicKey, clusterID, namespaceID, serviceID, expectedDelegationHash string) error {
	view := any(lease)
	var version, kind, actualCluster, actualNamespace, actualService, clientPublicKey, clientThumbprint, actualDelegationHash string
	var permissions []string
	var expiresAt time.Time
	var signature []byte
	switch v := view.(type) {
	case ConnectAccessLease:
		version, kind = v.Version, v.Kind
		actualCluster, actualNamespace, actualService = v.ClusterID, v.NamespaceID, v.ServiceID
		clientPublicKey, clientThumbprint = v.ClientPublicKey, v.ClientKeyThumbprint
		permissions, expiresAt, signature = v.Permissions, v.ExpiresAt, v.Signature
		actualDelegationHash = delegationHash(v.DelegationPublishLease)
	case ConnectRefreshLease:
		version, kind = v.Version, v.Kind
		actualCluster, actualNamespace, actualService = v.ClusterID, v.NamespaceID, v.ServiceID
		clientPublicKey, clientThumbprint = v.ClientPublicKey, v.ClientKeyThumbprint
		permissions, expiresAt, signature = v.Permissions, v.ExpiresAt, v.Signature
		actualDelegationHash = delegationHash(v.DelegationPublishLease)
	default:
		return errors.New("unsupported connect lease type")
	}
	if version == "" || kind == "" {
		return errors.New("connect lease version and kind are required")
	}
	if actualCluster != clusterID {
		return fmt.Errorf("connect lease cluster id mismatch: got %q want %q", actualCluster, clusterID)
	}
	if actualNamespace != namespaceID {
		return fmt.Errorf("connect lease namespace id mismatch: got %q want %q", actualNamespace, namespaceID)
	}
	if actualService != serviceID {
		return fmt.Errorf("connect lease service id mismatch: got %q want %q", actualService, serviceID)
	}
	if expectedDelegationHash != actualDelegationHash {
		return errors.New("connect lease delegation mismatch")
	}
	if !hasConnectPermission(permissions) {
		return errors.New("connect lease missing connect permission")
	}
	if expiresAt.IsZero() {
		return errors.New("connect lease expires_at is required")
	}
	if time.Now().UTC().After(expiresAt.UTC()) {
		return errors.New("connect lease expired")
	}
	thumbprint, err := ConnectClientKeyThumbprint(clientPublicKey)
	if err != nil {
		return err
	}
	if clientThumbprint != thumbprint {
		return errors.New("connect lease client key thumbprint mismatch")
	}
	payload, err := canonical(lease)
	if err != nil {
		return err
	}
	if len(signature) == 0 {
		return errors.New("connect lease signature is required")
	}
	if !ed25519.Verify(signerPub, payload, signature) {
		return errors.New("invalid connect lease signature")
	}
	return nil
}

func canonicalAccessLeaseBytes(lease ConnectAccessLease) ([]byte, error) {
	return json.Marshal(canonicalConnectLease{
		Version:             lease.Version,
		Kind:                lease.Kind,
		JTI:                 lease.JTI,
		SessionID:           lease.SessionID,
		ShareInviteJTI:      lease.ShareInviteJTI,
		ClusterID:           lease.ClusterID,
		NamespaceID:         lease.NamespaceID,
		ServiceID:           lease.ServiceID,
		ClientPublicKey:     strings.TrimSpace(lease.ClientPublicKey),
		ClientKeyThumbprint: lease.ClientKeyThumbprint,
		AccessEpoch:         lease.AccessEpoch,
		Permissions:         canonicalConnectLeasePermissions(lease.Permissions),
		DelegationHash:      delegationHash(lease.DelegationPublishLease),
		IssuedAt:            canonicalConnectLeaseTime(lease.IssuedAt),
		ExpiresAt:           canonicalConnectLeaseTime(lease.ExpiresAt),
	})
}

func canonicalRefreshLeaseBytes(lease ConnectRefreshLease) ([]byte, error) {
	return json.Marshal(canonicalConnectLease{
		Version:             lease.Version,
		Kind:                lease.Kind,
		JTI:                 lease.JTI,
		SessionID:           lease.SessionID,
		ShareInviteJTI:      lease.ShareInviteJTI,
		ClusterID:           lease.ClusterID,
		NamespaceID:         lease.NamespaceID,
		ServiceID:           lease.ServiceID,
		ClientPublicKey:     strings.TrimSpace(lease.ClientPublicKey),
		ClientKeyThumbprint: lease.ClientKeyThumbprint,
		AccessEpoch:         lease.AccessEpoch,
		Permissions:         canonicalConnectLeasePermissions(lease.Permissions),
		DelegationHash:      delegationHash(lease.DelegationPublishLease),
		IssuedAt:            canonicalConnectLeaseTime(lease.IssuedAt),
		ExpiresAt:           canonicalConnectLeaseTime(lease.ExpiresAt),
	})
}

func canonicalConnectLeasePermissions(in []string) []string {
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

func canonicalConnectLeaseTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func hasConnectPermission(perms []string) bool {
	for _, perm := range perms {
		if perm == capability.PermissionConnect {
			return true
		}
	}
	return false
}

func delegationHash(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	h := sha256.Sum256(bytes.TrimSpace(b))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func publishLeaseAllowsDelegatedConnect(perms []string) bool {
	for _, perm := range perms {
		if perm == capability.PermissionShareMint || perm == capability.PermissionConnect {
			return true
		}
	}
	return false
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func newConnectLeaseJTI(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + fmt.Sprintf("%x", b[:]), nil
}
