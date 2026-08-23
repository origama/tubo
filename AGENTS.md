# AGENTS.md — Tubo architecture-driven coding-agent contract

## Role and durable memory

- Architecture Guardian / Orchestrator: GPT-5.6 Sol.
- Worker / Builder: GPT-5.6 Terra.
- Repository and GitHub are durable memory. Do not rely on conversational memory.
- Before changing code, inspect current repository reality, assigned issue, linked issues, current main, and relevant prototype code/tests.

## Product definition

Tubo is a secure peer-to-peer connectivity and collaboration platform.

At its smallest:

```text
local service -> Tubo attach -> exact invite -> clean Tubo installation
              -> short-lived Access Lease -> authorized encrypted connectivity
```

Same primitives grow into scoped collaboration, signed Pipe discovery, private infrastructure, persistent connectivity, gateways, catalogs, MCP, A2A, agent discovery, capability search, and future integrations.

Tubo is not primarily a VPN, reverse proxy, service mesh, or centralized tunnel broker.

## Implementation reset and prototype

`prototype-v2-final` permanently records pre-A0 implementation baseline `637c3859051103c844a042dac04c40b7ce0627e1`. Code, tests, and documentation reachable from that tag are prototype reference baseline. Main progressively diverges during A0 and later phases; it may contain architecture-driven code alongside remaining prototype-derived code.

- Consult prototype implementation/tests at tag for hard-won failure and security knowledge.
- Do not copy prototype coupling blindly.
- Current internal APIs, wire formats, state formats, CLI behavior, and package layout are not internal compatibility constraints.
- Retain learned security/failure invariants unless explicitly superseded.
- New feature development against prototype architecture stops. Explicitly required security fixes remain allowed.
- Do not implement A0 work before its Architecture Guardian decomposition is accepted.

See [`docs/implementation/PROTOTYPE_BASELINE.md`](docs/implementation/PROTOTYPE_BASELINE.md).

## Required reading

1. assigned GitHub Issue and linked issues;
2. [`docs/README.md`](docs/README.md);
3. [`docs/reference/HIGH_LEVEL_DESIGN.md`](docs/reference/HIGH_LEVEL_DESIGN.md);
4. [`docs/reference/ARCHITECTURE.md`](docs/reference/ARCHITECTURE.md);
5. [`docs/reference/ONTOLOGY.md`](docs/reference/ONTOLOGY.md), [`INVARIANTS.md`](docs/reference/INVARIANTS.md), [`SECURITY.md`](docs/reference/SECURITY.md);
6. [`docs/implementation/ROADMAP.md`](docs/implementation/ROADMAP.md) and [`PROTOTYPE_BASELINE.md`](docs/implementation/PROTOTYPE_BASELINE.md);
7. [`docs/testing/ARCHITECTURE_ACCEPTANCE.md`](docs/testing/ARCHITECTURE_ACCEPTANCE.md) and [`ACCEPTANCE_MATRIX.md`](docs/testing/ACCEPTANCE_MATRIX.md);
8. [`docs/reference/PROTOCOL.md`](docs/reference/PROTOCOL.md) for wire work, [`STATE.md`](docs/reference/STATE.md) for persistence work, [`CLI.md`](docs/reference/CLI.md) for CLI work;
9. [`docs/runbooks/THREE_MACHINE_E2E.md`](docs/runbooks/THREE_MACHINE_E2E.md), [`CHANGELOG.md`](CHANGELOG.md), and [`VERSIONING.md`](docs/reference/VERSIONING.md).

HLD is normative for future architecture. Prototype-specific reference documents must say when they describe existing code.

## Canonical model

```text
Service    = local application endpoint; HTTP, TCP, Unix socket
Pipe       = stable directed connectivity resource: Ingress -> Pipe -> Target
Session    = concrete active traversal; owns byte flow and continuity
Identity   = stable cryptographic participant/resource-owner identity
Scope      = optional collaboration/coordination/security domain
Invite     = exact bootstrap/redemption credential
Access Lease = short-lived authorization to open exact remote Pipe
Descriptor = higher-level discoverable metadata referencing Pipes
```

A Pipe target is exactly one Service or Pipe. Pipe-to-Pipe composition is fundamental. Service is local, never automatically public or discoverable. `attach` and `connect` are use-case presets, never runtime kinds. A2A and MCP are product/application capabilities, not fundamental Service transport kinds.

## Non-negotiable security rules

**Every remote Pipe open requires a short-lived, cryptographically verifiable Access Lease.** No Access Lease means no remote Pipe open.

Pipe ID, endpoint, record, relay route, invite, or membership token alone are insufficient. Receiving/server boundary enforces authorization and fails closed on invalid signature, wrong subject, wrong Pipe audience, expiry, revocation, stale authorization epoch, or unavailable required revocation state.

Connectivity depends on authorization. Connectivity must not depend directly on Scope topology.

