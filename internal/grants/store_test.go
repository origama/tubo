package grants

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/origama/tubo/internal/capability"
)

func TestStoreCreateReloadApproveDenyExpireAndDedupe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.json")
	base := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	store := NewStore(path)
	store.now = func() time.Time { return base }

	req := sampleRequest(base)
	created, err := store.CreatePending(req)
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != StatusPending {
		t.Fatalf("unexpected created request: %#v", created)
	}
	dup, err := store.CreatePending(req)
	if err != nil {
		t.Fatal(err)
	}
	if dup.ID != created.ID {
		t.Fatalf("duplicate request id = %q, want %q", dup.ID, created.ID)
	}

	reloaded := NewStore(path)
	reloaded.now = func() time.Time { return base }
	pending, err := reloaded.ListPending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != created.ID {
		t.Fatalf("unexpected pending after reload: %#v", pending)
	}

	approved, err := reloaded.Approve(created.ID, capability.ServiceClaim{ClusterID: req.ClusterID, NamespaceID: req.NamespaceID, ServiceID: req.ServiceID, SubjectPeerID: req.ServicePeerID, Permissions: req.RequestedPermissions, ExpiresAt: base.Add(time.Hour), Signature: []byte("sig")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if approved.Status != StatusApproved || approved.ServiceClaim == nil {
		t.Fatalf("unexpected approved request: %#v", approved)
	}

	second := sampleRequest(base)
	second.ServiceName = "other"
	second.ServiceID = "service-other"
	second.ServicePeerID = "12D3-other"
	createdSecond, err := reloaded.CreatePending(second)
	if err != nil {
		t.Fatal(err)
	}
	denied, err := reloaded.Deny(createdSecond.ID, "no")
	if err != nil {
		t.Fatal(err)
	}
	if denied.Status != StatusDenied || denied.ServiceClaim != nil {
		t.Fatalf("unexpected denied request: %#v", denied)
	}

	expiring := sampleRequest(base)
	expiring.ServiceName = "expiring"
	expiring.ServiceID = "service-expiring"
	expiring.ServicePeerID = "12D3-expiring"
	expiring.ExpiresAt = base.Add(time.Minute)
	createdExpiring, err := reloaded.CreatePending(expiring)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.now = func() time.Time { return base.Add(2 * time.Minute) }
	changed, err := reloaded.ExpirePending()
	if err != nil {
		t.Fatal(err)
	}
	if changed != 1 {
		t.Fatalf("expired count = %d, want 1", changed)
	}
	if _, err := reloaded.Approve(createdExpiring.ID, capability.ServiceClaim{}, nil, ""); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expected expired approval error, got %v", err)
	}
}

func TestStoreCorruptFileFailsClearly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "requests.json")
	if err := os.WriteFile(path, []byte("not-json"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := NewStore(path).ListAll()
	if err == nil || !strings.Contains(err.Error(), "decode grant request store") {
		t.Fatalf("expected corrupt store error, got %v", err)
	}
}

func sampleRequest(now time.Time) Request {
	return Request{
		ClusterName:          "home",
		ClusterID:            "cluster-123",
		NamespaceID:          "default",
		RequesterPeerID:      "12D3-requester",
		ServiceName:          "myapi",
		ServiceID:            "service-myapi",
		ServicePeerID:        "12D3-service",
		RequestedPermissions: []string{capability.PermissionAttach, capability.PermissionAnnounce},
		RequestedTTLSeconds:  int64((7 * 24 * time.Hour).Seconds()),
		RequestedAt:          now,
		ExpiresAt:            now.Add(time.Hour),
	}
}
