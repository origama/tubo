# Prototype behavioral acceptance matrix

**Prototype reference evidence:** This matrix records behavioral/security knowledge for `prototype-v2-final`. It is not canonical A0–A4 architecture roadmap or promise of internal compatibility. Future architecture acceptance is [`ARCHITECTURE_ACCEPTANCE.md`](ARCHITECTURE_ACCEPTANCE.md); retain relevant negative/failure scenarios unless explicitly superseded.

Implementation status is tracked in GitHub Issues, not here.

## A0 exit criteria 1-12 status (Step 10B reconciliation — all COMPLETE)

Canonical A0 architecture acceptance lives in [`ARCHITECTURE_ACCEPTANCE.md`](ARCHITECTURE_ACCEPTANCE.md). This mirror table records the final A0 status with implementation PR/SHA, test evidence, architecture evidence, and distributed (three-machine, real `relay.tubo.click`) evidence. Criterion #12 is satisfied by the real distributed proof, not the private-http-only integration. Evidence root: `/tmp/a0-10b-final/evidence`; sanitized summary: `A0_STEP10B_EVIDENCE.md`.

| # | Criterion | Status | Implementation | Test / architecture evidence | Distributed evidence |
|---|---|---|---|---|---|
| 1 | No remote Pipe open bypasses successful `OpenAuthorizer`-style result before local target open | COMPLETE | #48 (openauth seam), #49/#47 (Exact Pipe Authority + Access Lease), #70 merge `c031185c1cd45bf4e58fdbad16f64df2fd76999b` | `TestExactOpenAuthorizerFailsClosedForInvalidLease` (8), `TestExactOpenAuthorizerRejectsInvalidProof`, `TestExactOpenAuthorizerChecksRevocation`, `TestExactOpenAuthorizerNoRevocationState`, `TestAuthorizeOpenDenialNeverDialsTarget` (7), `TestAuthorizeOpenZeroExpiryNeverOk`; receive-side serve imports no concrete authority/store (R9) | Tampered invite → `authorization_denied`, no Session, no fixture hit; valid path re-run after negative succeeds |
| 2 | Exact Pipe Authority redemption + Scope-less share same lease contract via L2; L2 imports no concrete L3 | COMPLETE | #49/#47, #61/#63 (8B), #70 | `TestRedemptionAtomicity`, `TestLeaseWrongSubject/WrongPipeAudience/TamperedSignature/Expired`, `TestInviteTamperedBinding`, `TestInviteNonBoundSigner`; openauth neutral, R12 | Invite redemption → canonical AccessLease → possession proof → ExactOpenAuthorizer → local target open → Session on real machines |
| 3 | Signed bundle verifies trust/identity/version/validity/signature fail closed; zero-config `relay.tubo.click` resolution | COMPLETE | #44; PR #66 merge `6dd60e1c7113da726b6c39567b72da454a919e3a`; PR #67 merge `277e4366de4feb58e473308b0967d9bea9164dab` (relay no PSK) | 12 trust-material tests incl. `Test4SignatureVerifies`, `Test5AddressPeerIDMatches`, `Test12NoPrivateKeyEmbedded`, `TestResolverProductionBundleResolves`; reachability imports only node + bundle | config.yaml ABSENT both machines; relay resolved only via embedded signed bundle; relay PeerID `12D3KooWM2hGwzrJYGyjUwtzrzLZuPWAmjiG8JeNM5YYJNPeHLME` |
| 4 | Session depends on Carrier contract, not libp2p stream/host/store | COMPLETE | #28 | Carrier full-duplex + exact half-close tests; session.go import guard R10 | Real relayed byte flow: B bytes_in == A bytes_out == 7341674 (symmetry) |
| 5 | Explicit runtime ownership; no package-global dispatch state | COMPLETE | #26/#27 (`7b6c06f`), #57 | Ownership/cancellation/fencing tests (race); R11 semantic check (instanceRegistry absent) | Fresh homes on A/B; Journey-D restart new PID, same PipeID + node identity |
| 6 | Reachability testable as carrier provider without policy decision | COMPLETE | #44 | `TestProviderDirectThenFallbackInitialSelection`, bundle→Provider→Carrier→Session pump | Direct-first evidenced as component-only; real path = relay circuit (both NAT'd; environmental limitation documented, no direct-path claim) |
| 7 | Pipe Engine composes Service/Pipe targets; loop/hop rules | COMPLETE | #57 | Engine TraversalState loop/hop tests; engine import guard | Recipient ingress Pipe target = `pipe/pip_vtxitat5nt3mbparbkdds7ewa62ltqo7` composed over real tunnel |
| 8 | Durable Service/Pipe distinct from PipeRuntime/Session; persistence/ownership basis | COMPLETE | #59 (PR62 `7722fa6`), #61 (PR63 `75f44051`) | Structural asserts; identity survives repo restart; transient writes never mutate definitions | Journey-D: clean stop/start → PipeID `pip_vtxitat5nt3mbparbkdds7ewa62ltqo7` UNCHANGED, node identity UNCHANGED, PID changed |
| 9 | CLI invokes use cases, not storage/runtime choreography | COMPLETE | #59/#61/#63, #70 | `tests/contracts/cli.sh` PASS; `TestZeroConfigShareEmbedsCanonicalRunningOwnerLocator`, `TestShareRuntimeUnavailableEmitsNoInvite` (12) | Real `attach`/`share`/`connect`; share exit 0, invite len 1604, mode 0600 |
| 10 | Persistence consumers use narrow ports; failure fails closed | COMPLETE | #59, #61/#63 | Store adapters; `runtime_unavailable` fail-closed before invite; R14/R15 | Tamper home rejected before durable state; state.db 0600 |
| 11 | `go list -deps` architecture harness proves edges | COMPLETE | #64 | 12 archcheck self-tests; R1-R15; `make architecture` PASS | `make verify-local` PASS on exact head (verify-local.log) |
| 12 | Unit/component, in-process, local CLI, and distributed proof cover Journey A; no shortcut | COMPLETE | #70 merge `c031185c…` + this acceptance slice | 20 named exact-head component tests PASS; private-http integration PASS; no-shortcut audit (12 checks) PASS | REAL distributed proof: A=oripi, B=macb, R=relay.tubo.click; small 200/`tubo-a0-final`, 3x sequential 200/13B, 7 MiB (7340032 B) sha256 `67560affd1…51e17`, post-large OK; path=relayed both ends, byte symmetry 7341674/880; relay-side sockets; tampered-invite negative; zero Scope/discovery/membership; Journey-D stable PipeID |

