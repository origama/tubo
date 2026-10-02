package runtime

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/origama/tubo/internal/authority"
	"github.com/origama/tubo/internal/discovery"
	"github.com/origama/tubo/internal/domain"
	wire "github.com/origama/tubo/internal/protocol"
	"github.com/origama/tubo/internal/store"
)

func remoteRecord(sequence uint64, peer, address string) discovery.PipeRecord {
	return discovery.PipeRecord{
		PipeID:        "pip_remote",
		RuntimePeerID: peer,
		Scope:         domain.Scope{Cluster: "solar", Namespace: "default"},
		Addresses:     []string{address},
		Sequence:      sequence,
		IssuedAt:      time.Now().UTC(),
		ExpiresAt:     time.Now().UTC().Add(time.Hour),
	}
}

func TestLocalPipeRecordResolverDetectsEndpointChangeForReconnect(t *testing.T) {
	resolver := newLocalPipeRecordResolver(remoteRecord(1, "peer-old", "/ip4/127.0.0.1/tcp/1/p2p/peer-old"))
	resolver.fetch = func(context.Context, *store.Store, string, FetchRecordsOptions) ([]discovery.PipeRecord, error) {
		return []discovery.PipeRecord{remoteRecord(2, "peer-new", "/ip4/127.0.0.1/tcp/2/p2p/peer-new")}, nil
	}
	changed, err := resolver.resolve(context.Background(), nil, false)
	if err != nil || !changed {
		t.Fatalf("resolve changed=%v err=%v", changed, err)
	}
	current := resolver.current()
	if current.Sequence != 2 || current.RuntimePeerID != "peer-new" || current.Addresses[0] != "/ip4/127.0.0.1/tcp/2/p2p/peer-new" {
		t.Fatalf("updated endpoint = %#v", current)
	}
}

func TestLocalPipeRecordResolverRejectsRollbackAndSameSequenceConflict(t *testing.T) {
	initial := remoteRecord(2, "peer-current", "/ip4/127.0.0.1/tcp/2/p2p/peer-current")
	cases := []struct {
		name      string
		candidate discovery.PipeRecord
	}{
		{name: "rollback", candidate: remoteRecord(1, "peer-old", "/ip4/127.0.0.1/tcp/1/p2p/peer-old")},
		{name: "same sequence endpoint conflict", candidate: remoteRecord(2, "peer-other", "/ip4/127.0.0.1/tcp/3/p2p/peer-other")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := newLocalPipeRecordResolver(initial)
			resolver.fetch = func(context.Context, *store.Store, string, FetchRecordsOptions) ([]discovery.PipeRecord, error) {
				return []discovery.PipeRecord{tc.candidate}, nil
			}
			if changed, err := resolver.resolve(context.Background(), nil, false); err == nil || changed {
				t.Fatalf("resolve changed=%v err=%v", changed, err)
			}
			if got := resolver.current(); got.Sequence != initial.Sequence || !sameEndpoint(got, initial) {
				t.Fatalf("untrusted record replaced endpoint: %#v", got)
			}
		})
	}
}

func TestLocalPipeRecordResolverRetainsEndpointOnRejectedSnapshot(t *testing.T) {
	initial := remoteRecord(1, "peer-current", "/ip4/127.0.0.1/tcp/1/p2p/peer-current")
	for _, reason := range []string{"tampered record", "record expired", "invalid authoritative snapshot"} {
		t.Run(reason, func(t *testing.T) {
			resolver := newLocalPipeRecordResolver(initial)
			resolver.fetch = func(context.Context, *store.Store, string, FetchRecordsOptions) ([]discovery.PipeRecord, error) {
				return nil, errors.New(reason)
			}
			if changed, err := resolver.resolve(context.Background(), nil, false); err == nil || changed {
				t.Fatalf("resolve changed=%v err=%v", changed, err)
			}
			if got := resolver.current(); got.Sequence != initial.Sequence || !sameEndpoint(got, initial) {
				t.Fatalf("rejected snapshot replaced endpoint: %#v", got)
			}
		})
	}
}

