package discovery

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/authority"
	"github.com/origama/tubo-rebuilt/internal/domain"
)

func TestRecordVerifiesAndNeverContainsServiceTarget(t *testing.T) {
	authorityPublic, authorityPrivate, _ := ed25519.GenerateKey(rand.Reader)
	ownerPublic, ownerPrivate, _ := ed25519.GenerateKey(rand.Reader)
	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	pipeID := authority.PipeID(ownerPublic)
	grant, _ := authority.NewToken("pipe.publish", scope, pipeID, pipeID, time.Hour)
	_ = grant.Sign(authorityPrivate)
	grantString, _ := authority.Encode(grant)
	now := time.Now().UTC()
	record := PipeRecord{Version: 2, PipeID: pipeID, DisplayName: "echo", RuntimePeerID: "peer", Scope: scope, Protocol: "/tubo/tunnel/2.0", Capabilities: []string{"raw-duplex"}, Addresses: []string{"/ip4/127.0.0.1/tcp/1/p2p/peer"}, Visibility: domain.VisibilityDiscoverable, Access: domain.AccessMembers, PublishGrant: grantString, IssuedAt: now, ExpiresAt: now.Add(time.Minute), Sequence: 1}
	_ = record.Sign(ownerPrivate)
	data, err := record.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "service/") || strings.Contains(string(data), "127.0.0.1:8080") {
		t.Fatal("record leaked local target")
	}
	if err := record.Verify(authorityPublic, scope, now); err != nil {
		t.Fatal(err)
	}
	record.DisplayName = "tampered"
	if err := record.Verify(authorityPublic, scope, now); err == nil {
		t.Fatal("tampered record verified")
	}
}
