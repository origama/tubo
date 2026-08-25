# Architecture

**Normative role:** [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md) defines Tubo product and layer invariants. This document defines implementation-boundary rules for A0 onward. It does not claim current prototype packages are target boundaries.

## Status

`prototype-v2-final` permanently preserves pre-A0 reference baseline (`637c3859051103c844a042dac04c40b7ce0627e1`). Main progressively diverges during A0 and later phases, combining architecture-driven code with remaining prototype-derived code while migration proceeds. Tag package graph documents learned behavior, not future dependency authority. See [`../implementation/PROTOTYPE_BASELINE.md`](../implementation/PROTOTYPE_BASELINE.md).

## Implemented A0 slice: L0 domain boundary

Issue #21 establishes `internal/domain` as first checked semantic L0 boundary. Unused prototype grant/envelope types with L3 authority semantics are removed rather than preserved. Service validation accepts HTTP(S), TCP, and Unix local endpoints and rejects A2A/MCP product capabilities. Pipe validation requires one Service or Pipe target, rejects self-targeting, and treats transitional Scope data as optional but validated when present. This domain change does **not** make prototype runtime paths Scope-less: its published-Pipe entrypoint rejects a Scope-less Tubo definition before dereference. `tests/verify-architecture.sh` invokes `go list -deps` and fails if this selected L0 package has non-stdlib dependencies. Later A0 slices remain responsible for runtime ownership, Scope-independent runtime connectivity, Carrier/Session, authorization, persistence, and use-case boundaries.

## Implemented A0 slice: neutral Carrier seam (issue #28)

Issue #28 replaces Session's direct libp2p concrete host/stream/peer and `*store.Store` coupling with a neutral Carrier contract realized at the host/L1 edge. `internal/node` (the host-integration adapter) defines and implements `node.Carrier` (raw full-duplex bytes, `Close`/`Reset`/`CloseWrite` half-close, `SetDeadline`) over a libp2p stream; it imports neither runtime, authority, discovery, store, nor CLI. The Session engine (`internal/runtime/session.go`) consumes only `node.Carrier` and no longer imports libp2p host/network/peer/protocol packages or `*store.Store`; the unread `*store.Store` field is removed (exit criterion 4 store clause). The production tunnel data plane on both the responder (`tunnelServer.serve`) and initiator (`openLocalConnection`) sides is re-grounded through an owner-bound `Session` + active `Carrier`: the active carrier is admitted through the `PipeRuntime`-owned registry, the authorization timer (lease expiry) is transferred to `Session.Stop`, and the raw `copyDuplex` direct tunnel path is deleted while wire frames, lease-validation-before-target-open ordering, `authorizationTimer` lease-expiry close, `store.Session` telemetry, and quarantine negative tests are preserved.

