package grants

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/origama/tubo/internal/capability"
)

func TestStoreConcurrentInstancesPreserveAllRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.json")
	const count = 32
	var wg sync.WaitGroup
	errs := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := NewStore(path).CreatePending(distinctStoreRequest(index))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	requests, err := NewStore(path).ListAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != count {
		t.Fatalf("persisted requests = %d, want %d", len(requests), count)
	}
}

func TestStoreConcurrentPerRequesterAndServiceLimits(t *testing.T) {
	tests := []struct {
		name   string
		policy PendingPolicy
		adjust func(*Request)
	}{
		{
			name:   "requester",
			policy: PendingPolicy{MaxPendingRequests: 100, MaxPendingPerRequester: 3, MaxPendingPerService: 100},
			adjust: func(req *Request) { req.RequesterPeerID = "shared-requester" },
		},
		{
			name:   "service",
			policy: PendingPolicy{MaxPendingRequests: 100, MaxPendingPerRequester: 100, MaxPendingPerService: 3},
			adjust: func(req *Request) {
				req.ServiceID = "shared-service"
				req.ServicePeerID = "shared-service-peer"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "requests.json")
			const workers = 20
			var wg sync.WaitGroup
			for i := 0; i < workers; i++ {
				wg.Add(1)
				go func(index int) {
					defer wg.Done()
					req := distinctStoreRequest(index)
					test.adjust(&req)
					_, _ = NewStore(path).CreatePendingWithPolicy(req, test.policy)
				}(i)
			}
			wg.Wait()
			pending, err := NewStore(path).ListPending()
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 3 {
				t.Fatalf("pending requests = %d, want limit 3", len(pending))
			}
		})
	}
}

func TestGrantServerConcurrentSubmitsRespectAtomicLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.json")
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	server, err := NewServer(ServerConfig{
		ClusterName:            "home",
		ClusterID:              "cluster-123",
		NamespaceID:            "default",
		Store:                  NewStore(path),
		Now:                    func() time.Time { return now },
		MaxPendingRequests:     4,
		MaxPendingPerRequester: 100,
		MaxPendingPerService:   100,
	})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 20
	responses := make(chan Message, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			label := fmt.Sprintf("concurrent-%d", index)
			responses <- server.HandleMessage(signedSubmit(label, "service-"+strconv.Itoa(index), "service-peer-"+strconv.Itoa(index)), peer.ID("requester-"+strconv.Itoa(index)))
		}(i)
	}
	wg.Wait()
	close(responses)
	pendingResponses := 0
	for response := range responses {
		if response.Type == TypePending {
			pendingResponses++
		}
	}
	pending, err := NewStore(path).ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if pendingResponses != 4 || len(pending) != 4 {
		t.Fatalf("pending responses=%d persisted=%d, want 4", pendingResponses, len(pending))
	}
}

func TestStorePendingPolicyAtomicAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess coverage")
	}
	path := filepath.Join(t.TempDir(), "requests.json")
	barrier := filepath.Join(t.TempDir(), "start")
	const (
		workers = 16
		limit   = 4
	)
	commands := make([]*exec.Cmd, 0, workers)
	outputs := make([]*bytes.Buffer, 0, workers)
	for i := 0; i < workers; i++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestGrantStoreProcessHelper$")
		cmd.Env = append(os.Environ(),
			"TUBO_GRANT_STORE_HELPER=submit",
			"TUBO_GRANT_STORE_PATH="+path,
			"TUBO_GRANT_STORE_BARRIER="+barrier,
			"TUBO_GRANT_STORE_INDEX="+strconv.Itoa(i),
			"TUBO_GRANT_STORE_LIMIT="+strconv.Itoa(limit),
		)
		output := &bytes.Buffer{}
		cmd.Stdout = output
		cmd.Stderr = output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
		outputs = append(outputs, output)
	}
	if err := os.WriteFile(barrier, []byte("go"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper failed: %v\n%s", err, outputs[i].String())
		}
	}
	requests, err := NewStore(path).ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != limit {
		t.Fatalf("pending requests = %d, want exact policy limit %d", len(requests), limit)
	}
}

