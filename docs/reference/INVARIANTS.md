# Binding invariants

These invariants are acceptance criteria, not aspirations.

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

1. Protocol 2.0 performs a bounded handshake before any target is dialed.
2. After `OpenOK`, traffic is raw full-duplex bytes for HTTP, WebSocket, TCP, and TLS passthrough.
3. No application payload is buffered in full by Tubo.
4. Half-close is propagated in both directions where the platform supports it.
5. Handshake deadlines are cleared after authorization and target selection; established sessions have no absolute lifetime timeout.
6. A failed setup may be retried before application bytes flow. Tubo never transparently replays bytes after forwarding begins.
7. Direct paths are preferred when usable; relay is the deterministic fallback; a session may later upgrade to direct.

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
