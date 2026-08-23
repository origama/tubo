# Architecture

**Normative role:** [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md) defines Tubo product and layer invariants. This document defines implementation-boundary rules for A0 onward. It does not claim current prototype packages are target boundaries.

## Status

`prototype-v2-final` permanently preserves pre-A0 reference baseline (`637c3859051103c844a042dac04c40b7ce0627e1`). Main progressively diverges during A0 and later phases, combining architecture-driven code with remaining prototype-derived code while migration proceeds. Tag package graph documents learned behavior, not future dependency authority. See [`../implementation/PROTOTYPE_BASELINE.md`](../implementation/PROTOTYPE_BASELINE.md).

## Boundary model

```text
L5 Product Capabilities
  -> L4 Application Use Cases
  -> L3 Coordination and Security
  -> L2 Secure Connectivity
  -> L1 Reachability
  -> L0 Core
```

Dependencies point downward only. A layer must not reach sideways/upward to complete feature-specific work. Detailed package topology evolves during A0; package names are not fixed before boundaries are proven. In particular, `internal/pipe` and `internal/transport` are intended concepts, not current established package boundaries.

## Implementation responsibilities

| Layer | Owns | Must not own |
|---|---|---|
| L0 Core | identity, refs, immutable domain values, crypto primitives, clocks, errors, small contracts | network, persistence, CLI, OS lifecycle |
| L1 Reachability | node/host integration, direct/relay carriers, hole punching, bootstrap, provider adapters | remote Pipe authorization, Scope policy |
| L2 Secure Connectivity | Carrier, endpoint adapters, Pipe Engine, Session, forwarding, raw byte flow, recovery, receiving-boundary open enforcement | concrete authority/revocation implementation, discovery policy, Scope membership, product protocols |
| L3 Coordination/Security | Exact Pipe/Scope authority implementations, lease decode/crypto validation, issuer trust, revocation/epochs, membership/policy, refresh, records, discovery | Session transport implementation |
| L4 Use cases | attach/connect/share/publish/join/lifecycle orchestration | concrete protocol/runtime/store assembly in CLI |
| L5 Product | gateway, catalogs, MCP, A2A, agent capabilities | lower-layer shortcuts |

## Open authorization boundary

L2 owns receiving-boundary enforcement, consumer-side open-authorization contract, and minimal authorization result needed by connectivity. A conceptual contract may resemble:

```go
type OpenAuthorizer interface {
    AuthorizeOpen(ctx context.Context, req OpenRequest) (Authorization, error)
}
```

Contract is owned beside L2 consumer need or in lower neutral contract location; no package topology follows from example. L3 may implement it. L2 must not import concrete L3 authority/revocation implementations. L3 owns lease decode and cryptographic validation, issuer trust, revocation, epochs, membership/policy, refresh, and issuer behavior. “Authorization is enforced at receiving boundary” means L2 invokes contract and opens local target only after success; it does not move authority implementation into L2. No successful authorization result means no local target open. Reachability returns carrier, not permission.

## Runtime ownership

```text
NodeRuntime -> PipeRuntime(s) -> SessionRegistry -> Session(s)
```

Each Session registry belongs to explicit runtime owner. Stream dispatch receives/inherits this owner. No package-global mutable runtime registry, process-global session dispatch map, or ambient mutable runtime state.

## Persistence boundary

SQLite may implement transactional state. Consumer-owned narrow ports live beside consumer, such as PipeRepository or RevocationRepository. Do not use one generic ports package or spread concrete `*store.Store` through L0/L2 contracts. Persistence failures must prevent claimed mutation success.

## Discovery boundary

Discovery finds Pipe; reachability reaches identified peer; authorization permits open. PipeRecord is network metadata only. Descriptor/catalog discovery belongs above it. Exact invites bypass discovery.

## Public infrastructure

Public bootstrap/relay is L1 reachability. A0 includes minimal real zero-config public foundation: signed public bootstrap bundle contract; embedded/preinstalled bundle verification; public relay/bootstrap identity and addresses; version, validity, and signature checks that fail closed; initial `relay.tubo.click` provider; and zero-config reachability resolution. Journey A must not complete through a final hardcoded or temporary-provider shortcut.

This foundation is separate from encrypted gossip, peer sync, and discovery-key rotation. A2 generalizes it with operator/private providers, public/private/hybrid composition, richer substitution, trust-root and bundle/key rotation lifecycle, private bootstrap, and justified operational/provider policy. Bundle never conveys Scope membership or authorization.

## A0 implementation sequence

A0 establishes contracts before moving feature behavior: pure L0 values, explicit runtime ownership, Carrier/Session seam, real Reachability seam, Pipe Engine, receiving-boundary open-authorization contract, minimal real public bootstrap foundation, narrow persistence capabilities, and L4 use-case composition. It also establishes durable Service/Pipe definition distinct from transient runtime/Session state. First functional proof is zero-config exact share, but A0 also requires dependency and ownership evidence.

## Enforcement plan

After real A0 boundaries exist, add lightweight deterministic architecture check using `go list -deps` and explicit allowed/forbidden internal edges. Keep rule source small and reviewed. Do not introduce heavyweight architecture framework or brittle package-layout check in preparation phase.

## Review checklist

- Does dependency point downward?
- Is remote open impossible without verified Access Lease?
- Is Scope absent from direct connectivity requirement?
- Does handler have explicit runtime ownership?
- Is persistence consumed through narrow need-specific capability?
- Did feature add independently justified primitive rather than shortcut?
- Do unit/integration/E2E evidence and dependency checks cover change?
