package domain

import (
	"errors"
	"fmt"
	"time"
)

type GrantKind string

const (
	GrantMembership     GrantKind = "membership"
	GrantPipePublish    GrantKind = "pipe.publish"
	GrantPipeForward    GrantKind = "pipe.forward"
	GrantShareInvite    GrantKind = "share.invite"
	GrantConnectAccess  GrantKind = "connect.access"
	GrantConnectRefresh GrantKind = "connect.refresh"
)

type SignedEnvelope struct {
	Kind        GrantKind      `json:"kind"`
	Issuer      string         `json:"issuer"`
	Subject     string         `json:"subject"`
	Audience    string         `json:"audience,omitempty"`
	Scope       Scope          `json:"scope"`
	Permissions []string       `json:"permissions,omitempty"`
	JTI         string         `json:"jti"`
	IssuedAt    time.Time      `json:"issued_at"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Payload     map[string]any `json:"payload,omitempty"`
	Signature   []byte         `json:"signature"`
}

func (e SignedEnvelope) ValidateMetadata(now time.Time) error {
	switch e.Kind {
	case GrantMembership, GrantPipePublish, GrantPipeForward, GrantShareInvite, GrantConnectAccess, GrantConnectRefresh:
	default:
		return fmt.Errorf("unsupported grant kind %q", e.Kind)
	}
	if e.Issuer == "" || e.Subject == "" || e.JTI == "" {
		return errors.New("issuer, subject, and jti are required")
	}
	if err := e.Scope.Validate(); err != nil {
		return err
	}
	if e.IssuedAt.IsZero() || e.ExpiresAt.IsZero() {
		return errors.New("issued_at and expires_at are required")
	}
	if !e.ExpiresAt.After(e.IssuedAt) {
		return errors.New("expires_at must be after issued_at")
	}
	if !now.IsZero() && !now.Before(e.ExpiresAt) {
		return errors.New("grant expired")
	}
	if len(e.Signature) == 0 {
		return errors.New("signature is required")
	}
	return nil
}
