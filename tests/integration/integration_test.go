package integration_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const integrationEnvVar = "RUN_INTEGRATION"

type integrationStack struct {
	repoRoot string
}

type servicesResponse struct {
	Count int `json:"count"`
}

type route struct {
	Hostname   string `json:"hostname"`
	PathPrefix string `json:"path_prefix"`
	Service    string `json:"service"`
	PeerID     string `json:"peer_id"`
}

type dummyResponse struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	RawQuery string            `json:"raw_query"`
	Headers  map[string]string `json:"headers"`
	BodyB64  string            `json:"body_b64"`
}

func TestEdgeAutoDiscoveryAndProxy(t *testing.T) {
	stack := newIntegrationStack(t)
	stack.waitBaseReady(t)

	payload := []byte("integration-auto-discovery")
	status, body := edgeRequest(t, "POST", "/v1/dummy?from=integration", "myapi", map[string]string{
		"Content-Type": "text/plain",
	}, payload, 30*time.Second)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}

	var resp dummyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, body)
	}

	if resp.Method != "POST" {
		t.Fatalf("expected method POST, got %q", resp.Method)
	}
	if resp.Path != "/v1/dummy" {
		t.Fatalf("expected path /v1/dummy, got %q", resp.Path)
	}
	if resp.RawQuery != "from=integration" {
		t.Fatalf("expected raw_query from=integration, got %q", resp.RawQuery)
	}
	expectedBody := base64.StdEncoding.EncodeToString(payload)
	if resp.BodyB64 != expectedBody {
		t.Fatalf("unexpected body_b64: got %q want %q", resp.BodyB64, expectedBody)
	}
}

func TestStreamingLargeBodiesNoHang(t *testing.T) {
	stack := newIntegrationStack(t)
	stack.waitBaseReady(t)

	payload := bytes.Repeat([]byte("A"), 256*1024)
	status, body := edgeRequest(t, "POST", "/v1/dummy?from=streaming", "myapi", map[string]string{
		"Content-Type": "application/octet-stream",
	}, payload, 45*time.Second)

	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}

	var resp dummyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	expectedBody := base64.StdEncoding.EncodeToString(payload)
	if resp.BodyB64 != expectedBody {
		t.Fatalf("streaming body mismatch: got len=%d want len=%d", len(resp.BodyB64), len(expectedBody))
	}
}

func TestLeaseExpiryRemovesServiceAndRoute(t *testing.T) {
	stack := newIntegrationStack(t)
	stack.waitBaseReady(t)

	if out, err := stack.compose("stop", "service-agent"); err != nil {
		t.Fatalf("compose stop service-agent failed: %v\n%s", err, out)
	}

	waitUntil(t, 75*time.Second, func() bool {
		count, err := stack.servicesCount()
		if err != nil {
			return false
		}
		if count != 0 {
			return false
		}
		routes, err := stack.routes()
		if err != nil {
			return false
		}
		for _, rt := range routes {
			if rt.Hostname == "myapi" {
				return false
			}
		}
		return true
	}, "service expiry and route removal")

	status, body := edgeRequest(t, "GET", "/v1/dummy?from=expiry", "myapi", nil, nil, 15*time.Second)
	if status != http.StatusNotFound {
		t.Fatalf("expected 404 after expiry, got %d body=%s", status, body)
	}
}

func TestHopByHopHeadersStrippedE2E(t *testing.T) {
	stack := newIntegrationStack(t)
	stack.waitBaseReady(t)

	headers := map[string]string{
		"Connection":            "keep-alive",
		"Keep-Alive":            "timeout=5",
		"Proxy-Authenticate":    "Basic realm=test",
		"Proxy-Authorization":   "Basic abc",
		"Te":                    "trailers",
		"Trailer":               "X-Trailer",
		"Transfer-Encoding":     "chunked",
		"Upgrade":               "websocket",
		"X-Integration-Request": "hop-by-hop",
	}

	status, body := edgeRequest(t, "GET", "/v1/dummy?from=hop", "myapi", headers, nil, 30*time.Second)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}

	var resp dummyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, body)
	}

	hopByHop := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}
	for _, h := range hopByHop {
		if _, ok := resp.Headers[h]; ok {
			t.Fatalf("hop-by-hop header %q should be stripped, got value %q", h, resp.Headers[h])
		}
	}

	if got := resp.Headers["X-Integration-Request"]; got != "hop-by-hop" {
		t.Fatalf("expected custom header to pass through, got %q", got)
	}
}

