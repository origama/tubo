# Binding invariants

**Normative precedence:** [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md) defines future architecture. This file preserves prototype acceptance/security knowledge unless an entry is explicitly generalized below. Prototype-specific Scope/token/protocol details do not require future compatibility.

## Architecture-foundation invariants

1. Every remote Pipe open requires successful short-lived, cryptographically verifiable Access Lease authorization at receiving boundary. Pipe ID, endpoint, record, route, invite, or membership alone is insufficient; no successful authorization result means no local target open.
2. Exact Pipe Authority and Scope Authority issue same exact-Pipe lease contract. Session/tunnel code does not branch by issuer topology.
3. Discovery finds, Reachability connects, Authorization permits. No concern is evidence for another.
4. Scope is optional coordination/security domain, never prerequisite for connectivity.
5. Public infrastructure provides reachability only, never ambient authorization.
6. Runtime ownership is explicit: `NodeRuntime -> PipeRuntime(s) -> SessionRegistry -> Session(s)`. No package-global mutable runtime registry or process-global dispatch state.
7. Core/use-case consumers depend on narrow persistence capabilities; persistence is adapter, not domain model.
8. L2 owns enforcement and consumer-side/neutral open-authorization contract; L3 owns concrete lease validation, trust, revocation/epochs, policy, and refresh implementation. L2 must not import concrete L3 authority/revocation implementations.
9. Layer dependencies point downward under L0–L5. End-to-end success cannot waive this rule.

## Prototype reference invariants

These invariants are acceptance criteria and learned behavior for preserved prototype code.

## Domain

1. A service is local-only and is never inserted into network discovery.
2. A pipe has exactly one target: `service/...` or `pipe/...`.
3. Only a pipe with Tubo ingress may be advertised.
4. `attach` and `connect` compile to pipe definitions and runs; no attach/connect runtime type may exist.
5. Stable identity and policy belong to a pipe, never to process state.
6. A run may cache status but cannot become the source of truth for a pipe or service.
7. A session belongs to a pipe run and may not outlive its authorization.
8. Pipe setup rejects repeated pipe IDs and more than eight logical hops by default.
9. Re-exporting another pipe requires explicit `pipe.forward` authorization.
10. Display names are non-unique. Secure resolution always uses `pipe_id`.

## Privacy

1. Local service targets are never present in pipe records, invites, remote status, or remote trace output.
2. Public/default sharing is unlisted and invite-driven by default.
3. Collaborative discovery uses a real namespace secret, never a key derived solely from public IDs.
4. Full distributed path tracing is opt-in; redacted local trace is the default.
5. Secrets and identity seeds are never printed by ordinary config or inspect commands.

## Authority and authorization

1. Capability is not authority.
2. Relays, caches, discovery query peers, and sync peers are never trust roots.
3. There is one active logical authority per scope.
4. Membership, publish, share, access, refresh, and forwarding grants have explicit issuer, subject, scope, audience, JTI, issuance, and expiry.
5. `PublishGrant`, `ShareInvite`, `ConnectAccessLease`, and `ConnectRefreshLease` bind to `pipe_id`.
6. Every network hop authorizes its immediate caller independently.
7. A forwarding pipe is the caller observed by its next pipe unless an explicit, verified delegation chain is implemented.
8. Client-controlled role/name fields never bypass connect proof validation.
9. Revocation guarantees are bounded by access TTL and freshness of issuer state; immediate revocation must not be claimed without an online check.
10. Failed persistence, signature verification, policy evaluation, or authorization fails closed.

## Data plane

Invariants 1-6 below are current on the A0 path. Invariants 7-15 describe the session-resume/planned-switch/carrier-recovery machinery that was **prototype machinery removed from the A0 path** by the fail-closed session-resume quarantine (issue #39). That machinery (resume/control streams, retention/replay window, epoch fencing, resume handshake/proofs, planned-switch barrier, detached grace) does not exist on the A0 data plane; it is deferred post-A0 resilient-Session debt (#32). The authorization-reporting invariants in the Authority section remain current: the authorization/lifetime timer closes target+stream at lease expiry, and no successful authorization result means no local target open.

1. Protocol 2.0 performs a bounded handshake before any target is dialed.
2. After `OpenOK`, traffic is raw full-duplex bytes for HTTP, WebSocket, TCP, and TLS passthrough.
3. No application payload is buffered in full by Tubo.
4. Half-close is propagated in both directions where the platform supports it.
5. Handshake deadlines are cleared after authorization and target selection; established sessions have no absolute lifetime timeout.
6. A failed setup may be retried before application bytes flow. Tubo never transparently replays bytes after forwarding begins.
7. Direct paths are preferred when usable; relay is the deterministic fallback; a session may later upgrade to direct.
8. A resume-capable session (negotiated `session-resume-v1`) uses a separate control stream for continuous ACKs and barrier coordination.
9. Tubo buffers at most the negotiated per-direction unacknowledged window (default 1 MiB). It never buffers a complete application payload.
10. A resume-capable session may retransmit only bytes not yet committed to the receiving application connection. Bytes already acknowledged as delivered must never be delivered again.
11. Migration requires an authenticated, monotonic-epoch resume handshake and receiver-committed byte offsets. Failure, ambiguity, offset mismatch, or an out-of-window resume closes the application connection.
12. Without negotiated resume support, recovery creates a new application connection and session.
13. Carrier EOF is not application half-close during migration. Half-close is sequenced after its final byte offset.
14. One active carrier per epoch is enforced by epoch fencing. Late bytes from an old carrier are discarded after epoch advance.
15. Session resume secret is memory-only, never persisted or logged.

## Discovery

1. Gossip, query, sync, and relay forwarding exchange the same signed `PipeRecord` envelope.
2. Receivers verify records before projecting them as trusted pipes.
3. Record cache keys use scope plus `pipe_id`, never display name.
4. Wrong-scope, expired, replayed, malformed, unauthorized, or invalidly signed records are rejected.
5. Relays may store opaque records only under fixed count, byte, record-size, refresh-rate, TTL, and response-size limits.
6. Empty discovery results distinguish no records, unreachable peers, authorization denial, and filtering.

## Persistence and lifecycle

1. Human-editable configuration contains contexts/defaults only; it is not mutable runtime state.
2. Mutable metadata uses one embedded transactional store with schema versioning.
3. Private keys and namespace secrets remain separate `0600` files or protected keyring entries.
4. No successful command may report a state change that failed to commit.
5. Detached startup publishes PID/run metadata only after readiness; every post-spawn failure kills and waits for the owned child.
6. `stop pipe/...` stops a run but preserves the pipe definition. `rm pipe/...` removes the definition only after resolving live-run conflicts.
7. Three-machine tests use isolated run directories and never mutate another Tubo installation.

## CLI output

1. stdout is the primary result.
2. stderr is progress, warnings, hints, and diagnostics.
3. technical logs are hidden unless verbosity/log level enables them.
4. `--json` stdout remains one parseable JSON value through every implicit subflow.
5. Human output may evolve; documented JSON field meaning and error classes require versioned decisions.
6. `watch pipes --json` is NDJSON: each change is one parseable JSON object, identified by `scope` plus `pipe_id`, with explicit `event_type` (`added`, `updated`, `expired`, `removed`, `degraded`, or `recovered`); it projects verified local state only.
