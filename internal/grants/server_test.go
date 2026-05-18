package grants

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/origama/tubo/internal/p2p"
)

func TestGrantServerSubmitPollInvalidScopeAndRequesterBinding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "grant-server")
	if err != nil {
		t.Fatal(err)
	}
	defer serverHost.Close()
	clientHost, err := p2p.NewHostWithSeed("/ip4/127.0.0.1/tcp/0", "grant-client")
	if err != nil {
		t.Fatal(err)
	}
	defer clientHost.Close()
	store := NewStore(filepath.Join(t.TempDir(), "requests.json"))
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	server.Register(serverHost)
	info := peer.AddrInfo{ID: serverHost.ID(), Addrs: serverHost.Addrs()}

	resp, err := Submit(ctx, clientHost, info, validSubmit())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Type != TypePending || resp.RequestID == "" {
		t.Fatalf("unexpected submit response: %#v", resp)
	}
	stored, ok, err := store.Get(resp.RequestID)
	if err != nil || !ok {
		t.Fatalf("stored request missing ok=%t err=%v", ok, err)
	}
	if stored.RequesterPeerID != clientHost.ID().String() {
		t.Fatalf("requester peer id = %q want %q", stored.RequesterPeerID, clientHost.ID())
	}
	poll, err := Poll(ctx, clientHost, info, resp.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if poll.Type != TypePending || poll.RequestID != resp.RequestID {
		t.Fatalf("unexpected poll response: %#v", poll)
	}

	bad := signedSubmit("bad-scope", "myapi", "12D3-service")
	bad.NamespaceID = "other"
	badResp, err := Submit(ctx, clientHost, info, bad)
	if err != nil {
		t.Fatal(err)
	}
	if badResp.Type != TypeDenied {
		t.Fatalf("expected denied invalid scope, got %#v", badResp)
	}
}

func TestGrantServerPendingLimitsAndServiceCollision(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.json"))
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: store, Now: func() time.Time { return now }, MaxPendingRequests: 2, MaxPendingPerRequester: 1, MaxPendingPerService: 1})
	if err != nil {
		t.Fatal(err)
	}
	requester := peer.ID("12D3-requester")
	first := server.HandleMessage(validSubmit(), requester)
	if first.Type != TypePending {
		t.Fatalf("expected pending first request: %#v", first)
	}
	second := signedSubmit("limit-second", "other", "12D3-other")
	limitedRequester := server.HandleMessage(second, requester)
	if limitedRequester.Type != TypeDenied || limitedRequester.Reason == "" {
		t.Fatalf("expected requester rate limit denial: %#v", limitedRequester)
	}
	conflict := signedSubmit("default", "myapi", "12D3-conflict")
	conflictResp := server.HandleMessage(conflict, peer.ID("12D3-other-requester"))
	if conflictResp.Type != TypeDenied || conflictResp.Reason == "" {
		t.Fatalf("expected service collision denial: %#v", conflictResp)
	}
}

func TestGrantServerGlobalPendingLimit(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.json"))
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: store, Now: func() time.Time { return now }, MaxPendingRequests: 1, MaxPendingPerRequester: 10, MaxPendingPerService: 10})
	if err != nil {
		t.Fatal(err)
	}
	first := server.HandleMessage(validSubmit(), peer.ID("12D3-requester-1"))
	if first.Type != TypePending {
		t.Fatalf("expected pending first request: %#v", first)
	}
	second := signedSubmit("global-second", "other", "12D3-other")
	limited := server.HandleMessage(second, peer.ID("12D3-requester-2"))
	if limited.Type != TypeDenied || limited.Reason == "" {
		t.Fatalf("expected global rate limit denial: %#v", limited)
	}
}

func TestGrantServerDuplicateRequest(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.json"))
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: store, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	requester := peer.ID("12D3-requester")
	first := server.HandleMessage(validSubmit(), requester)
	second := server.HandleMessage(validSubmit(), requester)
	if first.RequestID == "" || first.RequestID != second.RequestID {
		t.Fatalf("duplicate requests not deduped: first=%#v second=%#v", first, second)
	}
}
