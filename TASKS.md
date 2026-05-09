# TASKS.md — Implementation Tracker

> **Last updated:** 2026-05-09 16:25 UTC
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
| 5.1 | Bootstrap node configuration | ✅ | `BOOTSTRAP_PEERS` env var in service, `RELAY_PEERS` in edge |
| 5.2 | AutoNAT client/server setup | 🔲 | Determine NAT type (open, symmetric, etc.) |
| 5.3 | Relay fallback circuit dialing | ✅ | Verified end-to-end in isolated-network NAT-like Docker scenario after fixing relay public reachability config and opening tunnel streams with `network.WithAllowLimitedConn(...)` on relayed connections |
| 5.4 | Hole punching coordination | 🔲 | libp2p circuit v2 / ICE-based hole punch |
| 5.5 | Dedicated relay/bootstrap binary | ✅ | Added `tubo relay run` with relay service v2, AutoNAT service, health API, resource limits |
| 5.6 | Static AutoRelay support (service) | ✅ | Added `RELAY_PEERS`, `ENABLE_AUTORELAY`, `ENABLE_HOLE_PUNCHING`, `FORCE_REACHABILITY_PRIVATE` handling in `tubo service run` |
| 5.7 | Discovery pubsub router on public relay | ✅ | `relay` joins `/discovery/v1.0` so NAT/NAT peers can discover services via the public node only |

---

## Phase 6: Security & Auth ⏳ PARTIAL

