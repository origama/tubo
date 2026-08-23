# Architecture-driven implementation roadmap

**Canonical roadmap.** Future implementation follows [`../reference/HIGH_LEVEL_DESIGN.md`](../reference/HIGH_LEVEL_DESIGN.md). `prototype-v2-final` is reference evidence, not compatibility target.

## Development policy

- Freeze prototype baseline as historical/reference evidence.
- Land architecture preparation before A0 implementation.
- Stop new feature development against prototype architecture; allow explicitly required security fixes.
- No internal backward compatibility with prototype packages required.
- Do not preserve bad abstractions to minimize diff.
- Prefer small coherent A0 PRs. Architecture Guardian decomposes A0 after preparation is accepted.

## A0 — Layered Architecture Foundation

**Goal:** Make composable authorized connectivity real before adding collaboration features.

**Architectural outcomes**

- Enforce L0–L5 dependency direction with real A0 boundaries.
- Establish pure L0 values/contracts with no I/O.
- Establish explicit `NodeRuntime -> PipeRuntime -> SessionRegistry -> Session` ownership; no globals.
- Define Carrier/Session seam and make L1 Reachability return carriers, not authorization decisions.
- Materialize Pipe Engine for `Ingress -> Pipe -> Target`, Pipe composition, loop/hop handling.
- Define consumer-side/neutral `OpenAuthorizer`-style contract: L2 enforces successful authorization before local target open; L3 implements lease decoding/crypto validation, issuer trust, revocation/epochs, policy, and refresh. L2 must not import concrete L3 authority/revocation implementations.
- Make remote open consume short-lived Access Lease independent of Scope topology.
- Deliver minimal real public foundation for Journey A: signed public bootstrap bundle contract, embedded/preinstalled verification, initial `relay.tubo.click` identity/addresses, version/validity/signature fail-closed semantics, and zero-config reachability resolution.
- Establish durable Service/Pipe definition distinct from PipeRuntime and transient Session; introduce consumer-defined persistence capabilities and remove broad concrete Store runtime coupling on paths moved into A0.
- Introduce L4 use cases so CLI becomes parsing/presentation adapter.
- Define initial lightweight dependency-edge harness only after edges exist.

**Functional proof:** Journey A, zero-config public exact share: clean A attaches HTTP and emits exact invite; clean B connects; traffic succeeds over verified signed-bundle-resolved `relay.tubo.click` reachability with direct path when available and relay fallback otherwise; no Scope, namespace, discovery, membership, user-configured authority, or final hardcoded/temporary provider path; receiver gets successful authorization result before local target open.

**Non-goals:** Scoped collaboration, catalog, A2A/MCP semantics, broad migration/compatibility layer, package-per-layer tree, advanced gossip, peer sync, discovery-key rotation, operator/private provider substitution, trust-root or bundle/key rotation lifecycle, full prototype feature parity.

**Dependencies:** approved HLD/architecture prep; embedded/preinstalled public trust material for minimal signed bundle. Public infrastructure remains reachability-only.

**Exit criteria**

1. No remote Pipe open path bypasses successful `OpenAuthorizer`-style result before local target open.
2. Exact Pipe Authority redemption and Scope-less share produce same lease contract consumed through L2 contract; L2 imports no concrete L3 authority/revocation implementation.
3. Minimal signed bundle verifies embedded/preinstalled trust, identity/address, version, validity, and signature fail closed; zero-config Journey A resolves initial `relay.tubo.click` reachability without final hardcoded/temporary-provider path.
4. Session code depends on Carrier contract, not concrete libp2p stream/host/store on A0 path.
5. Runtime registry/stream dispatch ownership is explicit and no mutable package-global dispatch state remains on A0 path.
6. Reachability can be tested as carrier provider without policy decision.
7. Pipe Engine composes local Service/Pipe targets and enforces loop/hop rules.
8. Durable Service/Pipe definition remains distinct from PipeRuntime and transient Session; A0 establishes persistence/ownership basis for later persistent-connectivity recovery.
9. CLI invokes use cases, not storage/runtime/control-plane choreography.
10. Persistence consumers use narrow ports where moved; persistence failure fails closed.
11. `go list -deps`-based architecture harness proves documented edges.
12. Unit/component, in-process, local CLI, and distributed proof cover Journey A; architecture review confirms no shortcut.

## A1 — Scoped Collaboration

**Goal:** Add Scope coordination/security without redesigning A0 connectivity.

**Architectural outcomes:** Scope Authority, membership/policy, scoped publishing/discovery, lease issuance, refresh, revocation, recovery. Reuse A0 Carrier, Session, Pipe Engine, and exact lease contract.

**Functional proof:** Journey B: members join, publish/discover, receive Scope Authority exact-Pipe lease, connect using public reachability-only infrastructure.

**Non-goals:** private-provider replacement, catalog metadata, A2A integration, Session redesign for Scope.

**Dependencies:** A0 exit criteria; threat analysis for membership/revocation/discovery.

**Exit criteria:** Scope is optional to connectivity; scoped discovery returns validated PipeRecord only; revocation/refresh freshness guarantees documented and tested; L1/L2 changes independently justified.

## A2 — Private Infrastructure

**Goal:** Make public/private/bootstrap/relay providers replaceable.

**Architectural outcomes:** generalize A0 signed public foundation with operator/private provider adapters, public/private/hybrid composition, richer provider substitution, private bootstrap, trust-root and bundle/key-rotation lifecycle, and justified provider policy behind L1.

**Functional proof:** Journey C: scoped collaboration works over operator-controlled relay/bootstrap; above L1 unchanged in substance.

**Non-goals:** Scope/policy rewrite, catalog, A2A, arbitrary infrastructure plugin framework.

**Dependencies:** A0 minimal signed public-bundle proof, A1 scoped proof, bootstrap trust threat analysis.

**Exit criteria:** provider substitution/private bootstrap/hybrid composition verified; public infrastructure remains reachability-only; A2 rotation lifecycle and richer provider policy have explicit trust decisions; upper layers remain unchanged in substance.

## A3 — Resource and Capability Catalog

**Goal:** Add discoverable higher-level descriptors and catalog/search.

**Architectural outcomes:** Descriptor model and catalog capability above PipeRecord. Application metadata remains outside network PipeRecord.

**Functional proof:** discover capability descriptor, obtain Pipe reference, resolve, lease, Session.

**Non-goals:** A2A protocol integration, changing Session/transport semantics, generic metadata framework.

**Dependencies:** A0/A1 stable Pipe/discovery/authorization.

**Exit criteria:** descriptor validation/discovery has privacy and authority rules; PipeRecord remains bounded connectivity metadata; search path completes without L2 change.

## A4 — A2A and Agent Discovery

**Goal:** Build AgentDescriptor, capability/skill discovery, A2A integration above stable primitives.

**Architectural outcomes:** A2A/MCP live as L5 product/application capability consumers, not Service kinds.

**Functional proof:** Journey E: search AgentDescriptor/capability, resolve Pipe, obtain lease, Session, A2A use.

**Non-goals:** Session, Carrier, reachability, or authorization model rewrite for A2A.

**Dependencies:** A3 catalog/descriptors, A0–A2 connectivity/security foundation.

**Exit criteria:** A2A serves as architecture fitness test: no transport semantic change; descriptor-to-Pipe-to-lease flow tested end-to-end; capability metadata remains separate from PipeRecord.
