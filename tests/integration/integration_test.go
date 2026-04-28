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
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

const integrationEnvVar = "RUN_INTEGRATION"

type integrationStack struct {
	repoRoot     string
	composeFiles []string
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

func TestRelayFallbackAcrossIsolatedNetworks(t *testing.T) {
	stack := newIntegrationStackWithFiles(t, "docker-compose.nat.yml")
	stack.waitBaseReady(t)

	status, body := edgeRequest(t, "GET", "/v1/dummy?from=relay-nat", "myapi", nil, nil, 45*time.Second)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", status, body)
	}

	var resp dummyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, body)
	}
	if resp.RawQuery != "from=relay-nat" {
		t.Fatalf("expected raw_query relay-nat, got %q", resp.RawQuery)
	}

	waitUntil(t, 20*time.Second, func() bool {
		logs, err := stack.logs("edge-gateway")
		if err != nil {
			return false
		}
		return strings.Contains(logs, "connection_path=relayed")
	}, "relay fallback edge log")
}

type stressCase struct {
	name     string
	method   string
	path     string
	headers  map[string]string
	body     []byte
	timeout  time.Duration
	validate func(path string, body []byte) error
}

type stressResult struct {
	name    string
	status  int
	latency time.Duration
	err     error
	body    []byte
}

type stressSummary struct {
	total            int
	ok               int
	transportErrors  int
	badStatus        int
	validationErrors int
	p50              time.Duration
	p95              time.Duration
	max              time.Duration
	failureSamples   []string
}

func (s stressSummary) describeFailures() string {
	if len(s.failureSamples) == 0 {
		return "none"
	}
	return strings.Join(s.failureSamples, " | ")
}

func TestRelayNATMixedTrafficStress(t *testing.T) {
	stack := newIntegrationStackWithFiles(t, "docker-compose.nat.yml")
	stack.waitBaseReady(t)

	largeBody := bytes.Repeat([]byte("L"), 512*1024)
	jsonBody := []byte(`{"kind":"stress","items":[1,2,3,4],"ok":true}`)
	textBody := []byte("relay-stress-text")

	cases := []stressCase{
		{
			name:    "get-query",
			method:  "GET",
			path:    "/v1/dummy?case=get-query&n=1",
			timeout: 30 * time.Second,
			validate: func(path string, body []byte) error {
				return validateDummyResponse(path, body, "GET", nil)
			},
		},
		{
			name:    "post-text",
			method:  "POST",
			path:    "/v1/dummy?case=post-text",
			headers: map[string]string{"Content-Type": "text/plain", "X-Stress-Case": "post-text"},
			body:    textBody,
			timeout: 30 * time.Second,
			validate: func(path string, body []byte) error {
				return validateDummyResponse(path, body, "POST", textBody)
			},
		},
		{
			name:    "put-json",
			method:  "PUT",
			path:    "/v1/dummy?case=put-json",
			headers: map[string]string{"Content-Type": "application/json", "X-Stress-Case": "put-json"},
			body:    jsonBody,
			timeout: 30 * time.Second,
			validate: func(path string, body []byte) error {
				return validateDummyResponse(path, body, "PUT", jsonBody)
			},
		},
		{
			name:    "post-large-binary",
			method:  "POST",
			path:    "/v1/dummy?case=post-large-binary",
			headers: map[string]string{"Content-Type": "application/octet-stream", "X-Stress-Case": "post-large-binary"},
			body:    largeBody,
			timeout: 60 * time.Second,
			validate: func(path string, body []byte) error {
				return validateDummyResponse(path, body, "POST", largeBody)
			},
		},
	}

	const concurrency = 12
	const roundsPerWorker = 8

	results := make(chan stressResult, concurrency*roundsPerWorker)
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for round := 0; round < roundsPerWorker; round++ {
				c := cases[(worker+round)%len(cases)]
				path := fmt.Sprintf("%s&worker=%d&round=%d", c.path, worker, round)
				started := time.Now()
				status, body, err := edgeRequestRaw(c.method, path, "myapi", c.headers, c.body, c.timeout)
				res := stressResult{name: c.name, status: status, latency: time.Since(started), err: err, body: body}
				if err == nil && status == http.StatusOK {
					res.err = c.validate(path, body)
				}
				results <- res
			}
		}(worker)
	}

	wg.Wait()
	close(results)

	summary := summarizeStressResults(results)
	t.Logf("relay NAT mixed stress summary: total=%d ok=%d transport_errors=%d bad_status=%d validation_errors=%d p50=%s p95=%s max=%s",
		summary.total, summary.ok, summary.transportErrors, summary.badStatus, summary.validationErrors, summary.p50, summary.p95, summary.max)

	if summary.total != concurrency*roundsPerWorker {
		t.Fatalf("unexpected result count: got %d want %d", summary.total, concurrency*roundsPerWorker)
	}
	if summary.transportErrors > 0 || summary.badStatus > 0 || summary.validationErrors > 0 {
		t.Fatalf("mixed relay stress detected failures: %s", summary.describeFailures())
	}

	waitUntil(t, 20*time.Second, func() bool {
		logs, err := stack.logs("edge-gateway")
		if err != nil {
			return false
		}
		return strings.Contains(logs, "connection_path=relayed")
	}, "relay fallback edge log after stress")
}

