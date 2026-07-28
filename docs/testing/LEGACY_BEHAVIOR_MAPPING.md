# Previous behavioral coverage mapped to the rebuilt model

This is a requirements mapping, not a compatibility promise. Old wire messages, files, commands, and internal packages must not be copied merely to satisfy it.

| Previous behavior | Rebuilt behavior |
|---|---|
| attach advertises service | attach creates a local service and advertises a pipe |
| connect persists client pipe | connect creates a local pipe targeting exact remote `pipe_id` |
| service discovery by `service_id` | pipe discovery/resolution by `pipe_id` |
| duplicate service display names | duplicate pipe labels; exact ID required on ambiguity |
| public default unlisted service | unlisted published pipe with self-contained invite |
| namespace member/viewer | member lists/connects; viewer lists but cannot access pipe |
| service publish lease | pipe publish grant |
| service share invite | pipe share invite |
| connect access/refresh leases | pipe access/refresh leases from one scope authority |
| service delegated grant endpoint | removed; authority owns connect issuance |
| delegated-to-authority rollover | removed; one issuer/refresh route |
| Discovery V3 announcement | encrypted signed `PipeRecord` |
| relay query cache | bounded opaque/verified pipe record cache |
| HTTP frame protocol | authenticated open then standard raw HTTP bytes |
| raw TCP mode switch | same authenticated open then raw bytes |
| process/attach and process/connect | run whose primary ref is `pipe/...` |
| selected service route | per-session logical and transport path |
| service restart rebind | stable pipe owner/id publishes new runtime peer binding |
| agent resource proposal | local service metadata exposed through a pipe record capability summary |

## Useful test ideas to port

Port behavior, not imports:

- large/concurrent TCP echo;
- 8 MiB half-close transfer;
- TLS passthrough;
- HTTP query/body/header fidelity;
- stolen proof key rejection;
- one-time invite concurrency;
- revocation transaction races;
- opaque relay tamper rejection;
- relay reservation renewal with a fake clock;
- detached readiness rollback;
- multi-machine NAT/relay convergence.

Each ported test should be rewritten against `ServiceSpec`, `PipeSpec`, `PipeRecord`, `PipeSession`, and protocol 2.0.
