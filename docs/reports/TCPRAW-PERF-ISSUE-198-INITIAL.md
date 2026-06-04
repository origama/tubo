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

## Tracking snapshot for future comparisons

Latest validate snapshot used as the current tracking baseline for issue #198:
- command: `./tests/perf/tcpraw/run.sh --validate`
- duration: `5s`
- run timestamp: `2026-06-04T16:28:06Z`

| Scenario | Sender Mbit/s | Receiver Mbit/s | vs Docker baseline | Relative throughput | Slowdown |
| --- | ---: | ---: | ---: | ---: | ---: |
| Docker baseline direct | 16139.44 | 16136.77 | baseline | 100.00% | 1.00x |
| Tubo direct reverse (`-R`) | 1335.69 | 1318.57 | -91.72% sender / -91.83% receiver | 8.28% sender / 8.17% receiver | 12.08x sender / 12.24x receiver |
| Tubo direct parallel (`-P 4`) | 1244.63 | 1183.70 | -92.29% sender / -92.66% receiver | 7.71% sender / 7.34% receiver | 12.97x sender / 13.63x receiver |
| Tubo relayed forward | 0.00 | 0.00 | failed | n/a | n/a |
| Tubo relayed reverse (`-R`) | 0.00 | 0.00 | failed | n/a | n/a |
| Tubo relayed parallel (`-P 4`) | 0.00 | 0.00 | failed | n/a | n/a |

Notes:
- only successful runs are normalized against the Docker baseline;
- current usable throughput comparisons are limited to the working direct `-R` and direct `-P 4` cases;
- direct forward and all relayed cases still fail with `control socket has closed unexpectedly`, so they are not yet suitable for before/after throughput claims.

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

## Root cause found

The relayed failures were caused by Tubo's relay config default:

- `relay.limit_data_bytes` defaulted to **16 MiB** in `internal/config.Defaults("relay")`;
- libp2p circuit relay v2 applies that data limit to the **whole relayed connection**, not to each application TCP stream;
- `tubo connect` reuses the relayed libp2p connection and opens multiple raw TCP tunnel streams on it;
- once cumulative traffic on that circuit crossed about 16 MiB, the relay reset the relayed connection, which reset both iperf3 control/data streams and surfaced as `control socket has closed unexpectedly`.

This explains the observed pattern:
- 8 MiB single-stream relayed transfer succeeded;
- the next transfer failed near another ~8 MiB because the shared circuit reached ~16 MiB total;
- larger single-stream and dual-socket runs failed at variable byte counts depending on prior traffic on the same circuit.

## Fix applied

Relay byte-cap semantics now treat `limit_data_bytes: 0` as no data byte cap:

- `Defaults("relay")` now uses `limit_data_bytes: 0`;
- `internal/app/relay` maps `LimitDataBytes <= 0` to an unlimited data sentinel while preserving the duration limit;
- explicit positive `RELAY_LIMIT_DATA_BYTES` / config values still install a finite circuit relay data cap.

## Fixed validation snapshot

After the fix, the same Docker validation command completes direct and relayed `iperf3` runs:

- command: `./tests/perf/tcpraw/run.sh --validate`
- duration: `5s`
- run timestamp: `2026-06-04T20:09:10Z`

| Scenario | Path | Sender Mbit/s | Receiver Mbit/s | Error |
| --- | --- | ---: | ---: | --- |
| Tubo direct forward | direct | 305.44 | 293.81 | - |
| Tubo direct reverse (`-R`) | direct | 325.63 | 309.52 | - |
| Tubo direct parallel (`-P 4`) | direct | 372.63 | 312.64 | - |
| Tubo relayed forward | relayed | 329.11 | 314.23 | - |
| Tubo relayed reverse (`-R`) | relayed | 320.19 | 308.60 | - |
| Tubo relayed parallel (`-P 4`) | relayed | 367.76 | 317.10 | - |

## Post-fix run ledger

The following post-fix runs are the current comparison set for issue `#198`.

| Timestamp | Command | Baseline receiver | Direct forward | Direct reverse | Direct P4 | Relayed forward | Relayed reverse | Relayed P4 | Artifacts |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| `2026-06-04T20:09:10Z` | `./tests/perf/tcpraw/run.sh --validate` | 16720.64 | 293.81 | 309.52 | 312.64 | 314.23 | 308.60 | 317.10 | summarized in report / issue |
| `2026-06-04T20:35:31Z` | `./tests/perf/tcpraw/run.sh --validate` | 18016.30 | 328.12 | 305.09 | 320.94 | 326.22 | 325.63 | 312.09 | summarized in issue |
| `2026-06-04T20:40:30Z` | `./tests/perf/tcpraw/run.sh --duration 10` | 17914.21 | 301.60 | 313.08 | 292.15 | 316.32 | 330.26 | 307.07 | `tests/perf/tcpraw/results/runs/20260604-204030/` |
| `2026-06-04T20:44:44Z` | `./tests/perf/tcpraw/run.sh --duration 30` | 17678.93 | 309.55 | 310.41 | 294.65 | 320.03 | 324.34 | 285.48 | `tests/perf/tcpraw/results/runs/20260604-204444/` |

## Longer-run observations

Across the current post-fix runs:

- reliability is stable: direct and relayed forward/reverse/P4 all complete;
- receiver throughput is consistently in the **~285–330 Mbit/s** band for both direct and relayed paths in this Docker harness;
- relayed throughput is no longer obviously worse than direct, and in some reruns it is slightly better;
- `-P 4` does **not** currently produce a clear throughput gain and can be slightly worse on the receiver side in longer runs;
- longer runs mainly increase retransmit counts rather than improving steady-state throughput.

## Updated next recommended step

With reliability fixed and a small run ledger in place, the next iteration should focus on throughput analysis:

- compare CPU samples from the saved `10s` and `30s` artifact directories;
- profile bridge/service copy-path costs during a `30s` run;
- only then test transport optimizations such as copy buffer sizing, pooling, or stream concurrency changes.
