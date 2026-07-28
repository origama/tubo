package runtime

import (
	"context"
	"os"
	"time"

	coremetrics "github.com/libp2p/go-libp2p/core/metrics"

	"github.com/origama/tubo-rebuilt/internal/node"
	"github.com/origama/tubo-rebuilt/internal/store"
)

func RunRelay(ctx context.Context, state *store.Store, listen, publicAddress, swarmKey string) error {
	bandwidth := coremetrics.NewBandwidthCounter()
	h, err := node.Open(ctx, node.Options{KeyFile: state.SecretPath("nodes", "relay-default.key"), SwarmKeyFile: swarmKey, Listen: listen, RelayService: true, BandwidthReporter: bandwidth})
	if err != nil {
		return err
	}
	defer h.Close()
	address := publicAddress + "/p2p/" + h.ID().String()
	run := store.Run{PrimaryRef: "relay/default", Kind: "relay", PID: os.Getpid(), Source: RunSource(), Status: "ready", PeerID: h.ID().String(), Address: address, StartedAt: time.Now().UTC()}
	if err := state.PutRun(ctx, run); err != nil {
		return err
	}
	runtimeInfof("ready relay/default peer=%s", h.ID())
	stopTelemetry := startRuntimeTelemetry(ctx, state, "relay/default", h, bandwidth)
	defer stopTelemetry()
	defer func() {
		run.Status = "stopped"
		_ = state.PutRun(context.Background(), run)
		runtimeInfof("stopped relay/default")
	}()
	return waitSignal(ctx)
}
