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
	Version             string    `json:"version"`
	Kind                string    `json:"kind"`
	JTI                 string    `json:"jti"`
	SessionID           string    `json:"session_id"`
	ShareInviteJTI      string    `json:"share_invite_jti,omitempty"`
	ClusterID           string    `json:"cluster_id"`
	NamespaceID         string    `json:"namespace_id"`
	ServiceID           string    `json:"service_id"`
	ClientPublicKey     string    `json:"client_public_key"`
	ClientKeyThumbprint string    `json:"client_key_thumbprint"`
	Permissions         []string  `json:"permissions"`
	IssuedAt            time.Time `json:"issued_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	Signature           []byte    `json:"signature,omitempty"`
}

type ConnectRefreshLease struct {
	Version             string    `json:"version"`
	Kind                string    `json:"kind"`
	JTI                 string    `json:"jti"`
	SessionID           string    `json:"session_id"`
	ShareInviteJTI      string    `json:"share_invite_jti,omitempty"`
	ClusterID           string    `json:"cluster_id"`
	NamespaceID         string    `json:"namespace_id"`
	ServiceID           string    `json:"service_id"`
	ClientPublicKey     string    `json:"client_public_key"`
	ClientKeyThumbprint string    `json:"client_key_thumbprint"`
	Permissions         []string  `json:"permissions"`
	IssuedAt            time.Time `json:"issued_at"`
	ExpiresAt           time.Time `json:"expires_at"`
	Signature           []byte    `json:"signature,omitempty"`
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
	Permissions         []string `json:"permissions"`
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
	return verifyConnectLease(canonicalAccessLeaseBytes, lease, authorityPub, clusterID, namespaceID, serviceID)
}

func VerifyConnectRefreshLease(lease ConnectRefreshLease, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID string) error {
	if lease.Version != ConnectAccessLeaseVersion {
		return fmt.Errorf("unsupported connect refresh lease version %q", lease.Version)
	}
	if lease.Kind != ConnectRefreshLeaseKind {
		return fmt.Errorf("unsupported connect refresh lease kind %q", lease.Kind)
	}
	return verifyConnectLease(canonicalRefreshLeaseBytes, lease, authorityPub, clusterID, namespaceID, serviceID)
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
		JTI:                 jti,
		SessionID:           refresh.SessionID,
		ShareInviteJTI:      refresh.ShareInviteJTI,
		ClusterID:           refresh.ClusterID,
		NamespaceID:         refresh.NamespaceID,
		ServiceID:           refresh.ServiceID,
		ClientPublicKey:     refresh.ClientPublicKey,
		ClientKeyThumbprint: refresh.ClientKeyThumbprint,
		Permissions:         []string{capability.PermissionConnect},
		IssuedAt:            now.UTC(),
		ExpiresAt:           expiresAt,
	}, priv)
}

func verifyConnectLease[T ConnectAccessLease | ConnectRefreshLease](canonical func(T) ([]byte, error), lease T, authorityPub ed25519.PublicKey, clusterID, namespaceID, serviceID string) error {
	view := any(lease)
	var version, kind, actualCluster, actualNamespace, actualService, clientPublicKey, clientThumbprint string
	var permissions []string
	var expiresAt time.Time
	var signature []byte
	switch v := view.(type) {
	case ConnectAccessLease:
		version, kind = v.Version, v.Kind
		actualCluster, actualNamespace, actualService = v.ClusterID, v.NamespaceID, v.ServiceID
		clientPublicKey, clientThumbprint = v.ClientPublicKey, v.ClientKeyThumbprint
		permissions, expiresAt, signature = v.Permissions, v.ExpiresAt, v.Signature
	case ConnectRefreshLease:
		version, kind = v.Version, v.Kind
		actualCluster, actualNamespace, actualService = v.ClusterID, v.NamespaceID, v.ServiceID
		clientPublicKey, clientThumbprint = v.ClientPublicKey, v.ClientKeyThumbprint
		permissions, expiresAt, signature = v.Permissions, v.ExpiresAt, v.Signature
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
	if !ed25519.Verify(authorityPub, payload, signature) {
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
		Permissions:         canonicalConnectLeasePermissions(lease.Permissions),
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
		Permissions:         canonicalConnectLeasePermissions(lease.Permissions),
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

func newConnectLeaseJTI(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + fmt.Sprintf("%x", b[:]), nil
}
