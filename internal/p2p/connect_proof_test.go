package p2p

import (
	"crypto/ed25519"
	crand "crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	libcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	grantspkg "github.com/origama/tubo/internal/grants"
	"github.com/origama/tubo/internal/protocol"
	"golang.org/x/crypto/ssh"
)

func TestConnectAccessLeaseProofValidation(t *testing.T) {
	authPub, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := newProofClient(t)
	invite, err := grantspkg.BuildServiceShareArtifacts(authPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := grantspkg.BuildConnectLeaseArtifacts(authPriv, invite.Payload, client.authorizedKey, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	proof := newAccessProof(t, leases.AccessLease, client)
	validator := ConnectProofValidation{Require: true, AuthorityPublicKey: authPub, ClusterID: "cluster-123", NamespaceID: "default", ServiceID: "svc-123", Replay: NewConnectProofReplayCache(16)}
	if err := validator.Validate(client.peerID, client.pub, &proof); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}
	if err := validator.Validate(client.peerID, client.pub, &proof); err == nil || !strings.Contains(err.Error(), "replay") {
		t.Fatalf("expected replay rejection, got %v", err)
	}
}

func TestConnectAccessLeaseProofRejectsWrongKeyHashAndExpiry(t *testing.T) {
	authPub, authPriv, err := ed25519.GenerateKey(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	client := newProofClient(t)
	stolenClient := newProofClient(t)
	invite, err := grantspkg.BuildServiceShareArtifacts(authPriv, "home", "cluster-123", "default", "myapi", "svc-123", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	leases, err := grantspkg.BuildConnectLeaseArtifacts(authPriv, invite.Payload, client.authorizedKey, time.Minute, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	validator := ConnectProofValidation{Require: true, AuthorityPublicKey: authPub, ClusterID: "cluster-123", NamespaceID: "default", ServiceID: "svc-123", Replay: NewConnectProofReplayCache(16)}

	stolenProof := newAccessProof(t, leases.AccessLease, stolenClient)
	if err := validator.Validate(stolenClient.peerID, stolenClient.pub, &stolenProof); err == nil || !strings.Contains(err.Error(), "thumbprint mismatch") {
		t.Fatalf("expected stolen-key rejection, got %v", err)
	}

	badHash := newAccessProof(t, leases.AccessLease, client)
	badHash.AccessLeaseHash = []byte("bad-hash")
	badHash = signProof(t, badHash, client)
	if err := validator.Validate(client.peerID, client.pub, &badHash); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected hash mismatch, got %v", err)
	}

	expired := leases.AccessLease
	expired.IssuedAt = time.Now().Add(-2 * time.Hour)
	expired.ExpiresAt = time.Now().Add(-time.Hour)
	expired, err = grantspkg.SignConnectAccessLease(expired, authPriv)
	if err != nil {
		t.Fatal(err)
	}
	expiredProof := newAccessProof(t, expired, client)
	if err := validator.Validate(client.peerID, client.pub, &expiredProof); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired access rejection, got %v", err)
	}
}

type proofClient struct {
	priv          libcrypto.PrivKey
	pub           libcrypto.PubKey
	peerID        peer.ID
	authorizedKey string
}

func newProofClient(t *testing.T) proofClient {
	t.Helper()
	priv, pub, err := libcrypto.GenerateEd25519Key(crand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	rawPub, err := pub.Raw()
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(ed25519.PublicKey(rawPub))
	if err != nil {
		t.Fatal(err)
	}
	return proofClient{priv: priv, pub: pub, peerID: pid, authorizedKey: strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))}
}

func newAccessProof(t *testing.T, lease grantspkg.ConnectAccessLease, client proofClient) protocol.ConnectProof {
	t.Helper()
	leaseBytes, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	rawPriv, err := client.priv.Raw()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := protocol.NewConnectProofWithPayload(lease.ClusterID, lease.NamespaceID, lease.ServiceID, lease.ExpiresAt, leaseBytes, grantspkg.ConnectAccessLeaseHashBytes(leaseBytes), client.peerID.String(), ed25519.PrivateKey(rawPriv))
	if err != nil {
		t.Fatal(err)
	}
	return proof
}

func signProof(t *testing.T, proof protocol.ConnectProof, client proofClient) protocol.ConnectProof {
	t.Helper()
	rawPriv, err := client.priv.Raw()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := protocol.SignConnectProof(proof, ed25519.PrivateKey(rawPriv))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
