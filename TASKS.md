# TASKS.md — Implementation Tracker

> **Last updated:** 2026-05-20 06:20 UTC
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
| 7.5 | Dedicated cluster-aware compose workflow smoke | ✅ | Added `docker-compose.tubo-workflow.yml` + `tests/smoke-compose-tubo-workflow.sh`; covers create/get/describe/share/connect against a fresh cluster/namespace/service setup and now passes with namespace-scoped membership + service isolation |
| 7.6 | Issue #108 — deterministic Docker E2E harness | ✅ | Added `tests/e2e/` harness and `tests/e2e/scenarios/001-default-cluster-default-namespace`; fixed timing by waiting for Alice publication, used service share token for connect, and validated with `tests/e2e/run.sh 001-default-cluster-default-namespace` |

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
| C.53 | Issue #89 — service sharing: connect-only grants for service access | ✅ | Done: added `tubo share service/<name>` and `tubo connect --token <service-share>` for connect-only bearer grants scoped to cluster/namespace/service; verified with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.54 | Peer allowlist end-to-end across runtime binaries | ✅ | Wired `LIBP2P_ALLOWED_PEERS` into relay/edge/service/bridge host creation; added integration coverage for allowed connections and rogue-peer rejection |
| C.55 | Issue #90 — data-plane connect proof authorization | ✅ | Added protocol connect-proof frames, service-side proof verification/replay protection, bridge proof emission from connect grants, and integration coverage for valid/missing/expired/replayed/scope-mismatched proofs; verified with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.56 | Issue #91 — namespace-scoped service listing and query authorization | ✅ | Done: added namespace-aware auth for `get services`, `get service/...`, `describe`, `inspect`, and `watch`, including per-namespace capability checks, `-A` authorization, and scoped filtering; verified with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.57 | Issue #93 — remove legacy swarm discovery mode | ✅ | Done: legacy swarm discovery removed from runtime/docs; cluster/namespace discovery V2 is now the only supported path |
| C.58 | Issue #94 — namespace invite bootstrap and cross-machine discovery regression | ✅ | Resolved end-to-end after deploying/restarting the public relay on `relay.tubo.click` with the current branch binary + public swarm key: clean two-machine flow (`join`, `create cluster/namespace`, `share`, `attach`, remote `join`, `get services`) now returns the attached service from relay cache (`received 1 services`) |
| C.59 | Issue #95/#96 — Publish Grant prerequisite: mandatory ServiceClaim for Discovery V2 | ✅ | Done: Discovery V2 subscriber now requires non-empty `service_id` + valid authority-signed `ServiceClaim`, bounds cache TTL by claim expiry, and gateways reject query-protocol cache mutation; added adversarial unit tests for missing/expired/wrong-authority/wrong-peer/wrong-service claims plus runtime integration coverage for rejecting a claimless service; verified with `go test ./...`, `SMOKE_FORCE_BUILD=1 ./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.60 | Issue #95/#97 — Persist stable service identity for attach | ✅ | Done: `tubo attach` now materializes/reuses scoped service identity before service runtime startup, generates `service_seed` once instead of falling back to demo/ephemeral seeds in cluster mode, derives the service peer before membership/claim handling, and preserves namespace-separated identities; verified with `go test ./...`, `SMOKE_FORCE_BUILD=1 ./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.61 | Issue #95/#98 — Introduce attach authorization resolver | ✅ | Done: added attach publish authorization resolver for valid existing `ServiceClaim`, local authority minting, clear no-grant error for non-authority nodes, wrong-peer/expired claim rejection, and namespace-membership + service-claim Discovery V2 acceptance; verified with `go test ./...`, `SMOKE_FORCE_BUILD=1 ./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration` |
| C.62 | Issue #95/#99 — Local grant request store for authority nodes | ✅ | Done: added persistent atomic grant request store with pending/list/get/approve/deny/expire/reload/dedupe/corrupt-file coverage |
| C.63 | Issue #95/#100 — Publish Grant protocol message types and validation | ✅ | Done: added `/tubo/grants/1.0` message types, encode/decode, validation, TTL/payload/service-name/permission bounds, and tests |
| C.64 | Issue #95/#101 — `tubo grants serve` for Publish Grant requests | ✅ | Done: added authority-side grant service handler and `tubo grants serve`; submit/poll create and return pending store entries, bind requester PeerID from stream, reject invalid scope, dedupe duplicates, and avoid auto-signing claims |
| C.65 | Issue #95/#102 — Grants pending/approve/deny/history CLI | ✅ | Done: added local authority CLI for pending, describe, approve, deny, and history; approval signs scoped `ServiceClaim`, denial does not, expired/missing-authority cases fail clearly, and service-name collision policy rejects already-approved different peers |
| C.66 | Issue #95/#103 — Client-side grants request flow | ✅ | Done: added `tubo grants request service/<name>` submit/poll flow, stable identity derivation, grant request metadata persistence, approved claim validation/saving, and invalid/denied/expired response handling |
| C.67 | Issue #95/#104 — Wire Publish Grant into service publication command | ✅ | Done: `attach` now submits/polls saved grant routes before publication, persists pending request metadata, saves approved claims, rejects denied/expired/pending states clearly, and still supports authority-local minting or existing valid claims |
| C.68 | Issue #95/#105 — Extend cluster invite with grant-requester role | ✅ | Done: added signed `grant-requester` invites with `grant:request`, `jti`, grant service protocol/peers, join persistence, client fallback to stored grant service metadata, and tests for creation/join/tamper/expiry/no-publish-rights/request flow |
| C.69 | Issue #95/#106 — Harden Publish Grant flow | ✅ | Done: added local invite reuse tracking by `jti`, server pending limits globally/per requester/per service, active service-name collision rejection, and tests for duplicate invites, flooding bounds, and duplicate service names; documented existing payload size/name restrictions and denial policy |
| C.70 | Issue #95/#107 — Relay-aware Grant Service without discovery | ✅ | Done: added shared overlay host/reachability helper, wired `grants serve` and grant clients to configured bootstrap/relay/autorelay/hole-punching/private reachability, relay reservation maintenance, relay-aware printed addresses, and tests for relayed address generation/direct-only failure plus stored invite grant metadata request flow |
| C.71 | Public attach/connect UX on the public bundle | ✅ | Done: extended `tubo-public` bundle metadata with `home/default`, cluster authority public key, and grant-service peers; added public auto-approve grant service mode, clean-config `attach`/`connect` bootstrap notes, and docs updates for the simplified share/connect flow. Fresh-config Bob connect is now exercised end-to-end in the deterministic e2e harness. |
| C.72 | Issue #120 — ConnectAccessLease/ConnectRefreshLease + bridge PoP renewal | ✅ | Done on `0.7.0.b0`: ShareInvite redemption through grant-service metadata now yields client-key-bound access/refresh leases, bridge refreshes access leases before expiry, PoP binds scope/service/access hash/nonce/JTI/issued-at, service validation rejects stolen key/hash/replay/expired proofs, and e2e `public_connect_auto_renew` passes. |
| C.73 | Issue #121 — Revocation primitives and epoch validation | ✅ | Done on `0.7.0.b0`: added issuer-side revocation store, `tubo revoke invite/session/service-access/publish`, access/publish epoch fields, grant-service checks for revoked invite/session/stale service-access epoch/publish revoke, share minting hooks, docs, unit tests, and e2e `public_revoke_invite`, `public_revoke_session`, `public_revoke_service_access`. |
| C.74 | Cross-cutting — architecture deepening review | ✅ | Done via issue #132: attach/publish authorization is now deepened into `internal/attachauth`, startup + renewal both route through it, redundant CLI-side branching was removed, and verification passed with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration`. |
| C.75 | Issue #133 — refactor `cmd/tubo/main.go` into explicit CLI/use-case modules | ✅ | Done via issue #133: extracted `internal/catalog`, `internal/processes`, `internal/connectflow`, and `internal/launcher`; reduced `cmd/tubo/main.go` to thinner CLI/config wiring; added local verification targets in `Makefile`; fixed a race surfaced by `go test -race ./...` in `internal/app/bridge`; refreshed `tests/smoke-cli-ux.sh` for cluster-aware attach/share/connect flow; and hardened `tests/e2e/run.sh all` to reset containers/workdirs between scenarios. Final validation passed with `go test ./...`, `go test -race ./...`, `go build ./...`, `./tests/smoke-compose.sh`, `./tests/smoke-cli-ux.sh`, `RUN_INTEGRATION=1 go test -v ./tests/integration`, `make e2e-default`, and `make e2e`. |
| C.76 | Issue #134 — remove legacy `topology` CLI/docs surface | ✅ | Done on `0.7.0.b0`: removed `tubo topology` and `tubo init topology`, deleted topology tests from `cmd/tubo/main_test.go`, refreshed `docs/cli.md`, `docs/README.md`, `docs/OPERABILITY.md`, and `docs/COMPARISON-TUNNELING-PROJECTS.md`, revalidated with `go test ./...`, `./tests/smoke-compose.sh`, and `RUN_INTEGRATION=1 go test -v ./tests/integration`, then commented and closed #134. |

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

