# Prototype baseline

## Record

- **Git tag:** `prototype-v2-final`
- **Commit:** `637c3859051103c844a042dac04c40b7ce0627e1`
- **Status:** permanently preserved pre-A0 reference baseline.
- **Purpose:** retain working experiments, tests, security learning, operational evidence, and failure modes while resetting implementation direction.

Code, tests, and documentation reachable from tag are prototype reference baseline. Main progressively diverges during A0 and later phases, combining architecture-driven code with remaining prototype-derived code while migration proceeds. Exact Git history preserves tag source; source was intentionally not moved or duplicated into archive directory.

## What remains reusable

Prototype code and tests are valuable characterization material. Consult especially:

- `internal/protocol` and tests: bounded framing and strict decoding;
- `internal/authority`, `internal/discovery`, `internal/store`: lease expiry, revocation, record validation, replay/rollback, identity persistence, transactional failure behavior;
- `internal/runtime/pipe.go`, `session.go`, and tests: half-close, relay fallback, direct/hole-punch experiments, restart/address change, resume, path migration, no application-byte replay before successful open/re-resolution;
- `internal/node`: libp2p lifecycle and reachability findings;
- `internal/cli` and contract tests: output, detached lifecycle, diagnostics;
- `tests/integration` and `tests/e2e`: isolation and distributed recovery knowledge.

Learned hard problems include bounded framing, lease expiry, revocation, record replay/rollback, identity persistence, half-close, restart/address change, relay fallback, hole punching, resume, path migration, and no app-byte replay before successful open/re-resolution.

## What is not compatibility constraint

Future A0–A4 code need not preserve prototype internal APIs, package graph, concrete Store contracts, runtime registry behavior, CLI behavior, state schema, wire format, protocol IDs, or migration paths. Do not add adapters merely to retain prototype shape.

Known prototype coupling is deliberate reference for work to avoid: Session directly knows libp2p host/stream and concrete Store; runtime Pipe mixes reachability, discovery, authorization, persistence, execution, Session creation, and service dialing; CLI orchestrates persistence/runtime/control-plane work; Scope is coupled to ingress/token behavior; A2A/MCP appear as Service kinds; Session dispatch uses global mutable registry.

## Preservation policy

Retain learned security/failure invariants unless an explicit design decision supersedes them. Historical issues remain reference, especially revocation, direct connectivity, discovery recovery, and bootstrap threat analysis. Security-critical prototype fixes may occur only when explicitly required. New feature work follows HLD and roadmap.
