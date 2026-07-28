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
	ServiceA2A  ServiceKind = "a2a"
	ServiceMCP  ServiceKind = "mcp"
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
	case ServiceHTTP, ServiceA2A, ServiceMCP:
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
	switch p.Ingress.Kind {
	case IngressLocal:
		if strings.TrimSpace(p.Ingress.Listen) == "" {
			return fmt.Errorf("pipe %s local ingress requires listen", p.Ref)
		}
	case IngressTubo:
		if p.Scope == nil {
			return fmt.Errorf("pipe %s Tubo ingress requires scope", p.Ref)
		}
		if err := p.Scope.Validate(); err != nil {
			return fmt.Errorf("pipe %s scope: %w", p.Ref, err)
		}
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
