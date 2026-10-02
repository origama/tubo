package discovery

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/origama/tubo/internal/authority"
	"github.com/origama/tubo/internal/domain"
)

const MaxRecordSize = 64 << 10

type PipeRecord struct {
	Version        int                 `json:"version"`
	PipeID         string              `json:"pipe_id"`
	DisplayName    string              `json:"display_name,omitempty"`
	OwnerPublicKey string              `json:"owner_public_key"`
	RuntimePeerID  string              `json:"runtime_peer_id"`
	Scope          domain.Scope        `json:"scope"`
	Protocol       string              `json:"protocol"`
	Capabilities   []string            `json:"capabilities"`
	Addresses      []string            `json:"addresses"`
	Visibility     domain.Visibility   `json:"visibility"`
	Access         domain.AccessPolicy `json:"access"`
	PublishGrant   string              `json:"publish_grant"`
	IssuedAt       time.Time           `json:"issued_at"`
	ExpiresAt      time.Time           `json:"expires_at"`
	Sequence       uint64              `json:"sequence"`
	Signature      string              `json:"signature"`
}

func (r PipeRecord) signingBytes() ([]byte, error) { r.Signature = ""; return json.Marshal(r) }
func (r *PipeRecord) Sign(private ed25519.PrivateKey) error {
	r.OwnerPublicKey = base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey))
	message, err := r.signingBytes()
	if err != nil {
		return err
	}
	r.Signature = base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, message))
	return nil
}
func (r PipeRecord) Marshal() ([]byte, error) {
	data, err := json.Marshal(r)
	if err == nil && len(data) > MaxRecordSize {
		return nil, errors.New("record exceeds 64 KiB")
	}
	return data, err
}
func Parse(data []byte) (PipeRecord, error) {
	if len(data) > MaxRecordSize {
		return PipeRecord{}, errors.New("record exceeds 64 KiB")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var r PipeRecord
	if err := decoder.Decode(&r); err != nil {
		return r, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return r, errors.New("trailing record payload")
	}
	return r, nil
}

func (r PipeRecord) Verify(authorityPublic ed25519.PublicKey, scope domain.Scope, now time.Time) error {
	if r.Version != 2 {
		return fmt.Errorf("unsupported record version %d", r.Version)
	}
	if r.Scope != scope {
		return errors.New("record scope mismatch")
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if len(r.Addresses) == 0 || len(r.Addresses) > 16 || len(r.Capabilities) > 64 {
		return errors.New("invalid record bounds")
	}
	if r.Visibility != domain.VisibilityDiscoverable && r.Visibility != domain.VisibilityUnlisted {
		return errors.New("invalid published visibility")
	}
	if r.Access != domain.AccessInviteOnly && r.Access != domain.AccessMembers && r.Access != domain.AccessPublic {
		return errors.New("invalid access policy hint")
	}
	if r.Sequence == 0 {
		return errors.New("record sequence is required")
	}
	for _, value := range r.Addresses {
		if len(value) > 8192 {
			return errors.New("record address exceeds limit")
		}
	}
	if !r.ExpiresAt.After(r.IssuedAt) || !now.Before(r.ExpiresAt) || r.IssuedAt.After(now.Add(30*time.Second)) {
		return errors.New("record lifetime invalid, expired, or issued in future")
	}
	owner, err := authority.PublicFromString(r.OwnerPublicKey)
	if err != nil {
		return err
	}
	if authority.PipeID(owner) != r.PipeID {
		return errors.New("pipe_id does not match owner key")
	}
	signature, err := base64.RawURLEncoding.DecodeString(r.Signature)
	if err != nil {
		return errors.New("invalid record signature encoding")
	}
	message, err := r.signingBytes()
	if err != nil {
		return err
	}
	if !ed25519.Verify(owner, message, signature) {
		return errors.New("invalid record owner signature")
	}
	grant, err := authority.Decode(r.PublishGrant)
	if err != nil {
		return fmt.Errorf("publish grant: %w", err)
	}
	if err := grant.Verify(authorityPublic, now); err != nil {
		return fmt.Errorf("publish grant: %w", err)
	}
	if grant.Type != "pipe.publish" || grant.Audience != r.PipeID || grant.Scope != r.Scope {
		return errors.New("publish grant binding mismatch")
	}
	if r.ExpiresAt.After(grant.ExpiresAt) {
		return errors.New("record outlives publish grant")
	}
	return nil
}
