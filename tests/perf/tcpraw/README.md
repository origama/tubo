# TCP raw throughput benchmark

Docker-based benchmark harness for Tubo raw TCP services.

It provisions:
- one relay/bootstrap node;
- one direct attach/client pair on a shared Docker network;
- one forced-relayed attach/client pair on isolated Docker networks;
- `iperf3` servers behind `tubo attach tcp://...`;
- `iperf3` clients behind `tubo connect --token ... --local ...`.

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

Useful shorter local run:

```bash
./tests/perf/tcpraw/run.sh --duration 10
```

## Artifacts

Latest artifacts are written to:

- `tests/perf/tcpraw/results/`

Timestamped copies are also saved under:

- `tests/perf/tcpraw/results/runs/<timestamp>/`

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
