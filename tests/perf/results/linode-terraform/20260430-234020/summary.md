# NAT/Relay performance report — linode-terraform

- Generated: 2026-04-30 23:40:20 UTC
- Git commit: `26d40ddf02e98bf943b1da77b03ddb578f0fbbcd`
- Duration: 157.1s
- Relay: `172.104.128.174` (eu-central)
- Edge: `45.79.168.161` (us-east)
- Service: `172.104.190.233` (ap-south)
- Total requests: 747
- Overall success rate: 100.0%
- Worst p95: 3999.0 ms (`Large upload burst (512 KiB)`)
- Primary risk: `None observed`

## Scenarios

| Scenario | Requests | Success | p50 ms | p95 ms | p99 ms | RPS | Failures |
|---|---:|---:|---:|---:|---:|---:|---:|
| Sequential GET baseline | 20 | 100.0% | 489.5 | 492.3 | 493.3 | 2.1 | 0 |
| Concurrent small mixed traffic | 96 | 100.0% | 490.0 | 812.5 | 814.5 | 22.1 | 0 |
| Large upload burst (512 KiB) | 48 | 100.0% | 1654.4 | 3999.0 | 4037.6 | 3.7 | 0 |
| Mixed traffic including 512 KiB uploads | 96 | 100.0% | 528.1 | 1620.9 | 1667.6 | 15.1 | 0 |
| Traffic during service restart | 343 | 100.0% | 491.9 | 576.1 | 6918.0 | 16.7 | 0 |
| Repeated large upload bursts | 144 | 100.0% | 1658.1 | 3579.9 | 3971.5 | 3.7 | 0 |

## Strengths

- Traffico piccolo/misto senza payload grandi: stabilita' eccellente.
- P95 sotto 1s per traffico leggero (812.5 ms).
- Relay-first baseline p50 489.5 ms.
- Burst singolo 512 KiB stabilizzato: p95 3999.0 ms.
- Burst grandi consecutivi senza errori nel run corrente.

## Delta vs previous saved run

| Scenario | Δ success pp | Δ p50 ms | Δ p95 ms | Δ RPS | Δ failures |
|---|---:|---:|---:|---:|---:|
| Sequential GET baseline | +0.0 | -755.5 | -753.8 | +1.3 | +0 |
| Concurrent small mixed traffic | +0.0 | -799.6 | -755.8 | +13.3 | +0 |
| Large upload burst (512 KiB) | +0.0 | -977.4 | -739.2 | +1.2 | +0 |
| Mixed traffic including 512 KiB uploads | +1.0 | -761.5 | -828.3 | +8.5 | -1 |
| Traffic during service restart | +0.0 | -835.0 | -6777.3 | +10.3 | +0 |
| Repeated large upload bursts | +0.7 | -893.8 | -879.6 | +1.2 | -1 |
