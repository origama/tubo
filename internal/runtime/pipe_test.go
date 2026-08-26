package runtime

import (
	"bufio"
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

func TestRunPublishedPipeRejectsScopeLessTuboIngress(t *testing.T) {
	ref, err := domain.ParseRef("pipe/scope-less")
	if err != nil {
		t.Fatal(err)
	}
	err = runPublishedPipe(context.Background(), nil, domain.PipeSpec{
		Ref:        ref,
		Ingress:    domain.PipeIngress{Kind: domain.IngressTubo},
		Target:     mustServiceRef(t, "service/origin"),
		Visibility: domain.VisibilityUnlisted,
		Access:     domain.AccessInviteOnly,
	}, "", "")
	if err == nil || !strings.Contains(err.Error(), "Step 8") {
		t.Fatalf("expected Step-8 deferred error, got %v", err)
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
