package main

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"testing"
	"time"

	grantspkg "github.com/origama/tubo/internal/grants"
)

func TestConnectWorkflowParseShareTokenKeepsLegacyGrantWhenGrantServiceMetadataExists(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifacts, err := grantspkg.BuildServiceShareArtifacts(priv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	payload := artifacts.Payload
	payload.GrantService = grantspkg.GrantServiceEndpoint{
		Protocol: grantspkg.ProtocolID,
		Peers:    []string{"/ip4/127.0.0.1/tcp/1/p2p/12D3KooWFallbackGrantPeer"},
	}
	payload.ServiceEndpoint = grantspkg.ServiceEndpoint{PeerID: "12D3KooWServicePeer", Addresses: []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWRelay/p2p-circuit/p2p/12D3KooWServicePeer"}}
	token, err := grantspkg.SignServiceShareToken(payload, priv)
	if err != nil {
		t.Fatal(err)
	}
	info, err := newConnectWorkflow().ParseShareToken(token)
	if err != nil {
		t.Fatal(err)
	}
	if info.ConnectInviteToken != token {
		t.Fatalf("ConnectInviteToken = %q, want original token", info.ConnectInviteToken)
	}
	if len(info.ConnectGrantPeers) != 1 {
		t.Fatalf("ConnectGrantPeers = %#v", info.ConnectGrantPeers)
	}
	if info.ConnectGrant == nil {
		t.Fatal("expected embedded legacy connect grant to be preserved")
	}
	if info.ConnectGrant.ServiceID != payload.TargetServiceID {
		t.Fatalf("ConnectGrant.ServiceID = %q, want %q", info.ConnectGrant.ServiceID, payload.TargetServiceID)
	}
	if info.ServiceEndpointPeer != payload.ServiceEndpoint.PeerID || len(info.ServiceEndpointAddrs) != 1 || info.ServiceEndpointAddrs[0] != payload.ServiceEndpoint.Addresses[0] {
		t.Fatalf("service endpoint = %q %#v", info.ServiceEndpointPeer, info.ServiceEndpointAddrs)
	}
}
