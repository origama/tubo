# TASKS.md — Implementation Tracker

> **Last updated:** 2026-04-28 13:42 UTC
> **Status legend:** ✅ Done | ⏳ In progress | 🔲 Not started | ❌ Broken/needs fix

---

## Phase 1: Wire Protocol (Binary Framing) ✅ COMPLETE

| # | Task | Status | Notes |
|---|------|--------|-------|
| 1.1 | Frame types & constants (`types.go`) | ✅ | RequestHeader, ResponseHeader, BodyChunk, Error defined |
| 1.2 | Varint encoding/decoding (`framing.go`) | ✅ | `EncodeFrame()` / `DecodeFrame()` with varint length prefix |
| 3.3 | StreamReader / StreamWriter (`stream.go`) | ✅ | High-level read/write over libp2p streams |
| 1.4 | Unit tests (`protocol_test.go`) | ✅ | Encode→decode roundtrip, multi-chunk streaming, error frames (12 tests) |

---

## Phase 2: Discovery via Pubsub ✅ COMPLETE

| # | Task | Status | Notes |
|---|------|--------|-------|
| 2.1 | Announcement struct + signing (`announcement.go`) | ✅ | Ed25519 signature verification implemented |
| 2.2 | PubSubSubscriber with topic filtering (`discovery.go`) | ✅ | Filters announcements, validates signatures, emits events |
| 2.3 | ServiceEntry cache with TTL eviction (`cache.go`) | ✅ | Fixed: switched to `sync.Mutex` (was deadlock-prone RWMutex) |
| 2.4 | Cache cleanup goroutine | ✅ | Periodic expiry + heartbeat renewal |
| 2.5 | Unit tests (`discovery_test.go`) | ✅ | All 10 tests passing (fixed peer.ID comparison bug, timing race) |

---

## Phase 3: Edge Gateway (HTTP Ingress) ✅ COMPLETE

| # | Task | Status | Notes |
|---|------|--------|-------|
| 3.1 | HTTP server scaffold + healthz endpoint | ✅ | `http.Server` with `/healthz` returns 200 |
| 3.2 | Routing: hostname+path → peer_id lookup | ✅ | Wired into gateway via `internal/routing` (longest-prefix match) |
| 3.3 | Discovery integration (subscribe + cache) | ✅ | Pubsub subscriber wired in, auto-discovery→route goroutine |
| 3.4 | Stream opening to resolved peer | ✅ | Direct dial with relay fallback (`tryRelayFallback`); direct attempt now uses short configurable timeout (`EDGE_DIRECT_STREAM_TIMEOUT`, default `750ms`) so NAT/relay requests do not stall ~10s before fallback |
| 3.5 | HTTP → protocol framing forwarding | ✅ | Uses `p2p.HandleClientRequest()` (hop-by-hop header stripping) |
| 3.6 | Protocol framing → HTTP response forwarding | ✅ | Read frames from stream, reconstruct HTTP response to client |
| 3.7 | Admin API | ✅ | `/services`, `/routes`, `POST /add_route` on admin port |

---

## Phase 4: Service-Agent ✅ COMPLETE

| # | Task | Status | Notes |
|---|------|--------|-------|
| 4.1 | libp2p host creation with seed key | ✅ | `internal/p2p.NewHost()` works |
| 4.2 | Pubsub announcement publishing | ✅ | Wired in main.go via `discovery.Publisher` |
| 4.3 | Heartbeat / lease renewal loop | ✅ | Configurable via `HEARTBEAT_INTERVAL` env var |
| 4.4 | Stream handler registration (`/p2p-tunnel/1.0`) | ✅ | `HandleServiceStream` wired in main.go |
| 4.5 | Protocol framing → HTTP request reconstruction | ✅ | In `internal/p2p/forward.go` |
| 4.6 | HTTP response → protocol framing | ✅ | Capture response, write ResponseHeader + BodyChunk frames |
| 4.7 | Localhost/unix socket forwarding | ✅ | Configurable target via `SERVICE_TARGET` env var |
| 4.8 | Debug/health HTTP endpoints | ✅ | `/healthz`, `/debug/pprof`, `/debug/peer` exposed |

---

## Phase 5: NAT Traversal & Relay ⏳ PARTIAL

| # | Task | Status | Notes |
|---|------|--------|-------|
| 5.1 | Bootstrap node configuration | ✅ | `BOOTSTRAP_PEERS` env var in service-agent, `RELAY_PEERS` in edge-gateway |
| 5.2 | AutoNAT client/server setup | 🔲 | Determine NAT type (open, symmetric, etc.) |
| 5.3 | Relay fallback circuit dialing | ✅ | Verified end-to-end in isolated-network NAT-like Docker scenario after fixing relay public reachability config and opening tunnel streams with `network.WithAllowLimitedConn(...)` on relayed connections |
| 5.4 | Hole punching coordination | 🔲 | libp2p circuit v2 / ICE-based hole punch |
| 5.5 | Dedicated relay/bootstrap binary | ✅ | Added `cmd/p2p-relay` with relay service v2, AutoNAT service, health API, resource limits |
| 5.6 | Static AutoRelay support (service-agent) | ✅ | Added `RELAY_PEERS`, `ENABLE_AUTORELAY`, `ENABLE_HOLE_PUNCHING`, `FORCE_REACHABILITY_PRIVATE` handling in `cmd/service-agent` |
| 5.7 | Discovery pubsub router on public relay | ✅ | `p2p-relay` joins `/discovery/v1.0` so NAT/NAT peers can discover services via the public node only |

