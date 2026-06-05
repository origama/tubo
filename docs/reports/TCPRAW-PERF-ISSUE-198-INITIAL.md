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
- validate the direct data plane from attach logs, not just from `tubo connect path: direct`;
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

| Timestamp | Command | Baseline receiver | Direct forward | Direct reverse | Direct P4 | Relayed forward | Relayed reverse | Relayed P4 | Direct data plane | Artifacts |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |
| `2026-06-04T20:09:10Z` | `./tests/perf/tcpraw/run.sh --validate` | 16720.64 | 293.81 | 309.52 | 312.64 | 314.23 | 308.60 | 317.10 | contaminated by relay | summarized in report / issue |
| `2026-06-04T20:35:31Z` | `./tests/perf/tcpraw/run.sh --validate` | 18016.30 | 328.12 | 305.09 | 320.94 | 326.22 | 325.63 | 312.09 | contaminated by relay | summarized in issue |
| `2026-06-04T20:40:30Z` | `./tests/perf/tcpraw/run.sh --duration 10` | 17914.21 | 301.60 | 313.08 | 292.15 | 316.32 | 330.26 | 307.07 | contaminated by relay | local ignored run dir |
| `2026-06-04T20:44:44Z` | `./tests/perf/tcpraw/run.sh --duration 30` | 17678.93 | 309.55 | 310.41 | 294.65 | 320.03 | 324.34 | 285.48 | contaminated by relay | local ignored run dir |
| `2026-06-04T21:36:18Z` | `./tests/perf/tcpraw/run.sh --validate` | 17409.78 | 1032.98 | 1189.07 | 1125.30 | 312.40 | 296.48 | 290.75 | relay-free | local ignored run dir |
| `2026-06-04T21:38:50Z` | `./tests/perf/tcpraw/run.sh --duration 10` | 17959.83 | 1067.91 | 1049.62 | 1069.74 | 313.01 | 310.74 | 311.97 | relay-free | local ignored run dir |
| `2026-06-04T21:43:07Z` | `./tests/perf/tcpraw/run.sh --duration 30` | 17395.76 | 1112.56 | 1121.06 | 1137.34 | 305.98 | 321.45 | 302.31 | relay-free | local ignored run dir |

## Longer-run observations

After forcing direct candidates to use libp2p `WithForceDirectDial`, the benchmark now separates true direct data-plane traffic from relayed traffic:

- reliability is stable: direct and relayed forward/reverse/P4 all complete;
- true direct receiver throughput is now consistently around **~1.05–1.14 Gbit/s** in this Docker harness;
- relayed receiver throughput remains around **~0.30–0.32 Gbit/s**;
- the relayed path is therefore roughly **3.4x–3.7x slower** than the true direct path in these post-fix runs;
- `-P 4` still does not produce a large gain over single-stream direct in this environment.

The direct-data-plane proof in the `20260604-214307` run, now enforced by `./tests/perf/tcpraw/run.sh --validate`:

- service logs show a direct inbound connection from the client container (`/ip4/172.20.0.3/tcp/...`);
- relay CPU during `direct-forward` was near idle (`~0.03%` average), while attach/client each used roughly half a CPU;
- relayed-forward kept the relay around `~40%` CPU.

## Targeted profiling snapshot

I also captured host-side `perf` profiles before the force-direct fix for `30s` forward runs in both modes:

- direct forward profile: `tests/perf/tcpraw/results/profiles/20260604-205011-direct-forward/`
- relayed forward profile: `tests/perf/tcpraw/results/profiles/20260604-205136-relayed-forward/`

Main findings:

- the dominant hot path is **not** `ProxyTCPStream(...)` itself;
- the largest user-space cost is consistently in libp2p transport security / private-swarm plumbing:
  - `golang.org/x/crypto/salsa20/salsa.salsa2020XORKeyStream`
  - `go-libp2p ... pnet.(*pskConn).Read/Write`
  - `go-yamux/v5.(*Session).recvLoop/sendLoop`
  - TLS AES-GCM encrypt/decrypt helpers
- `runtime.memmove` is present but much smaller than the transport crypto/mux layers in these samples.

A second very important finding came out of the same profiling pass:

- in the profiled `direct forward` run, the service-side inbound peer still arrived over `/p2p-circuit/`;
- the relay container still consumed roughly the same CPU as in the explicit relayed case and moved gigabytes of traffic;
- so the old Docker harness `path: direct` result was at least partially **data-plane contaminated by relay traversal**.

The bridge now forces direct dials/streams for direct service candidates, so future profiles should be collected against the relay-free runs above.

## Clean force-direct profiling snapshot

I captured a second `perf` profiling pass after the force-direct fix:

- direct forward profile: `tests/perf/tcpraw/results/profiles/20260604-214643-direct-forward/`
- relayed forward profile: `tests/perf/tcpraw/results/profiles/20260604-214754-relayed-forward/`

Important caveat: these `perf record` runs perturb throughput heavily, so their `iperf3` numbers are **not** used as throughput benchmarks. They are used only for hotspot attribution.

The clean profiles confirm the earlier hotspot shape:

- direct data-plane proof remains valid: service logs show both the prior grant/relay connection and a separate direct inbound data connection from the client container;
- relayed data-plane proof remains valid: service logs show only `/p2p-circuit/` for the client data path;
- the largest sampled user-space hotspot remains private-swarm encryption and libp2p mux/security plumbing:
  - `salsa2020XORKeyStream` through `pnet.(*pskConn).Read/Write`;
  - `yamux.(*Session).recvLoop/sendLoop`;
  - TLS AES-GCM helpers;
- `ProxyTCPStream(...)` / `io.copyBuffer` appears only as a smaller secondary cost in these samples.

This points the next optimization investigation toward transport/security/mux overhead first, not toward a simple copy-buffer-only fix.

## Artifact policy

Benchmark outputs under `tests/perf/tcpraw/results/` are generated locally and now intentionally git-ignored. The repository keeps only the harness, docs, and report; raw JSON/log/profile/invite outputs stay out of version control.

## Updated next recommended step

With reliability fixed and the benchmark now proving a relay-free direct data plane, the next iteration should profile the cleaned-up `direct-forward` path and compare it to the explicit `relayed-forward` path. Only after that should we test transport optimizations such as copy buffer sizing, pooling, or stream concurrency changes.