`tests/verify-architecture.sh` extends the deterministic `go list`/symbol guard to require that the Session boundary file has no direct libp2p host/network/peer/protocol or store import and that the host-integration adapter has no runtime coupling. Session byte flow, cancellation, owner bound, and inbound exact-owner dispatch are exercised through the Carrier seam (see `session_continuity_test.go`); the re-grounded end-to-end authorized tunnel path is covered in-process by `TestLocalPipeRecoveryFetchesNewAuthoritySnapshotAfterPublisherRestart`. Wire, authorization ordering, and provider topology are unchanged; distributed evidence is not material to this type-seam replacement (#39 precedent).

## Owner-bound Exact Authority contract (issue #52, step 6B1)

Step 6B1 lands only the pure/lower-level value contract foundation, not the wire receiving path. `internal/accesslease` is stdlib-only and defines three Scope-independent credentials with a strict bounded flat-JSON decode before allocation: the canonical exact-Pipe `AccessLease` (`version=1`, `type=pipe.open.access`), the immutable owner-signed `OwnerBinding` (binds exactly one dedicated Exact Authority key plus bounded neutral redemption peer addresses/protocol and an authorization epoch to one exact Pipe), and the redesigned recipient-unbound `ExactInvite` that composes the OwnerBinding (embedding it as a bounded base64url reference). A PipeID is self-deriving (`sha256(ownerPublic)`), so `VerifyPipeOwner(pipeID, ownerPublic)` and `OwnerBinding.VerifyOwnerAuthority(...)` keep the Exact Authority owner-bound and owner-local: a remote/ambient key, relay, bootstrap provider, discovery record, or endpoint can never mint or imply authority for someone else's Pipe. Each credential signs over its own NUL-delimited domain prefix (Access Lease, Owner Binding, Exact Invite), so signed bytes cannot be re-used or mistaken across kinds. `internal/exactauthority` depends only on the pure `accesslease` contract and hosts the deterministic owner-local dedicated-key lifecycle (`Provision`/`Load`/`SecretPath`) with mandatory owner proof; read-only `Load` fails closed on missing/corrupt/mismatched material and creates no filesystem entry. 6B1 is not wire integration: the owner-bound output is not yet wired into the receiving remote-open path (that is 6B2) and step 6B is not declared complete.

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

Issue #48 implements the 6A transitional seam-only step of this boundary: a neutral consumer contract (internal/openauth - OpenRequest/Authorization/OpenAuthorizer, no Scope/discovery/target/trust-store/metadata feed, stdlib-only) is injected into the receiving L2 serve path, which builds the request, invokes the authorizer, and independently binds PipeID/Subject/non-zero future expiry before dialing the local target. The receiving-boundary file (internal/runtime/serve.go) no longer imports concrete internal/authority or internal/store for remote-open validation. The existing prototype Scope-token decode/verify/revocation/epoch/proof validation is preserved fail-closed behind a TRANSITIONAL concrete adapter (runtime.scopeTokenOpenAuthorizer) in a separate file; that adapter is NOT a target Access Lease contract and issue #49 replaces it with the canonical Scope-independent exact-Pipe Access Lease + Exact Pipe Authority.

## Runtime ownership

```text
NodeRuntime -> PipeRuntime(s) -> SessionRegistry -> Session(s)
```

Issue #26 implements this in-process ownership/lifecycle primitive in `internal/runtime`: each `PipeRuntime` owns an isolated Session registry; Session creation, cancellation, removal, and resume/control dispatch are owner-bound; Node cancellation closes descendant PipeRuntimes and Sessions before close completion. Registration rejects duplicate IDs within one owner and closed owners reject registration and dispatch. `StreamRoleDeclaration` is consumed once through a strict 4 KiB bound at owner-bound dispatch before control/resume handling; accepted inbound and outbound streams are reset during Session close so they cannot attach or activate a later carrier; close completion waits all admitted dispatch, outbound operation, activation, pump, and ACK work. Lifecycle close ownership is separate from mutable transport protocol state.

This slice does **not** decide whether PipeRuntimes share a libp2p host. Its same-SessionID isolation is only separate in-process owner/registry dispatch; current role declaration remains SessionID-only and does not make duplicate IDs wire-routable on one shared host. It adds no PipeID field, node-level Carrier/Reachability routing, permanent host topology assumption, Carrier redesign, Store boundary, or Session-state redesign. No package-global mutable runtime registry, process-global session dispatch map, or ambient mutable runtime state remains on this path.

## Persistence boundary

SQLite may implement transactional state. Consumer-owned narrow ports live beside consumer, such as PipeRepository or RevocationRepository. Do not use one generic ports package or spread concrete `*store.Store` through L0/L2 contracts. Persistence failures must prevent claimed mutation success.

## Discovery boundary

Discovery finds Pipe; reachability reaches identified peer; authorization permits open. PipeRecord is network metadata only. Descriptor/catalog discovery belongs above it. Exact invites bypass discovery.

## Public infrastructure

Public bootstrap/relay is L1 reachability. A0 includes minimal real zero-config public foundation: signed public bootstrap bundle contract; embedded/preinstalled bundle verification; public relay/bootstrap identity and addresses; version, validity, and signature checks that fail closed; initial `relay.tubo.click` provider; and zero-config reachability resolution. Journey A must not complete through a final hardcoded or temporary-provider shortcut.

Implemented now (issue #44): the neutral L1 Reachability boundary (`internal/reachability`) returns a `node.Carrier` / dial target from verified provider info (bundle or test-injected `peer.AddrInfo`) and cannot express authorization; the bounded signed public-bootstrap bundle verifier (`internal/reachability/bundle`) enforces supported-version, downgrade, dedicated bootstrap-role trust root (no TOFU), no-grace validity, signature, and decode-before-allocation bounds fail closed; and zero-config resolution fails closed deterministically when no valid production bundle is installed. Production trust material, the final `relay.tubo.click` relay identity/addresses, and a dedicated bootstrap signing root are deliberately absent from this slice and await explicit ratification (issue #45); until then zero-config reachability fails closed and no Journey A production resolution claim is made.

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
