# Architecture-driven implementation roadmap

**Canonical roadmap.** Future implementation follows [`../reference/HIGH_LEVEL_DESIGN.md`](../reference/HIGH_LEVEL_DESIGN.md). `prototype-v2-final` is reference evidence, not compatibility target.

## Development policy

- Freeze prototype baseline as historical/reference evidence.
- Land architecture preparation before A0 implementation.
- Stop new feature development against prototype architecture; allow explicitly required security fixes.
- Optional capabilities and prototype-parity work are not silent critical-path items. A capability may be disabled/removed fail-closed, which files explicit deferred debt, continues the phase, and permits later reintroduction behind a named real consumer. Capability negotiation is optional-intersection wire: removing a capability on both sides degrades deterministically to the ordinary supported path.
- Required security fixes and corrections to the current supported path are explicit exceptions and may proceed outside the accepted phase scope.
- After two independent review cycles reach the same phase-blocking conclusion, choose FIX/REDESIGN, DISABLE/DEFER, or DECOMPOSE rather than opening further serialized blocked/split/ordering review rounds.
- Deferred debt records: capability/behavior deferred; current path reachability; consequence; mitigation (quarantine/removal); reconsideration prerequisite (a real consumer, plus the seam and the contract the reintroduction needs) and phase; and retained evidence references.
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

**Functional proof:** Journey A, zero-config public exact share: clean A attaches HTTP and emits exact invite; clean B connects; traffic succeeds over verified signed-bundle-resolved `relay.tubo.click` reachability with direct path when available and relay fallback otherwise; no Scope, namespace, discovery, membership, user-configured authority, or final hardcoded/temporary provider path; receiver gets successful authorization result before local target open. Journey A requires no resume and no mid-session carrier/Pipe migration: direct-versus-relay is initial path selection only; the direct-when-available policy returns only with the L1 Reachability slice (deferred A0-enabling debt).

**A0 vertical critical path**

1. L0 domain boundary (done: #21/#22, `f2ce40a`).
2. Explicit runtime ownership `NodeRuntime -> PipeRuntime -> SessionRegistry -> Session` (done: #26/#27, `7b6c06f`).
3. Executed fail-closed session-resume quarantine (issue #39) — disables the defective prototype resume surface, removes a live attack surface, and unblocks the vertical path. The quarantine is wire-safe because capability negotiation is optional-intersection and because the post-quarantine binary simply does not advertise/offer/accept resume; mixed peers degrade to the ordinary authorized non-resume `/tubo/tunnel/2.0` tunnel.
4. Neutral Carrier seam minimal (rescoped #28; supersedes closed PR #29) — Session consumes a Carrier contract, not libp2p stream/host/*store.Store concrete types — exit criterion 4, basis for 6/7.
5. Reachability as L1 boundary returning carriers (no policy) + minimal signed public-bootstrap-bundle contract (version/validity/signature fail closed) — exit criteria 3, 6 (done: #44; see ARCHITECTURE_ACCEPTANCE.md evidence). Criterion 6 implemented; criterion 3 implemented with production trust material ratification (issue #45, Step 10A). Journey A production reachability deferred to step 10B.
6. Consumer-side/neutral open-authorization contract at the receiving boundary + Exact Pipe Authority invite redemption → short-lived exact-Pipe Access Lease; L2 imports no concrete L3 — exit criteria 1, 2. 6A: neutral receiving-boundary OpenAuthorizer seam done (#48). 6B: canonical Scope-independent exact-Pipe Access Lease + Exact Pipe Authority done (#49/#47). The receiving serve path injects the concrete `ExactOpenAuthorizer` (composition-root adapter in `internal/runtime`) behind the neutral openauth seam; the prototype Scope-token adapter is quarantined (`newScopeTokenOpenAuthorizer` panics). The concrete adapter validates 16 fail-closed cases (canonical AccessLease + possession proof), returns only generic "authorization denied", and the authorization timer still terminates Session+Carrier+target on lease expiry. The store adapter (`store.ExactAuthorityRevocationState`) provides revocation/epoch/redemption persistence without `*store.Store` in `internal/exactauthority`. Pipe Engine (step 7) may proceed.
7. Pipe Engine (`Ingress → Pipe → Target`) composition, Pipe-to-Pipe, loop/hop rules — exit criterion 7 (done: #57; see ARCHITECTURE_ACCEPTANCE.md evidence).
8. Durable Service/Pipe definition distinct from PipeRuntime/Session + narrow persistence ports + L4 use cases + thin CLI — exit criteria 8, 9, 10. 8A (usecase boundary + narrow ports + durable/runtime separation, issue #59) done via PR62 (`7722fa6`); 8B (thin CLI + Scope-less exact runtime path + local Journey A proof, issue #61) done via PR63 (`75f44051`).
9. Real `go list -deps` L0-L5 dependency-edge harness — exit criterion 11 (done: #64, this PR).
10. Journey A zero-config exact-share proof + acceptance-matrix reconciliation — exit criterion 12 + epic #18 DoD.
     10A. Production trust material ratification (issue #45, done): embedded signed bootstrap bundle for `relay.tubo.click`, pinned `tubo-bootstrap-root-2026-01` root, 12 trust-material tests, `defaultBootstrap()` replaced with `bundle.ParseVerify(now)` path. Relay at `relay.tubo.click` runs prototype code with swarm PSK — must be upgraded to A0-compatible code before 10B.
     10B. Distributed Journey A proof (pending): deploy A0-compatible relay, run three-machine positive/negative tests, preserve logs, update evidence. Requires 10A merged and relay deployed.

Resume/carrier-replacement correctness, epoch, and no-replay hardening are deferred post-A0 resilient-Session debt (#32, children #34-#37), not on the A0 path.

**Non-goals:** no mid-session resume or carrier/Pipe migration in Journey A; otherwise Scoped collaboration, catalog, A2A/MCP semantics, broad migration/compatibility layer, package-per-layer tree, advanced gossip, peer sync, discovery-key rotation, operator/private provider substitution, trust-root or bundle/key rotation lifecycle, full prototype feature parity.

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