Notation: all criteria text verbatim from ROADMAP A0 exit criteria; no criterion redefinition.

Legend for evidence classes:

- U: deterministic unit test;
- I: in-process or isolated integration test;
- D: Docker multi-actor scenario;
- 3M: real `tuborelay` + `oripi` + `macb` scenario.

## Ontology and lifecycle

| Behavior | Evidence |
|---|---|
| service is local and never appears in discovery | U, I, 3M |
| attach creates/reuses service plus published pipe | I, D, 3M |
| connect creates/reuses local pipe targeting exact remote `pipe_id` | I, D, 3M |
| `ps` reports pipe primary refs, never attach/connect runtime kinds | I, 3M |
| stop preserves pipe definition; rm handles live conflict | I, D |
| duplicate pipe display names require exact ID resolution | U, D, 3M |
| pipe chain detects cycles and hop-limit overflow | U, I |
| re-export without `pipe.forward` is denied | I, D, 3M |

## Transport/data plane

| Behavior | Evidence |
|---|---|
| HTTP methods, path, query, multi-value headers, streaming body | I, D, 3M |
| WebSocket upgrade is full duplex | I, D |
| raw TCP echo, 256 KiB+, concurrent sessions | I, D, 3M |
| 8 MiB transfer and half-close | I, D, 3M |
| TLS passthrough with native TLS client | I, D, 3M |
| direct preferred, relay fallback, path transition observable | I, 3M |
| no replay after application bytes flow | I |
| session resume negotiated with `session-resume-v1` capability | U, I |
| planned relay->direct switch with byte continuity | I, race |
| abrupt carrier failure triggers relay recovery with epoch+1 | I, race |
| no byte loss/duplication across path switch | I, race |
| half-close sequenced across switch (FIN after final bytes) | I |
| resume handshake proof validation rejects wrong proof/nonce/epoch/offset/peer | U, I |
| buffer exhaustion backpressures source, does not drop | I, race |
| late old-carrier bytes discarded after epoch advance | I, race |
| path events recorded for each transport transition | U, I |
| resume secret never persisted or logged | U, I |
| source pipe restart/rebind keeps stable `pipe_id` | I, D, 3M |
| relay reservation renews before expiry | I with fake clock, long 3M campaign |
| relay/network interruption marks degraded, serves verified cache, clears stale dial backoff, and self-heals without restart | U, I, 3M |
| DCUtR hole punching enabled with 5s direct dial timeout | I |
| AutoNATv2 auto reachability detection | I |
| QUIC-v1 listen enabled alongside TCP | I |

