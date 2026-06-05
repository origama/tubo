# TCP raw throughput benchmark

Docker-based benchmark harness for Tubo raw TCP services.

It provisions:
- one relay/bootstrap node;
- one direct attach/client pair on a shared Docker network;
- one forced-relayed attach/client pair on isolated Docker networks;
- `iperf3` servers behind `tubo attach tcp://...`;
- `iperf3` clients behind by-name `tubo connect ... --local ...` after cluster invite join.

## Commands

Start or inspect the topology manually:

```bash
./tests/perf/tcpraw/docker-up.sh
./tests/perf/tcpraw/docker-down.sh
```

Run the benchmark matrix:

```bash
./tests/perf/tcpraw/run.sh
```

Lightweight validation mode:

```bash
./tests/perf/tcpraw/run.sh --validate
```

Validation now checks both control-plane selection and data-plane reality:
- direct mode must log `path: direct` and show a non-`/p2p-circuit` inbound service connection in the attach log;
- relayed mode must log `path: relayed` and show only `/p2p-circuit` inbound service connections in the attach log.

Useful shorter local run:

```bash
./tests/perf/tcpraw/run.sh --duration 10
```

Note: relayed raw TCP runs depend on the relay byte cap. `relay.limit_data_bytes: 0` means no byte cap; positive values cap cumulative bytes for the whole relayed circuit connection, not each TCP stream.

## Artifacts

Latest artifacts are written to:

- `tests/perf/tcpraw/results/`

Timestamped copies are also saved under:

- `tests/perf/tcpraw/results/runs/<timestamp>/`

These artifacts are intentionally git-ignored. Keep the harness and markdown/report files under version control; do not commit generated JSON, logs, invites, profiles, or timestamped run directories.

Expected files include:

- `direct-forward.json`
- `direct-reverse.json`
- `direct-p4.json`
- `relayed-forward.json`
- `relayed-reverse.json`
- `relayed-p4.json`
- `baseline-direct.json`
- `summary.md`
- `*.log`
- `cpu-*.jsonl`
