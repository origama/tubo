# Architecture acceptance

These criteria complement prototype [`ACCEPTANCE_MATRIX.md`](ACCEPTANCE_MATRIX.md). They govern A0–A4 architecture, not prototype parity.

## Evidence levels

| Level | Required evidence |
|---|---|
| Unit/component | Pure contracts, negative authorization/decoder/ownership tests |
| In-process integration | Carrier, Pipe Engine, authority, persistence-port composition |
| Local CLI contract | Use-case behavior, stdout/stderr/JSON contract without CLI internal orchestration |
| Distributed E2E | Isolated multi-machine traffic and failure behavior |
| Architecture dependency | Deterministic `go list -deps` allowed/forbidden edge check and review of explicit runtime ownership |

No E2E pass can waive architecture dependency evidence.

## Implemented evidence: A0 issue #21

`go test ./internal/domain` covers canonical HTTP(S)/TCP/Unix Services, rejected A2A/MCP product capabilities, Scope-less Tubo Pipe definition, malformed optional Scope, and invalid/self Pipe targets. Unused prototype grant/envelope types with L3 authority semantics have no L0 consumer and are removed. `go test ./internal/runtime` proves prototype published-Pipe runtime rejects a Scope-less Tubo definition before dereference. `make architecture` runs deterministic `go list -deps` validation that permits only standard-library dependencies for `internal/domain`. This is L0-only evidence: prototype runtime remains Scope-centric, and Journey A, runtime ownership, Carrier/Session, authorization, persistence, and use-case acceptance remain later A0 work.

## Implemented evidence: A0 issue #26 runtime ownership

`go test ./internal/runtime` and `go test -race ./internal/runtime` cover owner-bound `NodeRuntime -> PipeRuntime -> SessionRegistry -> Session` lifecycle: duplicate IDs reject within one PipeRuntime; the same synthetic SessionID under two sibling PipeRuntimes remains isolated in separately owned registries; parent cancellation closes owned Sessions and clears the registry; concurrent Session/Pipe/Node close waits for admitted pump/outbound and local cleanup work before reporting completion; and terminal close ownership remains fenced even when a pump has a local non-EOF failure. Existing runtime tests retain bound-setting, non-resume half-close (FIN once, after the final byte), ownership/cancellation, and remote-open authorization evidence. Bounds, half-close, ownership/cancellation, and remote-open authorization are the current positive A0-path evidence claims; there is no resume, epoch, proof, retention/replay, or planned-switch evidence on the A0 path.

This is not shared-host routing evidence. The A0 path performs no owner-bound stream dispatch (no `StreamRoleDeclaration`): every authorized `/tubo/tunnel/2.0` session runs the plain raw-duplex data plane, and no PipeID wire field or node-level Carrier/Reachability routing is added. This slice does not decide host sharing, encode per-Pipe host ownership, or complete Carrier, persistence, authorization, Scope-independent connectivity, CLI, Journey A, or distributed behavior.

## Implemented evidence: A0 issue #39 session-resume quarantine

`go test ./internal/runtime`, `go test -race ./internal/runtime`, and the deterministic source-scan tests in `quarantine_test.go` prove the fail-closed session-resume quarantine is implemented on the A0 path:

- No production code path advertises, offers, selects, accepts, or registers a handler for `session-resume-v1` or `/tubo/tunnel-resume/2.1`. The tunnel adapter publishes and echoes only `raw-duplex`/`half-close` capabilities, builds an `OpenOK` with no `ResumeOffer`/secret, and registers exactly one stream handler (the authorized `/tubo/tunnel/2.0` tunnel). An unsolicited resume stream has no handler and is reset by libp2p; an unsolicited `ResumeOffer` from a legacy peer is rejected by the strict decoder (unknown `resume_offer` field) and fails closed with zero Session state mutation.
- The session engine (`session.go`/`ownership.go`) is reduced to the smallest non-resume one-active-transport Session foundation that #28 re-grounds behind a Carrier seam: explicit ownership, admission/work/close fencing/cancellation, and a raw duplex pump. Resume/control/replay/candidate/proof/planned-switch/detached-recovery machinery is deleted, not dormant.
- Carrier failure terminates the Session; there is no resume, no detached grace, and no in-session path migration on the A0 path.
- Preserved unchanged: authorized initial `/tubo/tunnel/2.0` handshake order (lease verify -> revocation -> epoch -> proof -> target dial), `authorizationTimer` closing target+stream at lease expiry (pipe.go), full duplex, half-close (`TestHalfCloseSequencing` on the non-resume data plane), and lease-before-target-open ordering. No successful authorization means no local target open.
- Protocol constants/types (`frame.go`) are dead-code-removed from the compiled surface; the `/tubo/tunnel/2.0` structs are untouched.