func newIntegrationStack(t *testing.T) *integrationStack {
	t.Helper()
	requireIntegration(t)
	if !dockerDaemonAvailable() {
		t.Skip("docker daemon unavailable; skipping integration test")
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	repoRoot := filepath.Clean(filepath.Join(wd, "../.."))
	stack := &integrationStack{repoRoot: repoRoot}

	_, _ = stack.compose("down", "--remove-orphans")

	var lastErr error
	var lastOut string
	for attempt := 1; attempt <= 3; attempt++ {
		out, err := stack.compose("up", "-d", "--build")
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		lastOut = out
		t.Logf("compose up attempt %d failed: %v", attempt, err)
		time.Sleep(2 * time.Second)
	}
	if lastErr != nil {
		if !dockerDaemonAvailable() || strings.Contains(lastOut, "docker.sock") || strings.Contains(lastOut, "error reading from server: EOF") {
			t.Skipf("docker daemon unavailable during integration setup: %v", lastErr)
		}
		t.Fatalf("compose up failed: %v\n%s", lastErr, lastOut)
	}

	t.Cleanup(func() {
		if out, err := stack.compose("down", "--remove-orphans"); err != nil {
			t.Logf("compose down failed: %v\n%s", err, out)
		}
	})

	return stack
}

func requireIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv(integrationEnvVar) != "1" {
		t.Skipf("set %s=1 to run integration tests", integrationEnvVar)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skipf("docker not found: %v", err)
	}
}

func dockerDaemonAvailable() bool {
	cmd := exec.Command("docker", "version")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func (s *integrationStack) compose(args ...string) (string, error) {
	cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
	cmd.Dir = s.repoRoot
	cmd.Env = append(os.Environ(),
		"DOCKER_BUILDKIT=0",
		"COMPOSE_DOCKER_CLI_BUILD=0",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s *integrationStack) waitBaseReady(t *testing.T) {
	t.Helper()

	waitUntil(t, 60*time.Second, func() bool {
		return httpOK("http://127.0.0.1:8443/healthz")
	}, "edge health")
	waitUntil(t, 60*time.Second, func() bool {
		return httpOK("http://127.0.0.1:8444/healthz")
	}, "edge admin health")
	waitUntil(t, 60*time.Second, func() bool {
		return httpOK("http://127.0.0.1:8091/healthz")
	}, "service-agent health")

	waitUntil(t, 60*time.Second, func() bool {
		count, err := s.servicesCount()
		if err != nil {
			return false
		}
		if count != 1 {
			return false
		}
		routes, err := s.routes()
		if err != nil {
			return false
		}
		for _, rt := range routes {
			if rt.Hostname == "myapi" && rt.PathPrefix == "/" {
				return true
			}
		}
		return false
	}, "discovery+route readiness")
}

func (s *integrationStack) servicesCount() (int, error) {
	resp, err := http.Get("http://127.0.0.1:8444/services")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var payload servicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return 0, err
	}
	return payload.Count, nil
}

func (s *integrationStack) routes() ([]route, error) {
	resp, err := http.Get("http://127.0.0.1:8444/routes")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload []route
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func edgeRequest(t *testing.T, method, path, host string, headers map[string]string, body []byte, timeout time.Duration) (int, []byte) {
	t.Helper()

	client := &http.Client{Timeout: timeout}
	url := "http://127.0.0.1:8443" + path
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("edge request failed: %v", err)
	}
	defer resp.Body.Close()

	respBody := new(bytes.Buffer)
	if _, err := respBody.ReadFrom(resp.Body); err != nil {
		t.Fatalf("read response: %v", err)
	}
	return resp.StatusCode, respBody.Bytes()
}

func httpOK(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func waitUntil(t *testing.T, timeout time.Duration, check func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(1 * time.Second)
	}
	t.Fatalf("timeout waiting for %s after %s", label, timeout)
}

func TestMain(m *testing.M) {
	code := m.Run()
	if os.Getenv(integrationEnvVar) == "1" {
		fmt.Printf("integration tests executed with %s=1\n", integrationEnvVar)
	}
	os.Exit(code)
}
