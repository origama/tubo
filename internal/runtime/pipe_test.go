package runtime

import (
	"bufio"
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/origama/tubo/internal/domain"
	"github.com/origama/tubo/internal/store"
)

func TestRunPublishedPipeScopeLessIngressOpensWithoutPublishGrant(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	ref, err := domain.ParseRef("pipe/scope-less")
	if err != nil {
		t.Fatal(err)
	}
	// Scope-less published ingress must not fail with a "deferred" error:
	// it takes the Journey-A scope-less path, which requires reachability
	// configuration. With no overlay configured it fails deterministically
	// on reachability (never a temporary provider).
	err = runPublishedPipe(ctx, state, domain.PipeSpec{
		Ref:        ref,
		Ingress:    domain.PipeIngress{Kind: domain.IngressTubo},
		Target:     mustServiceRef(t, "service/origin"),
		Visibility: domain.VisibilityUnlisted,
		Access:     domain.AccessInviteOnly,
	}, "", "")
	if err == nil || strings.Contains(err.Error(), "Step 8") || strings.Contains(err.Error(), "pipe.publish") || strings.Contains(err.Error(), "membership") || strings.Contains(err.Error(), "scope") {
		t.Fatalf("scope-less path must not require discovery/scope material, got %v", err)
	}
}

func mustServiceRef(t *testing.T, raw string) domain.Ref {
	t.Helper()
	ref, err := domain.ParseRef(raw)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestDialServiceHTTPSVerifiesTLSAndCarriesHTTP(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("secure")) }))
	defer server.Close()
	ref, _ := domain.ParseRef("service/secure")
	service := domain.ServiceSpec{Ref: ref, Kind: domain.ServiceHTTP, Target: server.URL}
	if _, err := dialService(service); err == nil {
		t.Fatal("system trust unexpectedly accepted test certificate")
	}
	config := server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	config.MinVersion = tls.VersionTLS12
	conn, err := dialServiceTLS(service, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: test\r\nConnection: close\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status: %s", response.Status)
	}
	if version := conn.(*tls.Conn).ConnectionState().Version; version < tls.VersionTLS12 {
		t.Fatalf("TLS version: %x", version)
	}
}

func TestDialServiceHTTPRemainsPlainTCP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer server.Close()
	ref, _ := domain.ParseRef("service/plain")
	conn, err := dialService(domain.ServiceSpec{Ref: ref, Kind: domain.ServiceHTTP, Target: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if strings.Contains(conn.RemoteAddr().String(), "https") {
		t.Fatal("unexpected TLS address")
	}
}
