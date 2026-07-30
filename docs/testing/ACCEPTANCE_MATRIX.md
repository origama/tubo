# Behavioral acceptance matrix

This matrix defines required evidence. Implementation status is tracked in GitHub Issues, not here.

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
| source pipe restart/rebind keeps stable `pipe_id` | I, D, 3M |
| relay reservation renews before expiry | I with fake clock, long 3M campaign |
| relay/network interruption marks degraded, serves verified cache, clears stale dial backoff, and self-heals without restart | U, I, 3M |

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
| complete pinned-authority 2.1 snapshot atomically reconciles verified records and trusted freshness; local publication/expiry advances metadata; delayed rollback, same-generation ambiguity, partial/cache/peer/2.0 response never deletes local/authority rows | U, I, race |
| membership-backed published/local runtimes elect one fenced periodic authority-2.1 sync owner per local state/scope; expiry takeover, stale token/PID fencing, retry suppression, outage recovery, and redacted scope status preserve cache/authority boundaries | U, I, race, 3M (`periodic-sync` pass `20260730T092006Z-83493`; `generated/e2e-three-machine/20260730T092006Z-83493/artifacts/report.json`) |
| cache-first `get pipes`: strictly-under-five-minute fresh/default and `--cached-only` reach zero overlay-host operations; exact TTL boundary stale default bounded-refreshes with verified-cache fallback; `--refresh` bypasses cooldown; scoped local/JSON rows preserve unaffected scopes | U, I, race |
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
| concurrent/delayed authority-2.1 snapshot reconciliation preserves newer generation records and freshness; authority restart preserves monotonic generation; failed refresh enforces bounded retry deadline | U, I, race |
| transactional per-scope sync lease commit/renew/release fences delayed final reconciliation/failure writes; crash expiry permits one successor without token/status secret leakage | U, race |
| invite redemption and quota checks are atomic | U, I, race |
| killed writer leaves parseable committed state | I |
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
