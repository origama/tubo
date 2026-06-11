package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/capability"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/peers"
	"github.com/origama/tubo/internal/serviceidentity"
)

type grantRequestFixture struct {
	pub        ed25519.PublicKey
	priv       ed25519.PrivateKey
	serviceID  string
	servicePub string
}

func newGrantRequestFixture(t *testing.T) grantRequestFixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return grantRequestFixture{
		pub:        pub,
		priv:       priv,
		serviceID:  serviceidentity.ServiceIDFromPublicKey(pub),
		servicePub: serviceidentity.EncodePublicKey(pub),
	}
}

func (fx grantRequestFixture) request(t *testing.T, serviceName, servicePeerID, requesterPeerID, nonce string, requestedAt, expiresAt time.Time) grantspkg.Request {
	t.Helper()
	signedReq, err := grantspkg.SignPublishLeaseRequest(grantspkg.PublishLeaseRequest{
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		ServiceID:             fx.serviceID,
		ServicePublicKey:      fx.servicePub,
		PublisherPeerID:       servicePeerID,
		RequestedCapabilities: []string{capability.PermissionAttach, capability.PermissionAnnounce, capability.PermissionShareMint},
		Nonce:                 nonce,
	}, fx.priv)
	if err != nil {
		t.Fatal(err)
	}
	return grantspkg.Request{
		ClusterName:           "home",
		ClusterID:             "cluster-123",
		NamespaceID:           "default",
		RequesterPeerID:       requesterPeerID,
		ServiceName:           serviceName,
		ServiceID:             signedReq.ServiceID,
		ServicePublicKey:      signedReq.ServicePublicKey,
		ServiceOwnerSignature: signedReq.ServiceOwnerSignature,
		RequestNonce:          signedReq.Nonce,
		ServicePeerID:         servicePeerID,
		RequestedPermissions:  append([]string(nil), signedReq.RequestedCapabilities...),
		RequestedAt:           requestedAt,
		ExpiresAt:             expiresAt,
	}
}