1. **Issue #112 — layered security model completion**: ⏳ In progress on `0.7.0.b0`; #120 and #121 implemented; #123 is now complete; next tranche is #128 deterministic E2E, then #127 migration, #124 aliases, #125 Level 2 private namespace, #126 Level 3 private overlay (`security`, `area:service`, `area:cli`, `area:testbench`, `prio:high`)
2. **Issue #95 — Publish Grant epic review/merge prep**: subissue #96–#107 implementate su branch; prossimo step review finale PR/merge (`security`, `area:service`, `area:cli`, `prio:high`)
2. **Issue #129 — expired approved grants should not block reattach**: ✅ Done on `0.7.0.b0`; grant store now expires approved grants using their effective claim/lease expiry, collision checks ignore stale approved records, and tests cover expired vs active approved grants plus the CLI request path (`security`, `area:service`, `area:cli`, `prio:high`)
3. **Issue #130 — attach restart loses service share token UX after grant approval**: ✅ Done on `0.7.0.b0`; `attach` now makes publish-lease reuse explicit, treats an expired lease like a missing one so it re-enters the normal renew/request path, preserves the publish-lease path when a fresh grant-approved attach starts the runtime, and share-invite issuer pinning now compares authority key material instead of the full SSH authorized-key string so comment-only differences no longer break `connect --token` (`security`, `area:service`, `area:cli`, `prio:high`)
3. **Issue #119 — attach publish lease renewal / reprint**: ✅ Done on `0.7.0.b0`; attach rinnova il publish lease quando disponibile, re-stampa token validi, e i percorsi e2e `001-default-cluster-default-namespace`, `public_attach_reprint_share_token`, e `public_revoke_invite` sono verdi dopo il fix del seed/config Bob per gli scenari manual-overlay (`security`, `area:service`, `area:cli`, `prio:high`)
2. **Issue #12 / C.36 — repeatable performance baselines**: continuare a salvare benchmark confrontabili, soprattutto sul bench Linode (`performance`, `area:testbench`, `area:linode`)
3. **Issue #11 / C.25 — stable CI coverage for NAT/relay stress**: promuovere gli stress test a coverage stabile dopo gli ultimi fix runtime (`test`, `area:testbench`)
4. **Issue #5 / C.32 — relay restart recovery**: far riprendere in modo affidabile il traffico relay-first dopo restart del relay (`bug`, `area:relay`, `prio:high`)
5. **Issue #6 — stale relay circuit/backoff state**: pulire stato stale su edge dopo disruption del relay (`bug`, `area:edge`, `area:relay`, `prio:high`)
6. **Issue #9 — malformed security handshake after restarts**: capire e correggere gli errori intermittenti post-restart (`bug`, `security`, `area:protocol`, `investigation`, `prio:high`)

