// Durable/runtime separation assertions — case 12 (structural).
package usecase

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/domain"
)

// verifyNoRuntimeFields asserts that a durable PipeSpec carries no transient
// runtime state: no process PID, Carrier, Session, current address, or lease.
func verifyNoRuntimeFields(t *testing.T, spec domain.PipeSpec) {
	t.Helper()
	value := reflect.TypeOf(spec)
	for i := 0; i < value.NumField(); i++ {
		name := value.Field(i).Name
		for _, forbidden := range []string{"Carrier", "Session", "Address", "Lease"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("durable PipeSpec carries transient runtime field %q", name)
			}
		}
		if name == "PID" {
			t.Fatalf("durable PipeSpec carries process PID field")
		}
	}
}

// TestCase12_PipeSpecCarriesNoTransientRuntimeFields is the structural
// assertion that durable definitions never carry PID/Carrier/Session/address/
// lease state. ServiceSpec and PipeSpec are pure definitions; runtime state
// lives in store.Run / store.Session / runtime registries.
func TestCase12_PipeSpecCarriesNoTransientRuntimeFields(t *testing.T) {
	pipeFields := map[string]bool{}
	pipeType := reflect.TypeOf(domain.PipeSpec{})
	for i := 0; i < pipeType.NumField(); i++ {
		pipeFields[pipeType.Field(i).Name] = true
	}

	for _, forbidden := range []string{"Carrier", "Session", "Address", "Lease", "PID", "PeerID", "ProcessIdentity"} {
		for field := range pipeFields {
			if strings.Contains(field, forbidden) {
				t.Fatalf("PipeSpec carries transient runtime field %q (forbidden %q)", field, forbidden)
			}
		}
	}

	serviceFields := map[string]bool{}
	serviceType := reflect.TypeOf(domain.ServiceSpec{})
	for i := 0; i < serviceType.NumField(); i++ {
		serviceFields[serviceType.Field(i).Name] = true
	}
	for _, forbidden := range []string{"Carrier", "Session", "Address", "Lease", "PID", "PeerID", "ProcessIdentity", "Status"} {
		for field := range serviceFields {
			if strings.Contains(field, forbidden) {
				t.Fatalf("ServiceSpec carries transient runtime field %q (forbidden %q)", field, forbidden)
			}
		}
	}
}

// TestCase13_FakePipeSpecIdentitySurvivesRepositoryRestart asserts the
// durable identity invariant through a fresh repository instance (restart).
// The real store restart test lives in internal/store (usecase_repository_test.go).
func TestCase13_PipeSpecIdentitySurvivesRepositoryRestart(t *testing.T) {
	ctx := context.Background()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.Now = fixedClock(now)

	result, err := u.Attach(ctx, AttachInput{Name: "restartsafe", Target: "http://127.0.0.1:8080"})
	if err != nil {
		t.Fatal(err)
	}

	// Repository "restart": fresh instances seeded from the same durable data.
	restartedPipes := newMemoryPipeRepo()
	for ref, spec := range pipes.snapshotSpecs() {
		keyFile := pipes.ownerKeys[ref]
		_, _ = restartedPipes.Upsert(ctx, spec, keyFile)
	}

	loaded, _, err := restartedPipes.Get(ctx, result.PipeRef.String())
	if err != nil {
		t.Fatalf("get after restart: %v", err)
	}
	if loaded.PipeID != result.PipeID {
		t.Fatalf("pipe identity changed across restart: %s -> %s", result.PipeID, loaded.PipeID)
	}
	if loaded.Target.String() != "service/restartsafe" {
		t.Fatalf("pipe target changed across restart: %s", loaded.Target)
	}
}

func (r *memoryPipeRepo) snapshotSpecs() map[string]domain.PipeSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := make(map[string]domain.PipeSpec, len(r.specs))
	for k, v := range r.specs {
		copy[k] = v
	}
	return copy
}