func TestRelayNATTrafficDuringServiceRestart(t *testing.T) {
	stack := newIntegrationStackWithFiles(t, "docker-compose.nat.yml")
	stack.waitBaseReady(t)

	stopAt := time.Now().Add(25 * time.Second)
	results := make(chan stressResult, 512)
	var wg sync.WaitGroup

	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			round := 0
			for time.Now().Before(stopAt) {
				body := []byte(fmt.Sprintf("restart-worker-%d-round-%d", worker, round))
				path := fmt.Sprintf("/v1/dummy?case=restart&worker=%d&round=%d", worker, round)
				started := time.Now()
				status, respBody, err := edgeRequestRaw("POST", path, "myapi", map[string]string{"Content-Type": "text/plain"}, body, 20*time.Second)
				res := stressResult{name: "restart-traffic", status: status, latency: time.Since(started), err: err, body: respBody}
				if err == nil && status == http.StatusOK {
					res.err = validateDummyResponse(path, respBody, "POST", body)
				}
				results <- res
				round++
			}
		}(worker)
	}

	time.Sleep(4 * time.Second)
	if out, err := stack.compose("restart", "service-agent"); err != nil {
		t.Fatalf("compose restart service-agent failed: %v\n%s", err, out)
	}

	wg.Wait()
	close(results)

	summary := summarizeStressResults(results)
	t.Logf("relay NAT restart stress summary: total=%d ok=%d transport_errors=%d bad_status=%d validation_errors=%d p50=%s p95=%s max=%s failures=%s",
		summary.total, summary.ok, summary.transportErrors, summary.badStatus, summary.validationErrors, summary.p50, summary.p95, summary.max, summary.describeFailures())

	if summary.total < 12 {
		t.Fatalf("restart stress too small, got only %d requests", summary.total)
	}
	if summary.ok == 0 {
		t.Fatalf("restart stress had zero successful requests: %s", summary.describeFailures())
	}

	stack.waitBaseReady(t)
	status, body := edgeRequest(t, "GET", "/v1/dummy?from=post-restart-recovery", "myapi", nil, nil, 30*time.Second)
	if status != http.StatusOK {
		t.Fatalf("expected recovery request to succeed, got %d body=%s", status, body)
	}

	if summary.transportErrors > 0 || summary.badStatus > 0 || summary.validationErrors > 0 {
		t.Fatalf("restart stress exposed failures: %s", summary.describeFailures())
	}
}

func newIntegrationStack(t *testing.T) *integrationStack {
	return newIntegrationStackWithFiles(t, "docker-compose.yml")
}