---

## Phase 6: Security & Auth ⏳ PARTIAL

| # | Task | Status | Notes |
|---|------|--------|-------|
| 6.1 | Bearer token auth for HTTP ingress | 🔲 | `internal/auth` scaffold exists, not wired in |
| 6.2 | Peer identity binding (token → peer ID) | 🔲 | Validate that requesting peer matches token scope |
| 6.3 | Tenant isolation | 🔲 | Multi-tenant routing with namespace separation |
| 6.4 | Rate limiting on pubsub + HTTP | 🔲 | Per-peer and global rate limits |
| 6.5 | Replay protection (nonce/timestamp) | 🔲 | Prevent announcement replay attacks |
| 6.6 | Private libp2p network (PSK) support | ✅ | Added `LIBP2P_PRIVATE_NETWORK_KEY` / `_B64` loading and host initialization in edge/service/client-bridge |
| 6.7 | PeerID allowlist (connection-level) | ⏳ | Added `LIBP2P_ALLOWED_PEERS` parser + `ConnectionGater` and enabled it in `cmd/p2p-relay`; remaining binaries pending |

---

## Phase 7: Testing & CI/CD ⏳ PARTIAL

| # | Task | Status | Notes |
|---|------|--------|-------|
| 7.1 | Unit tests for all packages | ✅ | protocol (12) + discovery (10) + routing (14) + forwarding (3) = 39 tests passing |
| 7.2 | Integration tests (multi-node scenarios) | ⏳ | Added `tests/integration` with auto-discovery/proxy, large-body streaming, lease expiry, hop-by-hop header stripping, isolated-network relay fallback coverage (`TestRelayFallbackAcrossIsolatedNetworks`, `RUN_INTEGRATION=1`), plus NAT/relay stress scenarios (`TestRelayNATMixedTrafficStress`, `TestRelayNATTrafficDuringServiceRestart`) |
| 7.3 | E2E docker-compose test suite | ⏳ | Added `tests/smoke-compose.sh`, `tests/smoke-compose-relay-nat.sh`, and `tests/smoke-compose-private-overlay-multi-service.sh`; coverage now includes isolated-network relay traffic plus a 3-service private-overlay Host-routing scenario |
| 7.4 | CI pipeline (GitHub Actions) | ✅ | `.github/workflows/ci.yml`: build + test + golangci-lint on push/PR |

---

## Cross-Cutting / Technical Debt ✅ FIXED