Non-claims: no phase advancement of resume itself (deferred post-A0 debt #32); no Carrier seam work (that is #28); no three-machine run required (mixed pre/post-quarantine peers deterministically degrade to the ordinary authorized non-resume tunnel; verified by negotiation/unit tests, not distributed).


## Implemented evidence: durable accepted remote-Pipe binding (Journey A extension)

`go test ./internal/exactauthority ./internal/store ./internal/usecase
./internal/runtime -race` plus `tests/integration/journey-a-durable.sh`
cover the durable accepted-binding lifecycle: one-time invite bootstrap
(`AcceptExactInvite` -> durable `RemotePipeBinding`, no local Pipe/ingress),
renewal/exit criteria runs (fresh short-lived AccessLease from the
owner-bound Exact Authority via `/tubo/exact-renewal/1` recipient
possession proof; invite expired, lease expired, consumer restart, owner
restart), revocation denial (pipe-access epoch advance stops renewal and no
new open), fail-closed negatives (no accepted binding, wrong subject/wrong
pipe, invalid/replayed proofs, revocation-state errors, oversized/malformed
frames), and architecture guards R6/R17 (engine never imports/names binding
or renewal policy) with `store` adapters at the composition boundary. The
invite is never a lease and is never required after acceptance; the
accepted binding is not itself L2 permission, is not a local Pipe, and does
not define an ingress.

## Implemented evidence: A0 issue #44 (L1 Reachability + signed bootstrap bundle)

`go test ./internal/reachability/...`, `go test ./internal/runtime -run 'TestReachabilityBundleCarrierThroughSession|TestOpenOverlayHostZeroConfigFailsClosed'`, `go test -race ./internal/runtime ./internal/reachability/...`, and the deterministic architecture guard (`make architecture`) prove the L1 Reachability seam and the bounded signed bootstrap-bundle contract:

- Bundle verifier (`internal/reachability/bundle`) fails closed on: invalid signature, wrong issuer, unpinned/unknown trust root, non-bootstrap (authority) root role, unknown version, version downgrade below the minimum supported, no-grace not-yet-valid/expired validity, malformed, trailing bytes, duplicate addresses, oversized (whole-bundle and per-field bounds, decode-before-allocation), and identity/address mismatch. Canonical signed-byte determinism is pinned. No TOFU: a public bundle references a pinned root by key ID and can never introduce a root.
- Reachability seam (`internal/reachability`) returns a `node.Carrier` from a signed test bundle / injected `peer.AddrInfo`; path selection is direct-first with relay-fallback, initial-only (no mid-session migration). Its type surface carries no policy/lease/scope/authorization input or output.
- In-process integration: signed bundle → Provider → `node.Carrier` → existing Session pump carries full-duplex bytes with exact half-close (no external network).
- Fail-closed zero-config: with no production trust material, zero-config resolution and the `openOverlayHost` seam return the deterministic `ErrNoBootstrapBundle` with no overlay fallback, hardcoded address, or temporary provider.
- Production trust material (issue #45): embedded signed bootstrap bundle for `relay.tubo.click` (PeerID `12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME`), pinned bootstrap root `tubo-bootstrap-root-2026-01` (Ed25519, role `tubo.bootstrap.v1`), 180-day validity window. `defaultBootstrap()` now verifies the embedded bundle via `bundle.ParseVerify(now)` and returns a `Provider`. 12 trust-material tests (deterministic fixture time + current-time smoke) pass.

Architecture effect: `internal/reachability` imports only the L1 host adapter (`internal/node`) and its own pure `bundle` subpackage; it must not import runtime/authority/discovery/store/CLI, and the pure bundle subpackage must not import the node adapter or any runtime/authority/store/discovery/CLI package (checked by `tests/verify-architecture.sh`). `openOverlayHost`/`runPublishedPipe` route carrier acquisition through the seam via `relayOrBootstrap`; user overlay flows unchanged.

**Criteria status (binding):** criterion 6 (Reachability testable as carrier provider without policy decision) is implemented. Criterion 3 (zero-config Journey A resolves `relay.tubo.click` via verified bundle) is **implemented**: production trust material (relay peer ID `12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME`, multiaddrs `/dns4/relay.tubo.click/tcp/4001` and `/ip4/172.232.189.160/tcp/4001`, dedicated bootstrap signing root `tubo-bootstrap-root-2026-01` distinct from any authority key, bundle version 1, 180-day validity window, single-use root expiring 2027-02-23) is embedded and verified. 12 trust-material tests (deterministic fixture time + current-time smoke) pass (issue #45 / PR #66 merge `6dd60e1c7113da726b6c39567b72da454a919e3a`). Relay upgrade done (PR #67 merge `277e4366de4feb58e473308b0967d9bea9164dab`): `relay.tubo.click` now runs A0-compatible code with no swarm PSK, same pinned PeerID preserved, existing bundle remains valid. Distributed Journey A proof COMPLETE at Step 10B (PR #70 merge `c031185c1cd45bf4e58fdbad16f64df2fd76999b`); see `A0_STEP10B_EVIDENCE.md` and the criteria 1-12 reconciliation below.

## Implemented evidence: A0 issue #48 (neutral receiving-boundary OpenAuthorizer seam, 6A)

`go test ./internal/runtime -run 'TestAuthorizeOpen|TestScopeTokenOpen'`, full `go test ./internal/runtime ./internal/openauth`, and the deterministic architecture guard (`tests/verify-architecture.sh`) prove the 6A seam-only step:

- Neutral `internal/openauth` contract (`OpenRequest`/`Authorization`/`OpenAuthorizer`, stdlib-only) carries no Scope, discovery, target, PipeRecord, reachability, trust store, membership/policy, or arbitrary-metadata map.
- The receiving L2 serve path (`internal/runtime/serve.go`) builds the neutral request, invokes the injected `OpenAuthorizer`, and independently binds result PipeID/Subject/non-zero future ExpiresAt before dialing the local target. It imports no concrete `internal/authority` or `internal/store` for remote-open validation.
- Deterministic fake-authorizer tests prove: authorizer denial, wrong PipeID in result, wrong Subject, zero/expired ExpiresAt, and missing injected authorizer never dial the target (dial count 0); a valid result dials the target exactly once.
- Transitional `scopeTokenOpenAuthorizer` preserves the existing prototype Scope-token decode/verify/revocation/epoch/proof validation fail-closed; tests prove missing/malformed/other-type/wrong-key/wrong-scope/wrong-audience/tampered/expired leases deny, and a revoked already-valid credential denies.

This is **seam-only transitional** evidence: the canonical Scope-independent exact-Pipe Access Lease + Exact Pipe Authority remain issue #49 (6B). No prototype `authority.Token` is renamed/wrapped as the target lease; issue #49 removes the transitional remote-open path. `tests/verify-architecture.sh` fails if `serve.go` imports concrete authority/store or if the openauth contract carries a Scope/metadata map.

## Implemented evidence: A0 issue #59 (step 8A use-case boundary + narrow persistence ports)

`go test ./internal/usecase ./internal/store -count=1`, `go test -race ./internal/usecase ./internal/store`, and the deterministic architecture guard (`make architecture`) prove the L4 use-case composition boundary and the durable/runtime separation foundation:

- **Narrow ports:** `internal/usecase` defines consumer-owned `ServiceRepository` and `PipeRepository` interfaces (Upsert/Get/Delete) beside the consumer; no generic `Repository[T]`, UnitOfWork, ORM, or giant Storage interface. `internal/store` satisfies the same contracts via `ServiceRepoAdapter`/`PipeRepoAdapter` (duplicated contracts in the store package; no usecase import). Compile-time bridges assert both the store-local contract and the usecase port satisfaction.
- **L4 use cases compose, they do not orchestrate internals:** Attach validates and upserts a durable Scope-less Service + owned Pipe with a self-deriving stable PipeID; ShareExact proves ownership, provisions the owner-local dedicated Exact Authority key once (create-or-load via `localkey`/`exactauthority`), and mints a bounded one-time canonical ExactInvite; ConnectExact decodes/verifies the invite, acquires a carrier exclusively through an injected `ReachabilityAcquirer` (never Scope/discovery/membership), redeems through an injected `RedemptionService`, verifies the returned AccessLease binds recipient+exact-Pipe+bound-issuer, and creates a durable local ingress Pipe (never started in 8A); StartPipe delegates runtime launch to an injected `PipeStarter` (explicit `runtime_unavailable` when not wired).
- **Durable vs transient (exit criterion 8 foundation):** structural assertion that `PipeSpec`/`ServiceSpec` carry no PID/Carrier/Session/address/lease fields; Pipe identity survives repository restart (SQLite close/reopen in `internal/store`); transient Run/Session writes never mutate definition rows; deleting a Pipe definition removes only the durable definition and leaves Run rows and unrelated Services/Pipes intact.
- **Fail-closed persistence:** validation rejection propagates before any write; unknown refs return `ErrNotFound` (no zero-value success); repository errors propagate as `persistence_failed` and never report success.
- **Architecture guard:** `tests/verify-architecture.sh` now fails if `internal/usecase` imports `store`/`runtime`/`discovery`/`authority`/`cli`/`node`/`engine`/`reachability`, exposes `*store.Store`, or introduces a generic repository framework.

**Non-claims:** 8A makes zero CLI, serving-path, or runtime behavior change. Scope-less publishing, redemption serving, thin-CLI rewiring, and the local Journey A proof are step 8B / issue #61. The Attach-created Scope-less published Pipe definition is durable only; `runPublishedPipe` still rejects scope-less published ingress at runtime until 8B. Journey D richer behavior (restart/supervision/recovery) is later-phase.

## Implemented persistence evidence: issue #25

`internal/store` uses WAL plus SQLite-native immediate writer arbitration for Store write transactions. Deterministic tests prove writer reservation before mutation-controlling reads, characterize deferred read-to-write `SQLITE_BUSY`, retain atomic snapshot/freshness rollback and lease fencing, and prove cancellation released after a blocked `Store.Open` writer acquisition performs no migration/state mutation. Existing five-second SQLite busy policy bounds writer wait; prompt interruption of that driver busy wait is not claimed. This evidence fixes snapshot reconciliation contention only. It does **not** characterize or claim resolution of original private-HTTP `Store.Open`/migration `SQLITE_BUSY` run `32638512295` / job `97191929136`; exact command, home, lock holder, and statement remain unobserved.

## Journey A — zero-config exact share (A0)

Clean machine A runs `tubo attach http://127.0.0.1:8080` and receives exact invite. Clean machine B runs `tubo connect --token <invite>`. Real traffic succeeds.

Required properties:

- no Scope, membership, namespace, discovery, or user-configured authority;
- embedded/preinstalled signed public bundle verifies identity/address, version, validity, and signature fail closed; zero-config resolves initial `relay.tubo.click` reachability, not final hardcoded/temporary provider path;
- direct route when possible, relay fallback otherwise;
- invite is redeemed into short-lived exact-Pipe Access Lease;
- receiving boundary invokes L2 consumer-side/neutral open-authorization contract and rejects missing, invalid, wrong-subject, wrong-audience, expired, revoked, stale-epoch, or unavailable-required-revocation-state authorization; no successful result means no local target open;
- L3 owns concrete lease/crypto/trust/revocation/policy/refresh implementation; L2 imports none of it;
- local Service target never leaves owner;
- Carrier/Session and runtime ownership are explicit; architecture edges pass.
- no mid-session resume or carrier/Pipe migration; direct-versus-relay is initial path selection only (direct-when-available returns with the L1 Reachability slice); no live path migration on the A0 path.

## Journey B — scoped collaboration on public infrastructure (A1)

Members join Scope, publish/discover Pipes, obtain Scope Authority exact-Pipe leases, and connect. Public relay/bootstrap is reachability-only.

Required properties: scoped discovery validates PipeRecord; membership alone does not open Pipe; Scope Authority output matches A0 lease contract; L1/L2 require minimal/no redesign from Journey A; refresh/revocation/recovery guarantees have negative and distributed evidence.

## Journey C — private / hybrid infrastructure (A2)

Replace or supplement public relay/bootstrap with operator provider. Scoped collaboration works without substantive changes above L1.

Required properties: provider trust/config validates; no private Scope authority implied by provider; private bootstrap and hybrid composition work; upper-layer behavior and dependency edges unchanged except justified provider adapter.

## Journey D — persistent service connectivity (foundation A0; richer behavior later)

Users define durable Services and Pipes, run or supervise them, survive process restart/address change where supported, and observe runtime, authorization, path, degradation, and recovery state without redefining resource.

Required properties: durable resource identity/definition is distinct from PipeRuntime and transient Session; explicit runtime ownership controls lifecycle; persistence records durable state without turning it into domain model; no local target or secret leaks through state/observability; later phases may add richer restart, migration, supervision, and distributed-recovery behavior without redefining resource.

A0 proof establishes definition/runtime/session separation and persistence/ownership foundation. It does not promise all prototype persistence or recovery behavior. Step 10B re-confirmed the bounded foundation on the distributed proof (see criteria table below): durable Service/Pipe specs and node identity stable across a supported clean stop/start; runtime process transient (new PID); no resume, migration, refresh, or recovery claims.

## Journey E — higher-level capability (A3/A4)

```text
capability/AgentDescriptor search -> Pipe reference -> exact resolution
                                   -> Access Lease -> Session
```

Required properties: descriptor metadata remains outside PipeRecord; exact Pipe open still validates lease; A2A/agent discovery does not alter Session or transport semantics.

## Phase gates

| Phase | Minimum proof |
|---|---|
| A0 | Journey A plus minimal signed public-bundle proof, L0 unit contracts, Carrier/Pipe Engine in-process tests, local CLI contract, isolated distributed proof, dependency/ownership checks; Journey D definition/runtime/session foundation |
| A1 | Journey B plus membership/discovery/revocation/refresh negative tests and distributed recovery |
| A2 | Journey C with operator/private/public/hybrid provider substitution, private bootstrap, and rotation-lifecycle evidence |
| A3 | Descriptor/catalog component and integration proof; no PipeRecord application metadata |
| A4 | Journey E, capability/skill discovery and A2A integration with unchanged L2 semantics |

### Phase advancement review

A slice or alleged phase blocker advances the current phase only when it is current-phase critical. Every advancement argument answers the five-question critical-path test, separately per block:

1. Which exact current-phase exit criterion does this block or advance?
2. Is a critical current-phase-enabling issue actually open and blocked behind it?
3. Is disable/remove/defer genuinely impossible, or is the capability optional-intersection / removable fail-closed?
4. Is this a required current-phase primitive, or prototype-parity / optional capability?
5. Does deferral make the current-phase path wrong or insecure?

Optional capabilities and prototype-parity work may be disabled or removed fail-closed. Removal files explicit deferred debt with required fields (capability; current path reachability; consequence; mitigation; reconsideration prerequisite — a real consumer plus the seam and contract the reintroduction needs — and phase; retained evidence references), continues the phase, and permits later reintroduction behind a named real consumer. Required security fixes and corrections to the current supported path are explicit exceptions.

After two independent review cycles reach the same phase-blocking conclusion, choose one terminal outcome — FIX/REDESIGN, DISABLE/DEFER, or DECOMPOSE — rather than opening further serialized blocked/split/ordering review rounds. Every review returns two separate answers: scope correctness of the slice, and current-phase critical-path necessity. A scope pass does not place a slice on the critical path.

Phase-advancement items carry a required Phase Advancement (or explicit current-phase-mandatory exemption) section naming exact exit criteria advanced, predecessor commits/slices, successor slices, deferred debt, and the governance record (epic issue #18). Example: the executed fail-closed session-resume quarantine (issue #39) was the enabling predecessor for A0 exit criterion 4; it removed the defective prototype resume surface from the A0 path. None of the A0 exit criteria 1-12 require resume, epoch, or no-replay evidence, so those remain deferred post-A0 debt (#32) — evidentiary docs must not claim them as supported A0 evidence.

## Implemented evidence: A0 issue #64 (Step 9 — dependency-edge architecture harness)

`go test ./tests/archcheck`, `go run ./tests/archcheck`, and `make archcheck` prove the deterministic `go list -deps`-based architecture dependency-edge harness:

- **Declarative rules:** `tests/archcheck/rules.yaml` defines 6 semantic zones (pure-core, host-reachability, secure-connectivity, coordination-security, application-usecase, outer-adapter-composition), 8 dependency-edge rules (R1-R8), 2 file-level guards (R9-R10), and 5 semantic pattern checks (R11-R15).
- **12 self-tests pass:** allowed direct import, forbidden direct, transitive closure pass/fail, error naming, malformed rule fail-closed, unknown protected package, real graph passes, synthetic engine->store transitive, synthetic usecase->runtime, pure package gaining third-party dep, self non-trigger.
- **File guards:** `internal/runtime/serve.go` imports no concrete authority/store/discovery (R9). `internal/runtime/session.go` imports no libp2p host/network/peer/protocol or store (R10).
- **Semantic checks:** global dispatch symbols (`instanceRegistry`, `SetInstanceRegistry`, `HandleResumeStreamID`) absent from `internal/runtime` (R11). `internal/openauth` carries no Scope/map shape (R12). Pure L3 packages carry no Scope/map shape (R13). `internal/usecase` exposes no `*store.Store` (R14) or generic repository framework (R15).
- **Architecture effect:** `tests/verify-architecture.sh` is now a thin wrapper calling `go run ./tests/archcheck`. `Makefile` has `archcheck` target. Real repo graph passes all rules.
- **Exit criterion 11 status:** IMPLEMENTED. `go list -deps`-based harness proves documented edges. Criterion 11 is satisfied.

## A0 exit criteria 1-12 status — Step 10B reconciliation (all COMPLETE)

Each criterion row carries implementation PR/merge SHA, test evidence, architecture evidence, and distributed evidence. Distributed evidence for criteria 3, 8, 9, 10, and 12 is the real three-machine Journey-A proof (A=oripi, B=macb, R=relay.tubo.click) recorded in `A0_STEP10B_EVIDENCE.md`; criterion 12 is **not** claimed from the private-http-only integration.

| # | Criterion | Status | Implementation | Test evidence | Architecture evidence | Distributed evidence |
|---|---|---|---|---|---|---|
| 1 | No remote Pipe open bypasses successful `OpenAuthorizer`-style result before local target open | COMPLETE | #48 (openauth seam), #49/#47 (Exact Pipe Authority + Access Lease), #70 (`c031185c1cd45bf4e58fdbad16f64df2fd76999b`) | `TestExactOpenAuthorizerFailsClosedForInvalidLease` (8 cases), `TestExactOpenAuthorizerRejectsInvalidProof`, `TestExactOpenAuthorizerChecksRevocation`, `TestExactOpenAuthorizerNoRevocationState`, `TestAuthorizeOpenDenialNeverDialsTarget` (7 cases), `TestAuthorizeOpenZeroExpiryNeverOk` (internal/runtime, internal/accesslease) | Neutral `internal/openauth` contract; receive-side serve imports no concrete authority/store (R9); no target dial before authorizer success | Tampered-invite negative: `authorization_denied`, no Session, no fixture hit; valid path after negative still succeeds (`post-negative-validity.txt`) |
| 2 | Exact Pipe Authority redemption + Scope-less share produce same lease contract through L2; L2 imports no concrete L3 authority/revocation implementation | COMPLETE | #49/#47 (ExactPipeAuthority), #61/#63 (8B thin CLI + exact path), #70 (`c031185c…`) | `TestRedemptionAtomicity`, `TestExactOpenAuthorizerFailsClosedForInvalidLease` non-canonical/wrong-key cases, `TestInviteTamperedBinding`, `TestInviteNonBoundSigner`, `TestLeaseWrongSubject`, `TestLeaseWrongPipeAudience`, `TestLeaseTamperedSignature`, `TestLeaseExpired` | `internal/openauth` neutral; `tests/archcheck/rules.yaml` L2/L3 edges; openauth carries no Scope/map (R12) | Real share→connect across machines: invite redemption → canonical AccessLease → possession proof → ExactOpenAuthorizer → local target open → Session (`recipient/state-summary.txt` authorization chain) |
| 3 | Minimal signed bundle verifies trust/identity-address/version/validity/signature fail closed; zero-config Journey A resolves `relay.tubo.click` without hardcoded/temporary provider | COMPLETE | #44 (seam + bundle contract), #45 (issue) / PR #66 merge `6dd60e1c7113da726b6c39567b72da454a919e3a` (embedded production bundle), PR #67 merge `277e4366de4feb58e473308b0967d9bea9164dab` (relay no PSK) | 12 trust-material tests: `Test4SignatureVerifies`, `Test5AddressPeerIDMatches`, `Test12NoPrivateKeyEmbedded`, `TestResolverProductionBundleResolves`, bundle fail-closed set (invalid signature/wrong issuer/unpinned root/non-bootstrap role/unknown version/downgrade/not-yet-valid/expired/malformed/trailing/duplicate/oversized/identity-address mismatch), `TestOpenOverlayHostZeroConfigFailsClosed` | `internal/reachability` imports only node adapter + pure `bundle` subpackage; `tests/verify-architecture.sh`; no overlay fallback, no hardcoded relay outside bundle | config.yaml ABSENT on both A and B; relay resolved only via embedded signed bundle (`ResolveDefault`); relay health: PeerID `12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME` matching embedded material |
| 4 | Session depends on Carrier contract, not concrete libp2p stream/host/store on A0 path | COMPLETE | #28 (neutral Carrier seam) | runtime Carrier/session tests: full-duplex + exact half-close FIN ordering, `sessionPump` clean both-FIN vs forced `Reset`, mid-cancel abrupt-reset fail-closed | `internal/runtime/session.go` imports no libp2p host/network/peer/protocol or store (R10) | Real relayed byte flow across machines: B bytes_in == A bytes_out == 7341674, byte symmetry (relay/path-evidence.txt) |
| 5 | Runtime registry/stream dispatch ownership explicit; no mutable package-global dispatch state on A0 path | COMPLETE | #26/#27 (`7b6c06f`), #57 (engine) | `TestDuplicateIDRejects`, sibling registry isolation, parent cancel closes owned Sessions, concurrent close fencing (internal/runtime, race) | `instanceRegistry`/`SetInstanceRegistry`/`HandleResumeStreamID` absent from internal/runtime (R11); `NodeRuntime → PipeRuntime → SessionRegistry → Session` | Separate fresh homes on A/B, each with own runtime; Journey-D restart: new PID (1246901→1249418), same PipeID + node identity |
| 6 | Reachability testable as carrier provider without policy decision | COMPLETE | #44 | `TestProviderDirectThenFallbackInitialSelection`, `TestResolverProductionBundleResolves`, signed-bundle → Provider → Carrier → Session pump (full-duplex + half-close, in-process) | reachability type surface carries no policy/lease/scope/authorization input or output; archguard edges | Deterministic initial path selection evidenced as component-only; real topology used relay-circuit (only advertised path, both NAT'd) — environmental limitation, not direct-path claim |
| 7 | Pipe Engine composes local Service/Pipe targets; enforces loop/hop rules | COMPLETE | #57 (step 7) | Engine `TraversalState` advance/loop detection/hop-limit; Service target via injected dialer; Pipe target via injected DownstreamOpener; serve path `authorizeOpenOnly` + `Engine.Forward` | engine imports no store/discovery/authority/CLI; guarded in archcheck | Recipient ingress Pipe target = `pipe/pip_vtxitat5nt3mbparbkdds7ewa62ltqo7` composed over real tunnel session (recipient/state-summary.txt) |
| 8 | Durable Service/Pipe definition distinct from PipeRuntime/transient Session; persistence/ownership basis | COMPLETE | #59 (8A) / PR62 `7722fa6`, #61 (8B) / PR63 `75f44051` | PipeSpec/ServiceSpec carry no PID/Carrier/Session/address/lease fields (structural assert); identity survives repository restart; transient Run/Session writes never mutate definition rows, `ErrNotFound`/`persistence_failed` fail closed | consumer-owned narrow ports (no generic Repository[T]/UnitOfWork); R14 no `*store.Store` in usecase | Journey-D: clean stop + start → PipeID `pip_vtxitat5nt3mbparbkdds7ewa62ltqo7` UNCHANGED, node identity UNCHANGED, PID changed (journey-d/before.txt, after.txt) |
| 9 | CLI invokes use cases, not storage/runtime/control-plane choreography | COMPLETE | #59/#61/#63, #70 (locator wiring in internal/node + cli) | CLI contract `tests/contracts/cli.sh` PASS; `TestZeroConfigShareEmbedsCanonicalRunningOwnerLocator`, `TestShareRuntimeUnavailableEmitsNoInvite` (12 cases: missing/stopped/degraded/stale/wrong-kind/malformed/mismatched/relay-only/malformed-locator/…/verified-relay-unavailable) | usecase imports no store/runtime/discovery/authority/cli/node/engine/reachability (archcheck); `make architecture` PASS | Real `tubo attach`/`tubo share`/`tubo connect` over machines; share exit 0, canonical invite length 1604, mode 0600 (owner/share-facts.txt) |
| 10 | Persistence consumers use narrow ports where moved; persistence failure fails closed | COMPLETE | #59 (narrow ports + adapters), #61/#63 | store adapter tests; explicit `runtime_unavailable` fail-closed before invite output (12 cases); `ErrNotFound` no zero-value success; `persistence_failed` propagated | R14/R15 no `*store.Store`/generic framework in usecase; persistence is adapter behind consumer ports | Tamper home created NO pipe/runtime/session (rejected before durable state, recipient/state-summary.txt); fresh homes mode 0600 state.db |
| 11 | `go list -deps`-based architecture harness proves documented edges | COMPLETE | #64 (step 9) | 12 archcheck self-tests; real graph passes; R1-R15 rules; file guards R9/R10; semantic checks R11-R15 | `go run ./tests/archcheck` deterministic; `tests/verify-architecture.sh` wrapper; Makefile `archcheck` | `make verify-local` PASS on exact head incl. archcheck; verify-local.log |
| 12 | Unit/component, in-process, local CLI, and distributed proof cover Journey A; architecture review confirms no shortcut | COMPLETE | #70 merge `c031185c1cd45bf4e58fdbad16f64df2fd76999b` (exact main proof), this acceptance slice | 20 named exact-head component tests (accesslease 6, runtime 7, cli 2, reachability 2, bundle 3) all PASS (component-tests.txt); private-http integration PASS | No-shortcut audit 12 checks PASS (no-shortcut-audit.txt): no hardcoded relay outside bundle, no unsigned fallback, no TOFU, no test provider in CLI, no old overlay config, no Scope/discovery/membership prerequisite, no user-configured authority, invite≠lease, no prototype Token on canonical path, no pre-auth target open, no debug bypasses, no fabricated relay/ownership | REAL distributed proof on exact merged main: A=oripi, B=macb, R=relay.tubo.click; small HTTP 200 body `tubo-a0-final`, 3 sequential requests 200/13B, 7 MiB (7340032 B) exact sha256 `67560affd1…51e17`, post-large OK; path=relayed both ends, byte symmetry 7341674/880; relay-side established sockets under both source IPs; tampered-invite `authorization_denied`; zero Scope/discovery/membership; Journey-D stable PipeID (A0_STEP10B_EVIDENCE.md) |

Evidence root: `/tmp/a0-10b-final/evidence` (manifest.txt, 37 files with sha256). No criterion redefinition: criteria text is the ROADMAP A0 exit-criteria list verbatim.

## Dependency-check plan

A0 Step 9 implemented the plan: a small deterministic Go program (`tests/archcheck/`) runs `go list -deps` for each protected package and checks every resolved non-stdlib dependency against declarative YAML rules. It tests semantic boundaries, remains deterministic, and avoids dependency on exact file layout. No heavyweight framework.

## Shared safety expectations

Bound wire inputs before allocation; test failed persistence fail-closed behavior; preserve half-close; never replay application bytes after successful open; redact Service targets and secrets; use isolated state for distributed tests. Update canonical docs and evidence with behavior changes.