func newIntegrationStackWithFiles(t *testing.T, composeFiles ...string) *integrationStack {
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
	stack := &integrationStack{repoRoot: repoRoot, composeFiles: composeFiles}

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
	composeArgs := []string{"compose"}
	for _, file := range s.composeFiles {
		composeArgs = append(composeArgs, "-f", file)
	}
	composeArgs = append(composeArgs, args...)
	cmd := exec.Command("docker", composeArgs...)
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

	if s.usesComposeFile("docker-compose.nat.yml") {
		waitUntil(t, 60*time.Second, func() bool {
			debugPeer, err := s.serviceDebugPeer()
			if err != nil {
				return false
			}
			return strings.Contains(debugPeer, "/p2p-circuit")
		}, "service-agent relay reservation / p2p-circuit address")
	}
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

func (s *integrationStack) logs(service string) (string, error) {
	return s.compose("logs", service, "--no-color")
}

func (s *integrationStack) usesComposeFile(name string) bool {
	for _, file := range s.composeFiles {
		if file == name || strings.HasSuffix(file, "/"+name) {
			return true
		}
	}
	return false
}

func (s *integrationStack) serviceDebugPeer() (string, error) {
	resp, err := http.Get("http://127.0.0.1:8091/debug/peer")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func edgeRequest(t *testing.T, method, path, host string, headers map[string]string, body []byte, timeout time.Duration) (int, []byte) {
	t.Helper()

	status, respBody, err := edgeRequestRaw(method, path, host, headers, body, timeout)
	if err != nil {
		t.Fatalf("edge request failed: %v", err)
	}
	return status, respBody
}

func edgeRequestRaw(method, path, host string, headers map[string]string, body []byte, timeout time.Duration) (int, []byte, error) {
	client := &http.Client{Timeout: timeout}
	url := "http://127.0.0.1:8443" + path
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Host = host
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	respBody := new(bytes.Buffer)
	if _, err := respBody.ReadFrom(resp.Body); err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, respBody.Bytes(), nil
}

func validateDummyResponse(requestPath string, body []byte, expectedMethod string, expectedBody []byte) error {
	var resp dummyResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("unmarshal response: %w body=%s", err, body)
	}
	if resp.Method != expectedMethod {
		return fmt.Errorf("unexpected method: got %q want %q", resp.Method, expectedMethod)
	}
	if resp.Path != "/v1/dummy" {
		return fmt.Errorf("unexpected path: got %q", resp.Path)
	}
	expectedQuery := ""
	if parts := strings.SplitN(requestPath, "?", 2); len(parts) == 2 {
		expectedQuery = parts[1]
	}
	if resp.RawQuery != expectedQuery {
		return fmt.Errorf("unexpected raw query: got %q want %q", resp.RawQuery, expectedQuery)
	}
	expectedBodyB64 := base64.StdEncoding.EncodeToString(expectedBody)
	if resp.BodyB64 != expectedBodyB64 {
		return fmt.Errorf("unexpected body_b64 len: got=%d want=%d", len(resp.BodyB64), len(expectedBodyB64))
	}
	return nil
}

func summarizeStressResults(results <-chan stressResult) stressSummary {
	summary := stressSummary{}
	latencies := make([]time.Duration, 0, 128)
	for res := range results {
		summary.total++
		latencies = append(latencies, res.latency)
		switch {
		case res.err != nil:
			if res.status == http.StatusOK {
				summary.validationErrors++
			} else if res.status == 0 {
				summary.transportErrors++
			} else {
				summary.badStatus++
			}
			if len(summary.failureSamples) < 8 {
				summary.failureSamples = append(summary.failureSamples, fmt.Sprintf("%s status=%d err=%v", res.name, res.status, res.err))
			}
		case res.status != http.StatusOK:
			summary.badStatus++
			if len(summary.failureSamples) < 8 {
				summary.failureSamples = append(summary.failureSamples, fmt.Sprintf("%s unexpected_status=%d body=%s", res.name, res.status, strings.TrimSpace(string(res.body))))
			}
		default:
			summary.ok++
		}
	}
	if len(latencies) == 0 {
		return summary
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	summary.p50 = latencies[len(latencies)/2]
	p95idx := int(float64(len(latencies)-1) * 0.95)
	summary.p95 = latencies[p95idx]
	summary.max = latencies[len(latencies)-1]
	return summary
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
