package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/origama/tubo-rebuilt/internal/discovery"
	"github.com/origama/tubo-rebuilt/internal/domain"
	"github.com/origama/tubo-rebuilt/internal/store"
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
