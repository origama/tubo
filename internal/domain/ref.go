package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

type ResourceKind string

const (
	KindService   ResourceKind = "service"
	KindPipe      ResourceKind = "pipe"
	KindRun       ResourceKind = "run"
	KindSession   ResourceKind = "session"
	KindRelay     ResourceKind = "relay"
	KindAuthority ResourceKind = "authority"
	KindGateway   ResourceKind = "gateway"
)

var resourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9@._-]{0,62}$`)

type Ref struct {
	Kind ResourceKind `json:"kind"`
	Name string       `json:"name"`
}

func NewRef(kind ResourceKind, name string) (Ref, error) {
	ref := Ref{Kind: kind, Name: strings.TrimSpace(name)}
	if err := ref.Validate(); err != nil {
		return Ref{}, err
	}
	return ref, nil
}

func ParseRef(raw string) (Ref, error) {
	kind, name, ok := strings.Cut(strings.TrimSpace(raw), "/")
	if !ok || strings.Contains(name, "/") {
		return Ref{}, fmt.Errorf("resource reference must be <kind>/<name>: %q", raw)
	}
	return NewRef(ResourceKind(kind), name)
}

func (r Ref) String() string {
	if r.Kind == "" && r.Name == "" {
		return ""
	}
	return string(r.Kind) + "/" + r.Name
}

func (r Ref) Validate() error {
	switch r.Kind {
	case KindService, KindPipe, KindRun, KindSession, KindRelay, KindAuthority, KindGateway:
	default:
		return fmt.Errorf("unsupported resource kind %q", r.Kind)
	}
	if !resourceNamePattern.MatchString(r.Name) {
		return fmt.Errorf("invalid %s name %q", r.Kind, r.Name)
	}
	return nil
}

func (r Ref) RequireKind(kind ResourceKind) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if r.Kind != kind {
		return fmt.Errorf("resource %s has kind %q, want %q", r, r.Kind, kind)
	}
	return nil
}

var ErrNotFound = errors.New("resource not found")
