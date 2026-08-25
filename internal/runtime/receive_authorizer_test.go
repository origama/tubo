package runtime

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/openauth"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
)

// fakeOpenAuthorizer is a deterministic in-memory OpenAuthorizer used to prove
// the receiving-boundary seam decides target dial solely from the neutral
// contract result. No network, session engine, or sleep is involved.
type fakeOpenAuthorizer struct {
	auth openauth.Authorization
	err  error
}

func (f *fakeOpenAuthorizer) AuthorizeOpen(context.Context, openauth.OpenRequest) (openauth.Authorization, error) {
	return f.auth, f.err
}

type dialCounter struct {
	n    int
	fail error
}

func (d *dialCounter) dial(domain.ServiceSpec) (net.Conn, error) {
	d.n++
	if d.fail != nil {
		return nil, d.fail
	}
	server, _ := net.Pipe()
	return server, nil
}

const seamPID = "pip_x"
const seamPub = "client-public-key"

func newOpen() wire.OpenPipe {
	return wire.OpenPipe{PipeID: seamPID, ChallengeProof: "", AccessLease: "lease", HopLimit: 1}
}

func newSeamServer(a openauth.OpenAuthorizer) (*tunnelServer, *dialCounter) {
	dials := &dialCounter{}
	s := &tunnelServer{
		spec:       domain.PipeSpec{PipeID: seamPID},
		service:    domain.ServiceSpec{},
		authorizer: a,
		dialFunc:   dials.dial,
	}
	return s, dials
}

func TestAuthorizeOpenDenialNeverDialsTarget(t *testing.T) {
	now := time.Now().UTC()

	cases := []struct {
		name string
		fake *fakeOpenAuthorizer
	}{
		{name: "authorizer denies", fake: &fakeOpenAuthorizer{err: errors.New("authorization denied")}},
		{name: "wrong pipe id in result", fake: &fakeOpenAuthorizer{auth: openauth.Authorization{PipeID: "pip_other", Subject: seamPub, ExpiresAt: now.Add(time.Hour)}}},
		{name: "wrong subject in result", fake: &fakeOpenAuthorizer{auth: openauth.Authorization{PipeID: seamPID, Subject: "someone-else", ExpiresAt: now.Add(time.Hour)}}},
		{name: "zero expiry in result", fake: &fakeOpenAuthorizer{auth: openauth.Authorization{PipeID: seamPID, Subject: seamPub}}},
		{name: "expired expiry in result", fake: &fakeOpenAuthorizer{auth: openauth.Authorization{PipeID: seamPID, Subject: seamPub, ExpiresAt: now.Add(-time.Minute)}}},
		{name: "no injected authorizer", fake: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var authorizer openauth.OpenAuthorizer
			if tc.fake != nil {
				authorizer = tc.fake
			}
			s, dials := newSeamServer(authorizer)
			_, conn, err := s.authorizeOpen(context.Background(), seamPub, "challenge", newOpen(), now)
			if err == nil {
				_ = conn.Close()
				t.Fatalf("expected authorization denial, got nil")
			}
			if dials.n != 0 {
				t.Fatalf("target dialed %d times on denial, want 0", dials.n)
			}
		})
	}
}

func TestAuthorizeOpenHappyPathDialsTargetExactlyOnce(t *testing.T) {
	now := time.Now().UTC()
	okAuth := openauth.Authorization{PipeID: seamPID, Subject: seamPub, Issuer: "issuer", LeaseID: "jti", ExpiresAt: now.Add(10 * time.Minute), AccessEpoch: 3}
	s, dials := newSeamServer(&fakeOpenAuthorizer{auth: okAuth})
	gotAuth, conn, err := s.authorizeOpen(context.Background(), seamPub, "challenge", newOpen(), now)
	if err != nil {
		t.Fatalf("happy authorization failed: %v", err)
	}
	if dials.n != 1 {
		t.Fatalf("target dial count = %d, want exactly 1 after authorization", dials.n)
	}
	if conn == nil {
		t.Fatal("expected non-nil target connection")
	}
	if gotAuth.Subject != seamPub || gotAuth.PipeID != seamPID || !gotAuth.ExpiresAt.Equal(okAuth.ExpiresAt) {
		t.Fatalf("unexpected authorization returned: %#v", gotAuth)
	}
	_ = conn.Close()
}

// TestAuthorizeOpenZeroExpiryIsHandledBeforeDial guards the zero-time corner so
// a zero (1970) ExpiresAt cannot be mistaken for a valid lease because
// time.After(now) on the wire would then be true relative to the zero value.
func TestAuthorizeOpenZeroExpiryNeverOk(t *testing.T) {
	now := time.Unix(1_000_000_000, 0).UTC() // far after Unix epoch zero
	okZero := openauth.Authorization{PipeID: seamPID, Subject: seamPub, ExpiresAt: time.Time{}}
	s, _ := newSeamServer(&fakeOpenAuthorizer{auth: okZero})
	if _, _, err := s.authorizeOpen(context.Background(), seamPub, "challenge", newOpen(), now); err == nil {
		t.Fatal("zero ExpiresAt must be rejected defensively before dial")
	}
}

// TestAuthorizeOpenTargetUnreachableIsNotAuthorizationDenial proves the
// pre-#48 behavior is preserved: a dial failure returns the target-unreachable
// sentinel, distinct from an authorization denial, so the serve path writes the
// target_unreachable wire code.
func TestAuthorizeOpenTargetUnreachableIsNotAuthorizationDenial(t *testing.T) {
	now := time.Now().UTC()
	okAuth := openauth.Authorization{PipeID: seamPID, Subject: seamPub, ExpiresAt: now.Add(10 * time.Minute)}
	s, dials := newSeamServer(&fakeOpenAuthorizer{auth: okAuth})
	dials.fail = errors.New("tcp dial failed")
	_, conn, err := s.authorizeOpen(context.Background(), seamPub, "challenge", newOpen(), now)
	if err == nil {
		if conn != nil {
			_ = conn.Close()
		}
		t.Fatal("expected target-unreachable error, got nil")
	}
	if !errors.Is(err, errTargetUnreachable) {
		t.Fatalf("dial failure must map to errTargetUnreachable, got %v", err)
	}
	if dials.n != 1 {
		t.Fatalf("target dial count = %d, want 1 (dial attempted after authorization succeeds)", dials.n)
	}
}
