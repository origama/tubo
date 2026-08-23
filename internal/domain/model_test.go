package domain

import (
	"strings"
	"testing"
)

func mustRef(t *testing.T, raw string) Ref {
	t.Helper()
	ref, err := ParseRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestPipeValidationAllowsScopeLessTuboIngress(t *testing.T) {
	pipe := PipeSpec{
		Ref:        mustRef(t, "pipe/lmstudio-team"),
		Ingress:    PipeIngress{Kind: IngressTubo, Protocol: "http"},
		Target:     mustRef(t, "service/lmstudio"),
		Visibility: VisibilityDiscoverable,
		Access:     AccessMembers,
	}
	if err := pipe.Validate(); err != nil {
		t.Fatal(err)
	}
	if !pipe.IsPublished() {
		t.Fatal("expected pipe to be published")
	}
}

func TestPipeValidationRejectsMalformedOptionalScope(t *testing.T) {
	pipe := PipeSpec{
		Ref:        mustRef(t, "pipe/lmstudio-team"),
		Scope:      &Scope{Cluster: "solar!", Namespace: "default"},
		Ingress:    PipeIngress{Kind: IngressTubo},
		Target:     mustRef(t, "service/lmstudio"),
		Visibility: VisibilityUnlisted,
		Access:     AccessInviteOnly,
	}
	if err := pipe.Validate(); err == nil {
		t.Fatal("expected malformed optional scope to fail")
	}
}

func TestPipeValidationRejectsInvalidAndSelfTargets(t *testing.T) {
	for _, test := range []struct {
		name    string
		target  Ref
		wantErr string
	}{
		{name: "missing", target: Ref{}, wantErr: "unsupported resource kind"},
		{name: "invalid-kind", target: mustRef(t, "run/lmstudio"), wantErr: "target must be service/... or pipe/..."},
		{name: "self", target: mustRef(t, "pipe/lmstudio-team"), wantErr: "cannot target itself"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pipe := PipeSpec{
				Ref:        mustRef(t, "pipe/lmstudio-team"),
				Ingress:    PipeIngress{Kind: IngressTubo},
				Target:     test.target,
				Visibility: VisibilityUnlisted,
				Access:     AccessInviteOnly,
			}
			if err := pipe.Validate(); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("expected %s target error containing %q, got %v", test.name, test.wantErr, err)
			}
		})
	}
}

func TestServiceTargetValidation(t *testing.T) {
	for name, service := range map[string]ServiceSpec{
		"http":  {Ref: mustRef(t, "service/http"), Kind: ServiceHTTP, Target: "http://127.0.0.1:8080"},
		"https": {Ref: mustRef(t, "service/https"), Kind: ServiceHTTP, Target: "https://127.0.0.1:8443"},
		"tcp":   {Ref: mustRef(t, "service/postgres"), Kind: ServiceTCP, Target: "tcp://127.0.0.1:5432"},
		"unix":  {Ref: mustRef(t, "service/socket"), Kind: ServiceUnix, Target: "unix:///tmp/tubo.sock"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := service.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServiceTargetValidationRejectsMismatchedEndpoint(t *testing.T) {
	service := ServiceSpec{Ref: mustRef(t, "service/postgres"), Kind: ServiceTCP, Target: "http://127.0.0.1:5432"}
	if err := service.Validate(); err == nil {
		t.Fatal("expected TCP service with HTTP target to fail")
	}
}

func TestServiceTargetValidationRejectsProductCapabilities(t *testing.T) {
	for _, kind := range []ServiceKind{"a2a", "mcp"} {
		t.Run(string(kind), func(t *testing.T) {
			service := ServiceSpec{Ref: mustRef(t, "service/capability"), Kind: kind, Target: "http://127.0.0.1:8080"}
			if err := service.Validate(); err == nil {
				t.Fatalf("expected %q service kind to fail", kind)
			}
		})
	}
}
