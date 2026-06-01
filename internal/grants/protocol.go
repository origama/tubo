package grants

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/origama/tubo/internal/capability"
)

const (
	ProtocolID = "/tubo/grants/1.0"
	VersionV1  = "v1"

	TypeSubmit   = "grant_request.submit"
	TypePoll     = "grant_request.poll"
	TypePending  = "grant_request.pending"
	TypeApproved = "grant_request.approved"
	TypeDenied   = "grant_request.denied"
	TypeExpired  = "grant_request.expired"

	TypeShareRedeem      = "share_invite.redeem"
	TypeShareMintRequest = "share_invite.mint"
	TypeShareMintGranted = "share_invite.granted"
	TypeConnectRequest   = "connect_lease.request"
	TypeConnectGranted   = "connect_lease.granted"
	TypeConnectRefresh   = "connect_lease.refresh"

	MaxMessageBytes = 64 << 10
	MinTTL          = time.Minute
	MaxTTL          = 30 * 24 * time.Hour
)

var serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Message struct {
	Type                  string                           `json:"type"`
	Version               string                           `json:"version"`
	Token                 string                           `json:"token,omitempty"`
	ClusterID             string                           `json:"cluster_id,omitempty"`
	NamespaceID           string                           `json:"namespace_id,omitempty"`
	ServiceName           string                           `json:"service_name,omitempty"`
	ServiceID             string                           `json:"service_id,omitempty"`
	ServiceKind           string                           `json:"service_kind,omitempty"`
	ServicePublicKey      string                           `json:"service_public_key,omitempty"`
	ServiceOwnerSignature []byte                           `json:"service_owner_signature,omitempty"`
	ServicePeerID         string                           `json:"service_peer_id,omitempty"`
	ServiceAddresses      []string                         `json:"service_addresses,omitempty"`
	PublisherInstanceKey  string                           `json:"publisher_instance_public_key,omitempty"`
	RequestNonce          string                           `json:"request_nonce,omitempty"`
	RequestedPermissions  []string                         `json:"requested_permissions,omitempty"`
	RequestedTTLSeconds   int64                            `json:"requested_ttl_seconds,omitempty"`
	RequestIssuedAt       time.Time                        `json:"request_issued_at,omitempty"`
	RequestID             string                           `json:"request_id,omitempty"`
	ExpiresAt             time.Time                        `json:"expires_at,omitempty"`
	Message               string                           `json:"message,omitempty"`
	Reason                string                           `json:"reason,omitempty"`
	ServiceClaim          *capability.ServiceClaim         `json:"service_claim,omitempty"`
	PublishLease          *PublishLease                    `json:"publish_lease,omitempty"`
	MembershipCapability  *capability.MembershipCapability `json:"membership_capability,omitempty"`
	MembershipGrantToken  string                           `json:"membership_grant_token,omitempty"`
	ServiceShareToken     string                           `json:"service_share_token,omitempty"`
	ShareInviteToken      string                           `json:"share_invite_token,omitempty"`
	ClientPublicKey       string                           `json:"client_public_key,omitempty"`
	ConnectAccessLease    *ConnectAccessLease              `json:"connect_access_lease,omitempty"`
	ConnectRefreshLease   *ConnectRefreshLease             `json:"connect_refresh_lease,omitempty"`
}

