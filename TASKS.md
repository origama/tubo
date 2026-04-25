# TASKS.md — Implementation Tracker

> **Last updated:** 2026-04-24  
> **Status legend:** ✅ Done | ⏳ In progress | 🔲 Not started | ❌ Broken/needs fix

---

## Phase 1: Wire Protocol (Binary Framing)

| # | Task | Status | Notes |
|---|------|--------|-------|
| 1.1 | Frame types & constants (`types.go`) | ✅ | RequestHeader, ResponseHeader, BodyChunk, Error defined |
| 1.2 | Varint encoding/decoding (`framing.go`) | ✅ | `EncodeFrame()` / `DecodeFrame()` with varint length prefix |
| 3.3 | StreamReader / StreamWriter (`stream.go`) | ✅ | High-level read/write over libp2p streams |
| 1.4 | Unit tests (`protocol_test.go`) | ✅ | Encode→decode roundtrip, multi-chunk streaming, error frames |

---

## Phase 2: Discovery via Pubsub

| # | Task | Status | Notes |
|---|------|--------|-------|
| 2.1 | Announcement struct + signing (`announcement.go`) | ✅ | Ed25519 signature verification implemented |
| 2.2 | PubSubSubscriber with topic filtering (`discovery.go`) | ✅ | Filters announcements, validates signatures, emits events |
| 2.3 | ServiceEntry cache with TTL eviction (`cache.go`) | ✅ | Fixed: switched to `sync.Mutex` (was deadlock-prone RWMutex) |
| 2.4 | Cache cleanup goroutine | ✅ | Periodic expiry + heartbeat renewal |
| 2.5 | Unit tests (`discovery_test.go`) | ✅ | All 10 tests passing (fixed peer.ID comparison bug, timing race) |

---

## Phase 3: Edge Gateway (HTTP Ingress)

| # | Task | Status | Notes |
|---|------|--------|-------|
| 3.1 | HTTP server scaffold + healthz endpoint | ✅ | Basic `http.Server` with `/healthz` returns 200 |
| 3.2 | Routing: hostname+path → peer_id lookup | ✅ | `internal/routing` exists but untested; not wired into gateway |
| 3.3 | Discovery integration (subscribe + cache) | ✅ | No pubsub subscriber in edge-gateway yet |
| 3.4 | Stream opening to resolved peer | ✅ | Need dial logic with relay fallback |
| 3.5 | HTTP → protocol framing forwarding | ✅ | Use `internal/forwarding` + `protocol.StreamWriter` |
| 3.6 | Protocol framing → HTTP response forwarding | ✅ | Read frames from stream, reconstruct HTTP response |
| 3.7 | Admin API (OpenAPI spec) | ✅ | List services, view peers, health dashboard |

---

## Phase 4: Connector Agent (Service-Agent)

| # | Task | Status | Notes |
|---|------|--------|-------|
| 4.1 | libp2p host creation with seed key | ✅ | `internal/p2p.NewHost()` works |
| 4.2 | Pubsub announcement publishing | ✅ | Logic exists in discovery package; not yet called from service-agent main |
| 4.3 | Heartbeat / lease renewal loop | ✅ | Periodic re-announcement to keep cache entries alive |
| 4.4 | Stream handler registration (`/p2p-tunnel/1.0`) | ✅ | `HandleServiceStream` implemented in `internal/p2p` |
| 4.5 | Protocol framing → HTTP request reconstruction | ✅ | Read frames, build `*http.Request`, forward to origin |
| 4.6 | HTTP response → protocol framing | ✅ | Capture response, write ResponseHeader + BodyChunk frames |
| 4.7 | Localhost/unix socket forwarding | ✅ | Configurable target (HTTP or unix socket) |
| 4.8 | Debug/health HTTP endpoints | ✅ | `/debug/pprof`, `/health` exposed |

---

## Phase 5: NAT Traversal & Relay

| # | Task | Status | Notes |
|---|------|--------|-------|
| 5.1 | Bootstrap node configuration | 🔲 | Hardcoded or config-driven bootstrap peers |
| 5.2 | AutoNAT client/server setup | 🔲 | Determine NAT type (open, symmetric, etc.) |
| 5.3 | Relay fallback circuit dialing | ✅ | When direct dial fails, route through relay |
| 5.4 | Hole punching coordination | 🔲 | libp2p circuit v2 / ICE-based hole punch |

---

## Phase 6: Security & Auth

| # | Task | Status | Notes |
|---|------|--------|-------|
| 6.1 | Bearer token auth for HTTP ingress | 🔲 | `internal/auth` scaffold exists, not wired in |
| 6.2 | Peer identity binding (token → peer ID) | 🔲 | Validate that requesting peer matches token scope |
| 6.3 | Tenant isolation | 🔲 | Multi-tenant routing with namespace separation |
| 6.4 | Rate limiting on pubsub + HTTP | 🔲 | Per-peer and global rate limits |
| 6.5 | Replay protection (nonce/timestamp) | 🔲 | Prevent announcement replay attacks |

---

## Phase 7: Testing & CI/CD

| # | Task | Status | Notes |
|---|------|--------|-------|
| 7.1 | Unit tests for all packages | ⏳ | protocol ✅, discovery ✅, routing ❌, forwarding ❌, auth ❌ |
| 7.2 | Integration tests (multi-node scenarios) | 🔲 | Test full request flow: client → edge → connector → origin |
| 7.3 | E2E docker-compose test suite | 🔲 | Spin up all services, run curl tests, verify responses |
| 7.4 | CI pipeline (GitHub Actions) | 🔲 | Build + test on push/PR |

---

## Cross-Cutting / Technical Debt

| # | Task | Status | Notes |
|---|------|--------|-------|
| C.1 | Fix `docs/README.md` broken links | 🔲 | References cli.md, operability.md, testing.md — don't exist |
| C.2 | Fix `docker-compose.yml` wrong Dockerfile ref | 🔲 | References `Dockerfile.connector`, actual is `Dockerfile.service-agent` |
| C.3 | Deduplicate root-level docs vs `docs/` dir | 🔲 | ARCHITECTURE.md, PROTOCOL.md, SECURITY.md exist in both places |
| C.4 | Update README quick-start commands | 🔲 | References `./connector` but binary is `service-agent` |
| C.5 | Add `.gitignore` entries | 🔲 | Missing: `*.log`, `vendor/`, IDE files, config secrets |

---

## Next Priority (What to work on next)

1. **Phase 3 — Edge Gateway**: Wire routing + discovery into the gateway so it can actually resolve and forward requests
2. **Phase 4.2-4.3** — Connector announcement publishing + heartbeat loop
3. **Cross-cutting C.2-C.4** — Fix broken docs/commands before they cause confusion