func TestGrantsPendingHumanOutputGroupsRepeatedAttemptsAndUsesAlias(t *testing.T) {
	now := time.Now().UTC()
	fx := newGrantRequestFixture(t)
	storePath := filepath.Join(t.TempDir(), "requests.json")
	aliasPath := filepath.Join(t.TempDir(), "aliases.json")
	t.Setenv("TUBO_PEER_ALIAS_STORE", aliasPath)
	aliasStore := peers.NewStore(aliasPath)
	if _, err := aliasStore.Upsert("12D3-requester", "oripi", "verified via SSH"); err != nil {
		t.Fatal(err)
	}
	store := grantspkg.NewStore(storePath)
	req1, err := store.CreatePending(fx.request(t, "myapi", "12D3-service-peer", "12D3-requester", "nonce-1", now.Add(-5*time.Minute), now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	req2, err := store.CreatePending(fx.request(t, "myapi", "12D3-service-peer", "12D3-requester", "nonce-2", now.Add(-4*time.Minute), now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return grantsPendingCmd([]string{"--store", storePath}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Pending grant requests", "oripi", "myapi", "ATTEMPTS", "2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("pending output missing %q: %s", want, out)
		}
	}
	if strings.Count(out, "myapi") != 1 {
		t.Fatalf("expected grouped pending output to mention myapi once, got: %s", out)
	}
	if strings.Contains(out, req1.ID) || strings.Contains(out, req2.ID) {
		t.Fatalf("expected compact pending output to hide full request IDs: %s", out)
	}
}

func TestGrantsHistoryWideKeepsTechnicalIdentifiers(t *testing.T) {
	now := time.Now().UTC()
	fx := newGrantRequestFixture(t)
	storePath := filepath.Join(t.TempDir(), "requests.json")
	store := grantspkg.NewStore(storePath)
	reqB, err := store.CreatePending(fx.request(t, "myapi", "12D3-service-b", "12D3-requester", "nonce-b", now.Add(-10*time.Minute), now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	reqA, err := store.CreatePending(fx.request(t, "myapi", "12D3-service-a", "12D3-requester", "nonce-a", now.Add(-5*time.Minute), now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return grantsHistoryCmd([]string{"--store", storePath, "--wide"}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Grant request history", "history source: authority/local store", "SERVICE_ID", "REQUESTER", "SERVICE_PEER", reqA.ServiceID, reqB.ServiceID, reqA.RequesterPeerID} {
		if !strings.Contains(out, want) {
			t.Fatalf("wide history output missing %q: %s", want, out)
		}
	}
}

func TestGrantsDescribeReviewCardIncludesHistoryAndHints(t *testing.T) {
	now := time.Now().UTC()
	fx := newGrantRequestFixture(t)
	storePath := filepath.Join(t.TempDir(), "requests.json")
	aliasPath := filepath.Join(t.TempDir(), "aliases.json")
	t.Setenv("TUBO_PEER_ALIAS_STORE", aliasPath)
	aliasStore := peers.NewStore(aliasPath)
	if _, err := aliasStore.Upsert("12D3-requester", "oripi", "verified via SSH"); err != nil {
		t.Fatal(err)
	}
	store := grantspkg.NewStore(storePath)
	if _, err := store.CreatePending(fx.request(t, "piwebui@oripi", "12D3-service-peer", "12D3-requester", "nonce-old", now.Add(-2*time.Hour), now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	current, err := store.CreatePending(fx.request(t, "piwebui@oripi", "12D3-service-peer", "12D3-requester", "nonce-new", now.Add(-5*time.Minute), now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return grantsDescribeCmd([]string{current.ID, "--store", storePath}) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Grant request ",
		current.ID,
		"Status: pending",
		"Scope: home/default",
		"Requester",
		"Alias: oripi",
		"Seen before: yes",
		"Previous decisions: 0 approved, 0 denied, 1 expired",
		"Service",
		"Service ID:",
		"Service peer:",
		"Requested permissions",
		"Suggested verification",
		"tubo grants approve ",
		"tubo grants deny ",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("describe output missing %q: %s", want, out)
		}
	}
	if !strings.Contains(out, current.RequesterPeerID) || !strings.Contains(out, current.ServiceID) {
		t.Fatalf("describe output should keep full identifiers: %s", out)
	}
}

func TestGrantsPendingJSONIsMachineReadable(t *testing.T) {
	now := time.Now().UTC()
	fx := newGrantRequestFixture(t)
	storePath := filepath.Join(t.TempDir(), "requests.json")
	store := grantspkg.NewStore(storePath)
	req, err := store.CreatePending(fx.request(t, "myapi", "12D3-service-peer", "12D3-requester", "nonce-json", now.Add(-5*time.Minute), now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return grantsPendingCmd([]string{"--store", storePath, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var payload grantRequestListJSON
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("pending json is not parseable: %v\n%s", err, out)
	}
	if payload.Mode != "pending" || len(payload.Requests) != 1 || len(payload.Groups) != 1 || payload.Requests[0].ID != req.ID {
		t.Fatalf("unexpected pending json payload: %#v", payload)
	}
}

func TestGrantsDescribeJSONIncludesFullRequestData(t *testing.T) {
	now := time.Now().UTC()
	fx := newGrantRequestFixture(t)
	storePath := filepath.Join(t.TempDir(), "requests.json")
	aliasPath := filepath.Join(t.TempDir(), "aliases.json")
	t.Setenv("TUBO_PEER_ALIAS_STORE", aliasPath)
	aliasStore := peers.NewStore(aliasPath)
	if _, err := aliasStore.Upsert("12D3-requester", "oripi", "verified via SSH"); err != nil {
		t.Fatal(err)
	}
	store := grantspkg.NewStore(storePath)
	if _, err := store.CreatePending(fx.request(t, "piwebui@oripi", "12D3-service-peer", "12D3-requester", "nonce-old", now.Add(-2*time.Hour), now.Add(-time.Hour))); err != nil {
		t.Fatal(err)
	}
	current, err := store.CreatePending(fx.request(t, "piwebui@oripi", "12D3-service-peer", "12D3-requester", "nonce-new", now.Add(-5*time.Minute), now.Add(time.Hour)))
	if err != nil {
		t.Fatal(err)
	}
	out, err := capture(func() error { return grantsDescribeCmd([]string{current.ID, "--store", storePath, "--json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var payload grantRequestDescribeJSON
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("describe json is not parseable: %v\n%s", err, out)
	}
	if payload.Request.ID != current.ID || payload.Group.Attempts != 2 || len(payload.RelatedRequests) != 2 || payload.RequesterAlias != "oripi" {
		t.Fatalf("unexpected describe json payload: %#v", payload)
	}
	if !strings.Contains(payload.Review.ApproveCommand, current.ID) || !strings.Contains(payload.Review.DenyCommand, current.ID) {
		t.Fatalf("unexpected review hints: %#v", payload.Review)
	}
	if !strings.Contains(payload.RequesterAlias, "oripi") || payload.Group.Expired != 1 {
		t.Fatalf("unexpected describe grouping data: %#v", payload)
	}
	if current.ID == "" {
		t.Fatalf("expected stored ID to be assigned")
	}
}

func TestPeersAliasCmdStoresAlias(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "aliases.json")
	out, err := capture(func() error {
		return peersAliasCmd([]string{"12D3-peer", "--name", "oripi", "--note", "verified via SSH", "--store", storePath})
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "saved alias") || !strings.Contains(out, "oripi") {
		t.Fatalf("unexpected peers alias output: %s", out)
	}
	alias, ok, err := peers.NewStore(storePath).Lookup("12D3-peer")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || alias.Name != "oripi" || alias.Note != "verified via SSH" {
		t.Fatalf("unexpected stored alias: %#v", alias)
	}
}
