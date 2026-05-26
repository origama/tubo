package grants

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/origama/tubo/internal/capability"
	"github.com/origama/tubo/internal/p2p"
	"github.com/origama/tubo/internal/serviceidentity"
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

func TestGrantServerExpiredApprovedGrantDoesNotBlockAndActiveOneStillDoes(t *testing.T) {
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	clusterName := "home"
	clusterID := "cluster-123"
	namespaceID := "default"
	serviceName := "myapi"
	label := "stale-approved"
	storePath := filepath.Join(t.TempDir(), "requests.json")

	t.Run("expired approved grant does not block", func(t *testing.T) {
		store := NewStore(storePath)
		store.now = func() time.Time { return now }
		seedApprovedGrant(t, store, clusterName, clusterID, namespaceID, label, serviceName, "12D3-old", "12D3-old-peer", now.Add(time.Hour))
		store.now = func() time.Time { return now.Add(2 * time.Hour) }
		server, err := NewServer(ServerConfig{ClusterName: clusterName, ClusterID: clusterID, NamespaceID: namespaceID, Store: store, Now: func() time.Time { return now.Add(2 * time.Hour) }})
		if err != nil {
			t.Fatal(err)
		}
		resp := server.HandleMessage(signedSubmit(label, serviceName, "12D3-new-peer"), peer.ID("12D3-requester"))
		if resp.Type != TypePending {
			t.Fatalf("expected pending after expired approved grant, got %#v", resp)
		}
		all, err := store.ListAll()
		if err != nil {
			t.Fatal(err)
		}
		for _, req := range all {
			if req.ServicePeerID == "12D3-old-peer" && req.Status == StatusApproved {
				t.Fatalf("expired approved grant remained active: %#v", req)
			}
		}
	})

	t.Run("active approved grant still blocks", func(t *testing.T) {
		store := NewStore(filepath.Join(t.TempDir(), "requests.json"))
		store.now = func() time.Time { return now }
		seedApprovedGrant(t, store, clusterName, clusterID, namespaceID, label, serviceName, "12D3-old", "12D3-old-peer", now.Add(time.Hour))
		server, err := NewServer(ServerConfig{ClusterName: clusterName, ClusterID: clusterID, NamespaceID: namespaceID, Store: store, Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		resp := server.HandleMessage(signedSubmit(label, serviceName, "12D3-new-peer"), peer.ID("12D3-requester"))
		if resp.Type != TypeDenied || !strings.Contains(resp.Reason, "already has an active grant request or claim for a different peer") {
			t.Fatalf("expected collision denial for active approved grant, got %#v", resp)
		}
	})
}

func TestGrantServerAutoApproveResolvesGrantPeersLazilyAtApprovalTime(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.json"))
	authorityPriv, _ := testOwnerKey("authority-grant-peers")
	currentPeers := []string{}
	server, err := NewServer(ServerConfig{
		ClusterName:         "home",
		ClusterID:           "cluster-123",
		NamespaceID:         "default",
		Store:               store,
		AutoApprove:         true,
		AuthorityPrivateKey: authorityPriv,
		ClaimTTL:            time.Hour,
		ServiceShareTTL:     time.Hour,
		GrantServicePeers:   []string{"/ip4/127.0.0.1/tcp/39385/p2p/12D3KooWStale"},
		GrantServicePeersProvider: func() []string {
			return append([]string(nil), currentPeers...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	currentPeers = []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWGrant"}
	ownerPriv, ownerPub := testOwnerKey("lazy-grant-peers")
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	leaseReq, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3-service",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "lazy-grant-peers-nonce",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	serviceAddrs := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3-service"}
	resp := server.HandleMessage(Message{Type: TypeSubmit, Version: VersionV1, ClusterID: "cluster-123", NamespaceID: "default", ServiceName: "myapi", ServiceID: serviceID, ServicePublicKey: leaseReq.ServicePublicKey, ServiceOwnerSignature: leaseReq.ServiceOwnerSignature, ServicePeerID: "12D3-service", ServiceAddresses: serviceAddrs, RequestNonce: leaseReq.Nonce, RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, RequestedTTLSeconds: int64((7 * 24 * time.Hour).Seconds())}, peer.ID("12D3-requester"))
	if resp.Type != TypeApproved || resp.ServiceShareToken == "" {
		t.Fatalf("expected approved response with share token, got %#v", resp)
	}
	payload, err := ParseAndVerifyServiceShareToken(resp.ServiceShareToken)
	if err != nil {
		t.Fatal(err)
	}
	if payload.GrantService.Protocol != ProtocolID {
		t.Fatalf("grant service protocol = %q, want %q", payload.GrantService.Protocol, ProtocolID)
	}
	if !reflect.DeepEqual(payload.GrantService.Peers, currentPeers) {
		t.Fatalf("grant service peers = %#v, want %#v", payload.GrantService.Peers, currentPeers)
	}
	if payload.ServiceEndpoint.PeerID != "12D3-service" || !reflect.DeepEqual(payload.ServiceEndpoint.Addresses, serviceAddrs) {
		t.Fatalf("service endpoint = %#v, want peer=%q addrs=%#v", payload.ServiceEndpoint, "12D3-service", serviceAddrs)
	}
}

func TestGrantServerAutoApproveOmitsGrantServiceMetadataWhenProviderHasNoUsablePeers(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.json"))
	authorityPriv, _ := testOwnerKey("authority-grant-peers-empty")
	server, err := NewServer(ServerConfig{
		ClusterName:         "home",
		ClusterID:           "cluster-123",
		NamespaceID:         "default",
		Store:               store,
		AutoApprove:         true,
		AuthorityPrivateKey: authorityPriv,
		ClaimTTL:            time.Hour,
		ServiceShareTTL:     time.Hour,
		GrantServicePeers:   []string{"/ip4/127.0.0.1/tcp/39385/p2p/12D3KooWStale"},
		GrantServicePeersProvider: func() []string {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ownerPriv, ownerPub := testOwnerKey("lazy-grant-peers-empty")
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	leaseReq, err := SignPublishLeaseRequest(PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             serviceID,
		ServicePublicKey:      serviceidentity.EncodePublicKey(ownerPub),
		PublisherPeerID:       "12D3-service",
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 "lazy-grant-peers-empty-nonce",
	}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp := server.HandleMessage(Message{Type: TypeSubmit, Version: VersionV1, ClusterID: "cluster-123", NamespaceID: "default", ServiceName: "myapi", ServiceID: serviceID, ServicePublicKey: leaseReq.ServicePublicKey, ServiceOwnerSignature: leaseReq.ServiceOwnerSignature, ServicePeerID: "12D3-service", RequestNonce: leaseReq.Nonce, RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, RequestedTTLSeconds: int64((7 * 24 * time.Hour).Seconds())}, peer.ID("12D3-requester"))
	if resp.Type != TypeApproved || resp.ServiceShareToken == "" {
		t.Fatalf("expected approved response with share token, got %#v", resp)
	}
	payload, err := ParseAndVerifyServiceShareToken(resp.ServiceShareToken)
	if err != nil {
		t.Fatal(err)
	}
	if payload.GrantService.Protocol != "" || len(payload.GrantService.Peers) != 0 {
		t.Fatalf("expected no grant_service metadata in parsed payload, got %#v", payload.GrantService)
	}
	raw := decodeServiceShareTokenPayloadJSON(t, resp.ServiceShareToken)
	if _, ok := raw["grant_service"]; ok {
		t.Fatalf("expected raw token to omit grant_service metadata, got %#v", raw["grant_service"])
	}
}

func TestGrantServerShareMintMintsFreshInviteAndCapsTTL(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "requests.json"))
	authorityPriv, _ := testOwnerKey("share-mint-server-authority")
	grantPeers := []string{"/dns4/grants.tubo.test/tcp/4001/p2p/12D3KooWGrantService"}
	server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: store, AuthorityPrivateKey: authorityPriv, ServiceShareTTL: 30 * time.Minute, GrantServicePeersProvider: func() []string { return grantPeers }})
	if err != nil {
		t.Fatal(err)
	}
	ownerPriv, ownerPub := testOwnerKey("share-mint-server-owner")
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	servicePeerID := "12D3KooWService"
	leaseReq, err := SignPublishLeaseRequest(PublishLeaseRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: serviceID, ServicePublicKey: serviceidentity.EncodePublicKey(ownerPub), PublisherPeerID: servicePeerID, RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: "share-mint-server"}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "myapi", time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	serviceAddrs := []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/" + servicePeerID}
	request, err := SignShareMintRequest(ShareMintRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: serviceID, PublishLease: leaseArtifacts.Lease, ServicePeerID: servicePeerID, ServiceAddresses: serviceAddrs, RequestedTTLSeconds: int64((2 * time.Hour).Seconds()), RequestNonce: "share-mint-server-1", RequestIssuedAt: time.Now().UTC()}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp := server.HandleMessage(Message{Type: TypeShareMintRequest, Version: VersionV1, ClusterID: request.ClusterID, NamespaceID: request.NamespaceID, ServiceName: "myapi", ServiceID: request.ServiceID, PublishLease: &request.PublishLease, ServiceOwnerSignature: request.ServiceOwnerSignature, ServicePeerID: request.ServicePeerID, ServiceAddresses: request.ServiceAddresses, RequestedTTLSeconds: request.RequestedTTLSeconds, RequestNonce: request.RequestNonce, RequestIssuedAt: request.RequestIssuedAt}, peer.ID("12D3-requester"))
	if resp.Type != TypeShareMintGranted || resp.ServiceShareToken == "" {
		t.Fatalf("expected granted response, got %#v", resp)
	}
	payload, err := ParseAndVerifyServiceShareToken(resp.ServiceShareToken)
	if err != nil {
		t.Fatal(err)
	}
	if payload.TargetServiceID != serviceID || payload.ServiceEndpoint.PeerID != servicePeerID {
		t.Fatalf("unexpected payload: %#v", payload)
	}
	if payload.GrantService.Protocol != ProtocolID || !reflect.DeepEqual(payload.GrantService.Peers, grantPeers) {
		t.Fatalf("unexpected grant service metadata: %#v", payload.GrantService)
	}
	if payload.ExpiresAt.After(leaseArtifacts.Lease.ExpiresAt.Add(time.Second)) {
		t.Fatalf("share token expiry %s exceeds lease expiry %s", payload.ExpiresAt, leaseArtifacts.Lease.ExpiresAt)
	}
	request.RequestNonce = "share-mint-server-2"
	request.RequestIssuedAt = time.Now().UTC()
	request, err = SignShareMintRequest(request, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	resp2 := server.HandleMessage(Message{Type: TypeShareMintRequest, Version: VersionV1, ClusterID: request.ClusterID, NamespaceID: request.NamespaceID, ServiceName: "myapi", ServiceID: request.ServiceID, PublishLease: &request.PublishLease, ServiceOwnerSignature: request.ServiceOwnerSignature, ServicePeerID: request.ServicePeerID, ServiceAddresses: request.ServiceAddresses, RequestedTTLSeconds: request.RequestedTTLSeconds, RequestNonce: request.RequestNonce, RequestIssuedAt: request.RequestIssuedAt}, peer.ID("12D3-requester"))
	if resp2.Type != TypeShareMintGranted || resp2.ServiceShareToken == "" {
		t.Fatalf("expected second granted response, got %#v", resp2)
	}
	payload2, err := ParseAndVerifyServiceShareToken(resp2.ServiceShareToken)
	if err != nil {
		t.Fatal(err)
	}
	if payload.JTI == payload2.JTI {
		t.Fatalf("expected fresh JTI, got %q", payload.JTI)
	}
}

func TestGrantServerShareMintRejectsInvalidRequests(t *testing.T) {
	authorityPriv, _ := testOwnerKey("share-mint-server-reject-authority")
	ownerPriv, ownerPub := testOwnerKey("share-mint-server-reject-owner")
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	servicePeerID := "12D3KooWService"
	makeRequest := func(caps []string) ShareMintRequest {
		t.Helper()
		leaseReq, err := SignPublishLeaseRequest(PublishLeaseRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: serviceID, ServicePublicKey: serviceidentity.EncodePublicKey(ownerPub), PublisherPeerID: servicePeerID, RequestedCapabilities: caps, Nonce: "share-mint-reject"}, ownerPriv)
		if err != nil {
			t.Fatal(err)
		}
		leaseArtifacts, err := BuildPublishLeaseArtifacts(authorityPriv, leaseReq, "myapi", time.Hour, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		req, err := SignShareMintRequest(ShareMintRequest{ClusterID: "cluster-123", NamespaceID: "default", ServiceID: serviceID, PublishLease: leaseArtifacts.Lease, ServicePeerID: servicePeerID, ServiceAddresses: []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/" + servicePeerID}, RequestedTTLSeconds: int64(time.Hour.Seconds()), RequestNonce: "share-mint-reject", RequestIssuedAt: time.Now().UTC()}, ownerPriv)
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	t.Run("wrong peer id", func(t *testing.T) {
		server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: NewStore(filepath.Join(t.TempDir(), "requests.json")), AuthorityPrivateKey: authorityPriv})
		if err != nil {
			t.Fatal(err)
		}
		req := makeRequest([]string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint})
		req.ServicePeerID = "12D3KooWWrongPeer"
		req, err = SignShareMintRequest(req, ownerPriv)
		if err != nil {
			t.Fatal(err)
		}
		resp := server.HandleMessage(Message{Type: TypeShareMintRequest, Version: VersionV1, ClusterID: req.ClusterID, NamespaceID: req.NamespaceID, ServiceID: req.ServiceID, PublishLease: &req.PublishLease, ServiceOwnerSignature: req.ServiceOwnerSignature, ServicePeerID: req.ServicePeerID, ServiceAddresses: req.ServiceAddresses, RequestedTTLSeconds: req.RequestedTTLSeconds, RequestNonce: req.RequestNonce, RequestIssuedAt: req.RequestIssuedAt}, peer.ID("12D3-requester"))
		if resp.Type != TypeDenied || !strings.Contains(resp.Reason, "publisher peer id mismatch") {
			t.Fatalf("expected wrong peer denial, got %#v", resp)
		}
	})
	t.Run("missing share.mint", func(t *testing.T) {
		server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: NewStore(filepath.Join(t.TempDir(), "requests.json")), AuthorityPrivateKey: authorityPriv})
		if err != nil {
			t.Fatal(err)
		}
		req := makeRequest([]string{capability.PermissionAttach, capability.PermissionAnnounce})
		resp := server.HandleMessage(Message{Type: TypeShareMintRequest, Version: VersionV1, ClusterID: req.ClusterID, NamespaceID: req.NamespaceID, ServiceID: req.ServiceID, PublishLease: &req.PublishLease, ServiceOwnerSignature: req.ServiceOwnerSignature, ServicePeerID: req.ServicePeerID, ServiceAddresses: req.ServiceAddresses, RequestedTTLSeconds: req.RequestedTTLSeconds, RequestNonce: req.RequestNonce, RequestIssuedAt: req.RequestIssuedAt}, peer.ID("12D3-requester"))
		if resp.Type != TypeDenied || !strings.Contains(resp.Reason, "share invite minting") {
			t.Fatalf("expected missing share.mint denial, got %#v", resp)
		}
	})
	t.Run("publish revoked", func(t *testing.T) {
		revocations := NewRevocationStore(filepath.Join(t.TempDir(), "revocations.json"))
		if _, err := revocations.RevokePublish(serviceID, "test"); err != nil {
			t.Fatal(err)
		}
		server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: NewStore(filepath.Join(t.TempDir(), "requests.json")), AuthorityPrivateKey: authorityPriv, Revocations: revocations})
		if err != nil {
			t.Fatal(err)
		}
		req := makeRequest([]string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint})
		resp := server.HandleMessage(Message{Type: TypeShareMintRequest, Version: VersionV1, ClusterID: req.ClusterID, NamespaceID: req.NamespaceID, ServiceID: req.ServiceID, PublishLease: &req.PublishLease, ServiceOwnerSignature: req.ServiceOwnerSignature, ServicePeerID: req.ServicePeerID, ServiceAddresses: req.ServiceAddresses, RequestedTTLSeconds: req.RequestedTTLSeconds, RequestNonce: req.RequestNonce, RequestIssuedAt: req.RequestIssuedAt}, peer.ID("12D3-requester"))
		if resp.Type != TypeDenied || !strings.Contains(resp.Reason, "publish revoked") {
			t.Fatalf("expected revoked denial, got %#v", resp)
		}
	})
	t.Run("local-only endpoint", func(t *testing.T) {
		server, err := NewServer(ServerConfig{ClusterName: "home", ClusterID: "cluster-123", NamespaceID: "default", Store: NewStore(filepath.Join(t.TempDir(), "requests.json")), AuthorityPrivateKey: authorityPriv})
		if err != nil {
			t.Fatal(err)
		}
		req := makeRequest([]string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint})
		req.ServiceAddresses = []string{"/ip4/127.0.0.1/tcp/1234/p2p/" + servicePeerID}
		req, err = SignShareMintRequest(req, ownerPriv)
		if err != nil {
			t.Fatal(err)
		}
		resp := server.HandleMessage(Message{Type: TypeShareMintRequest, Version: VersionV1, ClusterID: req.ClusterID, NamespaceID: req.NamespaceID, ServiceID: req.ServiceID, PublishLease: &req.PublishLease, ServiceOwnerSignature: req.ServiceOwnerSignature, ServicePeerID: req.ServicePeerID, ServiceAddresses: req.ServiceAddresses, RequestedTTLSeconds: req.RequestedTTLSeconds, RequestNonce: req.RequestNonce, RequestIssuedAt: req.RequestIssuedAt}, peer.ID("12D3-requester"))
		if resp.Type != TypeDenied || !strings.Contains(resp.Reason, "not remote-dialable") {
			t.Fatalf("expected endpoint denial, got %#v", resp)
		}
	})
}

