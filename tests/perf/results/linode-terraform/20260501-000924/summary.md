# NAT/Relay performance report — linode-terraform

- Generated: 2026-05-01 00:09:24 UTC
- Git commit: `8915c3087b400a3e0d39d595902ef3dd39add98d`
- Duration: 164.5s
- Relay: `172.104.128.174` (eu-central)
- Edge: `45.79.168.161` (us-east)
- Service: `172.104.190.233` (ap-south)
- Total requests: 742
- Overall success rate: 100.0%
- Worst p95: 3994.4 ms (`Large upload burst (512 KiB)`)
- Primary risk: `None observed`

## Scenarios

| Scenario | Requests | Success | p50 ms | p95 ms | p99 ms | RPS | Failures |
|---|---:|---:|---:|---:|---:|---:|---:|
| Sequential GET baseline | 20 | 100.0% | 488.8 | 492.3 | 492.5 | 2.1 | 0 |
| Concurrent small mixed traffic | 96 | 100.0% | 492.5 | 810.1 | 812.5 | 21.5 | 0 |
| Large upload burst (512 KiB) | 48 | 100.0% | 1688.1 | 3994.4 | 4063.1 | 3.7 | 0 |
| Mixed traffic including 512 KiB uploads | 96 | 100.0% | 560.1 | 1685.9 | 1723.0 | 14.5 | 0 |
| Traffic during service restart | 338 | 100.0% | 490.9 | 573.4 | 6861.8 | 16.6 | 0 |
| Repeated large upload bursts | 144 | 100.0% | 1743.1 | 3701.8 | 4326.8 | 3.5 | 0 |

## Strengths

- Small/mixed traffic without large payloads: excellent stability.
- P95 below 1s for light traffic (810.1 ms).
- Relay-first baseline p50 488.8 ms.
- Single 512 KiB burst stabilized: p95 3994.4 ms.
- Consecutive large bursts with no errors in the current run.

## Delta vs previous saved run

| Scenario | Δ success pp | Δ p50 ms | Δ p95 ms | Δ RPS | Δ failures |
|---|---:|---:|---:|---:|---:|
| Sequential GET baseline | +0.0 | -0.8 | +0.0 | +0.0 | +0 |
| Concurrent small mixed traffic | +0.0 | +2.5 | -2.4 | -0.6 | +0 |
| Large upload burst (512 KiB) | +0.0 | +33.7 | -4.6 | -0.1 | +0 |
| Mixed traffic including 512 KiB uploads | +0.0 | +31.9 | +65.0 | -0.6 | +0 |
| Traffic during service restart | +0.0 | -1.0 | -2.6 | -0.1 | +0 |
| Repeated large upload bursts | +0.0 | +85.0 | +121.9 | -0.3 | +0 |
