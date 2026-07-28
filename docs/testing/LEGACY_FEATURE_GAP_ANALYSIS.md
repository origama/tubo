# Legacy feature gap analysis

Snapshot: 2026-07-28.

This document compares product behavior present in the previous Tubo implementation with protocol-2.0 Tubo Rebuilt. It is a planning input, not a wire, state, CLI, or ontology compatibility promise. Implementation work still requires focused GitHub Issues and the evidence required by `ACCEPTANCE_MATRIX.md`.

Sources used:

- previous binary command help preserved during live migration;
- previous repository implementation and tests;
- current `README.md` and canonical reference contracts;
- live protocol-2.0 deployment evidence from run `live-v2-20260728T153922Z`.

## Classification

Compatibility:

- **native**: behavior fits current ontology and invariants directly;
- **redesign**: product capability fits, but old authority, protocol, state, or CLI design must not be reused;
- **conditional**: compatible only with an explicit security or product decision;
- **reject**: old behavior conflicts with rebuilt invariants and needs no compatibility implementation.

Effort is relative:

- **S**: focused slice, usually one package plus contract tests;
- **M**: cross-package slice with state/runtime/CLI integration;
- **L**: protocol or distributed-system slice requiring negative, race, and three-machine evidence.

## Capability gaps

| Capability present previously | Current rebuilt state | Compatibility | Effort | Protocol-2.0 shape |
|---|---|---:|---:|---|
| Direct dial, hole punching, relay fallback, later direct upgrade | Relay circuit path only | native | L | Keep logical path stable; record and inspect per-session transport path independently |
| Access lease refresh for established sessions | Session closes when current lease expires | redesign | L | One authority-owned refresh route; no delegated issuer rollover |
| Invite, session, access, and publish revocation | Not implemented | native | L | Transactional revocation state, bounded propagation, independent authorization at every hop |
| One-time resource share token and `connect --token` | Scope membership invites only | redesign | M/L | Invite targets exact `pipe_id`; redemption is atomic; token cannot introduce an untrusted authority |
| HTTP ingress gateway routed by `Host` | Not implemented | native | M | Gateway ingress creates sessions into pipes; gateway is infrastructure, not a service runtime |
| GossipSub namespace discovery and heartbeat announcements | Authority query/list only | redesign | L | Encrypted signed `PipeRecord` gossip; caches and peers remain non-authoritative |
| Discovery watch, live mode, cached-only mode, query/sync | Query/list only | native | M/L | `watch pipes`, bounded query/sync, verified cache, explicit freshness metadata |
| Manual publish-grant queue, history, approve, deny, policy | Publisher role receives grants automatically | redesign | M/L | Authority policy over `pipe.publish`; persistent requests and decisions keyed by pipe/subject |
| Namespace discovery-key rotation with grace | Not implemented | native | L | Current/previous encrypted-topic keys; bounded grace; transactional rotation |
| HTTPS upstream TLS handshake | Implemented with verified TLS 1.2+ and bounded dial/handshake | native | delivered | TLS dial uses target hostname; deterministic unit evidence |
| Per-resource logs with tail/follow | Implemented for Tubo-owned resource logs | native | delivered | Bounded tail/follow by validated primary resource ref; external supervisor logs remain external |
| `restart` lifecycle | Implemented for persisted pipes | native | delivered | Exact process identity, termination wait, detached readiness/fail-closed start |
| Resource removal and stale cleanup | Implemented for service/pipe definitions and stale runs | native | delivered | Transactional dependency checks; live conflict unless identity-safe force stop |
| `doctor`, config validation/printing, warnings | Implemented initial local diagnostic set | native | delivered | Overlay/config, permissions, run identity, and local target checks; YAML remains static config |
| Broad local inventory for services, scopes, peers, overlays, processes | Implemented; raw secret inventory intentionally omitted | native | delivered | Services remain local-only; configuration masks secret paths |
| Broad `describe`/`inspect` resource support | Implemented for service, pipe, scope, overlay, session, peer, authority, and relay | native | delivered | Local-only targets; tokens and key material omitted |
| Rich live `top`: rates, active/completed/error counts, request rate, path | Libp2p totals/rates, peers, connections, path, and completed-session totals implemented; active/error/request rates pending | native | partial | Runtime telemetry plus future active application counters; no request-body or target leakage |
| Degraded/authorization/retry diagnostics | Basic run status | native | M | Stable reason classes, lease expiry, retry time, target health kept local |
| Global quiet/verbosity/log-level controls | Implemented for CLI/runtime diagnostics | native | delivered | Diagnostics remain on stderr; JSON stdout stays one value |
| Local peer aliases, notes, and observed peers | Implemented in schema 3; observed runtime peers remain local views | native | delivered | Operator-only aliases plus per-run libp2p observations; never authority input |
| Current overlay/scope selection | `scope use` implemented | native | delivered | Atomic static-config current-scope update; mutable resources remain in SQLite |
| PeerID derivation utility from a seed | Not implemented | conditional | S | Prefer key-backed identity inspection; do not weaken secret separation |
| Configurable relay resource knobs and AutoNAT service | Mostly fixed relay settings | native | M | Bounded reservations/circuits and resource manager; no circuit lifetime that truncates authorized sessions |
| Connect self-healing after pre-stream failures | Configured-relay reconnect, dial-backoff clearing, reservation/record renewal, cached inventory, and status recovery implemented; record re-resolution remains pending | native | partial | Five-second bounded maintenance loop plus future multi-candidate re-resolution |
| Signed public bootstrap bundle and bundle URL | Intentionally not imported | conditional | L | Version-2 bundle may contain relay/PSK provisioning and a pre-pinned trust root, never ambient authority trust |
| Implicit first-run public join | Explicit join only | conditional | M | Only acceptable with preinstalled trust root, explicit disable control, and parseable output contract |
| Multi-scope all-namespace discovery UX | Memberships work; selection and aggregate UX are incomplete | native | M | Query each authorized scope independently; preserve authority and ambiguity boundaries |