| # | Task | Status | Notes |
|---|------|--------|-------|
| C.1 | Fix `docs/README.md` broken links | ✅ | Fixed in README.md update |
| C.2 | Fix `docker-compose.yml` wrong Dockerfile ref | ✅ | References corrected to `Dockerfile.service-agent` |
| C.3 | Deduplicate root-level docs vs `docs/` dir | ✅ | Consolidated |
| C.4 | Update README quick-start commands | ✅ | Updated to use `service-agent` binary name |
| C.5 | Add `.gitignore` entries | ✅ | Added: compiled binaries, `*.log`, `vendor/`, IDE files |
| C.6 | Add multi-host discovery runbook | ✅ | Added `docs/discovery-multi-host.md` with LM Studio laptop + Hermes Linode scenario |
| C.7 | Extend runbook for private NAT/NAT deployment | ✅ | Added PSK private swarm, PeerID allowlist, relay/bootstrap policy, 502 taxonomy, acceptance tests |
| C.8 | Make AGENTS the canonical coding-agent entry point | ✅ | Rewrote `AGENTS.md` with mandatory workflow, gate current, and docs policy |
| C.9 | Consolidate docs under `docs/` | ✅ | Root `ARCHITECTURE.md`, `PROTOCOL.md`, `SECURITY.md` converted to redirect stubs |
| C.10 | Add canonical operability runbook | ✅ | Added `docs/OPERABILITY.md` with explicit startup/secure tunnel steps for 2+ services |
| C.11 | Improve Docker build stability defaults | ✅ | Smoke/integration paths now default to `DOCKER_BUILDKIT=0` and `COMPOSE_DOCKER_CLI_BUILD=0` |
| C.12 | Replace relay/bootstrap scaffolds with runnable binary images | ✅ | `deploy/Dockerfile.relay` and `deploy/Dockerfile.bootstrap` now build/run `cmd/p2p-relay`; compose includes `p2p-relay` service |
| C.13 | Fix relay circuit multiaddr fallback on edge | ✅ | `cmd/edge-gateway` now builds relay path as `/p2p/<relay>/p2p-circuit/p2p/<target>` |
| C.14 | Document tested 3-host NAT/NAT runbook | ✅ | `docs/OPERABILITY.md` now includes tested flow: laptop LM Studio + edge host + public relay + extra service onboarding |
| C.15 | Fix upstream error frame handling | ✅ | Edge now reads service `Error` frames without blocking, so unavailable targets return a 502 instead of hanging |
| C.16 | Relay startup command hints + richer component logs | ✅ | Relay emits edge/service startup commands; runtime binaries log config, peer connections, proxy/stream lifecycle |
| C.17 | Fix discovery expiry event on `/services` polling | ✅ | `Cache.Count()` emits expiry callbacks so auto-routes are removed when services expire |
| C.18 | Fix empty request-body final chunk | ✅ | GET/empty-body requests send a final body chunk so service-agent streams do not hang |
| C.19 | Extract edge-gateway runtime from `main.go` | ⏳ | Introduced `internal/app/edge` and thin `cmd/edge-gateway` entrypoint with initial tests; relay-first base path is now verified again after direct-fallback latency fix, so refactoring can continue once relay large-body streaming is stabilized |
| C.20 | Add NAT-like isolated Docker repro for relay-first traffic | ✅ | Added `docker-compose.nat.yml`, `tests/smoke-compose-relay-nat.sh`, and integration coverage to simulate edge/service on separate private networks with a public relay |
| C.21 | Reproduce relay v2 negotiation failure under NAT-like isolation | ✅ | Reproduced and fixed in two steps: (1) relay must run with public reachability so circuit v2 hop is actually enabled, (2) relayed tunnel streams must be opened with `network.WithAllowLimitedConn(...)`; isolated-network smoke now passes end-to-end |
| C.22 | Simplify CLI/runtime startup interface | ✅ | Added `tubo` unified CLI with role subcommands, YAML config/env/flag merge, keygen/id/config/doctor/init/topology commands, shared service/relay/bridge runtime packages, thin legacy wrappers, docs, Dockerfile, and unit coverage |
| C.23 | Fix NAT/relay direct-first latency tax | ✅ | Edge direct stream attempts now use a short configurable timeout (`EDGE_DIRECT_STREAM_TIMEOUT`, default `750ms`) before relay fallback; isolated-network relay requests dropped from ~10s to sub-second latency |
| C.24 | Investigate relayed large-body stream resets under load | ❌ | NAT/relay stress testing shows small traffic is stable, but mixed and `512KiB` uploads still fail with `502` / `stream reset (remote)` / `unexpected EOF`; likely in request/response streaming, framing, or stream reset/close semantics across relayed libp2p streams |
| C.25 | Promote NAT/relay stress scenarios into stable acceptance coverage | ⏳ | Keep `TestRelayNATMixedTrafficStress` and `TestRelayNATTrafficDuringServiceRestart`; enable CI gating only after C.24 is fixed so relay streaming load tests become trustworthy regression coverage |
| C.26 | Add private-overlay multi-service acceptance scenario | ✅ | Added `docker-compose.private-overlay-multi-service.yml` plus `tests/smoke-compose-private-overlay-multi-service.sh` to validate one relay, one edge + curl client, and three isolated service nodes on the same private libp2p overlay with Host-based routing over a single edge endpoint |

---

## Packages Without Tests ⚠️

The following packages have no `_test.go` files yet:

- `cmd/edge-gateway` — integration tests needed (Phase 7.2)
- `cmd/service-agent` — integration tests needed (Phase 7.2)
- `cmd/client-bridge` — integration tests needed (Phase 7.2)
- `internal/auth` — scaffold only, not wired in anywhere
- `internal/observability` — logging/metrics setup
- `internal/bridge/proxy.go` — unclear if still used

---

## Next Priority (What to work on next)

1. **C.24 — Relay large-body streaming fix**: correggere i `502` / `stream reset (remote)` / `unexpected EOF` osservati sotto stress NAT/relay su payload grandi e traffico misto
2. **Phase 7.3 — Compose E2E hardening**: promuovere lo scenario NAT/NAT relay-first in CI dopo avere stabilizzato anche gli stress test NAT/relay
3. **Cross-cutting — Architecture**: riprendere il deepening di `internal/app/edge` e completare il refactor del runtime, ora che la latenza relay-first non paga piu' il timeout direct da ~10s
4. **Cross-cutting — CLI UX**: progettare una superficie CLI/config piu' semplice per avvio componenti, riducendo il numero di env vars richieste
5. **Phase 6 — Security**: estendere allowlist PeerID a edge/service/client-bridge + enforcement `ServiceName -> PeerID`
6. **Phase 5.2 — AutoNAT**: completare diagnostica reachability + client/server setup
7. **Phase 7.2 — Integration tests**: aggiungere acceptance test su PSK/allowlist/announcement invalidi