## Scope/discovery

| Behavior | Evidence |
|---|---|
| random namespace secret protects topic/payload | U, I |
| member can discover and connect | D, 3M |
| viewer can discover but connect is denied | D, 3M |
| outsider cannot discover collaborative pipes | D, 3M |
| invite-only pipe is absent from listing but exact invite connects | D, 3M |
| relay forwards opaque records but consumer verifies | I, D, 3M |
| tampered/wrong-scope/expired/replayed record rejected | U, I |
| local remote-pipe runtime re-resolves a newer exact-pipe record every five minutes and once after a classified transient pre-`OpenOK` failure; concurrent recovery is cooldown-bounded, rejected snapshots retain the endpoint, and no post-`OpenOK` bytes replay. In-process coverage simulates publisher restart/address change; the real three-machine interruption campaign is issue #14. | U, I, race |
| complete pinned-authority 2.1 snapshot atomically reconciles verified records and trusted freshness; local publication/expiry advances metadata; delayed rollback, same-generation ambiguity, partial/cache/peer/2.0 response never deletes local/authority rows | U, I, race |
| membership-backed published/local runtimes elect one fenced periodic authority-2.1 sync owner per local state/scope; expiry takeover, stale token/PID fencing, retry suppression, outage recovery, and redacted scope status preserve cache/authority boundaries | U, I, race, 3M (`periodic-sync` pass `20260730T092006Z-83493`; `generated/e2e-three-machine/20260730T092006Z-83493/artifacts/report.json`) |
| cache-first `get pipes`: strictly-under-five-minute fresh/default and `--cached-only` reach zero overlay-host operations; exact TTL boundary stale default bounded-refreshes with verified-cache fallback; `--refresh` bypasses cooldown; scoped local/JSON rows preserve unaffected scopes | U, I, race |
| `watch pipes` emits deterministic `added`, `updated`, `expired`, `removed`, `degraded`, and `recovered` transitions from verified local state only; authorized scopes aggregate without name collapse, unauthorized cached scopes never appear, NDJSON remains one object per change, and slow stdout backpressures without an unbounded queue | U, race |
| bounded relay cache and truncated response | U, I, D |
| service local target absent from all remote record/output | U, D, 3M |

## Authority/security

