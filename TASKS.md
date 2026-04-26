# TASKS.md — Implementation Tracker

> **Last updated:** 2026-04-26 21:37 UTC  
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
| 3.4 | Stream opening to resolved peer | ✅ | Direct dial with relay fallback (`tryRelayFallback`) |
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
| 5.3 | Relay fallback circuit dialing | ✅ | When direct dial fails, route through relay peers |
| 5.4 | Hole punching coordination | 🔲 | libp2p circuit v2 / ICE-based hole punch |

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

---

## Phase 7: Testing & CI/CD ⏳ PARTIAL

| # | Task | Status | Notes |
|---|------|--------|-------|
| 7.1 | Unit tests for all packages | ✅ | protocol (12) + discovery (10) + routing (14) + forwarding (3) = 39 tests passing |
| 7.2 | Integration tests (multi-node scenarios) | ⏳ | Added `tests/integration` with auto-discovery/proxy, large-body streaming, lease expiry, hop-by-hop header stripping (`RUN_INTEGRATION=1`) |
| 7.3 | E2E docker-compose test suite | ⏳ | Added `tests/smoke-compose.sh` and wired smoke run in `.github/workflows/ci.yml` (`smoke-compose` job) |
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

1. **Phase 7.2 — Integration tests**: Go integration tests in `tests/integration` matching AGENTS hard-gate scenarios
2. **Phase 7.3 — Compose E2E hardening**: Add relay/bootstrap profiles and run smoke in CI
3. **Phase 5.2 — AutoNAT**: Client/server setup for NAT type detection
4. **Phase 6 — Security**: Peer allowlist + ServiceName->PeerID authz enforcement