func seedApprovedGrant(t *testing.T, store *Store, clusterName, clusterID, namespaceID, label, serviceName, requesterPeerID, servicePeerID string, expiresAt time.Time) Request {
	t.Helper()
	ownerPriv, ownerPub := testOwnerKey(label)
	serviceID := serviceidentity.ServiceIDFromPublicKey(ownerPub)
	leaseReq, err := SignPublishLeaseRequest(PublishLeaseRequest{ClusterID: clusterID, NamespaceID: namespaceID, ServiceID: serviceID, ServicePublicKey: serviceidentity.EncodePublicKey(ownerPub), PublisherPeerID: servicePeerID, RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, Nonce: label + "-nonce"}, ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.CreatePending(Request{ClusterName: clusterName, ClusterID: clusterID, NamespaceID: namespaceID, RequesterPeerID: requesterPeerID, ServiceName: serviceName, ServiceID: serviceID, ServicePublicKey: leaseReq.ServicePublicKey, ServiceOwnerSignature: leaseReq.ServiceOwnerSignature, RequestNonce: leaseReq.Nonce, ServicePeerID: servicePeerID, RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint}, RequestedAt: expiresAt.Add(-24 * time.Hour), ExpiresAt: expiresAt})
	if err != nil {
		t.Fatal(err)
	}
	authorityPriv, _ := testOwnerKey("authority-" + label)
	claim, err := capability.SignServiceClaim(capability.ServiceClaim{ClusterID: clusterID, NamespaceID: namespaceID, ServiceID: serviceID, SubjectPeerID: servicePeerID, Permissions: []string{capability.PermissionAttach, capability.PermissionAnnounce}, ExpiresAt: expiresAt}, authorityPriv)
	if err != nil {
		t.Fatal(err)
	}
	approved, err := store.Approve(created.ID, claim, nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != StatusApproved {
		t.Fatalf("seeded grant not approved: %#v", approved)
	}
	return approved
}
