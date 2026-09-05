// Protocol-level exact redemption -> accepted relationship -> exact renewal
// tests over real framing (wire.WriteFrame/ReadFrame) on in-memory net.Pipe
// streams. These exercise the production handlers (redemption + renewal) with
// a real Store, covering the durable remote-Pipe binding lifecycle at the
// protocol boundary: initial redemption persists the authority-side accepted
// relationship, and renewal mints a fresh short-lived AccessLease without ever
// resending the invite.
package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/accesslease"
	"github.com/origama/tubo-rebuilt/internal/exactauthority"
	"github.com/origama/tubo-rebuilt/internal/localkey"
	wire "github.com/origama/tubo-rebuilt/internal/protocol"
	"github.com/origama/tubo-rebuilt/internal/store"
	"github.com/origama/tubo-rebuilt/internal/usecase"
)

// connCarrier adapts a net.Conn to the L4 usecase.Carrier contract.
type connCarrier struct {
	conn net.Conn
}

func (c *connCarrier) Read(p []byte) (int, error)  { return c.conn.Read(p) }
func (c *connCarrier) Write(p []byte) (int, error) { return c.conn.Write(p) }
func (c *connCarrier) Close() error                { return c.conn.Close() }

// renewalServerFixture provisions the owner/authority + accepted recipient key
// on a real store-backed TUBO_HOME and exposes both handlers.
type renewalServerFixture struct {
	home             string
	pipeID           string
	loader           exactauthority.Loader
	secretsDir       string
	recipientPrivate ed25519.PrivateKey
	recipientPublic  string
	ttl              time.Duration
}

func newRenewalServerFixture(t *testing.T) *renewalServerFixture {
	t.Helper()
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	secretsDir := state.SecretPath()
	_, ownerPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pipeID := accesslease.PipeIDFromOwner(ownerPriv.Public().(ed25519.PublicKey))
	keyPath, err := exactauthority.KeyPath(secretsDir, pipeID)
	if err != nil {
		t.Fatal(err)
	}
	workflow := exactauthority.LocalOwnerWorkflow{
		KeyPath:            keyPath,
		PipeID:             pipeID,
		Epoch:              1,
		RedemptionPeers:    "/dns4/relay.test/tcp/4001/p2p/peer/p2p-circuit/p2p/peer",
		RedemptionProtocol: usecaseRedemptionProtocolForTest,
	}
	auth, err := workflow.Provision(ownerPriv)
	if err != nil {
		t.Fatal(err)
	}
	// Accepted recipient identity key (deterministic post-acceptance path).
	recipientPath := filepath.Join(secretsDir, "pipes", "connect-"+pipeID+".key")
	recipientPrivate, err := localkey.Provision(recipientPath)
	if err != nil {
		t.Fatal(err)
	}
	return &renewalServerFixture{
		home:             state.Home(),
		pipeID:           pipeID,
		loader:           exactauthority.Loader{KeyPath: keyPath, Binding: auth.Binding},
		secretsDir:       secretsDir,
		recipientPrivate: recipientPrivate,
		recipientPublic:  accesslease.PublicString(recipientPrivate.Public().(ed25519.PublicKey)),
		ttl:              10 * time.Minute,
	}
}

const usecaseRedemptionProtocolForTest = "/tubo/exact-redemption/1"

// openStore opens the fixture's store again (authority-side restart). The
// test process keeps TUBO_HOME set by the fixture constructor.
func (f *renewalServerFixture) openStore(ctx context.Context) (*store.Store, error) {
	return store.Open(ctx)
}

