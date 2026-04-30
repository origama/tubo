# NAT/Relay performance report — linode-terraform

- Generated: 2026-04-30 18:24:42 UTC
- Git commit: `c5881db7ef8d04a1c738d95a14d0eaa96acd9b26`
- Duration: 215.8s
- Relay: `172.104.128.174` (eu-central)
- Edge: `45.79.168.161` (us-east)
- Service: `172.104.190.233` (ap-south)
- Total requests: 536
- Overall success rate: 99.6%
- Worst p95: 7353.4 ms (`Traffic during service restart`)
- Primary risk: `Mixed traffic including 512 KiB uploads`

## Scenarios

| Scenario | Requests | Success | p50 ms | p95 ms | p99 ms | RPS | Failures |
|---|---:|---:|---:|---:|---:|---:|---:|
| Sequential GET baseline | 20 | 100.0% | 1245.0 | 1246.0 | 1249.0 | 0.8 | 0 |
| Concurrent small mixed traffic | 96 | 100.0% | 1289.5 | 1568.3 | 1649.9 | 8.8 | 0 |
| Large upload burst (512 KiB) | 48 | 100.0% | 2631.8 | 4738.2 | 4821.6 | 2.6 | 0 |
| Mixed traffic including 512 KiB uploads | 96 | 99.0% | 1289.6 | 2449.2 | 3362.9 | 6.6 | 1 |
| Traffic during service restart | 132 | 100.0% | 1326.9 | 7353.4 | 7478.6 | 6.3 | 0 |
| Repeated large upload bursts | 144 | 99.3% | 2552.0 | 4459.4 | 4696.9 | 2.5 | 1 |

## Strengths

- Traffico piccolo/misto senza payload grandi: stabilita' eccellente.
- Burst singolo 512 KiB stabilizzato: p95 4738.2 ms.

## Weaknesses

- Traffico misto con 512 KiB ancora instabile: 1 errori su 96.
- Flakiness su burst consecutivi: burst 3: 1 fail.