### Next

5. **Issue #12 / C.36 — repeatable performance baselines**: continuare a salvare benchmark confrontabili, soprattutto sul bench Linode (`performance`, `area:testbench`, `area:linode`)
6. **Issue #11 / C.25 — stable CI coverage for NAT/relay stress**: promuovere gli stress test a coverage stabile dopo gli ultimi fix runtime (`test`, `area:testbench`)

### Later

8. **Issue #23 — release workflow v1 automation**: script di bump/tag/release per rendere meccaniche le prossime release (`release`, `planning`)
9. **Issue #24 — shared public relay security/capacity model**: documentare bene security, abuse resistance e sizing per relay pubblici su swarm condivisa (`docs`, `security`, `area:relay`); `docs/OPERABILITY.md` ora esplicita che `tubo-public` supporta multi-relay ma oggi richiede un solo Grant Service autorevole per cluster/namespace per evitare split-brain su `service name`
10. **Issue #111 — grant service operability visibility/history UX**: chiarire `tubo ps` vs processi systemd e rendere evidente lo store effettivo di `tubo grants serve` / `tubo grants history` (`bug`, `docs`, `area:cli`, `area:service`, `prio:medium`)
11. **Issue #113 — security guarantees, trust roots, and non-goals**: ✅ Done on `0.7.0.b0`; aggiunti `docs/security-model-0.7.md` e `docs/obsoletes/README.md`, riallineati `docs/SECURITY.md`/`docs/README.md`/`README.md`, corretta la nota Discovery V2 in `docs/discovery-multi-host.md`, spostati in `docs/obsoletes/` i documenti di architettura alternativi superseded (`cli-ux-v2.md`, `PLAN-EDGE-REVERSE-PROXY.md`, `architecture-flat-first.html`), e verificato con `go test ./...`
12. **Issue #114 — stable service identity primitives**: ✅ Done on `0.7.0.b0`; aggiunto `internal/serviceidentity`, introdotto `service_owner_key_file` nella config locale, derivato `service_id` dalla service owner key per le identita' nuove, reso esplicito il vincolo nel flusso `attach`, aggiornati test CLI/package e `docs/cli.md`
13. **Issue #115 — PublishLease by `service_id` with service-key proof**: ✅ Done on `0.7.0.b0`; introdotte `PublishLeaseRequest`/`PublishLease` firmate dalla service owner key, re-key di grants/publish su `service_id`, fallback compatibile al legacy `ServiceClaim`, e fixture compose/e2e riallineati al nuovo modello (`service_owner_key_file` + `service_publish_lease_file`). Verificato con `go test ./...`, `./tests/smoke-compose.sh`, `RUN_INTEGRATION=1 go test -v ./tests/integration`, e `tests/e2e/run.sh 001-default-cluster-default-namespace`
14. **Issue #116 — Discovery V2 service_id-first records**: ✅ Done on `0.7.0.b0`; Discovery V2 cache/storage is keyed primarily by `service_id`, display name is metadata/compat index, announcements carry service public key + `PublishLease`, validation rejects wrong key/wrong scope/untrusted or expired leases, duplicate display names are accepted as distinct records, `get services` surfaces `service_id`, and e2e gates `public_duplicate_display_names` + `public_stolen_access_token_rejected` were added. Verificato con `go test ./...`, `./tests/smoke-compose.sh`, `RUN_INTEGRATION=1 go test -v ./tests/integration`, `tests/e2e/run.sh 001-default-cluster-default-namespace`, `tests/e2e/run.sh public_duplicate_display_names`, e `tests/e2e/run.sh public_stolen_access_token_rejected`
15. **Issue #118 — ShareInvite as service_id bootstrap token**: ✅ Done on `0.7.0.b0`; token rinominato a `tubo-share-invite-v1`, mint da publish lease valida con `share.mint`, `connect` marca/controlla la revoca locale del JTI, `share revoke` e i gate e2e `public_attach_reprint_share_token` / `public_revoke_invite` sono passati; dopo il pin single-issuer gli scenari manual-overlay ora pre-seedano Bob con la stessa config/issuer di Alice prima di `connect`, evitando il falso negativo dovuto all'auto-join pubblico su `home/default`. Verificato con `go test ./cmd/tubo`, `tests/e2e/run.sh 001-default-cluster-default-namespace`, `tests/e2e/run.sh public_attach_reprint_share_token`, e `tests/e2e/run.sh public_revoke_invite`
16. **Issue #122 — single logical issuer per scope**: ✅ Done on `0.7.0.b0`; config now pins one issuer per scope, rogue invites are rejected at connect time, docs updated, and e2e `public_single_logical_issuer` passes
17. **Versioning/release maintenance**: keep release workflow/docs in sync with the current `v0.6.0` state
14. **Release v0.6.0**: ✅ Done on `main` (tag prep, changelog/version bump, `go test ./...`, `SMOKE_FORCE_BUILD=1 ./tests/smoke-compose.sh`, `RUN_INTEGRATION=1 go test -v ./tests/integration`, and `tests/e2e/run.sh 001-default-cluster-default-namespace` all passed)

### Keep on radar (not yet mapped to an issue here)

- **Cross-cutting — Architecture**: riprendere il deepening di `internal/app/edge` e completare il refactor del runtime
- **Cross-cutting — CLI UX**: semplificare la superficie CLI/config di avvio componenti
- **Phase 6 — Security**: estendere allowlist PeerID a edge/service/bridge + enforcement `ServiceName -> PeerID`
- **Phase 5.2 — AutoNAT**: completare diagnostica reachability + client/server setup
- **Phase 7.2 — Integration tests**: aggiungere acceptance test su PSK/allowlist/announcement invalidi
