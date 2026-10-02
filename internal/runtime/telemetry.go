package runtime

import (
	"context"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	coremetrics "github.com/libp2p/go-libp2p/core/metrics"

	"github.com/origama/tubo/internal/store"
)

const maxObservedRuntimePeers = 4096

func sampleRuntimeTelemetry(ctx context.Context, state *store.Store, ref string, h host.Host, reporter coremetrics.Reporter) error {
	bandwidth := reporter.GetBandwidthTotals()
	peerIDs := h.Network().Peers()
	sort.Slice(peerIDs, func(i, j int) bool { return peerIDs[i].String() < peerIDs[j].String() })
	if len(peerIDs) > maxObservedRuntimePeers {
		peerIDs = peerIDs[:maxObservedRuntimePeers]
	}
	peers := make([]store.RuntimePeer, 0, len(peerIDs))
	for _, peerID := range peerIDs {
		values := h.Peerstore().Addrs(peerID)
		if len(values) > 16 {
			values = values[:16]
		}
		addresses := make([]string, 0, len(values))
		for _, value := range values {
			if len(value.String()) <= 8192 {
				addresses = append(addresses, value.String())
			}
		}
		peers = append(peers, store.RuntimePeer{RunRef: ref, PeerID: peerID.String(), Addresses: addresses})
	}
	return state.PutRuntimeTelemetry(ctx, store.RuntimeStats{RunRef: ref, BytesIn: bandwidth.TotalIn, BytesOut: bandwidth.TotalOut, RateIn: bandwidth.RateIn, RateOut: bandwidth.RateOut, ConnectedPeers: len(peerIDs), OpenConnections: len(h.Network().Conns())}, peers)
}

func startRuntimeTelemetry(ctx context.Context, state *store.Store, ref string, h host.Host, reporter coremetrics.Reporter) func() {
	telemetryCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	if err := sampleRuntimeTelemetry(context.Background(), state, ref, h, reporter); err != nil {
		runtimeWarnf("telemetry %s reason=%T", ref, err)
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-telemetryCtx.Done():
				return
			case <-ticker.C:
				if err := sampleRuntimeTelemetry(context.Background(), state, ref, h, reporter); err != nil {
					runtimeWarnf("telemetry %s reason=%T", ref, err)
				}
			}
		}
	}()
	return func() { cancel(); <-done; _ = state.ClearRuntimePeers(context.Background(), ref) }
}
