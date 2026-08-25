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


## Implemented evidence: A0 issue #44 (L1 Reachability + signed bootstrap bundle)

`go test ./internal/reachability/...`, `go test ./internal/runtime -run 'TestReachabilityBundleCarrierThroughSession|TestOpenOverlayHostZeroConfigFailsClosed'`, `go test -race ./internal/runtime ./internal/reachability/...`, and the deterministic architecture guard (`make architecture`) prove the L1 Reachability seam and the bounded signed bootstrap-bundle contract:

- Bundle verifier (`internal/reachability/bundle`) fails closed on: invalid signature, wrong issuer, unpinned/unknown trust root, non-bootstrap (authority) root role, unknown version, version downgrade below the minimum supported, no-grace not-yet-valid/expired validity, malformed, trailing bytes, duplicate addresses, oversized (whole-bundle and per-field bounds, decode-before-allocation), and identity/address mismatch. Canonical signed-byte determinism is pinned. No TOFU: a public bundle references a pinned root by key ID and can never introduce a root.
- Reachability seam (`internal/reachability`) returns a `node.Carrier` from a signed test bundle / injected `peer.AddrInfo`; path selection is direct-first with relay-fallback, initial-only (no mid-session migration). Its type surface carries no policy/lease/scope/authorization input or output.
- In-process integration: signed bundle → Provider → `node.Carrier` → existing Session pump carries full-duplex bytes with exact half-close (no external network).
- Fail-closed zero-config: with no production trust material, zero-config resolution and the `openOverlayHost` seam return the deterministic `ErrNoBootstrapBundle` with no overlay fallback, hardcoded address, or temporary provider.

Architecture effect: `internal/reachability` imports only the L1 host adapter (`internal/node`) and its own pure `bundle` subpackage; it must not import runtime/authority/discovery/store/CLI, and the pure bundle subpackage must not import the node adapter or any runtime/authority/store/discovery/CLI package (checked by `tests/verify-architecture.sh`). `openOverlayHost`/`runPublishedPipe` route carrier acquisition through the seam via `relayOrBootstrap`; user overlay flows unchanged.

**Criteria status (binding):** criterion 6 (Reachability testable as carrier provider without policy decision) is implemented. Criterion 3 (zero-config Journey A resolves `relay.tubo.click` via verified bundle) is **structural only**: the contract, verifier, and fail-closed zero-config behavior are implemented, but production trust material (relay peer ID/stability, multiaddrs/DNS, a dedicated bootstrap signing root distinct from any authority key, bundle version, validity policy) is deliberately absent and tracked as a blocker (issue #45). Journey A production reachability and any distributed real-relay proof are **not claimed** (ROADMAP steps 10).

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

A0 proof establishes definition/runtime/session separation and persistence/ownership foundation. It does not promise all prototype persistence or recovery behavior.

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

## Dependency-check plan

A0 defines actual packages first. Then small reviewed script invokes `go list -deps` and rejects explicit forbidden internal edges. It must test semantic boundaries, remain deterministic, and avoid dependency on exact file layout. No heavyweight framework.

## Shared safety expectations

Bound wire inputs before allocation; test failed persistence fail-closed behavior; preserve half-close; never replay application bytes after successful open; redact Service targets and secrets; use isolated state for distributed tests. Update canonical docs and evidence with behavior changes.
