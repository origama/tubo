# NAT/Relay performance report — compose

- Generated: 2026-04-30 15:45:04 UTC
- Git commit: `c5881db7ef8d04a1c738d95a14d0eaa96acd9b26`
- Duration: 355.7s
- Total requests: 632
- Overall success rate: 100.0%
- Worst p95: 4637.4 ms (`Traffic during service restart`)
- Primary risk: `None observed`

## Scenarios

| Scenario | Requests | Success | p50 ms | p95 ms | p99 ms | RPS | Failures |
|---|---:|---:|---:|---:|---:|---:|---:|
| Sequential GET baseline | 20 | 100.0% | 754.7 | 758.1 | 758.2 | 1.4 | 0 |
| Concurrent small mixed traffic | 96 | 100.0% | 760.3 | 778.5 | 781.5 | 15.7 | 0 |
| Large upload burst (512 KiB) | 48 | 100.0% | 875.0 | 962.8 | 972.7 | 8.9 | 0 |
| Mixed traffic including 512 KiB uploads | 96 | 100.0% | 768.6 | 833.8 | 862.2 | 15.3 | 0 |
| Traffic during service restart | 228 | 100.0% | 757.5 | 4637.4 | 6755.2 | 11.2 | 0 |
| Repeated large upload bursts | 144 | 100.0% | 851.2 | 968.0 | 976.7 | 7.6 | 0 |

## Strengths

- Traffico piccolo/misto senza payload grandi: stabilità eccellente.
- P95 sotto 1s per traffico leggero (778.5 ms).
- Relay-first baseline p50 754.7 ms.
- Burst singolo 512 KiB stabilizzato: p95 962.8 ms.
- Burst grandi consecutivi senza errori nel run corrente.