| Behavior | Evidence |
|---|---|
| publish denied without authority grant | I, D, 3M |
| access lease binds exact pipe and client proof key | U, I, D |
| stolen access token without private key is rejected | I, D |
| one-time invite permits one successful redemption | I, D, 3M |
| denied redemption does not consume invite | I, D |
| access refresh exchanges online through one authority issuer; fresh leases apply only to subsequent sessions | U, I, race |
| invite/session/pipe-access/pipe-publish revocation and bounded OpenOK check | U, I, race |
| access and refresh lifetimes obey membership/publication bounds | U, I |
| malformed/oversized authority and tunnel input rejected before allocation | fuzz, I |

## Persistence/processes

| Behavior | Evidence |
|---|---|
| concurrent disjoint transactions are preserved | U, I, race |
| immediate writer reservation deterministically rejects competing zero-wait `BEGIN IMMEDIATE`; controlled deferred read-to-write upgrades return typed `SQLITE_BUSY`; concurrent/delayed authority-2.1 reconciliation preserves newer generation records/freshness; generation rollback and same-generation ambiguity fail closed; failed/canceled/lease-lost snapshots preserve prior state; authority restart preserves monotonic generation | U, I, race |
| transactional per-scope sync lease commit/renew/release fences delayed final reconciliation/failure writes; crash expiry permits one successor without token/status secret leakage | U, race |
| concurrent invite redemption has one `(jti, pipe_id)` winner; same-client retries are idempotent and competing clients are denied without raw SQLite errors; quota checks remain atomic | U, I, race |
| canceled `Store.Open` released after blocked immediate writer acquisition returns no Store and commits no migration/schema state; killed writer leaves parseable committed state | U, I |
| disk-full/read-only/lock failures return failure | I |
| detached readiness failure kills owned child and rolls back registry | I, D |
| stale PID/process identity cannot kill unrelated process | U, I |
| supervisor install/uninstall uses exact label and process identity; crash restart preserves pipe/authority identity; manual lifecycle cannot create duplicate supervised run | U, I, operator reboot/login campaign |
| `--json` remains parseable through discovery/authority progress | contract, D, 3M |
| secrets are masked and file permissions restrictive | U, I, hygiene |

## Mandatory three-machine release subset

Before the first usable release and every security/transport release:

1. private collaborative HTTP pipe through the public relay;
2. member discover/connect and viewer connect denial;
3. unlisted one-time invite from clean client state;
4. raw TCP large/concurrent/half-close;
5. TLS passthrough;
6. stable pipe ID and reconnect after origin runtime restart;
7. `ps`/`inspect` ontology and redacted logical/transport paths;
8. evidence that all test state was isolated from existing installations;
9. periodic-sync local election: split authority/publisher homes, two same-home contenders, exact crash/no-pre-expiry takeover, relay outage/recovery, and cached-only convergence (`tests/e2e/three-machine/scenarios/periodic-sync.sh`).

A checked-in scenario is not 3M completion. Required 3M evidence is a retained exact run ID and `generated/e2e-three-machine/<run-id>/artifacts/report.json` with `result: pass`.

### Current isolated three-machine evidence

- `private-http` PASS, run `20261002T103152Z-18042` (`generated/e2e-three-machine/20261002T103152Z-18042/artifacts/report.json`), on `fix/minimal-exact-readiness` (#76): relay transport on isolated port, member discovery + viewer connect denial, exact-invite connect preset on a scoped origin Pipe (this run exposed and proved the fix that the general pipe daemon must serve `/tubo/exact-redemption/1` and `/tubo/exact-renewal/1`), HTTP request fidelity, rebuilt process-ontology assertion, isolated-state cleanup. An earlier failing run (`20261002T102630Z-9495`, `pipe_unreachable: protocols not supported`) is retained as the failure record that motivated the fix.
- Durable exact-renewal operator campaign (2026-09-05): see [`THREE_MACHINE_DURABLE_RENEWAL_2026-09-05.md`](THREE_MACHINE_DURABLE_RENEWAL_2026-09-05.md). Its relay identity predates the current checkout; do not cite it as current-relay evidence without a rerun.