Two issuer topologies produce same lease contract:

1. Exact Pipe Authority: exact invite, recipient redemption plus identity proof, lease bound to exact Pipe.
2. Scope Authority: membership/policy, lease bound to exact Pipe.

Tunnel/Session code must not branch on `public_mode` versus `scope_mode`. Invite is never Access Lease.

### Authorization implementation boundary

L2 owns receiving-boundary enforcement, consumer-side open-authorization contract, and minimal successful authorization result required before local target open. L3 owns authority implementations, lease decoding and cryptographic validation, issuer trust, revocation, authorization epochs, membership/policy evaluation, and refresh semantics.

L3 may implement contract consumed by L2. Contract belongs with consumer need or lower neutral contract location; L2 must not import concrete L3 authority/revocation implementations. “Enforced at receiving boundary” does not place authority implementation inside L2. No successful authorization result means no local target open.

Relays, bootstrap providers, caches, query peers, and sync peers provide reachability or copies; none are ambient authorization or authority.

## Layer dependency model

```text
L5 Product Capabilities
L4 Application Use Cases
L3 Coordination and Security
L2 Secure Connectivity
L1 Reachability
L0 Core
```

Dependencies point downward only. Layers are semantic boundaries, not package mandate.

- L0: identity, references, immutable domain values, crypto primitives, clocks, errors, small contracts. No network/persistence I/O.
- L1: host integration, direct/relay connection establishment, hole punching, bootstrap, signed public bundle, infrastructure adapters. Obtains carrier; never authorizes Pipe open.
- L2: endpoint adapters, Pipe Engine/graph, Session, Carrier, forwarding, byte flow, recovery, exact endpoint resolution, handshake integration, receiving-boundary enforcement through injected/neutral open-authorization contract. No concrete L3 authority/revocation implementation, Scope policy, discovery policy, or application semantics.
- L3: Exact Pipe Authority, Scope Authority, membership, access/publish leases, refresh, revocation, discovery, record validation, scoped views.
- L4: attach, connect, share, publish, join, start, stop, inspect. CLI/API/UI automate use cases; they do not orchestrate storage/runtime internals.
- L5: gateway, catalog, MCP, A2A, agent discovery, capability search, integrations.

## Boundary rules

- Discovery: find resource. Connectivity: reach identified resource. Authorization: decide caller may open it. Keep separate.
- Exact invite bypasses discovery. PipeRecord contains only network/routing/connectivity metadata. Descriptors carry application metadata.
- Public infrastructure provides reachability, never ambient authorization. A0 zero-config sharing uses verified embedded/preinstalled minimal signed public bundle and `relay.tubo.click`, never final hardcoded/temporary provider path; it requires no Scope, namespace, discovery, membership, or user-configured authority.
- Explicit ownership: `NodeRuntime -> PipeRuntime(s) -> SessionRegistry -> Session(s)`. No package-global mutable registry, process-global session dispatch, or hidden ownership. Stream handlers inherit explicit runtime ownership.
- SQLite may remain one transactional database. Define narrow interfaces beside consumers; persistence is adapter, not domain. Do not inject `*store.Store` throughout core/runtime contracts.
- Use authenticated handshake then raw duplex bytes. Bound decoders before allocation. Fail writes and detached startup closed. Never report uncommitted persistence as success.

## Feature-growth rules

> Do not optimize for shortest path to a feature. Optimize for smallest coherent primitive that lets feature be expressed as composition.

> Passing end-to-end test is necessary, not sufficient. Change violating layer dependency model is incomplete.

> Higher layers consume stable lower-layer capabilities. Lower-layer change needs independently justified primitive/contract, not feature-specific shortcut.

Generalize only after second real consumer. Avoid speculative generic resource frameworks, universal plugin systems, `map[string]any` capability models, god ports packages, and abstraction for abstraction's sake.

## Engineering and delivery

- Prefer standard library before custom machinery. No mandatory central daemon, custom network layer above libp2p, DHT/rendezvous without concrete issue and threat analysis, hidden TODOs, or global mutable runtime state.
- Keep PRs small, coherent, independently verifiable. Add characterization/negative tests before security-sensitive or concurrent changes.
- A0 uses HLD; no prototype backward-compatibility layer required. Do not preserve bad abstractions to minimize diff.
- Future A0 dependency checks use `go list -deps` plus small deterministic allowed/forbidden-edge script. No heavyweight framework or fake checks before boundaries exist.

## Testing and documentation

Run `make verify-local` for local changes. Run distributed scenarios only when behavior requires them and preserve isolated-state safety. A passing functional test does not waive architecture dependency checks.

Any CLI, state, security, wire, operational, test, or architecture change updates canonical document in same PR. Update architecture acceptance and prototype behavior evidence where relevant. Never commit secrets, keys, leases, invite tokens, databases, process state, logs, generated artifacts, or user paths.