type readCountingConn struct {
	net.Conn
	reads atomic.Int32
}

func (c *readCountingConn) Read(p []byte) (int, error) {
	c.reads.Add(1)
	return c.Conn.Read(p)
}

func TestServeLocalConnectionRetriesOneTransientSetupWithoutReadingApplicationBytes(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()

	initial := remoteRecord(1, "peer-old", "/ip4/127.0.0.1/tcp/1/p2p/peer-old")
	updated := remoteRecord(2, "peer-new", "/ip4/127.0.0.1/tcp/2/p2p/peer-new")
	resolver := newLocalPipeRecordResolver(initial)
	var fetches atomic.Int32
	resolver.fetch = func(context.Context, *store.Store, string, FetchRecordsOptions) ([]discovery.PipeRecord, error) {
		fetches.Add(1)
		return []discovery.PipeRecord{updated}, nil
	}

	server, client := net.Pipe()
	local := &readCountingConn{Conn: server}
	defer client.Close()
	spec := domain.PipeSpec{Ref: mustPipeRef(t, "client"), PipeID: "pip_client"}
	firstAttempt := make(chan struct{})
	secondAttempt := make(chan struct{})
	allowSecondRead := make(chan struct{})
	errs := make(chan error, 3)
	done := make(chan struct{})
	var calls atomic.Int32
	payload := []byte("exactly-once-application-bytes")

	go func() {
		defer close(done)
		serveLocalConnectionWithOpener(ctx, state, spec, resolver, local, func(record discovery.PipeRecord) localConnectionResult {
			switch calls.Add(1) {
			case 1:
				if record.Sequence != initial.Sequence || local.reads.Load() != 0 {
					errs <- errors.New("first transient setup touched application bytes or wrong endpoint")
				}
				close(firstAttempt)
				return localConnectionResult{transient: true, reason: "pipe_unreachable"}
			case 2:
				if record.Sequence != updated.Sequence || record.RuntimePeerID != updated.RuntimePeerID {
					errs <- errors.New("retry did not use resolved endpoint")
				}
				close(secondAttempt)
				<-allowSecondRead
				got := make([]byte, len(payload))
				if _, err := io.ReadFull(local, got); err != nil {
					errs <- err
					return localConnectionResult{reason: "read_failed"}
				}
				if string(got) != string(payload) {
					errs <- errors.New("application bytes were changed or duplicated")
					return localConnectionResult{reason: "read_failed"}
				}
				return localConnectionResult{opened: true, reason: "ready"}
			default:
				errs <- errors.New("retry exceeded one bounded attempt")
				return localConnectionResult{reason: "unexpected_retry"}
			}
		})
	}()

	select {
	case <-firstAttempt:
	case <-time.After(time.Second):
		t.Fatal("first attempt did not run")
	}
	if got := local.reads.Load(); got != 0 {
		t.Fatalf("application read before OpenOK/retry: %d", got)
	}
	select {
	case <-secondAttempt:
	case <-time.After(time.Second):
		t.Fatal("retry did not start")
	}
	close(allowSecondRead)
	if _, err := client.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("retry setup did not finish")
	}
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("setup attempts = %d, want 2", got)
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("re-resolution fetches = %d, want 1", got)
	}
	if got := local.reads.Load(); got != 1 {
		t.Fatalf("application reads = %d, want exactly 1", got)
	}
}

