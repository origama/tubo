package grants

import (
	"strings"
	"testing"
)

func TestRevocationStoreRecordsObjectsAndEpochs(t *testing.T) {
	store := NewRevocationStore(t.TempDir() + "/revocations.json")
	if rec, err := store.RevokeInvite("si_123", "test"); err != nil || rec.ID != "si_123" {
		t.Fatalf("revoke invite = %#v err=%v", rec, err)
	}
	if ok, _, err := store.IsInviteRevoked("si_123"); err != nil || !ok {
		t.Fatalf("invite should be revoked ok=%t err=%v", ok, err)
	}
	if rec, err := store.RevokeSession("cs_123", "test"); err != nil || rec.ID != "cs_123" {
		t.Fatalf("revoke session = %#v err=%v", rec, err)
	}
	if ok, _, err := store.IsSessionRevoked("cs_123"); err != nil || !ok {
		t.Fatalf("session should be revoked ok=%t err=%v", ok, err)
	}
	epoch, err := store.RevokeServiceAccess("svc-123", "rotate")
	if err != nil || epoch != 1 {
		t.Fatalf("access epoch = %d err=%v", epoch, err)
	}
	epoch, err = store.RevokeServiceAccess("svc-123", "rotate-again")
	if err != nil || epoch != 2 {
		t.Fatalf("access epoch = %d err=%v", epoch, err)
	}
	publishEpoch, err := store.RevokePublish("svc-123", "stop")
	if err != nil || publishEpoch != 1 {
		t.Fatalf("publish epoch = %d err=%v", publishEpoch, err)
	}
	if ok, _, err := store.IsPublishRevoked("svc-123"); err != nil || !ok {
		t.Fatalf("publish should be revoked ok=%t err=%v", ok, err)
	}
	epochs, err := store.EpochsForService("svc-123")
	if err != nil {
		t.Fatal(err)
	}
	if epochs.AccessEpoch != 2 || epochs.PublishEpoch != 1 {
		t.Fatalf("epochs = %#v", epochs)
	}
}

func TestRevocationStoreRequiresIDs(t *testing.T) {
	store := NewRevocationStore(t.TempDir() + "/revocations.json")
	cases := []struct {
		name string
		err  error
	}{
		{"invite", func() error { _, err := store.RevokeInvite("", ""); return err }()},
		{"session", func() error { _, err := store.RevokeSession("", ""); return err }()},
		{"service-access", func() error { _, err := store.RevokeServiceAccess("", ""); return err }()},
		{"publish", func() error { _, err := store.RevokePublish("", ""); return err }()},
	}
	for _, tc := range cases {
		if tc.err == nil || !strings.Contains(tc.err.Error(), "required") {
			t.Fatalf("%s expected required error, got %v", tc.name, tc.err)
		}
	}
}
