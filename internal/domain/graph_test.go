package domain

import (
	"fmt"
	"testing"
)

func TestResolveLogicalPath(t *testing.T) {
	local := PipeSpec{Ref: mustRef(t, "pipe/client"), Ingress: PipeIngress{Kind: IngressLocal, Listen: "127.0.0.1:9000"}, Target: mustRef(t, "pipe/origin"), Visibility: VisibilityPrivate, Access: AccessMembers}
	origin := PipeSpec{Ref: mustRef(t, "pipe/origin"), Scope: &Scope{Cluster: "solar", Namespace: "default"}, Ingress: PipeIngress{Kind: IngressTubo}, Target: mustRef(t, "service/lmstudio"), Visibility: VisibilityDiscoverable, Access: AccessMembers}
	service := ServiceSpec{Ref: mustRef(t, "service/lmstudio"), Kind: ServiceHTTP, Target: "http://127.0.0.1:1234"}
	pipes := map[string]PipeSpec{local.Ref.String(): local, origin.Ref.String(): origin}
	services := map[string]ServiceSpec{service.Ref.String(): service}

	path, err := ResolveLogicalPath(local.Ref, 8,
		func(ref Ref) (PipeSpec, error) {
			value, ok := pipes[ref.String()]
			if !ok {
				return PipeSpec{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
			}
			return value, nil
		},
		func(ref Ref) (ServiceSpec, error) {
			value, ok := services[ref.String()]
			if !ok {
				return ServiceSpec{}, fmt.Errorf("%w: %s", ErrNotFound, ref)
			}
			return value, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(path.Pipes) != 2 || path.Terminal != service.Ref {
		t.Fatalf("unexpected path: %#v", path)
	}
}

func TestResolveLogicalPathRejectsCycle(t *testing.T) {
	a := PipeSpec{Ref: mustRef(t, "pipe/a"), Ingress: PipeIngress{Kind: IngressLocal, Listen: "127.0.0.1:1"}, Target: mustRef(t, "pipe/b"), Visibility: VisibilityPrivate, Access: AccessMembers}
	b := PipeSpec{Ref: mustRef(t, "pipe/b"), Ingress: PipeIngress{Kind: IngressLocal, Listen: "127.0.0.1:2"}, Target: mustRef(t, "pipe/a"), Visibility: VisibilityPrivate, Access: AccessMembers}
	pipes := map[string]PipeSpec{a.Ref.String(): a, b.Ref.String(): b}
	_, err := ResolveLogicalPath(a.Ref, 8, func(ref Ref) (PipeSpec, error) { return pipes[ref.String()], nil }, func(Ref) (ServiceSpec, error) { return ServiceSpec{}, nil })
	if err == nil {
		t.Fatal("expected cycle to fail")
	}
}
