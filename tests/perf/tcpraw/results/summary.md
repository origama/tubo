# TCP raw benchmark summary

- generated_at: 2026-06-04T15:59:05.793554+00:00
- duration_seconds: 5
- parallel_streams: 4
- mode: validate

## Baseline

| Scenario | Sender Mbit/s | Receiver Mbit/s | Retransmits | Error |
| --- | ---: | ---: | ---: | --- |
| direct docker baseline | 18484.69 | 18476.04 | 0 | - |

## Tubo direct

| Scenario | Path | Sender Mbit/s | Receiver Mbit/s | Retransmits | Error |
| --- | --- | ---: | ---: | ---: | --- |
| forward | direct | 0.0 | 0.0 | 0 | control socket has closed unexpectedly |
| reverse | direct | 1154.79 | 1137.67 | 55 | - |
| parallel P4 | direct | 1258.71 | 1196.47 | 100 | - |

## Tubo relayed

| Scenario | Path | Sender Mbit/s | Receiver Mbit/s | Retransmits | Error |
| --- | --- | ---: | ---: | ---: | --- |
| forward | relayed | 0.0 | 0.0 | 0 | control socket has closed unexpectedly |
| reverse | relayed | 0.0 | 0.0 | 0 | control socket has closed unexpectedly |
| parallel P4 | relayed | 0.0 | 0.0 | 0 | control socket has closed unexpectedly |

## Initial hypothesis

- this harness records direct vs relayed selection from `tubo connect` output, so throughput numbers are tied to an explicit chosen path rather than inferred topology;
- if reverse is consistently much faster than forward on the direct path, the likely hotspots remain the bridge/service raw TCP copy path, libp2p stream flow-control behavior, or local CPU scheduling/buffering rather than relay saturation alone;
- if relayed numbers collapse uniformly in both directions, relay/circuit overhead is a stronger factor;
- this branch currently focuses on reproducible measurement infrastructure first; it does not yet claim a transport optimization win.

## Artifacts

- `direct-forward.json`, `direct-reverse.json`, `direct-p4.json`
- `relayed-forward.json`, `relayed-reverse.json`, `relayed-p4.json`
- `baseline-direct.json`
- `direct-*.log`, `relayed-*.log`, `cpu-*.jsonl`