func TestExactRedemptionThenRenewalOverRealFraming(t *testing.T) {
	f := newRenewalServerFixture(t)
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	// Mint an invite for redemption.
	auth, err := f.loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	invite, err := accesslease.NewExactInvite(auth.Private, auth.Binding, f.pipeID, time.Now().UTC(), 10*time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// ---- Phase 1: redemption over a real framed stream ----
	server, client := net.Pipe()
	done := make(chan bool, 1)
	go func() {
		newExactRedemptionHandler(state, f.pipeID, f.ttl).handle(&fakeStream{conn: server})
		done <- true
	}()
	redemptionClient := NewExactRedemptionClient(f.secretsDir)
	lease, err := redemptionClient.Redeem(ctx, &connCarrier{conn: client}, invite, f.recipientPublic)
	<-done
	if err != nil {
		t.Fatalf("redemption: %v", err)
	}
	_ = client.Close()
	if lease.Subject != f.recipientPublic || lease.AudiencePipeID != f.pipeID {
		t.Fatalf("redemption lease mismatch: %#v", lease)
	}
	// The authority-side accepted relationship was persisted.
	adapter := store.NewExactAuthorityRevocationState(state, f.pipeID)
	jti, ok, err := adapter.AcceptedRedemption(ctx, f.pipeID, f.recipientPublic)
	if err != nil || !ok || jti == "" {
		t.Fatalf("accepted relationship not persisted after redemption: %q %v %v", jti, ok, err)
	}

	// ---- Phase 2: renewal over a fresh real framed stream (no invite) ----
	server2, client2 := net.Pipe()
	done2 := make(chan bool, 1)
	go func() {
		newExactRenewalHandler(state, f.pipeID, f.ttl).handle(&fakeStream{conn: server2})
		done2 <- true
	}()
	renewalClient := NewExactRenewalClient(f.secretsDir)
	renewed, err := renewalClient.Renew(ctx, &connCarrier{conn: client2}, usecase.RenewalRequest{
		PipeID:          f.pipeID,
		RecipientPublic: f.recipientPublic,
		OwnerBinding:    encodedBindingFor(auth.Binding),
	})
	<-done2
	if err != nil {
		t.Fatalf("renewal: %v", err)
	}
	_ = client2.Close()
	if renewed.JTI == lease.JTI {
		t.Fatal("renewal must mint a fresh lease")
	}
	if renewed.Subject != f.recipientPublic || renewed.AudiencePipeID != f.pipeID || !renewed.ExpiresAt.After(time.Now().UTC()) {
		t.Fatalf("renewed lease mismatch: %#v", renewed)
	}

	// ---- Phase 3: authority/consumer restart, renewal still works ----
	_ = state.Close()
	state2, err := f.openStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state2.Close()
	server3, client3 := net.Pipe()
	done3 := make(chan bool, 1)
	go func() {
		newExactRenewalHandler(state2, f.pipeID, f.ttl).handle(&fakeStream{conn: server3})
		done3 <- true
	}()
	renewed2, err := renewalClient.Renew(ctx, &connCarrier{conn: client3}, usecase.RenewalRequest{
		PipeID:          f.pipeID,
		RecipientPublic: f.recipientPublic,
		OwnerBinding:    encodedBindingFor(auth.Binding),
	})
	<-done3
	if err != nil {
		t.Fatalf("renewal after authority restart: %v", err)
	}
	_ = client3.Close()
	if renewed2.JTI == renewed.JTI || !renewed2.ExpiresAt.After(time.Now().UTC()) {
		t.Fatal("post-restart renewal must mint a fresh valid lease")
	}
}

func encodedBindingFor(b accesslease.OwnerBinding) string {
	encoded, _ := accesslease.EncodeOwnerBinding(b)
	return encoded
}

func TestExactRenewalNegativeProtocolCases(t *testing.T) {
	f := newRenewalServerFixture(t)
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	handler := newExactRenewalHandler(state, f.pipeID, f.ttl)
	auth, err := f.loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	binding := encodedBindingFor(auth.Binding)

	runExchange := func(req wire.ExactRenewalRequest, signer func(challenge string) string) (wire.ExactRenewalResponse, error) {
		server, client := net.Pipe()
		done := make(chan bool, 1)
		go func() {
			handler.handle(&fakeStream{conn: server})
			done <- true
		}()
		var resp wire.ExactRenewalResponse
		cleanup := func() {
			_ = client.Close()
			<-done
		}
		if err := wire.WriteFrame(client, req); err != nil {
			cleanup()
			return resp, err
		}
		// The server either challenges (it accepted the request shape) or
		// rejects the request outright with a response. Discriminate on the raw
		// first frame: a challenge carries "challenge"; anything else is the
		// final response.
		first, err := readRawFrame(client)
		if err != nil {
			cleanup()
			return resp, err
		}
		if strings.Contains(first, "challenge") {
			if signer == nil {
				cleanup()
				return resp, errors.New("no signer")
			}
			var challenge wire.ExactRenewalChallenge
			if err := json.Unmarshal([]byte(first), &challenge); err != nil {
				cleanup()
				return resp, err
			}
			if err := wire.WriteFrame(client, wire.ExactRenewalProof{ChallengeProof: signer(challenge.Challenge)}); err != nil {
				cleanup()
				return resp, err
			}
			raw, err := readRawFrame(client)
			if err != nil {
				cleanup()
				return resp, err
			}
			if err := json.Unmarshal([]byte(raw), &resp); err != nil {
				cleanup()
				return resp, err
			}
		} else {
			if err := json.Unmarshal([]byte(first), &resp); err != nil {
				cleanup()
				return resp, err
			}
		}
		cleanup()
		return resp, nil
	}

	// Unknown binding: never redeemed recipient/pipe.
	resp, err := runExchange(wire.ExactRenewalRequest{PipeID: f.pipeID, RecipientPublic: f.recipientPublic, OwnerBinding: binding}, func(ch string) string {
		return accesslease.SignProof(f.recipientPrivate, accesslease.RenewProofPurpose, f.pipeID, ch)
	})
	if err != nil || resp.OK || resp.Error == "" {
		t.Fatalf("unknown binding must be denied: %v %#v", err, resp)
	}

	// Establish the accepted relationship first.
	adapter := store.NewExactAuthorityRevocationState(state, f.pipeID)
	if _, err := adapter.RedeemInvite(ctx, "jti_neg", f.pipeID, "ses_neg", f.recipientPublic); err != nil {
		t.Fatal(err)
	}

	// Wrong subject: possession proof by a different key.
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	resp, err = runExchange(wire.ExactRenewalRequest{PipeID: f.pipeID, RecipientPublic: f.recipientPublic, OwnerBinding: binding}, func(ch string) string {
		return accesslease.SignProof(otherPriv, accesslease.RenewProofPurpose, f.pipeID, ch)
	})
	if err != nil || resp.OK {
		t.Fatal("wrong-subject proof must be denied")
	}

	// Replay: the request carries a fresh challenge; sign over a stale one.
	resp, err = runExchange(wire.ExactRenewalRequest{PipeID: f.pipeID, RecipientPublic: f.recipientPublic, OwnerBinding: binding}, func(ch string) string {
		return accesslease.SignProof(f.recipientPrivate, accesslease.RenewProofPurpose, f.pipeID, "stale-"+ch)
	})
	if err != nil || resp.OK {
		t.Fatal("replayed proof must be denied")
	}

	// Wrong pipe: request for a pipe this handler does not serve.
	resp, err = runExchange(wire.ExactRenewalRequest{PipeID: "pip_not_served", RecipientPublic: f.recipientPublic, OwnerBinding: binding}, func(ch string) string {
		return accesslease.SignProof(f.recipientPrivate, accesslease.RenewProofPurpose, "pip_not_served", ch)
	})
	if err != nil || resp.OK || resp.Error != "not_found" {
		t.Fatalf("wrong pipe must be not_found: %v %#v", err, resp)
	}

	// Revoked/epoch-stale: pipe-access revocation must deny renewal.
	if err := state.Revoke(ctx, f.pipeID, "pipe-access", f.pipeID, 1); err != nil {
		t.Fatal(err)
	}
	resp, err = runExchange(wire.ExactRenewalRequest{PipeID: f.pipeID, RecipientPublic: f.recipientPublic, OwnerBinding: binding}, func(ch string) string {
		return accesslease.SignProof(f.recipientPrivate, accesslease.RenewProofPurpose, f.pipeID, ch)
	})
	if err != nil || resp.OK {
		t.Fatal("revoked pipe must deny renewal")
	}
}

// readRawFrame reads one length-delimited frame body as JSON text. It is the
// test-side raw counterpart of wire.ReadFrame, used to discriminate a renewal
// challenge from a rejection response.
func readRawFrame(conn net.Conn) (string, error) {
	var size [4]byte
	if _, err := io.ReadFull(conn, size[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint32(size[:])
	if n == 0 || n > wire.MaxFrame {
		return "", errors.New("raw frame size out of bounds")
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(conn, data); err != nil {
		return "", err
	}
	return string(data), nil
}

func TestExactRenewalMalformedAndOversizedFrames(t *testing.T) {
	f := newRenewalServerFixture(t)
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	handler := newExactRenewalHandler(state, f.pipeID, f.ttl)

	// Malformed JSON frame body.
	server, client := net.Pipe()
	done := make(chan bool, 1)
	go func() {
		handler.handle(&fakeStream{conn: server})
		done <- true
	}()
	size := make([]byte, 4)
	binary.BigEndian.PutUint32(size[:], uint32(3))
	_, _ = client.Write(size[:])
	_, _ = client.Write(make([]byte, 3)) // not valid JSON
	var resp wire.ExactRenewalResponse
	if err := wire.ReadFrame(client, &resp); err != nil {
		t.Fatalf("malformed frame read: %v", err)
	}
	if resp.OK || resp.Error != "invalid_request" {
		t.Fatalf("malformed frame must be invalid_request: %#v", resp)
	}
	_ = client.Close()
	<-done

	// Oversized frame (length header > MaxFrame). Only the length header is
	// written: the bounded decoder rejects on the header alone and the server
	// replies invalid_request. A body is deliberately not written so a
	// rendezvous pipe cannot deadlock waiting for the server to consume it.
	server2, client2 := net.Pipe()
	done2 := make(chan bool, 1)
	go func() {
		handler.handle(&fakeStream{conn: server2})
		done2 <- true
	}()
	size2 := make([]byte, 4)
	binary.BigEndian.PutUint32(size2[:], wire.MaxFrame+1)
	_, _ = client2.Write(size2[:])
	var resp2 wire.ExactRenewalResponse
	if err := wire.ReadFrame(client2, &resp2); err != nil {
		t.Fatalf("oversized frame read: %v", err)
	}
	if resp2.OK || resp2.Error != "invalid_request" {
		t.Fatalf("oversized frame must be invalid_request: %#v", resp2)
	}
	_ = client2.Close()
	<-done2
}

func TestExactRenewalAuthorityUnavailableFailsClosed(t *testing.T) {
	f := newRenewalServerFixture(t)
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	// A pipe whose authority key never existed: renewal must fail closed and
	// never return a lease.
	handler := newExactRenewalHandler(state, "pip_missing_authority", f.ttl)
	server, client := net.Pipe()
	done := make(chan bool, 1)
	go func() {
		handler.handle(&fakeStream{conn: server})
		done <- true
	}()
	_ = wire.WriteFrame(client, wire.ExactRenewalRequest{PipeID: "pip_missing_authority", RecipientPublic: f.recipientPublic, OwnerBinding: "bGV4thobnose"})
	var resp wire.ExactRenewalResponse
	if err := wire.ReadFrame(client, &resp); err != nil {
		t.Fatalf("read: %v", err)
	}
	if resp.OK {
		t.Fatal("missing authority must deny renewal")
	}
	_ = client.Close()
	<-done
}

// TestExactRenewalConcurrentMints is race-relevant: concurrent renewals run
// against the same Store (SQLite WAL, one writer) and each must receive a
// distinct fresh lease or a fail-closed denial — never a wrong-subject lease.
func TestExactRenewalConcurrentMints(t *testing.T) {
	f := newRenewalServerFixture(t)
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	adapter := store.NewExactAuthorityRevocationState(state, f.pipeID)
	if _, err := adapter.RedeemInvite(ctx, "jti_concurrent", f.pipeID, "ses_conc", f.recipientPublic); err != nil {
		t.Fatal(err)
	}
	auth, err := f.loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	binding := encodedBindingFor(auth.Binding)
	handler := newExactRenewalHandler(state, f.pipeID, f.ttl)
	client := NewExactRenewalClient(f.secretsDir)

	var wg sync.WaitGroup
	ret := make([]error, 8)
	wg.Add(8)
	for i := range 8 {
		go func(i int) {
			defer wg.Done()
			server, pipe := net.Pipe()
			done := make(chan bool, 1)
			go func() {
				handler.handle(&fakeStream{conn: server})
				done <- true
			}()
			lease, err := client.Renew(ctx, &connCarrier{conn: pipe}, usecase.RenewalRequest{
				PipeID:          f.pipeID,
				RecipientPublic: f.recipientPublic,
				OwnerBinding:    binding,
			})
			_ = pipe.Close()
			<-done
			if err != nil {
				ret[i] = err
				return
			}
			if lease.Subject != f.recipientPublic || lease.AudiencePipeID != f.pipeID {
				ret[i] = errors.New("wrong-subject lease")
			}
		}(i)
	}
	wg.Wait()
	for i := range 8 {
		if ret[i] != nil {
			t.Fatalf("concurrent renewal %d: %v", i, ret[i])
		}
	}
}
