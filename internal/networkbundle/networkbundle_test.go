package networkbundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestParseAndVerifyValidBundle(t *testing.T) {
	payload := samplePayload(time.Now().Add(-1*time.Hour), time.Now().Add(1*time.Hour))
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	trusted, bundle := signedBundle(t, payloadBytes, "tubo-root-2026")
	parsed, err := Parse(bundle)
	if err != nil {
		t.Fatal(err)
	}
	verifiedPayload, keyID, err := Verify(parsed, trusted)
	if err != nil {
		t.Fatal(err)
	}
	if keyID != "tubo-root-2026" {
		t.Fatalf("key_id = %q", keyID)
	}
	decoded, err := DecodePayload(verifiedPayload)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePayload(decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PublicCluster == nil || decoded.PublicCluster.Name != "home" {
		t.Fatalf("expected public cluster metadata, got %#v", decoded.PublicCluster)
	}
}

func TestVerifyRejectsUnknownKey(t *testing.T) {
	payloadBytes, _ := json.Marshal(samplePayload(time.Now().Add(-1*time.Hour), time.Now().Add(1*time.Hour)))
	trusted, bundle := signedBundle(t, payloadBytes, "tubo-root-2026")
	delete(trusted, "tubo-root-2026")
	parsed, err := Parse(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(parsed, trusted); err == nil || !strings.Contains(err.Error(), "unknown bundle key_id") {
		t.Fatalf("expected unknown key error, got %v", err)
	}
}

func TestVerifyRejectsInvalidSignature(t *testing.T) {
	payloadBytes, _ := json.Marshal(samplePayload(time.Now().Add(-1*time.Hour), time.Now().Add(1*time.Hour)))
	trusted, bundle := signedBundle(t, payloadBytes, "tubo-root-2026")
	parsed, err := Parse(bundle)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Signature.Value = base64.RawURLEncoding.EncodeToString([]byte("not-a-signature"))
	if _, _, err := Verify(parsed, trusted); err == nil || !strings.Contains(err.Error(), "invalid bundle signature") {
		t.Fatalf("expected invalid signature error, got %v", err)
	}
}

func TestValidatePayloadAtRejectsExpiredBundle(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	payload := samplePayload(now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	if err := ValidatePayloadAt(&payload, now); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expiry error, got %v", err)
	}
}

func TestValidatePayloadAtRejectsNotYetValidBundle(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	payload := samplePayload(now.Add(1*time.Hour), now.Add(2*time.Hour))
	if err := ValidatePayloadAt(&payload, now); err == nil || !strings.Contains(err.Error(), "not valid yet") {
		t.Fatalf("expected not-before error, got %v", err)
	}
}

func TestValidatePayloadAtRejectsMalformedRelay(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	payload := samplePayload(now.Add(-1*time.Hour), now.Add(1*time.Hour))
	payload.Relays = []string{"not-a-multiaddr"}
	if err := ValidatePayloadAt(&payload, now); err == nil || !strings.Contains(err.Error(), "invalid relay") {
		t.Fatalf("expected relay error, got %v", err)
	}
}

func TestValidatePayloadAtRejectsRelayWithoutPeerID(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	payload := samplePayload(now.Add(-1*time.Hour), now.Add(1*time.Hour))
	payload.Relays = []string{"/dns4/relay.tubo.click/tcp/4001"}
	if err := ValidatePayloadAt(&payload, now); err == nil || !strings.Contains(err.Error(), "bootstrap peer") {
		t.Fatalf("expected bootstrap peer error, got %v", err)
	}
}

func TestValidatePayloadAtRejectsMalformedSwarmKey(t *testing.T) {
	now := time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	payload := samplePayload(now.Add(-1*time.Hour), now.Add(1*time.Hour))
	payload.SwarmKey.Value = "bad"
	if err := ValidatePayloadAt(&payload, now); err == nil || !strings.Contains(err.Error(), "swarm key") {
		t.Fatalf("expected swarm key error, got %v", err)
	}
}

func samplePayload(notBefore, notAfter time.Time) NetworkPayload {
	return NetworkPayload{
		Name:   "tubo-public",
		ID:     "tubo-public-v1",
		Relays: []string{"/dns4/relay.tubo.click/tcp/4001/p2p/12D3KooWFAEdvKQVbtqdo435wBxoCJxXSUpjC77MEwjVHmZk31t1"},
		SwarmKey: SwarmKeyPayload{
			Type:     "libp2p-pnet",
			Encoding: "text",
			Value:    "/key/swarm/psk/1.0.0/\n/base16/\n00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff\n",
		},
		Network: NetworkOptions{Autorelay: true, HolePunching: true, ForceReachability: "private"},
		PublicCluster: &PublicClusterPayload{
			Name:                 "home",
			ClusterID:            "cluster-public-123",
			AuthorityPublicKey:   "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIEA8cMzoOQb4clMnL7m4Rrp0RzAQXXCCT40PY1DYBOd root@localhost",
			DefaultNamespace:     "default",
			GrantServiceProtocol: "/tubo/grants/1.0",
			GrantServicePeers:    []string{"/dns4/grants.tubo.click/tcp/4001/p2p/12D3KooWFAEdvKQVbtqdo435wBxoCJxXSUpjC77MEwjVHmZk31t1"},
		},
		Validity: ValidityWindow{
			NotBefore: notBefore.UTC().Format(time.RFC3339),
			NotAfter:  notAfter.UTC().Format(time.RFC3339),
		},
	}
}

func signedBundle(t *testing.T, payloadBytes []byte, keyID string) (map[string]string, []byte) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(priv, payloadBytes)
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub)))
	env := Bundle{
		Kind:            "tubo.network.bundle",
		Version:         1,
		PayloadEncoding: "base64url",
		Payload:         base64.RawURLEncoding.EncodeToString(payloadBytes),
		Signature: BundleSignature{
			Alg:   "ed25519",
			KeyID: keyID,
			Value: base64.RawURLEncoding.EncodeToString(sig),
		},
	}
	bundleBytes, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{keyID: authorized}, bundleBytes
}