func TestServeLocalConnectionDoesNotRetryTerminalOrSecondTransientFailure(t *testing.T) {
	t.Setenv("TUBO_HOME", t.TempDir())
	ctx := context.Background()
	state, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	spec := domain.PipeSpec{Ref: mustPipeRef(t, "client"), PipeID: "pip_client"}
	initial := remoteRecord(1, "peer-old", "/ip4/127.0.0.1/tcp/1/p2p/peer-old")
	updated := remoteRecord(2, "peer-new", "/ip4/127.0.0.1/tcp/2/p2p/peer-new")

	for _, tc := range []struct {
		name      string
		first     localConnectionResult
		wantCalls int32
		wantFetch int32
	}{
		{name: "terminal", first: localConnectionResult{reason: "authorization_denied"}, wantCalls: 1, wantFetch: 0},
		{name: "second transient", first: localConnectionResult{transient: true, reason: "pipe_unreachable"}, wantCalls: 2, wantFetch: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolver := newLocalPipeRecordResolver(initial)
			var fetches, calls atomic.Int32
			resolver.fetch = func(context.Context, *store.Store, string, FetchRecordsOptions) ([]discovery.PipeRecord, error) {
				fetches.Add(1)
				return []discovery.PipeRecord{updated}, nil
			}
			server, client := net.Pipe()
			defer client.Close()
			serveLocalConnectionWithOpener(ctx, state, spec, resolver, server, func(discovery.PipeRecord) localConnectionResult {
				call := calls.Add(1)
				if call == 1 {
					return tc.first
				}
				return localConnectionResult{transient: true, reason: "pipe_unreachable"}
			})
			if got := calls.Load(); got != tc.wantCalls {
				t.Fatalf("setup attempts = %d, want %d", got, tc.wantCalls)
			}
			if got := fetches.Load(); got != tc.wantFetch {
				t.Fatalf("re-resolution fetches = %d, want %d", got, tc.wantFetch)
			}
		})
	}
}

