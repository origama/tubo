# TCP raw benchmark — issue #198 initial findings

Date: 2026-06-04

Scope: first reproducible Docker validation run for raw TCP performance under:
- explicit direct selection;
- explicit relayed selection.

Harness:
- `tests/perf/tcpraw/`
- command used: `./tests/perf/tcpraw/run.sh --validate`
- validation duration: `5s`

## What the harness proves already

The new Docker harness can now:
- build a dedicated benchmark topology locally;
- run a raw Docker baseline with `iperf3`;
- run Tubo raw TCP via `tubo attach` + `tubo connect`;
- record the selected Tubo path from `tubo connect` output;
- force a `relayed` selection by advertising only loopback direct addresses on the relayed publisher;
- collect `iperf3 --json`, attach/connect logs, relay logs, and lightweight container CPU samples.

## Initial observed results

### Docker baseline

- direct Docker baseline sender: **18484.69 Mbit/s**
- direct Docker baseline receiver: **18476.04 Mbit/s**
- retransmits: **0**

### Tubo direct

- forward (`client -> attach`): **fails** with `control socket has closed unexpectedly`
- reverse (`-R`): sender **1154.79 Mbit/s**, receiver **1137.67 Mbit/s**, retransmits **55**
- parallel (`-P 4`): sender **1258.71 Mbit/s**, receiver **1196.47 Mbit/s**, retransmits **100**

### Tubo relayed

- forward: **fails** with `control socket has closed unexpectedly`
- reverse: **fails** with `control socket has closed unexpectedly`
- parallel: **fails** with `control socket has closed unexpectedly`

## Current hypothesis

The first strong signal is no longer just “throughput is asymmetric”.

The harness now shows a more actionable failure mode:

1. raw Docker baseline is healthy and extremely fast;
2. Tubo can establish both direct-selected and relayed-selected raw TCP paths;
3. direct reverse and direct parallel runs can move substantial traffic;
4. forward direct, and all currently validated relayed runs, can fail at the iperf3 control/data channel level with `control socket has closed unexpectedly`.

This points to a likely bug or limitation in the current raw TCP stream handling rather than a pure relay bandwidth ceiling.

The most suspicious areas remain:
- `internal/p2p/forward.go`
  - `ProxyTCPStream`
  - `HandleServiceTCPStream`
  - `StartClientTCPTunnel`
- `internal/app/bridge/app.go`
  - `serveTCP`
  - `handleTCPConn`
- half-close behavior and stream shutdown semantics under iperf3’s control/data connection pattern;
- direct-vs-relayed path labeling versus the actual libp2p connection path used by the stream.

## Important log signal

During the failing direct validation, the harness captured:
- `tubo connect` reporting `path: direct`;
- successful reverse and parallel direct transfer numbers;
- failing forward direct runs returning `control socket has closed unexpectedly`.

That means the harness is already useful even before optimization work: it can repeatedly surface the same TCP raw behavior in a controlled environment.

## Next recommended step

Before attempting throughput tuning (`io.CopyBuffer`, buffer pooling, etc.), the next iteration should focus on:
- making `iperf3` forward and relayed runs complete reliably;
- then re-running the same matrix with `10s` and `30s` durations;
- only after that, comparing before/after throughput for any transport-path optimization.
