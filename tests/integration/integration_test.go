package integration_test

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
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

	capability "github.com/origama/tubo/internal/capability"
	cfgpkg "github.com/origama/tubo/internal/config"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
	"golang.org/x/crypto/ssh"
)

const integrationEnvVar = "RUN_INTEGRATION"

type integrationStack struct {
	repoRoot     string
	composeFiles []string
}

type servicesResponse struct {
	Count int              `json:"count"`
	Items []serviceSummary `json:"items"`
}

type serviceSummary struct {
	Name string `json:"name"`
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

	if out, err := stack.compose("stop", "service"); err != nil {
		t.Fatalf("compose stop service failed: %v\n%s", err, out)
	}

	waitUntil(t, 75*time.Second, func() bool {
		services, err := stack.services()
		if err != nil {
			return false
		}
		for _, svc := range services {
			if svc.Name == "myapi" {
				return false
			}
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
	stack := newIntegrationStackWithFiles(t, "tests/e2e/compose/relay-nat/compose.yml")
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
		logs, err := stack.logs("edge")
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
	stack := newIntegrationStackWithFiles(t, "tests/e2e/compose/relay-nat/compose.yml")
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
		logs, err := stack.logs("edge")
		if err != nil {
			return false
		}
		return strings.Contains(logs, "connection_path=relayed")
	}, "relay fallback edge log after stress")
}

func TestRelayNATTrafficRecoversAfterRelayRestart(t *testing.T) {
	stack := newIntegrationStackWithFiles(t, "tests/e2e/compose/relay-nat/compose.yml")
	stack.waitBaseReady(t)

	status, _ := edgeRequest(t, "GET", "/v1/dummy?from=pre-relay-restart", "myapi", nil, nil, 20*time.Second)
	if status != http.StatusOK {
		t.Fatalf("expected pre-restart request to succeed, got %d", status)
	}

	if out, err := stack.compose("restart", "relay"); err != nil {
		t.Fatalf("compose restart relay failed: %v\n%s", err, out)
	}

	waitUntil(t, 60*time.Second, func() bool {
		return httpOK("http://127.0.0.1:8092/healthz")
	}, "relay health after restart")
	waitUntil(t, 60*time.Second, func() bool {
		services, err := stack.services()
		if err != nil {
			return false
		}
		for _, svc := range services {
			if svc.Name == "myapi" {
				routes, err := stack.routes()
				if err != nil {
					return false
				}
				for _, rt := range routes {
					if rt.Hostname == "myapi" && rt.PathPrefix == "/" {
						return true
					}
				}
			}
		}
		return false
	}, "route after relay restart")
	waitUntil(t, 45*time.Second, func() bool {
		status, _, err := edgeRequestRaw("POST", "/v1/dummy?from=post-relay-restart", "myapi", map[string]string{"Content-Type": "text/plain"}, []byte("relay-restart-recovery"), 20*time.Second)
		return err == nil && status == http.StatusOK
	}, "relay-first data recovery after relay restart")

	logs, err := stack.logs("edge")
	if err != nil {
		t.Fatalf("edge logs: %v", err)
	}
	if !strings.Contains(logs, "connection_path=relayed") {
		t.Fatalf("expected relayed path in edge logs after relay restart recovery")
	}
}

func TestRelayNATTrafficRecoversAfterEdgeRestartFollowingRelayDisruption(t *testing.T) {
	stack := newIntegrationStackWithFiles(t, "tests/e2e/compose/relay-nat/compose.yml")
	stack.waitBaseReady(t)

	status, _ := edgeRequest(t, "GET", "/v1/dummy?from=pre-edge-restart", "myapi", nil, nil, 20*time.Second)
	if status != http.StatusOK {
		t.Fatalf("expected pre-restart request to succeed, got %d", status)
	}

	if out, err := stack.compose("stop", "relay"); err != nil {
		t.Fatalf("compose stop relay failed: %v\n%s", err, out)
	}
	if out, err := stack.compose("restart", "edge"); err != nil {
		t.Fatalf("compose restart edge failed: %v\n%s", err, out)
	}
	waitUntil(t, 60*time.Second, func() bool {
		return httpOK("http://127.0.0.1:8443/healthz") && httpOK("http://127.0.0.1:8444/healthz")
	}, "edge health after restart")
	if out, err := stack.compose("start", "relay"); err != nil {
		t.Fatalf("compose start relay failed: %v\n%s", err, out)
	}
	waitUntil(t, 60*time.Second, func() bool {
		return httpOK("http://127.0.0.1:8092/healthz")
	}, "relay health after restart")
	waitUntil(t, 60*time.Second, func() bool {
		services, err := stack.services()
		if err != nil {
			return false
		}
		for _, svc := range services {
			if svc.Name == "myapi" {
				routes, err := stack.routes()
				if err != nil {
					return false
				}
				for _, rt := range routes {
					if rt.Hostname == "myapi" && rt.PathPrefix == "/" {
						return true
					}
				}
			}
		}
		return false
	}, "route after edge restart following relay disruption")
	waitUntil(t, 45*time.Second, func() bool {
		status, _, err := edgeRequestRaw("POST", "/v1/dummy?from=post-edge-restart", "myapi", map[string]string{"Content-Type": "text/plain"}, []byte("edge-restart-relay-recovery"), 20*time.Second)
		return err == nil && status == http.StatusOK
	}, "relay-first data recovery after edge restart following relay disruption")

	logs, err := stack.logs("edge")
	if err != nil {
		t.Fatalf("edge logs: %v", err)
	}
	if !strings.Contains(logs, "connection_path=relayed") {
		t.Fatalf("expected relayed path in edge logs after edge restart recovery")
	}
}

func TestRelayNATTrafficDuringServiceRestart(t *testing.T) {
	stack := newIntegrationStackWithFiles(t, "tests/e2e/compose/relay-nat/compose.yml")
	stack.waitBaseReady(t)

	stopAt := time.Now().Add(25 * time.Second)
	results := make(chan stressResult, 8192)
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
	if out, err := stack.compose("restart", "service"); err != nil {
		t.Fatalf("compose restart service failed: %v\n%s", err, out)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(results)
		close(done)
	}()

	summary := summarizeStressResults(results)
	<-done
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

	failures := summary.transportErrors + summary.badStatus + summary.validationErrors
	if failures > 0 {
		failureRate := float64(failures) / float64(summary.total)
		if failures > 10 || failureRate > 0.002 {
			t.Fatalf("restart stress exposed failures: %s", summary.describeFailures())
		}
		t.Logf("restart stress tolerated tiny failure budget: failures=%d total=%d rate=%.4f", failures, summary.total, failureRate)
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
	if stack.usesComposeFile("tests/e2e/compose/relay-nat/compose.yml") {
		// Placeholder until #176 decides whether the relay-first NAT scenario becomes current coverage.
		t.Skip("relay-first NAT integration pending #176; not gated in the current integration suite")
	}

	if err := prepareIntegrationComposeConfig(repoRoot); err != nil {
		t.Fatalf("prepare integration config: %v", err)
	}

	_, _ = stack.compose("down", "--remove-orphans")

	var lastErr error
	var lastOut string
	for attempt := 1; attempt <= 3; attempt++ {
		out, err := stack.composeBuild()
		if err == nil {
			out, err = stack.compose("up", "-d", "--remove-orphans")
		}
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
		lastOut = out
		t.Logf("compose setup attempt %d failed: %v", attempt, err)
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

func prepareIntegrationComposeConfig(repoRoot string) error {
	cfgDir := filepath.Join(repoRoot, "generated", "integration", "tubo")
	if err := os.RemoveAll(cfgDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "clusters", "home", "namespaces", "tenant-a", "services"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(cfgDir, "clusters", "home", "namespaces", "tenant-b", "services"), 0o755); err != nil {
		return err
	}

	clusterID := "cluster-integration"
	namespace := "tenant-a"
	serviceName := "myapi"
	serviceSeed := integrationServiceSeed(clusterID, namespace, serviceName)
	servicePeerID, err := p2p.PeerIDFromSeed(serviceSeed)
	if err != nil {
		return err
	}

	authorityPub, authorityPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	authorityKeyPath := filepath.Join(cfgDir, "clusters", "home", "authority.key")
	if err := writePKCS8PrivateKey(authorityKeyPath, authorityPriv); err != nil {
		return err
	}
	authorityPubKey, err := ssh.NewPublicKey(authorityPub)
	if err != nil {
		return err
	}
	authorityPubKeyStr := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(authorityPubKey)))

	clusterMembershipCap, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   namespace,
		SubjectPeerID: servicePeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		return err
	}
	clusterMembershipCapPath := filepath.Join(cfgDir, "clusters", "home", "membership.cap.json")
	if err := writeJSONFile(clusterMembershipCapPath, clusterMembershipCap); err != nil {
		return err
	}
	namespaceMembershipCap, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   namespace,
		SubjectPeerID: clusterID,
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		return err
	}
	namespaceMembershipCapPath := filepath.Join(cfgDir, "clusters", "home", "namespaces", namespace, "membership.cap.json")
	if err := writeJSONFile(namespaceMembershipCapPath, namespaceMembershipCap); err != nil {
		return err
	}

	serviceOwnerPub, serviceOwnerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	serviceID := serviceidentity.ServiceIDFromPublicKey(serviceOwnerPub)
	serviceOwnerKeyPath := filepath.Join(cfgDir, "clusters", "home", "namespaces", namespace, "services", serviceName+".owner.key")
	if err := writePKCS8PrivateKey(serviceOwnerKeyPath, serviceOwnerPriv); err != nil {
		return err
	}
	leaseReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:             clusterID,
		NamespaceID:           namespace,
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(serviceOwnerPub),
		PublisherPeerID:       servicePeerID.String(),
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		Nonce:                 "integration-publish-lease",
	}, serviceOwnerPriv)
	if err != nil {
		return err
	}
	leaseArtifacts, err := grantspkg.BuildPublishLeaseArtifacts(authorityPriv, leaseReq, serviceName, time.Hour, time.Hour)
	if err != nil {
		return err
	}
	serviceClaim := leaseArtifacts.ServiceClaim
	serviceClaimPath := filepath.Join(cfgDir, "clusters", "home", "namespaces", namespace, "services", serviceName+".claim.json")
	if err := writeJSONFile(serviceClaimPath, serviceClaim); err != nil {
		return err
	}
	servicePublishLeasePath := filepath.Join(cfgDir, "clusters", "home", "namespaces", namespace, "services", serviceName+".publish-lease.json")
	if err := writeJSONFile(servicePublishLeasePath, leaseArtifacts.Lease); err != nil {
		return err
	}
	serviceMembershipCap, err := capability.SignMembershipCapability(capability.MembershipCapability{
		ClusterID:     clusterID,
		NamespaceID:   namespace,
		SubjectPeerID: servicePeerID.String(),
		Permissions: []string{
			capability.PermissionSubscribe,
			capability.PermissionList,
			capability.PermissionPublish,
			capability.PermissionConnect,
		},
		ExpiresAt: time.Now().Add(time.Hour),
	}, authorityPriv)
	if err != nil {
		return err
	}
	serviceMembershipCapPath := filepath.Join(cfgDir, "clusters", "home", "namespaces", namespace, "cluster.membership.cap.json")
	if err := writeJSONFile(serviceMembershipCapPath, serviceMembershipCap); err != nil {
		return err
	}

	swarmKeyPath := filepath.Join(cfgDir, "swarm.key")
	if err := writeSwarmKey(swarmKeyPath); err != nil {
		return err
	}
	containerRoot := "/home/nonroot/.config/tubo"
	authorityKeyContainerPath := filepath.Join(containerRoot, "clusters", "home", "authority.key")
	clusterMembershipCapContainerPath := filepath.Join(containerRoot, "clusters", "home", "membership.cap.json")
	namespaceMembershipCapContainerPath := filepath.Join(containerRoot, "clusters", "home", "namespaces", namespace, "membership.cap.json")
	serviceOwnerKeyContainerPath := filepath.Join(containerRoot, "clusters", "home", "namespaces", namespace, "services", serviceName+".owner.key")
	serviceClaimContainerPath := filepath.Join(containerRoot, "clusters", "home", "namespaces", namespace, "services", serviceName+".claim.json")
	servicePublishLeaseContainerPath := filepath.Join(containerRoot, "clusters", "home", "namespaces", namespace, "services", serviceName+".publish-lease.json")
	swarmKeyContainerPath := filepath.Join(containerRoot, "swarm.key")

	relayPeerID, err := p2p.PeerIDFromSeed("relay-demo-seed")
	if err != nil {
		return err
	}
	edgePeerID, err := p2p.PeerIDFromSeed("edge-demo-seed")
	if err != nil {
		return err
	}
	relayAddr := "/dns4/relay/tcp/4002/p2p/" + relayPeerID.String()
	edgeAddr := "/dns4/edge/tcp/4001/p2p/" + edgePeerID.String()

	relayCfg := cfgpkg.Defaults("relay")
	relayCfg.Node.Seed = "relay-demo-seed"
	relayCfg.Node.P2PListen = "/ip4/0.0.0.0/tcp/4002"
	relayCfg.Network.PrivateKeyFile = swarmKeyContainerPath
	relayCfg.Relay.HealthListen = ":8092"
	relayCfg.Relay.EnableRelayService = true
	relayCfg.Relay.EnableAutoNATService = true
	relayCfg.Relay.EnableDiscoveryPubSub = true
	relayCfg.Relay.ForceReachabilityPublic = true
	relayCfg.Relay.PrintRunCommands = false

	edgeCfg := cfgpkg.Defaults("edge")
	edgeCfg.Node.Seed = "edge-demo-seed"
	edgeCfg.Node.P2PListen = "/ip4/0.0.0.0/tcp/4001"
	edgeCfg.Network.PrivateKeyFile = swarmKeyContainerPath
	edgeCfg.Network.RelayPeers = []string{relayAddr}
	edgeCfg.Edge.Listen = ":8443"
	edgeCfg.Edge.AdminListen = ":8444"
	edgeCfg.CurrentCluster = "home"
	edgeCfg.CurrentNamespace = namespace
	edgeCfg.Clusters = map[string]cfgpkg.Cluster{
		"home": {
			ClusterID:                clusterID,
			AuthorityPublicKey:       authorityPubKeyStr,
			AuthorityPrivateKeyFile:  authorityKeyContainerPath,
			MembershipCapabilityFile: clusterMembershipCapContainerPath,
			Namespaces: map[string]cfgpkg.Namespace{
				namespace: {
					MembershipCapabilityFile: namespaceMembershipCapContainerPath,
					Services: map[string]cfgpkg.NamespaceService{
						serviceName: {
							ServiceID:               serviceID,
							ServiceSeed:             serviceSeed,
							ServiceOwnerKeyFile:     serviceOwnerKeyContainerPath,
							ServiceClaimFile:        serviceClaimContainerPath,
							ServicePublishLeaseFile: servicePublishLeaseContainerPath,
						},
					},
				},
			},
		},
	}

	serviceCfg := cfgpkg.Defaults("service")
	serviceCfg.Node.Seed = serviceSeed
	serviceCfg.Node.P2PListen = "/ip4/0.0.0.0/tcp/40123"
	serviceCfg.Network.PrivateKeyFile = swarmKeyContainerPath
	serviceCfg.Network.BootstrapPeers = []string{edgeAddr, relayAddr}
	serviceCfg.Network.RelayPeers = []string{relayAddr}
	serviceCfg.Network.Autorelay = true
	serviceCfg.Network.HolePunching = true
	serviceCfg.Service.Name = serviceName
	serviceCfg.Service.Target = "http://dummy-api-server:8000"
	serviceCfg.HealthListen = ":8091"
	serviceCfg.HeartbeatInterval = cfgpkg.Duration(5 * time.Second)
	serviceCfg.CurrentCluster = "home"
	serviceCfg.CurrentNamespace = namespace
	serviceCfg.Clusters = map[string]cfgpkg.Cluster{
		"home": {
			ClusterID:                clusterID,
			AuthorityPublicKey:       authorityPubKeyStr,
			AuthorityPrivateKeyFile:  authorityKeyContainerPath,
			MembershipCapabilityFile: clusterMembershipCapContainerPath,
			Namespaces: map[string]cfgpkg.Namespace{
				namespace: {
					MembershipCapabilityFile: namespaceMembershipCapContainerPath,
					Services: map[string]cfgpkg.NamespaceService{
						serviceName: {
							ServiceID:               serviceID,
							ServiceSeed:             serviceSeed,
							ServiceOwnerKeyFile:     serviceOwnerKeyContainerPath,
							ServiceClaimFile:        serviceClaimContainerPath,
							ServicePublishLeaseFile: servicePublishLeaseContainerPath,
						},
					},
				},
			},
		},
	}

	if err := cfgpkg.WriteFile(filepath.Join(cfgDir, "relay.yaml"), relayCfg, true); err != nil {
		return err
	}
	if err := cfgpkg.WriteFile(filepath.Join(cfgDir, "edge.yaml"), edgeCfg, true); err != nil {
		return err
	}
	if err := cfgpkg.WriteFile(filepath.Join(cfgDir, "service.yaml"), serviceCfg, true); err != nil {
		return err
	}
	if err := cfgpkg.WriteFile(filepath.Join(cfgDir, "config.yaml"), serviceCfg, true); err != nil {
		return err
	}
	for _, path := range []string{filepath.Join(cfgDir, "relay.yaml"), filepath.Join(cfgDir, "edge.yaml"), filepath.Join(cfgDir, "service.yaml"), filepath.Join(cfgDir, "config.yaml"), clusterMembershipCapPath, namespaceMembershipCapPath, serviceMembershipCapPath, serviceOwnerKeyPath, serviceClaimPath, servicePublishLeasePath, swarmKeyPath, authorityKeyPath} {
		if err := os.Chmod(path, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func integrationServiceSeed(clusterID, namespace, name string) string {
	sum := sha256.Sum256([]byte("seed\x00" + clusterID + "\x00" + namespace + "\x00" + name))
	return "service-" + hex.EncodeToString(sum[8:24])
}

func writeSwarmKey(path string) error {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	content := "/key/swarm/psk/1.0.0/\n/base16/\n" + hex.EncodeToString(key) + "\n"
	return os.WriteFile(path, []byte(content), 0o600)
}

func writePKCS8PrivateKey(path string, key ed25519.PrivateKey) error {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	return os.WriteFile(path, pemBytes, 0o600)
}

func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func dockerDaemonAvailable() bool {
	cmd := exec.Command("docker", "version")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

func (s *integrationStack) composeBuild() (string, error) {
	out, err := s.compose("build", "--no-parallel")
	if err == nil || !strings.Contains(out, "unknown flag") {
		return out, err
	}
	return s.compose("build")
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
	}, "service health")

	waitUntil(t, 60*time.Second, func() bool {
		services, err := s.services()
		if err != nil {
			return false
		}
		for _, svc := range services {
			if svc.Name == "myapi" {
				routes, err := s.routes()
				if err != nil {
					return false
				}
				for _, rt := range routes {
					if rt.Hostname == "myapi" && rt.PathPrefix == "/" {
						return true
					}
				}
			}
		}
		return false
	}, "discovery+route readiness")

	if s.usesComposeFile("tests/e2e/compose/relay-nat/compose.yml") {
		waitUntil(t, 60*time.Second, func() bool {
			debugPeer, err := s.serviceDebugPeer()
			if err != nil {
				return false
			}
			return strings.Contains(debugPeer, "/p2p-circuit")
		}, "service relay reservation / p2p-circuit address")
	}
}

func (s *integrationStack) services() ([]serviceSummary, error) {
	resp, err := http.Get("http://127.0.0.1:8444/services")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var payload servicesResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, err
	}
	return payload.Items, nil
}

func (s *integrationStack) servicesCount() (int, error) {
	services, err := s.services()
	if err != nil {
		return 0, err
	}
	return len(services), nil
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
