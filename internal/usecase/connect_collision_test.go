package usecase

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/origama/tubo-rebuilt/internal/domain"
)

// newCollisionHarness builds a UseCases with in-memory repos and fakes, plus
// one owned remote Pipe with a minted invite. invite returns the share token
// whose audience is that exact remote Pipe. The harness itself durably
// creates one Pipe (the remote definition), so baselines start at 1.
func newCollisionHarness(t *testing.T, remoteName string) (UseCases, *memoryPipeRepo, string) {
	t.Helper()
	services := newMemoryServiceRepo()
	pipes := newMemoryPipeRepo()
	secretsDir := t.TempDir()

	u := New(services, pipes)
	u.Secrets = secretsDir
	u.OwnerLocator = &fakeOwnerLocator{address: "relay.tubo.click:4001"}
	u.RedemptionProtocol = "/tubo/exact-redemption/1"
	u.Reach = newFakeReachability()
	u.Redemption = newFakeRedemptionService(secretsDir)

	invite, err := attachAndShare(t, u, remoteName)
	if err != nil {
		t.Fatal(err)
	}
	return u, pipes, invite
}

// attachAndShare durably creates an owned Pipe for remoteName and mints an
// invite for it on the given UseCases (so several remotes share one repo).
func attachAndShare(t *testing.T, u UseCases, remoteName string) (string, error) {
	t.Helper()
	attachResult, err := u.Attach(context.Background(), AttachInput{Name: remoteName, Target: "http://127.0.0.1:8080"})
	if err != nil {
		return "", err
	}
	shareResult, err := u.ShareExact(context.Background(), ShareExactInput{PipeRef: attachResult.PipeRef.String()})
	return shareResult.Invite, err
}

func pipeCount(pipes *memoryPipeRepo) int {
	pipes.mu.Lock()
	defer pipes.mu.Unlock()
	return len(pipes.specs)
}

// TestConnectIdentityRefCollisionNamedThenNoName covers: first connect with a
// friendly name, then reconnect without --name reuses the existing ref instead
// of creating a second identity (which would violate Store PipeID uniqueness).
// Compatible reuse means the durable spec is unchanged, so the same listen is
// used on the reconnect.
func TestConnectIdentityRefCollisionNamedThenNoName(t *testing.T) {
	ctx := context.Background()
	u, pipes, invite := newCollisionHarness(t, "remote-a")

	first, err := u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9001", Name: "west"})
	if err != nil {
		t.Fatal(err)
	}
	if first.PipeRef.String() != "pipe/west" {
		t.Fatalf("first ref = %q", first.PipeRef)
	}
	if pipeCount(pipes) != 2 { // remote-a + west
		t.Fatalf("pipes after first connect = %d", pipeCount(pipes))
	}

	// Reconnect without --name: must reuse pipe/west, not create
	// connect-<pipeID> and not fail with a uniqueness error.
	second, err := u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9001"})
	if err != nil {
		t.Fatalf("reconnect without name failed: %v", err)
	}
	if second.PipeRef.String() != "pipe/west" {
		t.Fatalf("reconnect ref = %q, want pipe/west (existing connection)", second.PipeRef)
	}
	if pipeCount(pipes) != 2 {
		t.Fatalf("pipes after reconnect = %d, want 2", pipeCount(pipes))
	}
}

// TestConnectIdentityRefCollisionNamedThenDifferentName covers: a re-connect
// with a different explicit name must fail with a stable conflict before any
// durable Pipe mutation, never a persistence/duplicate error.
func TestConnectIdentityRefCollisionNamedThenDifferentName(t *testing.T) {
	ctx := context.Background()
	u, pipes, invite := newCollisionHarness(t, "remote-a")

	if _, err := u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9001", Name: "west"}); err != nil {
		t.Fatal(err)
	}
	_, err := u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9001", Name: "east"})
	if err == nil {
		t.Fatal("different-name reconnect succeeded")
	}
	if !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), "pipe/west") {
		t.Fatalf("error not a stable named conflict: %v", err)
	}
	if errors.Is(err, errPipeIDDuplicate) || strings.Contains(err.Error(), "persistence_failed") {
		t.Fatalf("identity collision surfaced as storage error: %v", err)
	}
	if pipeCount(pipes) != 2 {
		t.Fatalf("pipes after failed rename = %d, want 2", pipeCount(pipes))
	}
	if _, _, err := u.Pipes.Get(ctx, "pipe/east"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("pipe/east mutated despite conflict: %v", err)
	}
}