func mustPipeRef(t *testing.T, name string) domain.Ref {
	t.Helper()
	ref, err := domain.ParseRef("pipe/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func TestLocalPipeRecoveryFetchesNewAuthoritySnapshotAfterPublisherRestart(t *testing.T) {
	ctx := context.Background()
	authorityHome, publisherHome, consumerHome := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("TUBO_HOME", authorityHome)
	authorityState, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer authorityState.Close()

	scope := domain.Scope{Cluster: "solar", Namespace: "default"}
	authorityPublic, authorityPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := authorityState.EnsureAuthoritySnapshot(ctx, scope.String()); err != nil {
		t.Fatal(err)
	}
	authorityHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer authorityHost.Close()
	authorityHandler := &authorityHandler{state: authorityState, scope: scope, private: authorityPrivate, public: authorityPublic}
	authorityHost.SetStreamHandler(protocol.ID(wire.AuthorityID), authorityHandler.handle)
	authorityHost.SetStreamHandler(protocol.ID(wire.AuthoritySnapshotID), authorityHandler.handleSnapshot)

	ownerPublic, ownerPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	publisherOne, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer publisherOne.Close()
	publisherTwo, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer publisherTwo.Close()

	t.Setenv("TUBO_HOME", publisherHome)
	publisherState, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer publisherState.Close()
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer echo.Close()
	go func() {
		for {
			conn, err := echo.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	pipeID := authority.PipeID(ownerPublic)
	recordOne := signedRestartRecord(t, authorityPrivate, ownerPrivate, scope, pipeID, 1, publisherOne.ID().String(), directAuthorityAddress(t, publisherOne.ID(), publisherOne.Addrs()))
	if err := putAuthoritativeRestartRecord(ctx, authorityState, recordOne); err != nil {
		t.Fatal(err)
	}

	serviceRef, err := domain.ParseRef("service/echo")
	if err != nil {
		t.Fatal(err)
	}
	originRef := mustPipeRef(t, "origin")
	originSpec := domain.PipeSpec{Ref: originRef, PipeID: pipeID, Scope: &scope, Ingress: domain.PipeIngress{Kind: domain.IngressTubo}, Target: serviceRef, Visibility: domain.VisibilityDiscoverable, Access: domain.AccessMembers}
	pubNode := NewNodeRuntime(ctx)
	defer pubNode.Close()
	pubPipe, err := pubNode.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	publisherTwo.SetStreamHandler(protocol.ID(wire.TunnelID), (&tunnelServer{
		state:   publisherState,
		spec:    originSpec,
		service: domain.ServiceSpec{Ref: serviceRef, Kind: domain.ServiceTCP, Target: "tcp://" + echo.Addr().String()},
		// Direct construction bypasses quarantine panic.
		authorizer: &scopeTokenOpenAuthorizer{state: publisherState, scope: originSpec.Scope.String(), authorityPublic: authorityPublic},
		runtime:    pubPipe,
	}).handle)

	t.Setenv("TUBO_HOME", consumerHome)
	consumerState, err := store.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer consumerState.Close()
	clientPrivate, err := authority.GeneratePrivate(consumerState.SecretPath("clients", "member.key"))
	if err != nil {
		t.Fatal(err)
	}
	membership, err := authority.NewToken("membership.invite", scope, authority.PublicString(clientPrivate.Public().(ed25519.PublicKey)), "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	membership.Role = "member"
	if err := membership.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	membershipToken, err := authority.Encode(membership)
	if err != nil {
		t.Fatal(err)
	}
	if err := consumerState.PutMembership(ctx, store.Membership{Scope: scope.String(), Role: "member", Token: membershipToken, AuthorityPublic: authority.PublicString(authorityPublic), AuthorityAddress: directAuthorityAddress(t, authorityHost.ID(), authorityHost.Addrs()), ClientKeyFile: consumerState.SecretPath("clients", "member.key")}); err != nil {
		t.Fatal(err)
	}
	consumerHost, err := libp2p.New()
	if err != nil {
		t.Fatal(err)
	}
	defer consumerHost.Close()
	initialSnapshot, err := authoritySnapshotRequest(ctx, consumerState, consumerHost, scope.String())
	if err != nil {
		t.Fatal(err)
	}
	initialRecords, err := reconcileAuthorityResponse(ctx, consumerState, scope.String(), authorityPublic, scope, initialSnapshot, time.Now().UTC())
	if err != nil || len(initialRecords) != 1 || initialRecords[0].RuntimePeerID != publisherOne.ID().String() {
		t.Fatalf("initial snapshot = %#v, err=%v", initialRecords, err)
	}

	// Publisher one is the old runtime. Its record remains usable until the
	// first setup reaches its pre-OpenOK transient failure; the authority then
	// receives the restarted runtime's newer endpoint.
	if err := publisherOne.Close(); err != nil {
		t.Fatal(err)
	}
	recordTwo := signedRestartRecord(t, authorityPrivate, ownerPrivate, scope, pipeID, 2, publisherTwo.ID().String(), directAuthorityAddress(t, publisherTwo.ID(), publisherTwo.Addrs()))
	if err := putAuthoritativeRestartRecord(ctx, authorityState, recordTwo); err != nil {
		t.Fatal(err)
	}

	resolver := newLocalPipeRecordResolver(initialRecords[0])
	var fetches atomic.Int32
	resolver.fetch = func(fetchCtx context.Context, _ *store.Store, fetchScope string, _ FetchRecordsOptions) ([]discovery.PipeRecord, error) {
		fetches.Add(1)
		response, err := authoritySnapshotRequest(fetchCtx, consumerState, consumerHost, fetchScope)
		if err != nil {
			return nil, err
		}
		return reconcileAuthorityResponse(fetchCtx, consumerState, fetchScope, authorityPublic, scope, response, time.Now().UTC())
	}
	localRef := mustPipeRef(t, "client")
	localSpec := domain.PipeSpec{Ref: localRef, PipeID: "pip_client", Ingress: domain.PipeIngress{Kind: domain.IngressLocal, Listen: "127.0.0.1:0"}, Target: domain.Ref{Kind: domain.KindPipe, Name: pipeID}, Visibility: domain.VisibilityPrivate, Access: domain.AccessMembers}
	consumerNode := NewNodeRuntime(ctx)
	defer consumerNode.Close()
	consumerPipe, err := consumerNode.NewPipeRuntime()
	if err != nil {
		t.Fatal(err)
	}
	server, app := net.Pipe()
	defer app.Close()
	done := make(chan struct{})
	go func() {
		serveLocalConnection(ctx, consumerState, consumerHost, consumerPipe, localSpec, resolver, &localLeaseCache{}, server)
		close(done)
	}()
	payload := []byte("publisher-restarted-with-new-runtime-address")
	if _, err := app.Write(payload); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(app, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("echo = %q, want %q", got, payload)
	}
	_ = app.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("local pipe did not stop after client close")
	}
	if got := fetches.Load(); got != 1 {
		t.Fatalf("re-resolution fetches = %d, want 1", got)
	}
	if current := resolver.current(); current.Sequence != recordTwo.Sequence || current.RuntimePeerID != publisherTwo.ID().String() {
		t.Fatalf("resolver did not install restarted publisher record: %#v", current)
	}
	stored, err := consumerState.RecordInScope(ctx, scope.String(), pipeID)
	if err != nil || stored.Sequence != recordTwo.Sequence {
		t.Fatalf("consumer cache not reconciled to restarted record: %#v %v", stored, err)
	}
}

func signedRestartRecord(t *testing.T, authorityPrivate, ownerPrivate ed25519.PrivateKey, scope domain.Scope, pipeID string, sequence uint64, runtimePeerID, address string) discovery.PipeRecord {
	t.Helper()
	grant, err := authority.NewToken("pipe.publish", scope, authority.PublicString(ownerPrivate.Public().(ed25519.PublicKey)), pipeID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := grant.Sign(authorityPrivate); err != nil {
		t.Fatal(err)
	}
	encoded, err := authority.Encode(grant)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := discovery.PipeRecord{Version: 2, PipeID: pipeID, DisplayName: "origin", RuntimePeerID: runtimePeerID, Scope: scope, Protocol: wire.TunnelID, Capabilities: []string{"raw-duplex", "half-close"}, Addresses: []string{address}, Visibility: domain.VisibilityDiscoverable, Access: domain.AccessMembers, PublishGrant: encoded, IssuedAt: now, ExpiresAt: now.Add(30 * time.Minute), Sequence: sequence}
	if err := record.Sign(ownerPrivate); err != nil {
		t.Fatal(err)
	}
	return record
}

func putAuthoritativeRestartRecord(ctx context.Context, state *store.Store, record discovery.PipeRecord) error {
	payload, err := record.Marshal()
	if err != nil {
		return err
	}
	return state.PutAuthoritativeRecord(ctx, store.Record{Scope: record.Scope.String(), PipeID: record.PipeID, Name: record.DisplayName, Payload: payload, ExpiresAt: record.ExpiresAt, Sequence: record.Sequence})
}

func TestLocalPipeRecordResolverCoalescesConcurrentRecovery(t *testing.T) {
	resolver := newLocalPipeRecordResolver(remoteRecord(1, "peer-old", "/ip4/127.0.0.1/tcp/1/p2p/peer-old"))
	var calls atomic.Int32
	resolver.fetch = func(context.Context, *store.Store, string, FetchRecordsOptions) ([]discovery.PipeRecord, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return []discovery.PipeRecord{remoteRecord(2, "peer-new", "/ip4/127.0.0.1/tcp/2/p2p/peer-new")}, nil
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = resolver.resolve(context.Background(), nil, true)
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("recovery fetch calls = %d, want 1", got)
	}
	if got := resolver.current(); got.RuntimePeerID != "peer-new" {
		t.Fatalf("recovery did not install new peer: %#v", got)
	}
}
