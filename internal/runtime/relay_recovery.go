package runtime

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/origama/tubo/internal/node"
	"github.com/origama/tubo/internal/store"
)

func setRunStatus(state *store.Store, ref, status string) bool {
	run, err := state.Run(context.Background(), ref)
	if err != nil || run.Status == status {
		return false
	}
	run.Status = status
	if state.PutRun(context.Background(), run) != nil {
		return false
	}
	return true
}

func startRelayRecovery(ctx context.Context, state *store.Store, ref string, h host.Host, relay peer.AddrInfo) func() {
	recoveryCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		degraded := false
		for {
			select {
			case <-recoveryCtx.Done():
				return
			case <-ticker.C:
				if node.PeerConnected(h, relay.ID) {
					continue
				}
				if err := node.EnsureRelay(recoveryCtx, h, relay, false); err != nil {
					setRunStatus(state, ref, "degraded")
					if !degraded {
						runtimeWarnf("degraded %s reason=relay_unavailable detail=%s", ref, safeLogError(err))
					}
					degraded = true
					continue
				}
				if degraded {
					setRunStatus(state, ref, "ready")
					runtimeInfof("recovered %s relay=%s", ref, relay.ID)
					degraded = false
				}
			}
		}
	}()
	return func() { cancel(); <-done }
}