## Partial parity

These behaviors exist but do not yet match previous operational depth:

- HTTP and raw TCP work over authenticated raw duplex streams;
- WebSocket upgrade/full-duplex behavior has isolated integration evidence;
- detached runs, `ps`, `top`, `inspect`, and identity-safe `stop` exist;
- remote publication and authority record announcement work;
- member/viewer/publisher authorization works;
- relay restart, publisher restart, stable `pipe_id`, half-close, 8 MiB transfer, and concurrent sessions have live evidence.

## Behaviors to replace, not port

| Previous behavior/design | Rebuilt decision |
|---|---|
| Advertise remotely addressable `service/...` resources | Advertise only signed pipe records; services remain local terminals |
| Separate service, bridge, and client runtimes | One pipe runtime model; attach/connect remain UX presets |
| Delegated service-side access issuer and authority rollover | One active authority per scope and one explicit refresh route |
| Mutable YAML as resource/process database | SQLite transactional state; YAML only for static config/defaults |
| Legacy discovery V1/V2/V3 fallback | Protocol 2.0 only; no negotiation or fallback |
| Legacy tunnel framing and service IDs | Authenticated tunnel 2.0 and secure `pipe_id` identity |
| Public token introducing authority trust | Authority must already be pinned or reached through an existing trusted scope |
| Name as network identity | Names remain non-unique labels; security decisions use exact IDs |

## Recommended delivery order

1. Runtime observability: active session state, rates, errors, per-session transport path, and richer degraded state.
2. Pipe share invite with atomic one-time redemption.
3. Revocation and access refresh.
4. Direct path, hole punching, and bounded self-healing.
5. Discovery watch/query/sync and encrypted gossip.
6. Gateway and optional explicit re-export.
7. Public bootstrap bundle v2 only after a separate trust-root threat analysis.
