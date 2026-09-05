package domain

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

type Scope struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
}

func (s Scope) String() string { return s.Cluster + "/" + s.Namespace }

func (s Scope) Validate() error {
	if !resourceNamePattern.MatchString(s.Cluster) {
		return fmt.Errorf("invalid cluster name %q", s.Cluster)
	}
	if !resourceNamePattern.MatchString(s.Namespace) {
		return fmt.Errorf("invalid namespace name %q", s.Namespace)
	}
	return nil
}

type ServiceKind string

const (
	ServiceHTTP ServiceKind = "http"
	ServiceTCP  ServiceKind = "tcp"
	ServiceUnix ServiceKind = "unix"
)

type ServiceSpec struct {
	Ref         Ref               `json:"ref"`
	Kind        ServiceKind       `json:"kind"`
	Target      string            `json:"target"`
	Description string            `json:"description,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func (s ServiceSpec) Validate() error {
	if err := s.Ref.RequireKind(KindService); err != nil {
		return err
	}
	target := strings.TrimSpace(s.Target)
	if target == "" {
		return fmt.Errorf("service %s target is required", s.Ref)
	}
	switch s.Kind {
	case ServiceHTTP:
		u, err := url.Parse(target)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("service %s requires an http:// or https:// target", s.Ref)
		}
	case ServiceTCP:
		u, err := url.Parse(target)
		if err != nil || u.Scheme != "tcp" || u.Host == "" {
			return fmt.Errorf("service %s requires a tcp://host:port target", s.Ref)
		}
		if _, _, err := net.SplitHostPort(u.Host); err != nil {
			return fmt.Errorf("service %s tcp target: %w", s.Ref, err)
		}
	case ServiceUnix:
		if !strings.HasPrefix(target, "unix:///") {
			return fmt.Errorf("service %s requires a unix:///path target", s.Ref)
		}
	default:
		return fmt.Errorf("service %s has unsupported kind %q", s.Ref, s.Kind)
	}
	return nil
}

type IngressKind string

const (
	IngressLocal     IngressKind = "local"
	IngressTubo      IngressKind = "tubo"
	IngressHTTPRoute IngressKind = "http-route"
)

type PipeIngress struct {
	Kind     IngressKind `json:"kind"`
	Listen   string      `json:"listen,omitempty"`
	Host     string      `json:"host,omitempty"`
	Path     string      `json:"path,omitempty"`
	Protocol string      `json:"protocol,omitempty"`
}

type Visibility string

const (
	VisibilityPrivate      Visibility = "private"
	VisibilityUnlisted     Visibility = "unlisted"
	VisibilityDiscoverable Visibility = "discoverable"
)

type AccessPolicy string

const (
	AccessInviteOnly AccessPolicy = "invite_only"
	AccessMembers    AccessPolicy = "members"
	AccessPublic     AccessPolicy = "public"
)

type PipeSpec struct {
	Ref           Ref          `json:"ref"`
	PipeID        string       `json:"pipe_id,omitempty"`
	Scope         *Scope       `json:"scope,omitempty"`
	Ingress       PipeIngress  `json:"ingress"`
	Target        Ref          `json:"target"`
	Visibility    Visibility   `json:"visibility"`
	Access        AccessPolicy `json:"access"`
	AllowReexport bool         `json:"allow_reexport,omitempty"`
	CreatedAt     time.Time    `json:"created_at,omitempty"`
	UpdatedAt     time.Time    `json:"updated_at,omitempty"`
}

func (p PipeSpec) Validate() error {
	if err := p.Ref.RequireKind(KindPipe); err != nil {
		return err
	}
	if err := p.Target.Validate(); err != nil {
		return fmt.Errorf("pipe %s target: %w", p.Ref, err)
	}
	if p.Target.Kind != KindService && p.Target.Kind != KindPipe {
		return fmt.Errorf("pipe %s target must be service/... or pipe/...", p.Ref)
	}
	if p.Target == p.Ref {
		return fmt.Errorf("pipe %s cannot target itself", p.Ref)
	}
	if p.Scope != nil {
		if err := p.Scope.Validate(); err != nil {
			return fmt.Errorf("pipe %s scope: %w", p.Ref, err)
		}
	}
	switch p.Ingress.Kind {
	case IngressLocal:
		if strings.TrimSpace(p.Ingress.Listen) == "" {
			return fmt.Errorf("pipe %s local ingress requires listen", p.Ref)
		}
	case IngressTubo:
	case IngressHTTPRoute:
		if strings.TrimSpace(p.Ingress.Host) == "" {
			return fmt.Errorf("pipe %s HTTP route ingress requires host", p.Ref)
		}
	default:
		return fmt.Errorf("pipe %s has unsupported ingress %q", p.Ref, p.Ingress.Kind)
	}
	switch p.Visibility {
	case VisibilityPrivate, VisibilityUnlisted, VisibilityDiscoverable:
	default:
		return fmt.Errorf("pipe %s has unsupported visibility %q", p.Ref, p.Visibility)
	}
	switch p.Access {
	case AccessInviteOnly, AccessMembers, AccessPublic:
	default:
		return fmt.Errorf("pipe %s has unsupported access policy %q", p.Ref, p.Access)
	}
	if p.Visibility == VisibilityDiscoverable && p.Ingress.Kind != IngressTubo {
		return fmt.Errorf("pipe %s is discoverable but has no Tubo ingress", p.Ref)
	}
	return nil
}

func (p PipeSpec) IsPublished() bool {
	return p.Ingress.Kind == IngressTubo && p.Visibility != VisibilityPrivate
}

// RemotePipeBinding is the consumer-side durable record of a validly accepted
// exact remote-Pipe access relationship (invite bootstrap -> accepted binding).
// It is an L0 neutral value: it carries no libp2p, Store, runtime, CLI, or Scope
// dependency and defines no network or persistence behavior.
//
// Invariants:
//   - AcceptedBinding != AccessLease: the binding is durable relationship/
//     reachability knowledge; it is never itself permission to open at L2.
//   - AcceptedBinding != local Pipe: the binding has no local ingress/listen
//     dependency; the same accepted remote Pipe stays usable regardless of
//     which future ingress is attached to it.
//   - AcceptedBinding != invite: the binding survives invite expiry; the invite
//     is evidence used to establish the relationship once.
//   - Renewal requires possession proof by the exact accepted recipient key.
type RemotePipeBinding struct {
	// RemotePipeID is the exact Pipe the recipient was accepted for.
	RemotePipeID string `json:"remote_pipe_id"`
	// AuthorityPublic is the owner-bound Exact Authority public key (the
	// invite issuer). It is the only issuer trusted for renewal of this Pipe.
	AuthorityPublic string `json:"authority_public"`
	// OwnerBinding is the canonical base64url-encoded owner-signed OwnerBinding
	// verified at acceptance. It anchors issuer trust to the exact Pipe owner.
	OwnerBinding string `json:"owner_binding"`
	// Locator is the verified neutral renewal/redemption peer address from the
	// accepted OwnerBinding.
	Locator string `json:"locator"`
	// RecipientPublic is the canonical public key of the accepted recipient
	// identity. Renewal requires possession proof by this exact key.
	RecipientPublic string `json:"recipient_public"`
	// Source records how this relationship was established.
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// Validate performs structural bounds checks only. Cryptographic validation
// (owner binding, recipient/authority keys) happens at acceptance and renewal
// in L3/L4 where the credential primitives are available; this package is
// stdlib-only by architecture rule R1.
func (b RemotePipeBinding) Validate() error {
	if !strings.HasPrefix(b.RemotePipeID, "pip_") || len(b.RemotePipeID) > 256 {
		return fmt.Errorf("remote pipe binding invalid remote pipe id")
	}
	if b.AuthorityPublic == "" || len(b.AuthorityPublic) > 4096 {
		return fmt.Errorf("remote pipe binding invalid authority public key")
	}
	if b.OwnerBinding == "" || len(b.OwnerBinding) > 48<<10 {
		return fmt.Errorf("remote pipe binding invalid owner binding")
	}
	if b.Locator == "" || len(b.Locator) > 4096 {
		return fmt.Errorf("remote pipe binding invalid locator")
	}
	if b.RecipientPublic == "" || len(b.RecipientPublic) > 4096 {
		return fmt.Errorf("remote pipe binding invalid recipient public key")
	}
	if b.Source == "" || len(b.Source) > 64 {
		return fmt.Errorf("remote pipe binding invalid source")
	}
	return nil
}

// AccessLeaseCache is the ephemeral consumer-side cache of the current
// short-lived AccessLease for an accepted remote Pipe. It is cache/runtime
// authorization material only, never the source of truth for the relationship:
// an expired cached lease simply causes reacquisition through the durable
// binding.
type AccessLeaseCache struct {
	RemotePipeID string    `json:"remote_pipe_id"`
	AccessLease  string    `json:"access_lease"`
	ExpiresAt    time.Time `json:"expires_at"`
}
