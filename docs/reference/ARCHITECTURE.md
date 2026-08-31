# Architecture

**Normative role:** [`HIGH_LEVEL_DESIGN.md`](HIGH_LEVEL_DESIGN.md) defines Tubo product and layer invariants. This document defines implementation-boundary rules for A0 onward. It does not claim current prototype packages are target boundaries.

## Status

`prototype-v2-final` permanently preserves pre-A0 reference baseline (`637c3859051103c844a042dac04c40b7ce0627e1`). Main progressively diverges during A0 and later phases, combining architecture-driven code with remaining prototype-derived code while migration proceeds. Tag package graph documents learned behavior, not future dependency authority. See [`../implementation/PROTOTYPE_BASELINE.md`](../implementation/PROTOTYPE_BASELINE.md).

## Implemented A0 slice: L0 domain boundary

Issue #21 establishes `internal/domain` as first checked semantic L0 boundary. Unused prototype grant/envelope types with L3 authority semantics are removed rather than preserved. Service validation accepts HTTP(S), TCP, and Unix local endpoints and rejects A2A/MCP product capabilities. Pipe validation requires one Service or Pipe target, rejects self-targeting, and treats transitional Scope data as optional but validated when present. This domain change does **not** make prototype runtime paths Scope-less: its published-Pipe entrypoint rejects a Scope-less Tubo definition before dereference. `tests/verify-architecture.sh` invokes `go list -deps` and fails if this selected L0 package has non-stdlib dependencies. Later A0 slices remain responsible for runtime ownership, Scope-independent runtime connectivity, Carrier/Session, authorization, persistence, and use-case boundaries.

## Implemented A0 slice: neutral Carrier seam (issue #28)

Issue #28 replaces Session's direct libp2p concrete host/stream/peer and `*store.Store` coupling with a neutral Carrier contract realized at the host/L1 edge. `internal/node` (the host-integration adapter) defines and implements `node.Carrier` (raw full-duplex bytes, `Close`/`Reset`/`CloseWrite` half-close, `SetDeadline`) over a libp2p stream; it imports neither runtime, authority, discovery, store, nor CLI. The Session engine (`internal/runtime/session.go`) consumes only `node.Carrier` and no longer imports libp2p host/network/peer/protocol packages or `*store.Store`; the unread `*store.Store` field is removed (exit criterion 4 store clause). The production tunnel data plane on both the responder (`tunnelServer.serve`) and initiator (`openLocalConnection`) sides is re-grounded through an owner-bound `Session` + active `Carrier`: the active carrier is admitted through the `PipeRuntime`-owned registry, the authorization timer (lease expiry) is transferred to `Session.Stop`, and the raw `copyDuplex` direct tunnel path is deleted while wire frames, lease-validation-before-target-open ordering, `authorizationTimer` lease-expiry close, `store.Session` telemetry, and quarantine negative tests are preserved.

