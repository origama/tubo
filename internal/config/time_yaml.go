package config

import (
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func parseConfigTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
	}
	var last error
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		} else {
			last = err
		}
	}
	return time.Time{}, fmt.Errorf("parsing time %q as RFC3339: %w", value, last)
}

type managedSecretRefYAML struct {
	Type      string `yaml:"type,omitempty"`
	KeyID     string `yaml:"key_id,omitempty"`
	File      string `yaml:"file,omitempty"`
	CreatedAt string `yaml:"created_at,omitempty"`
	ExpiresAt string `yaml:"expires_at,omitempty"`
}

func (r ManagedSecretRef) MarshalYAML() (any, error) {
	out := managedSecretRefYAML{Type: r.Type, KeyID: r.KeyID, File: r.File}
	if !r.CreatedAt.IsZero() {
		out.CreatedAt = r.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if !r.ExpiresAt.IsZero() {
		out.ExpiresAt = r.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return out, nil
}

func (r *ManagedSecretRef) UnmarshalYAML(v *yaml.Node) error {
	var out managedSecretRefYAML
	if err := v.Decode(&out); err != nil {
		return err
	}
	*r = ManagedSecretRef{Type: out.Type, KeyID: out.KeyID, File: out.File}
	if out.CreatedAt != "" {
		t, err := parseConfigTime(out.CreatedAt)
		if err != nil {
			return fmt.Errorf("created_at: %w", err)
		}
		r.CreatedAt = t
	}
	if out.ExpiresAt != "" {
		t, err := parseConfigTime(out.ExpiresAt)
		if err != nil {
			return fmt.Errorf("expires_at: %w", err)
		}
		r.ExpiresAt = t
	}
	return nil
}

type clusterMembershipGrantYAML struct {
	InviteToken          string   `yaml:"invite_token,omitempty"`
	InviteTokenFile      string   `yaml:"invite_token_file,omitempty"`
	InviteVersion        string   `yaml:"invite_version,omitempty"`
	InviteID             string   `yaml:"invite_id,omitempty"`
	ClusterName          string   `yaml:"cluster_name,omitempty"`
	ClusterID            string   `yaml:"cluster_id,omitempty"`
	AuthorityPublicKey   string   `yaml:"authority_public_key,omitempty"`
	Namespace            string   `yaml:"namespace,omitempty"`
	Role                 string   `yaml:"role,omitempty"`
	Permissions          []string `yaml:"permissions,omitempty"`
	GrantServiceProtocol string   `yaml:"grant_service_protocol,omitempty"`
	GrantServicePeers    []string `yaml:"grant_service_peers,omitempty"`
	IssuedAt             string   `yaml:"issued_at,omitempty"`
	ExpiresAt            string   `yaml:"expires_at,omitempty"`
}

func (g ClusterMembershipGrant) MarshalYAML() (any, error) {
	out := clusterMembershipGrantYAML{
		InviteToken:          g.InviteToken,
		InviteTokenFile:      g.InviteTokenFile,
		InviteVersion:        g.InviteVersion,
		InviteID:             g.InviteID,
		ClusterName:          g.ClusterName,
		ClusterID:            g.ClusterID,
		AuthorityPublicKey:   g.AuthorityPublicKey,
		Namespace:            g.Namespace,
		Role:                 g.Role,
		Permissions:          append([]string(nil), g.Permissions...),
		GrantServiceProtocol: g.GrantServiceProtocol,
		GrantServicePeers:    append([]string(nil), g.GrantServicePeers...),
	}
	if !g.IssuedAt.IsZero() {
		out.IssuedAt = g.IssuedAt.UTC().Format(time.RFC3339Nano)
	}
	if !g.ExpiresAt.IsZero() {
		out.ExpiresAt = g.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	return out, nil
}

func (g *ClusterMembershipGrant) UnmarshalYAML(v *yaml.Node) error {
	var out clusterMembershipGrantYAML
	if err := v.Decode(&out); err != nil {
		return err
	}
	*g = ClusterMembershipGrant{
		InviteToken:          out.InviteToken,
		InviteTokenFile:      out.InviteTokenFile,
		InviteVersion:        out.InviteVersion,
		InviteID:             out.InviteID,
		ClusterName:          out.ClusterName,
		ClusterID:            out.ClusterID,
		AuthorityPublicKey:   out.AuthorityPublicKey,
		Namespace:            out.Namespace,
		Role:                 out.Role,
		Permissions:          append([]string(nil), out.Permissions...),
		GrantServiceProtocol: out.GrantServiceProtocol,
		GrantServicePeers:    append([]string(nil), out.GrantServicePeers...),
	}
	if out.IssuedAt != "" {
		t, err := parseConfigTime(out.IssuedAt)
		if err != nil {
			return fmt.Errorf("issued_at: %w", err)
		}
		g.IssuedAt = t
	}
	if out.ExpiresAt != "" {
		t, err := parseConfigTime(out.ExpiresAt)
		if err != nil {
			return fmt.Errorf("expires_at: %w", err)
		}
		g.ExpiresAt = t
	}
	return nil
}
