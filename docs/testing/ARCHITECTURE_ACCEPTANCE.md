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

## Dependency-check plan

A0 defines actual packages first. Then small reviewed script invokes `go list -deps` and rejects explicit forbidden internal edges. It must test semantic boundaries, remain deterministic, and avoid dependency on exact file layout. No heavyweight framework.

## Shared safety expectations

Bound wire inputs before allocation; test failed persistence fail-closed behavior; preserve half-close; never replay application bytes after successful open; redact Service targets and secrets; use isolated state for distributed tests. Update canonical docs and evidence with behavior changes.