| # | Task | Status | Notes |
|---|------|--------|-------|
| 6.1 | Bearer token auth for HTTP ingress | 🔲 | `internal/auth` scaffold exists, not wired in |
| 6.2 | Peer identity binding (token → peer ID) | 🔲 | Validate that requesting peer matches token scope |
| 6.3 | Tenant isolation | 🔲 | Multi-tenant routing with namespace separation |
| 6.4 | Rate limiting on pubsub + HTTP | 🔲 | Per-peer and global rate limits |
| 6.5 | Replay protection (nonce/timestamp) | 🔲 | Prevent announcement replay attacks |
| 6.6 | Private libp2p network (PSK) support | ✅ | Added `LIBP2P_PRIVATE_NETWORK_KEY` / `_B64` loading and host initialization in edge/service/bridge |
| 6.7 | PeerID allowlist (connection-level) | ⏳ | Added `LIBP2P_ALLOWED_PEERS` parser + `ConnectionGater` and enabled it in `tubo relay run`; remaining binaries pending |

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
| C.2 | Fix `docker-compose.yml` wrong Dockerfile ref | ✅ | Superseded by unified `deploy/Dockerfile.tubo` compose setup |
| C.3 | Deduplicate root-level docs vs `docs/` dir | ✅ | Consolidated |
| C.4 | Update README quick-start commands | ✅ | Updated to use `service` binary name |
| C.5 | Add `.gitignore` entries | ✅ | Added: compiled binaries, `*.log`, `vendor/`, IDE files |
| C.6 | Add multi-host discovery runbook | ✅ | Added `docs/discovery-multi-host.md` with LM Studio laptop + Hermes Linode scenario |
| C.7 | Extend runbook for private NAT/NAT deployment | ✅ | Added PSK private swarm, PeerID allowlist, relay/bootstrap policy, 502 taxonomy, acceptance tests |
| C.8 | Make AGENTS the canonical coding-agent entry point | ✅ | Rewrote `AGENTS.md` with mandatory workflow, gate current, and docs policy |
| C.9 | Consolidate docs under `docs/` | ✅ | Root `ARCHITECTURE.md`, `PROTOCOL.md`, `SECURITY.md` converted to redirect stubs |
| C.10 | Add canonical operability runbook | ✅ | Added `docs/OPERABILITY.md` with explicit startup/secure tunnel steps for 2+ services |
| C.11 | Improve Docker build stability defaults | ✅ | Smoke/integration paths now default to `DOCKER_BUILDKIT=0` and `COMPOSE_DOCKER_CLI_BUILD=0` |
| C.12 | Replace relay/bootstrap scaffolds with runnable binary images | ✅ | Superseded by unified `deploy/Dockerfile.tubo`; compose includes `relay` via `tubo relay run` |
| C.13 | Fix relay circuit multiaddr fallback on edge | ✅ | `tubo edge run` now builds relay path as `/p2p/<relay>/p2p-circuit/p2p/<target>` |
| C.14 | Document tested 3-host NAT/NAT runbook | ✅ | `docs/OPERABILITY.md` now includes tested flow: laptop LM Studio + edge host + public relay + extra service onboarding |
| C.15 | Fix upstream error frame handling | ✅ | Edge now reads service `Error` frames without blocking, so unavailable targets return a 502 instead of hanging |
| C.16 | Relay startup command hints + richer component logs | ✅ | Relay emits edge/service startup commands; runtime binaries log config, peer connections, proxy/stream lifecycle |
| C.17 | Fix discovery expiry event on `/services` polling | ✅ | `Cache.Count()` emits expiry callbacks so auto-routes are removed when services expire |
| C.18 | Fix empty request-body final chunk | ✅ | GET/empty-body requests send a final body chunk so service streams do not hang |
| C.19 | Extract edge runtime from `main.go` | ⏳ | Introduced `internal/app/edge` and thin `tubo edge run` entrypoint with initial tests; relay-first base path is now verified again after direct-fallback latency fix, so refactoring can continue once relay large-body streaming is stabilized |
| C.20 | Add NAT-like isolated Docker repro for relay-first traffic | ✅ | Added `docker-compose.nat.yml`, `tests/smoke-compose-relay-nat.sh`, and integration coverage to simulate edge/service on separate private networks with a public relay |
| C.21 | Reproduce relay v2 negotiation failure under NAT-like isolation | ✅ | Reproduced and fixed in two steps: (1) relay must run with public reachability so circuit v2 hop is actually enabled, (2) relayed tunnel streams must be opened with `network.WithAllowLimitedConn(...)`; isolated-network smoke now passes end-to-end |
| C.22 | Simplify CLI/runtime startup interface | ✅ | Added `tubo` unified CLI with role subcommands, YAML config/env/flag merge, keygen/id/config/doctor/init/topology commands, shared service/relay/bridge runtime packages, docs, Dockerfile, compose updates, and unit/smoke coverage |
| C.23 | Fix NAT/relay direct-first latency tax | ✅ | Edge direct stream attempts now use a short configurable timeout (`EDGE_DIRECT_STREAM_TIMEOUT`, default `750ms`) before relay fallback; isolated-network relay requests dropped from ~10s to sub-second latency |
| C.24 | Investigate relayed large-body stream resets under load | ✅ | Fixed via merged issue #4 work: partial frame writes now flush fully, relay limits were raised, edge retries bounded transient pre-response failures, and compose + real Linode mixed/large-payload validation are green |
| C.25 | Promote NAT/relay stress scenarios into stable acceptance coverage | ⏳ | Keep `TestRelayNATMixedTrafficStress` and `TestRelayNATTrafficDuringServiceRestart`; enable CI gating only after C.24 is fixed so relay streaming load tests become trustworthy regression coverage |
| C.26 | Add private-overlay multi-service acceptance scenario | ✅ | Added `docker-compose.private-overlay-multi-service.yml` plus `tests/smoke-compose-private-overlay-multi-service.sh` to validate one relay, one edge + curl client, and three isolated service nodes on the same private libp2p overlay with Host-based routing over a single edge endpoint |
| C.27 | Document future protocol/reverse-proxy planning | ✅ | Added planning notes for HTTPS/TCP/UDP support, comparison with similar tunneling projects, and edge reverse-proxy route control |
| C.28 | Fix topology render missing bootstrap/relay peers | ✅ | `tubo topology render` now resolves `relay: <name>` into `/p2p/<peer_id>` and populates `network.bootstrap_peers` + `network.relay_peers` for edge/service configs; added regression test in `cmd/tubo/main_test.go` |
| C.29 | Add 2-host distributed smoke testbench | ✅ | Added `tests/smoke-distributed-two-host.sh` + docs; verified end-to-end with edge on `172.236.202.99`, relay on `172.232.189.160`, service + dummy origin co-hosted remotely but loopback-bound to force `connection_path=relayed` |
| C.30 | Scaffold Terraform Linode multi-region distributed testbench | ✅ | 3-node Linode bench is live and validated: Terraform stack, smoke harness, runbook, perf runner, and saved benchmark artifacts are all in place |
| C.31 | Run distributed failure campaign on 2-host bench | ✅ | Ran real failure campaign on the 2-host bench and documented results in `docs/FAILURE_CAMPAIGN_TWO_HOST_2026-04-29.md`; key findings: relay restart can wedge relay-first traffic, edge restart alone does not recover it, service restart recovery can take ~2 minutes, and dead services remain routable for ~30s |
| C.32 | Fix relay restart wedge on relay-first traffic | ⏳ | Partial progress: edge now drops stale limited conns and retries stream open for transient relay errors; service now maintains explicit circuit-v2 reservations and republishes once relay-ready, but full relay-restart failure-campaign revalidation is still pending because the distributed bench harness remains flaky |
| C.33 | Reduce slow service restart recovery on relay-first path | ✅ | Fixed enough to make `RUN_INTEGRATION=1 go test -count=1 -run TestRelayNATTrafficDuringServiceRestart -v ./tests/integration` pass: service now republishes dynamic announcements only when relay reservation is ready, and edge retries transient `NO_RESERVATION` / dial-backoff stream-open failures |
| C.34 | Reduce stale-route window after service loss | ⏳ | Implemented: edge discovery cache now honors per-announcement TTL and runs cleanup every 1s instead of 15s; still need explicit post-fix distributed-bench revalidation of the observed stale-route window |
| C.35 | Harden distributed bench pid/process management | ✅ | Done: bench teardown now clears stale pidfiles/listeners, distributed smoke passes, and the failure campaign runs repeatably without manual cleanup |
| C.36 | Preserve repeatable NAT/relay performance baselines | ⏳ | In progress: add saved per-run performance reports for both compose and distributed 2-host benches so regressions/improvements can be compared over time |
| C.37 | Define versioning and compatibility policy | ✅ | Added `docs/VERSIONING.md` and linked it from `README.md`, `AGENTS.md`, and `docs/README.md`; policy uses one product version for the whole `tubo` binary plus separate `protocol major.minor` compatibility version |
| C.38 | Add basic release artifacts and manual release flow | ✅ | Added root `VERSION`, `CHANGELOG.md`, and `docs/RELEASING.md`, then exercised the flow by cutting and publishing release `v0.1.1` |
| C.39 | Expose product/protocol version metadata in `tubo` | ✅ | Added `internal/version`, `tubo version`, protocol debug endpoints, and negotiation visibility; release artifacts now carry the correct metadata and issue #16 is closed |
| C.40 | Add protocol 1.1 hello handshake with legacy fallback | ✅ | Added protocol 1.1 hello frame carrying `major.minor`, role, and capabilities; edge/bridge now prefer `/p2p-tunnel/1.1` and fall back to `/p2p-tunnel/1.0`, service accepts both, and real Linode multi-host smoke validated both the handshake logs and protocol debug endpoints |
| C.41 | Add real Linode mixed-version compatibility harness | ✅ | Added `tests/smoke-terraform-linode-mixed-version.sh` and validated it on the real Linode multi-host bench against legacy ref `c9bbb1f`: current edge -> legacy service (`/p2p-tunnel/1.0` fallback), legacy edge -> current service (current service accepts legacy), and current edge -> current service (`/p2p-tunnel/1.1` hello negotiation) |
| C.42 | Signed public onboarding + CLI UX simplification (PR #68 follow-up) | ✅ | Implemented, merged to `main`, and released in v0.5.1; issues #69 #71 #72 #73 #74 are closed, PR #68 is closed, the public bundle and CLI UX are live, and real Linode validation passed end-to-end |
| C.43 | Issue #77 — overlay/cluster/namespace config resource model (Phase 1) | ✅ | Done: added local resource model + legacy `network:` compatibility; verified with `go test ./...` and `./tests/smoke-compose.sh`; manual temp run confirmed new + legacy YAML fields stay in sync |
| C.44 | Issue #78 — local resource CLI for overlays/clusters/namespaces (Phase 2a) | ✅ | Done: added `get`/`describe`/`use` local resource commands on top of #77 without touching runtime behavior; verified with `go test ./...`, `./tests/smoke-compose.sh`, and manual temp config run |
| C.45 | Issue #79 — capability foundation (signed membership/service claim/connect) | ✅ | Done: added cryptographic capability primitives and deterministic sign/verify helpers without wiring them into discovery/runtime yet; verified with `go test ./...` |
| C.46 | Issue #80 — CLI resource creation for clusters and namespaces | ✅ | Done: added local `create cluster/...` and `create namespace/...` flows with local authority keypair + membership capability persistence; verified with `go test ./...` and `./tests/smoke-compose.sh` |
| C.47 | Issue #81 — cluster invitations and local join flow | ✅ | Done: added local share/join flows for cluster membership invites on top of #77/#78/#79/#80; verified with `go test ./...` and `./tests/smoke-compose.sh` |
| C.48 | Issue #82 — scoped service identity and namespace-aware service commands | ✅ | Done: added `service/<name>` parsing plus current cluster/namespace resolution for `attach`, `connect`, `get`, `describe`, and `inspect`; verified with `go test ./...` and `./tests/smoke-compose.sh` |
| C.49 | Issue #85 — overlay resource join syntax and legacy overlay compatibility | ✅ | Done: added explicit overlay join forms for public/manual overlays while keeping legacy manual join and default public join compatibility; verified with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.50 | Issue #86 — Discovery V2 runtime on opaque namespace topics | ✅ | Done: added cluster-aware topic selection, V2 publish/subscribe path, and integration coverage for cluster-mode discovery/proxying; verified with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.51 | Issue #87 — harden Discovery V2 validation and replay protection | ✅ | Done: added topic/auth checks, authority-backed capability validation, optional service-claim validation, bounded nonce replay protection, and invalid-message tests; verified with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.52 | Issue #88 — service-claim lifecycle for namespace-scoped service publishing | ✅ | Done: added local `create service/<name>`, deterministic namespace-scoped service IDs, signed service-claim persistence, cluster-mode `attach`/Discovery V2 claim loading, and service claim validation keyed by `service_id`; verified with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |

---

## Packages Without Tests ⚠️

The following packages have no `_test.go` files yet:

- `tubo edge run` — integration tests needed (Phase 7.2)
- `tubo service run` — integration tests needed (Phase 7.2)
- `cmd/tubo` — integration tests needed (Phase 7.2)
- `internal/auth` — scaffold only, not wired in anywhere
- `internal/observability` — logging/metrics setup
- `internal/bridge/proxy.go` — unclear if still used

---

## Next Priority (simplified)

### Now

1. **Issue #12 / C.36 — repeatable performance baselines**: continuare a salvare benchmark confrontabili, soprattutto sul bench Linode (`performance`, `area:testbench`, `area:linode`)
2. **Issue #11 / C.25 — stable CI coverage for NAT/relay stress**: promuovere gli stress test a coverage stabile dopo gli ultimi fix runtime (`test`, `area:testbench`)
3. **Issue #5 / C.32 — relay restart recovery**: far riprendere in modo affidabile il traffico relay-first dopo restart del relay (`bug`, `area:relay`, `prio:high`)
4. **Issue #6 — stale relay circuit/backoff state**: pulire stato stale su edge dopo disruption del relay (`bug`, `area:edge`, `area:relay`, `prio:high`)
5. **Issue #9 — malformed security handshake after restarts**: capire e correggere gli errori intermittenti post-restart (`bug`, `security`, `area:protocol`, `investigation`, `prio:high`)

### Next

5. **Issue #12 / C.36 — repeatable performance baselines**: continuare a salvare benchmark confrontabili, soprattutto sul bench Linode (`performance`, `area:testbench`, `area:linode`)
6. **Issue #11 / C.25 — stable CI coverage for NAT/relay stress**: promuovere gli stress test a coverage stabile dopo gli ultimi fix runtime (`test`, `area:testbench`)

### Later

8. **Issue #23 — release workflow v1 automation**: script di bump/tag/release per rendere meccaniche le prossime release (`release`, `planning`)
9. **Issue #24 — shared public relay security/capacity model**: documentare bene security, abuse resistance e sizing per relay pubblici su swarm condivisa (`docs`, `security`, `area:relay`)
10. **Versioning/release maintenance**: keep release workflow/docs in sync with the current `v0.5.1` state

### Keep on radar (not yet mapped to an issue here)

- **Cross-cutting — Architecture**: riprendere il deepening di `internal/app/edge` e completare il refactor del runtime
- **Cross-cutting — CLI UX**: semplificare la superficie CLI/config di avvio componenti
- **Phase 6 — Security**: estendere allowlist PeerID a edge/service/bridge + enforcement `ServiceName -> PeerID`
- **Phase 5.2 — AutoNAT**: completare diagnostica reachability + client/server setup
- **Phase 7.2 — Integration tests**: aggiungere acceptance test su PSK/allowlist/announcement invalidi