// TestConnectIdentityRefCollisionDefaultThenDifferentName covers the same
// identity rule when the first connection used the deterministic fallback ref.
func TestConnectIdentityRefCollisionDefaultThenDifferentName(t *testing.T) {
	ctx := context.Background()
	u, pipes, invite := newCollisionHarness(t, "remote-a")

	first, err := u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9001"})
	if err != nil {
		t.Fatal(err)
	}
	ownerRef := first.PipeRef.String()
	if !strings.HasPrefix(ownerRef, "pipe/connect-") {
		t.Fatalf("default ref = %q", ownerRef)
	}
	_, err = u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9001", Name: "east"})
	if err == nil || !strings.Contains(err.Error(), "conflict") || !strings.Contains(err.Error(), ownerRef) {
		t.Fatalf("expected conflict identifying %s, got %v", ownerRef, err)
	}
	if pipeCount(pipes) != 2 {
		t.Fatalf("pipes = %d, want 2", pipeCount(pipes))
	}
}

// TestConnectIdentityRefCollisionSameNameReuse covers compatible reuse: the
// same name and same remote must reuse the existing durable definition.
func TestConnectIdentityRefCollisionSameNameReuse(t *testing.T) {
	ctx := context.Background()
	u, pipes, invite := newCollisionHarness(t, "remote-a")

	first, err := u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9001", Name: "west"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := u.ConnectExact(ctx, ConnectExactInput{Invite: invite, LocalListen: "127.0.0.1:9001", Name: "west"})
	if err != nil {
		t.Fatalf("same-name same-remote reuse failed: %v", err)
	}
	if second.PipeRef.String() != first.PipeRef.String() {
		t.Fatalf("refs differ: %s vs %s", second.PipeRef, first.PipeRef)
	}
	if pipeCount(pipes) != 2 {
		t.Fatalf("pipes = %d, want 2", pipeCount(pipes))
	}
}

// TestConnectIdentityRefCollisionSameNameDifferentRemote covers fail-closed
// behavior: the same name targeting a different remote Pipe must conflict
// before mutation, in both directions (ref conflict and identity conflict).
// Two harnesses create two distinct remote Pipes (remote-a, remote-b).
func TestConnectIdentityRefCollisionSameNameDifferentRemote(t *testing.T) {
	ctx := context.Background()
	u, pipes, inviteA := newCollisionHarness(t, "remote-a")
	inviteB, err := attachAndShare(t, u, "remote-b")
	if err != nil {
		t.Fatal(err)
	}
	if pipeCount(pipes) != 2 {
		t.Fatalf("baseline pipes = %d, want 2", pipeCount(pipes))
	}

	if _, err := u.ConnectExact(ctx, ConnectExactInput{Invite: inviteA, LocalListen: "127.0.0.1:9001", Name: "west"}); err != nil {
		t.Fatal(err)
	}
	// Same name, different remote: existing-ref guard fires first.
	_, err = u.ConnectExact(ctx, ConnectExactInput{Invite: inviteB, LocalListen: "127.0.0.1:9001", Name: "west"})
	if err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("same-name different-remote accepted: %v", err)
	}
	if pipeCount(pipes) != 3 {
		t.Fatalf("pipes = %d, want 3", pipeCount(pipes))
	}
	// Fresh name for remote B is a fresh connection.
	if _, err := u.ConnectExact(ctx, ConnectExactInput{Invite: inviteB, LocalListen: "127.0.0.1:9004", Name: "north"}); err != nil {
		t.Fatalf("fresh named connect to remote-b failed: %v", err)
	}
	if pipeCount(pipes) != 4 {
		t.Fatalf("pipes = %d, want 4", pipeCount(pipes))
	}
	// Now west owns remote A and north owns remote B. A nameless reconnect to
	// remote B must reuse north (identity lookup), not fail or duplicate.
	reused, err := u.ConnectExact(ctx, ConnectExactInput{Invite: inviteB, LocalListen: "127.0.0.1:9005"})
	if err != nil {
		t.Fatalf("nameless remote-b reconnect failed: %v", err)
	}
	if reused.PipeRef.String() != "pipe/north" {
		t.Fatalf("nameless remote-b reconnect ref = %q, want pipe/north", reused.PipeRef)
	}
	if pipeCount(pipes) != 4 {
		t.Fatalf("pipes = %d, want 4", pipeCount(pipes))
	}
}