`tests/verify-architecture.sh` extends the deterministic `go list`/symbol guard to require that the Session boundary file has no direct libp2p host/network/peer/protocol or store import and that the host-integration adapter has no runtime coupling. Session byte flow, cancellation, owner bound, and inbound exact-owner dispatch are exercised through the Carrier seam (see `session_continuity_test.go`); the re-grounded end-to-end authorized tunnel path is covered in-process by `TestLocalPipeRecoveryFetchesNewAuthoritySnapshotAfterPublisherRestart`. Wire, authorization ordering, and provider topology are unchanged; distributed evidence is not material to this type-seam replacement (#39 precedent).

## Owner-bound Exact Authority contract (issue #52, step 6B1a)

Step 6B1a lands only the pure/lower-level value credential contract: not the wire receiving path and not any durable secret lifecycle. `internal/accesslease` is stdlib-only and defines three Scope-independent credentials with a strict bounded flat-JSON decode before allocation: the canonical exact-Pipe `AccessLease` (`version=1`, `type=pipe.open.access`), the immutable owner-signed `OwnerBinding` (binds exactly one dedicated Exact Authority key plus bounded neutral redemption peer addresses/protocol and an authorization epoch to one exact Pipe), and the redesigned recipient-unbound `ExactInvite` that composes the OwnerBinding (embedding it as a bounded base64url reference). A PipeID is self-deriving (`sha256(ownerPublic)`), so `VerifyPipeOwner(pipeID, ownerPublic)` and `OwnerBinding.VerifyOwnerAuthority(...)` keep the Exact Authority owner-bound and owner-local: a remote/ambient key, relay, bootstrap provider, discovery record, or endpoint can never mint or imply authority for another's Pipe. Each credential signs over its own NUL-delimited domain prefix (Access Lease, Owner Binding, Exact Invite), so signed bytes cannot be re-used or mistaken across kinds. Every base64url text entry (public keys, signatures, outer credential bodies, embedded bindings) must decode and then re-encode byte-for-byte (RawURLEncoding equality); CR/LF/CRLF, padding, whitespace, wrong lengths, alternate spellings, and malformed text are rejected before any crypto or unmarshal. 6B1a is neither wire integration nor secret lifecycle: it makes no durable secret-path/state claim. Step 6B is not declared complete.

## Owner-local secure key lifecycle (issue #54, step 6B1b)

Step 6B1b adds the smallest shared owner-local Ed25519 private-key lifecycle for both Pipe owner keys (`authority.GeneratePrivate`/`ReadPrivate`) and dedicated Exact Authority keys (`internal/exactauthority`). `internal/localkey` is a narrow stdlib-only primitive, not a generic filesystem subsystem or secret manager: it stores one canonical raw-base64url Ed25519 private key per `0600` file under a `0700` directory, rejects final symlinks and unsafe modes, bounds reads before allocation, rejects partial/corrupt/oversized/noncanonical files, and provisions with local file locking plus write -> file fsync -> publish -> directory fsync before success. Concurrent local provisioners serialize; losers load the durable winner only after the winner publishes and fsyncs the containing directory.

`internal/exactauthority` is L3 owner-local lifecycle only. Trusted local owner workflow provisions/loads the dedicated key through `internal/localkey` and creates an owner-signed `OwnerBinding`; read-only `Loader` only loads an existing key and verifies it against an owner binding for redemption serving; the `RevocationState`/`RedemptionStore` ports stay narrow and store-free; `RedeemExactInvite` implements the mandatory 12-step remote redemption ordering with Loader-only capability, zero persistent mutation before the atomic redemption step, and the one-winner atomic redemption contract via a narrow persistence port. It imports only `internal/accesslease` and `internal/localkey`, with no runtime receiving path, OpenAuthorizer wiring, Scope policy, reachability, discovery, Store/SQLite schema, CLI, Pipe Engine, or old prototype authority token dependency. The concrete `ExactOpenAuthorizer` (canonical AccessLease + possession-proof validation behind the neutral openauth seam) is a composition-root adapter living in `internal/runtime`, not in this pure package, so receiving-boundary wiring meets the #48 rule that serve.go must not import concrete authority material. Step 6B2 wires canonical Access Lease authorization into the receiving remote-open path; Pipe Engine step 7 may proceed.

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

Issue #48 implements the 6A transitional seam-only step of this boundary: a neutral consumer contract (internal/openauth - OpenRequest/Authorization/OpenAuthorizer, no Scope/discovery/target/trust-store/metadata feed, stdlib-only) is injected into the receiving L2 serve path, which builds the request, invokes the authorizer, and independently binds PipeID/Subject/non-zero future expiry before dialing the local target. The receiving-boundary file (internal/runtime/serve.go) no longer imports concrete internal/authority or internal/store for remote-open validation.

Issue #49 / step 6B2 completes this boundary with the canonical Scope-independent exact-Pipe Access Lease + Exact Pipe Authority. The concrete `ExactOpenAuthorizer` (in `internal/runtime` as the composition-root adapter) validates a canonical AccessLease + receiver-challenge possession proof against the owner-bound Exact Authority key, performing 16 fail-closed cases (missing/malformed/non-canonical lease, wrong authority key, wrong subject, wrong pipe audience, expired lease, wrong-purpose proof, missing proof, wrong-key proof, revoked lease, stale epoch, revocation read error, epoch read error, invite-as-lease, prototype-token-as-lease, Scope-membership-alone) and returning only the generic "authorization denied" message so no internal trust/revocation detail leaks. The receiving serve path performs bounded decode -> AuthorizeOpen -> defensive result binding -> target dial -> OpenOK -> Session, and the authorization timer still terminates Session+Carrier+target on lease expiry. The transitional prototype Scope-token adapter (`runtime.scopeTokenOpenAuthorizer`) is quarantined (`newScopeTokenOpenAuthorizer` panics): the prototype Scope-token is UNREACHABLE as remote-open authorization. The Scope-believing tunnelServer path (`runPublishedPipe`) now wires the concrete `ExactOpenAuthorizer` behind the neutral seam (via `loadExactAuthorizer` loading the owner-local exact authority key), still importing no concrete authority in serve.go.

## Runtime ownership

```text
NodeRuntime -> PipeRuntime(s) -> SessionRegistry -> Session(s)
```

Issue #26 implements this in-process ownership/lifecycle primitive in `internal/runtime`: each `PipeRuntime` owns an isolated Session registry; Session creation, cancellation, removal, and resume/control dispatch are owner-bound; Node cancellation closes descendant PipeRuntimes and Sessions before close completion. Registration rejects duplicate IDs within one owner and closed owners reject registration and dispatch. `StreamRoleDeclaration` is consumed once through a strict 4 KiB bound at owner-bound dispatch before control/resume handling; accepted inbound and outbound streams are reset during Session close so they cannot attach or activate a later carrier; close completion waits all admitted dispatch, outbound operation, activation, pump, and ACK work. Lifecycle close ownership is separate from mutable transport protocol state.

This slice does **not** decide whether PipeRuntimes share a libp2p host. Its same-SessionID isolation is only separate in-process owner/registry dispatch; current role declaration remains SessionID-only and does not make duplicate IDs wire-routable on one shared host. It adds no PipeID field, node-level Carrier/Reachability routing, permanent host topology assumption, Carrier redesign, Store boundary, or Session-state redesign. No package-global mutable runtime registry, process-global session dispatch map, or ambient mutable runtime state remains on this path.

## Persistence boundary

SQLite may implement transactional state. Consumer-owned narrow ports live beside consumer, such as PipeRepository or RevocationRepository. Do not use one generic ports package or spread concrete `*store.Store` through L0/L2 contracts. Persistence failures must prevent claimed mutation success.

## Implemented A0 slice: L4 use cases + narrow persistence ports (issue #59, step 8A)

`internal/usecase` is the L4 use-case composition boundary for Journey A: `Attach`, `ShareExact`, `ConnectExact`, and `StartPipe` compose narrow injected capabilities. The package imports only `domain` (L0), `accesslease`/`exactauthority`/`localkey` (L3 owner-local credential/key lifecycle), and stdlib — no store, runtime, discovery, authority, CLI, node, engine, or concrete reachability. Consumer-owned ports (`ServiceRepository`, `PipeRepository`) are defined beside the consumer; `internal/store` provides composition-root adapters (`ServiceRepoAdapter`/`PipeRepoAdapter`) satisfying them structurally, so no `*store.Store` appears in use-case contracts. Use cases never orchestrate Store transactions, authority provisioning topology, discovery, or runtime assembly; they own multi-step ordering and explicit failure semantics (durable definition left in place and reported exactly when a later step fails; no generic transaction coordinator). Journey A exact-path use cases are Scope-free by construction.

The durable/runtime separation foundation (exit criterion 8, part 1): `domain.PipeSpec`/`domain.ServiceSpec` remain pure durable definitions with no PID/Carrier/Session/address/lease fields (structural assertion); Pipe identity is self-deriving and stable across repository restart; transient Run/Session persistence never mutates definition rows. The actual Scope-less published runtime path, redemption serving, thin CLI rewiring, and local Journey A proof remain step 8B (issue #61).

## Discovery boundary

Discovery finds Pipe; reachability reaches identified peer; authorization permits open. PipeRecord is network metadata only. Descriptor/catalog discovery belongs above it. Exact invites bypass discovery.

## Public infrastructure

Public bootstrap/relay is L1 reachability. A0 includes minimal real zero-config public foundation: signed public bootstrap bundle contract; embedded/preinstalled bundle verification; public relay/bootstrap identity and addresses; version, validity, and signature checks that fail closed; initial `relay.tubo.click` provider; and zero-config reachability resolution. Journey A must not complete through a final hardcoded or temporary-provider shortcut.

Implemented now (issue #44): the neutral L1 Reachability boundary (`internal/reachability`) returns a `node.Carrier` / dial target from verified provider info (bundle or test-injected `peer.AddrInfo`) and cannot express authorization; the bounded signed public-bootstrap bundle verifier (`internal/reachability/bundle`) enforces supported-version, downgrade, dedicated bootstrap-role trust root (no TOFU), no-grace validity, signature, and decode-before-allocation bounds fail closed; and zero-config resolution fails closed deterministically when no valid production bundle is installed.

Relay-reservation lifecycle is also L1 reachability. `internal/reachability.RelayReservation` owns initial relay connection and reservation acquisition, periodic reservation renewal, reconnect, reservation reacquisition after reconnect, and failure/recovery, and encodes the invariant that relay connection liveness is not proof of reservation validity (a connected relay whose Circuit Relay v2 reservation has lapsed is renewed). Renewal is scheduled from the relay-provided reservation expiration minus a conservative safety margin, never from an assumed relay TTL; a shorter returned TTL moves renewal earlier, and a minimum-delay floor prevents pathological (near-instant) expiration from driving a tight loop. Both scoped and scope-less published Pipes consume the same L1 `Acquire`/`Serve` lifecycle; the runtime only maps maintenance outcomes onto its existing degraded/ready Run status and must not reimplement reservation timing or validity. Production trust material, the final `relay.tubo.click` relay identity/addresses, and a dedicated bootstrap signing root are deliberately absent from this slice and await explicit ratification (issue #45); until then zero-config reachability fails closed and no Journey A production resolution claim is made.

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
