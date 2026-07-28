package domain

import "testing"

func mustRef(t *testing.T, raw string) Ref {
	t.Helper()
	ref, err := ParseRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestPipeValidationRequiresOneValidTargetAndNetworkScope(t *testing.T) {
	pipe := PipeSpec{
		Ref:        mustRef(t, "pipe/lmstudio-team"),
		Scope:      &Scope{Cluster: "solar", Namespace: "default"},
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

func TestServiceTargetValidation(t *testing.T) {
	service := ServiceSpec{Ref: mustRef(t, "service/postgres"), Kind: ServiceTCP, Target: "tcp://127.0.0.1:5432"}
	if err := service.Validate(); err != nil {
		t.Fatal(err)
	}
	service.Target = "http://127.0.0.1:5432"
	if err := service.Validate(); err == nil {
		t.Fatal("expected protocol mismatch to fail")
	}
}