func TestGrantStoreProcessHelper(t *testing.T) {
	mode := os.Getenv("TUBO_GRANT_STORE_HELPER")
	if mode == "" {
		return
	}
	path := os.Getenv("TUBO_GRANT_STORE_PATH")
	switch mode {
	case "submit":
		waitForTestFile(t, os.Getenv("TUBO_GRANT_STORE_BARRIER"))
		index, err := strconv.Atoi(os.Getenv("TUBO_GRANT_STORE_INDEX"))
		if err != nil {
			t.Fatal(err)
		}
		limit, err := strconv.Atoi(os.Getenv("TUBO_GRANT_STORE_LIMIT"))
		if err != nil {
			t.Fatal(err)
		}
		_, err = NewStore(path).CreatePendingWithPolicy(distinctStoreRequest(index), PendingPolicy{
			MaxPendingRequests:     limit,
			MaxPendingPerRequester: workersSafeLimit(limit),
			MaxPendingPerService:   workersSafeLimit(limit),
		})
		if err != nil && !strings.Contains(err.Error(), "too many pending grant requests") {
			t.Fatal(err)
		}
	case "hold-lock":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		lock, err := acquireGrantFileLock(ctx, path+".lock")
		if err != nil {
			t.Fatal(err)
		}
		defer lock.release()
		if err := os.WriteFile(os.Getenv("TUBO_GRANT_STORE_READY"), []byte("ready"), 0o600); err != nil {
			t.Fatal(err)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func TestStoreApproveDenyConcurrentHasSingleWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.json")
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	created, err := NewStore(path).CreatePending(distinctStoreRequest(1))
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		status string
		err    error
	}
	results := make(chan result, 2)
	go func() {
		<-start
		_, err := NewStore(path).Approve(created.ID, capability.ServiceClaim{ExpiresAt: base.Add(time.Hour)}, nil, nil, "")
		results <- result{status: StatusApproved, err: err}
	}()
	go func() {
		<-start
		_, err := NewStore(path).Deny(created.ID, "denied concurrently")
		results <- result{status: StatusDenied, err: err}
	}()
	close(start)
	first, second := <-results, <-results
	successes := 0
	winner := ""
	for _, outcome := range []result{first, second} {
		if outcome.err == nil {
			successes++
			winner = outcome.status
		}
	}
	if successes != 1 {
		t.Fatalf("successful decisions = %d, want 1: first=%v second=%v", successes, first.err, second.err)
	}
	stored, ok, err := NewStore(path).Get(created.ID)
	if err != nil || !ok {
		t.Fatalf("reload winner: ok=%t err=%v", ok, err)
	}
	if stored.Status != winner {
		t.Fatalf("persisted status = %q, successful decision = %q", stored.Status, winner)
	}
}

func TestRevocationConcurrentInstancesPreserveEpochIncrements(t *testing.T) {
	path := filepath.Join(t.TempDir(), "revocations.json")
	const count = 40
	var wg sync.WaitGroup
	errs := make(chan error, count*2)
	for i := 0; i < count; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := NewRevocationStore(path).RevokeServiceAccess("service-a", "test")
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := NewRevocationStore(path).RevokePublish("service-a", "test")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	epochs, err := NewRevocationStore(path).EpochsForService("service-a")
	if err != nil {
		t.Fatal(err)
	}
	if epochs.AccessEpoch != count || epochs.PublishEpoch != count {
		t.Fatalf("epochs after restart = %#v, want access=%d publish=%d", epochs, count, count)
	}
}

func TestShareRedemptionConcurrentInstancesConsumeOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "share-redemptions.json")
	record := ShareRedemptionRecord{JTI: "invite-one", ClusterID: "cluster-a", NamespaceID: "default", ServiceID: "service-a", TokenExpiresAt: time.Now().Add(time.Hour)}
	const count = 16
	var wg sync.WaitGroup
	results := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- NewShareRedemptionStore(path).TryConsume(record)
		}()
	}
	wg.Wait()
	close(results)
	successes := 0
	alreadyRedeemed := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrShareInviteAlreadyRedeemed):
			alreadyRedeemed++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || alreadyRedeemed != count-1 {
		t.Fatalf("consume results: successes=%d already_redeemed=%d", successes, alreadyRedeemed)
	}
}

func TestGrantStoreLockTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("direct lock fixture uses Unix lock ownership semantics")
	}
	path := filepath.Join(t.TempDir(), "requests.json")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	lock, err := acquireGrantFileLock(ctx, path+".lock")
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	store := NewStore(path)
	store.LockTimeout = 50 * time.Millisecond
	_, err = store.ListAll()
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("lock timeout error = %v", err)
	}
}

func TestGrantStoreFilesRemainPrivateAndMalformedStateFailsClosed(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "requests.json")
	staleTemp := filepath.Join(dir, ".requests.json.tmp-interrupted")
	if err := os.WriteFile(staleTemp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(requestPath).CreatePending(distinctStoreRequest(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staleTemp); !os.IsNotExist(err) {
		t.Fatalf("stale temp was not removed: %v", err)
	}
	for _, path := range []string{requestPath, requestPath + ".lock"} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode = %o, want 600", path, info.Mode().Perm())
		}
	}
	revocationPath := filepath.Join(dir, "revocations.json")
	if err := os.WriteFile(revocationPath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRevocationStore(revocationPath).RevokePublish("service-a", "test"); err == nil || !strings.Contains(err.Error(), "decode revocation store") {
		t.Fatalf("malformed revocation error = %v", err)
	}
	stored, err := os.ReadFile(revocationPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "not-json" {
		t.Fatalf("malformed state was overwritten: %q", stored)
	}
}

func TestGrantStoreRejectsSymlinkLockPath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reparse-point rejection is cross-compiled")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.json")
	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".lock"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path).ListAll(); err == nil {
		t.Fatal("expected symlink lock rejection")
	}
	stored, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "unchanged" {
		t.Fatalf("symlink target changed: %q", stored)
	}
}

func TestGrantStoreLockReleasedAfterProcessCrash(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("process signal semantics")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "requests.json")
	ready := filepath.Join(dir, "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestGrantStoreProcessHelper$")
	cmd.Env = append(os.Environ(),
		"TUBO_GRANT_STORE_HELPER=hold-lock",
		"TUBO_GRANT_STORE_PATH="+path,
		"TUBO_GRANT_STORE_READY="+ready,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForTestFile(t, ready)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	store := NewStore(path)
	store.LockTimeout = time.Second
	if _, err := store.CreatePending(distinctStoreRequest(99)); err != nil {
		t.Fatalf("mutation after lock holder crash: %v", err)
	}
}

func distinctStoreRequest(index int) Request {
	base := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	req := sampleRequest(base.Add(time.Duration(index) * time.Second))
	req.ServiceName = fmt.Sprintf("service-%d", index)
	req.ServiceID = fmt.Sprintf("service-id-%d", index)
	req.ServicePublicKey = fmt.Sprintf("public-key-%d", index)
	req.ServicePeerID = fmt.Sprintf("peer-%d", index)
	req.RequesterPeerID = fmt.Sprintf("requester-%d", index)
	req.RequestNonce = fmt.Sprintf("nonce-%d", index)
	return req
}

func workersSafeLimit(limit int) int { return limit * 10 }

func waitForTestFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", path)
}
