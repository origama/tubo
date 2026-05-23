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
}