func EncodeMessage(w io.Writer, msg Message) error {
	if err := ValidateMessage(msg); err != nil {
		return err
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	if len(b) > MaxMessageBytes {
		return fmt.Errorf("grant message too large: %d bytes", len(b))
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

func DecodeMessage(r io.Reader) (Message, error) {
	limited := io.LimitReader(r, MaxMessageBytes+1)
	b, err := io.ReadAll(limited)
	if err != nil {
		return Message{}, err
	}
	if len(b) > MaxMessageBytes {
		return Message{}, fmt.Errorf("grant message too large: %d bytes", len(b))
	}
	var msg Message
	if err := json.NewDecoder(bytes.NewReader(b)).Decode(&msg); err != nil {
		return Message{}, err
	}
	return msg, ValidateMessage(msg)
}

func ValidateMessage(msg Message) error {
	if msg.Version != VersionV1 {
		return fmt.Errorf("unsupported grant message version %q", msg.Version)
	}
	switch msg.Type {
	case TypeSubmit:
		return validateSubmit(msg)
	case TypePoll:
		if msg.RequestID == "" {
			return errors.New("request_id is required")
		}
	case TypePending:
		if msg.RequestID == "" || msg.ExpiresAt.IsZero() {
			return errors.New("pending response requires request_id and expires_at")
		}
	case TypeApproved:
		if msg.RequestID == "" || (msg.ServiceClaim == nil && msg.PublishLease == nil) {
			return errors.New("approved response requires request_id and service_claim or publish_lease")
		}
	case TypeDenied:
		if msg.RequestID == "" {
			return errors.New("denied response requires request_id")
		}
	case TypeExpired:
		if msg.RequestID == "" {
			return errors.New("expired response requires request_id")
		}
	case TypeShareRedeem:
		if msg.ShareInviteToken != "" && msg.ClientPublicKey != "" {
			return nil
		}
		if msg.ConnectAccessLease != nil && msg.ConnectRefreshLease != nil {
			return nil
		}
		return errors.New("share invite redemption requires share_invite_token/client_public_key or connect leases")
	case TypeShareMintRequest:
		if msg.ClusterID == "" || msg.NamespaceID == "" || msg.ServiceID == "" || msg.PublishLease == nil || strings.TrimSpace(msg.ServicePeerID) == "" || len(msg.ServiceAddresses) == 0 || msg.RequestedTTLSeconds <= 0 || strings.TrimSpace(msg.RequestNonce) == "" || msg.RequestIssuedAt.IsZero() || len(msg.ServiceOwnerSignature) == 0 {
			return errors.New("share invite mint request requires cluster_id/namespace_id/service_id/publish_lease/service_peer_id/service_addresses/requested_ttl_seconds/request_nonce/request_issued_at/service_owner_signature")
		}
	case TypeShareMintGranted:
		if strings.TrimSpace(msg.ServiceShareToken) == "" {
			return errors.New("share invite mint granted response requires service_share_token")
		}
	case TypeConnectRequest:
		if msg.ClusterID == "" || msg.NamespaceID == "" || msg.ServiceID == "" || strings.TrimSpace(msg.ClientPublicKey) == "" {
			return errors.New("connect lease request requires cluster_id/namespace_id/service_id/client_public_key")
		}
	case TypeConnectGranted:
		if msg.ConnectAccessLease == nil || msg.ConnectRefreshLease == nil {
			return errors.New("connect lease granted response requires connect leases")
		}
	case TypeConnectRefresh:
		if msg.ConnectRefreshLease != nil || msg.ConnectAccessLease != nil {
			return nil
		}
		return errors.New("connect lease refresh requires connect_refresh_lease or connect_access_lease")
	default:
		return fmt.Errorf("unsupported grant message type %q", msg.Type)
	}
	return nil
}

func validateSubmit(msg Message) error {
	if msg.ClusterID == "" || msg.NamespaceID == "" || msg.ServiceID == "" || msg.ServiceName == "" || msg.ServicePublicKey == "" || msg.ServicePeerID == "" || msg.RequestNonce == "" {
		return errors.New("submit request is missing required cluster/namespace/service fields")
	}
	if !serviceNameRE.MatchString(msg.ServiceName) {
		return fmt.Errorf("invalid service name %q", msg.ServiceName)
	}
	if !validGrantPermissions(msg.RequestedPermissions) {
		return errors.New("requested permissions must be limited to attach, announce, and share.mint")
	}
	if msg.RequestedTTLSeconds <= 0 {
		return errors.New("requested_ttl_seconds is required")
	}
	if len(msg.ServiceOwnerSignature) == 0 {
		return errors.New("service_owner_signature is required")
	}
	ttl := time.Duration(msg.RequestedTTLSeconds) * time.Second
	if ttl < MinTTL || ttl > MaxTTL {
		return fmt.Errorf("requested ttl %s outside allowed range %s..%s", ttl, MinTTL, MaxTTL)
	}
	return nil
}

func validGrantPermissions(perms []string) bool {
	if len(perms) == 0 {
		return false
	}
	seen := map[string]struct{}{}
	for _, perm := range perms {
		switch perm {
		case capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint:
			seen[perm] = struct{}{}
		default:
			return false
		}
	}
	_, attach := seen[capability.PermissionAttach]
	_, announce := seen[capability.PermissionAnnounce]
	return attach && announce
}

func PendingMessage(req Request) Message {
	return Message{Type: TypePending, Version: VersionV1, RequestID: req.ID, ExpiresAt: req.ExpiresAt, Message: "waiting for authority approval"}
}

func ResponseForRequest(req Request) Message {
	switch req.Status {
	case StatusApproved:
		return Message{Type: TypeApproved, Version: VersionV1, RequestID: req.ID, ServiceClaim: req.ServiceClaim, PublishLease: req.PublishLease, MembershipCapability: req.MembershipCapability, ServiceShareToken: req.ServiceShareToken}
	case StatusDenied:
		return Message{Type: TypeDenied, Version: VersionV1, RequestID: req.ID, Reason: req.DenialReason}
	case StatusExpired:
		return Message{Type: TypeExpired, Version: VersionV1, RequestID: req.ID, Reason: "grant request expired"}
	default:
		return PendingMessage(req)
	}
}
