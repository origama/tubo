# Changelog

**Historical prototype changelog:** Entries below describe preserved `prototype-v2-final` implementation cycle. Future A0–A4 direction is [`docs/implementation/ROADMAP.md`](docs/implementation/ROADMAP.md); do not read historical feature status as target architecture or compatibility promise.

## [Unreleased]

### Removed (A0)

- Session-resume surface removed from the A0 path (issue #39): `/tubo/tunnel-resume/2.1`, the `session-resume-v1` capability, resume/control stream roles, continuous ACK/retention/replay window, planned-switch barrier, abrupt carrier recovery with detached grace, `ResumeOffer`, and the memory-only `resume_secret`. A0 sessions are raw full-duplex authorized `/tubo/tunnel/2.0`; carrier failure terminates the session and a new authorized connection is required.
- Supersedes prototype resume work previously carried in PRs #38/#29/#33 (closed unmerged; retained only as evidence branches `issue-34-session-resume-data-plane` and `issue-28-neutral-carrier`). Session-resume/carrier-replacement correctness remains deferred post-A0 resilient-Session debt (#32).

### Added (A0)

- Neutral Carrier seam (issue #28): Session byte flow, cancellation, owner bound, and inbound exact-owner dispatch now run through a neutral `node.Carrier` contract (raw full-duplex bytes, `Close`/`Reset`/`CloseWrite` half-close, `SetDeadline`) realized at the host/L1 edge, instead of libp2p concrete host/stream/peer and `*store.Store`. The production tunnel data plane (responder `tunnelServer.serve` and initiator `openLocalConnection`) is re-grounded through an owner-bound `Session` + active `Carrier`; the direct `copyDuplex` tunnel path is deleted. Wire frames, lease-validation-before-target-open ordering, `authorizationTimer` lease-expiry close, `store.Session` telemetry, full-duplex and FIN ordering, and quarantine negative tests are preserved. `tests/verify-architecture.sh` now also enforces that the Session boundary imports no libp2p host/network/peer/protocol or store and that the host adapter has no runtime coupling (A0 exit criterion 4).

### Added (A0)

- L1 Reachability boundary (issue #44): new `internal/reachability` seam turns verified bootstrap/relay provider identity (a signed bootstrap bundle or test-injected `peer.AddrInfo`) into an acquirable dial target and a raw full-duplex `node.Carrier`. The API is transport-only by construction — it carries no policy, lease, scope, or authorization input and returns no authorization output. Initial path selection is direct-first with relay/circuit fallback, never mid-session migration. Zero-config resolution consults the embedded/preinstalled signed bootstrap bundle; with no production trust material installed it fails closed deterministically (`ErrNoBootstrapBundle`) — no overlay fallback, no hardcoded relay address, no temporary provider. Carrier acquisition in `openOverlayHost` and `runPublishedPipe` now routes through this seam (`relayOrBootstrap`); user-configured overlay flows are unchanged.
- Bounded signed public-bootstrap bundle contract (issue #44): pure `internal/reachability/bundle` verifier with strict fail-closed checks — fixed canonical big-endian binary encoding bounded (16 KiB) before allocation; supported-version and downgrade checks; dedicated bootstrap-role signing root (no TOFU, a public token cannot introduce a root, and an authority-role root is rejected); exact no-grace validity window (not-yet-valid/expired); Ed25519 signature; malformed/trailing/duplicate/oversized/identity-address-mismatch rejection. Deterministic test-only Ed25519 roots; no production key material ships.
- `internal/reachability` tests drive direct-then-fallback initial selection and a signed-bundle → Provider → `node.Carrier` → existing Session pump path (full-duplex + exact half-close) over in-process libp2p hosts, without external network.
- #43 finding 1 remediation: `sessionPump` now distinguishes clean both-FIN completion (graceful `Carrier.Close`, preserving mux FIN ordering) from error/forced termination (`Carrier.Reset`, fail-closed) at the Session/Carrier boundary; the continuity mid-cancel test now exercises the abrupt-reset fail-closed path.
- Architecture guard extended: `internal/reachability` may not import runtime/authority/discovery/store/CLI; the pure bundle subpackage may not import the node adapter or any runtime/authority/store/discovery/CLI package.

### Added

Resume-related entries immediately below are historical prototype records superseded by the quarantine above; they are not current A0 features.

- Session resume protocol `/tubo/tunnel-resume/2.1` with control and resume stream roles, authenticated via HMAC-SHA256 proof over memory-only resume secret.
- `sessionPump`: bounded, epoch-fenced, receiver-committed byte transport replacing uncoordinated `copyDuplex` for resume-capable sessions.
- Continuous ACK sender (every 64 KiB or 25 ms) with retained replay window (1 MiB per direction, 64 MiB aggregate).
- Planned switch barrier (relay ↔ direct): SwitchPrepare, Barrier, BarrierAck, SwitchAbort messages.
- Abrupt carrier recovery: epoch + 1 relay resume candidate, replay unacked only, detached grace timer (10 s).
- Resume handshake: ResumeHello, ResumeChallenge, ResumeCommit, ResumeResult, ResumeActivate, ResumeActivated.
- `current_transport_path` field on sessions and `path_events` table tracking transport transitions with epoch and committed offsets.
- DCUtR hole punching (`EnableHolePunching` with 5s `DirectDialTimeout`), AutoNATv2 auto reachability, and QUIC-v1 listen in libp2p host.
- `session-resume-v1` capability negotiation in tunnel handshake.
- `ResumeOffer` in `OpenResult` carrying resume secret, control protocol, and buffer limits.
- `tubo watch pipes [--scope …] [--interval …] [--json]`: deterministic, membership-authorized multi-scope pipe changes from verified local state with explicit `added`, `updated`, `expired`, `removed`, `degraded`, and `recovered` event types; JSON is NDJSON and slow stdout consumers backpressure polling without an event queue.
- Schema 8 transactional revocations, atomic one-time share-invite redemptions, 10-minute access leases, 48-hour online-checked refresh leases, and pipe-only invite endpoints. Refresh updates later sessions only; established tunnels remain bounded by initial access lease until a future tunnel resume/switch protocol.
- `tubo share pipe/<name> --json`, `tubo revoke invite|session|pipe-access|pipe-publish <id>`, and `connect --token <invite>` foundation.
- Greenfield repository contract for Tubo's service/pipe architecture.
- Initial domain invariants and deterministic local verification gate.
- Isolated three-machine E2E harness contract for `tuborelay`, `oripi`, and `macb`.
- Pure-Go SQLite schema 3 state store in WAL mode with atomic YAML overlay/current-scope config, local peer aliases, per-run libp2p telemetry/observed peers, and separate `0600` secrets.
- Private-swarm libp2p relay, renewable reservations/records, and relayed stream transport.
- Ed25519-signed membership invites, remote publisher grants/announcements, access leases, pipe records, and proof-of-possession tunnel handshake.
- Service/pipe creation, scope workflows, verified record discovery, detached connect runtime, path-aware deduplicated pipe inventory/inspection, local alias plus observed-peer inventory, relay-aware bandwidth/rate/connection telemetry in `ps`/`top`, logs, config/doctor diagnostics, restart/removal/stale cleanup, process registry, identity-safe stop, and explicit macOS `launchd` supervision for authority/pipes.
- Isolated local relay/publisher/member/viewer HTTP and WebSocket integration gate.
- Verified TLS 1.2+ upstream dialing for local HTTPS service targets and CLI verbosity controls that preserve JSON stdout.
- Negotiated `/tubo/authority/2.1` bounded `records.list` snapshots with persistent authority-global generation/high-water metadata, atomic reconciliation, rollback/same-generation ambiguity rejection, and no deletion fallback to authority 2.0.
- Schema 7 fenced per-scope periodic discovery-sync leases for membership-backed published/local pipe runtimes, with bounded jitter/deadline, retry suppression, guarded final writes, and redacted scope sync status. Local authority remains source-of-truth and does not self-sync.
- Isolated `periodic-sync` three-machine harness: split authority/publisher homes, same-home contender election, exact crash expiry takeover, relay recovery, cached-only convergence, and secret-artifact rejection. Three-machine campaign passed: `20260730T092006Z-83493` (`generated/e2e-three-machine/20260730T092006Z-83493/artifacts/report.json`).

### Changed
- `get pipes` is cache-first: complete pinned-authority snapshots are fresh for strictly less than five minutes, `--refresh` forces bounded refresh past retry cooldown, and `--cached-only` is socket-free. Scope-local typed freshness/error JSON and human freshness columns retain verified cached/local inventory on refresh failure.
- This codebase has no compatibility relationship with the previous Tubo implementation.
- State schema 4 adds run source and supervisor registration metadata; schema 3 upgrades in place, and older binaries reject schema 4.
- State schema 6 adds authority snapshot provenance and canonical snapshot digest. Schema-5 locally derived freshness ordering is reset transactionally without erasing valid cached records; older binaries reject schema 6. Authority snapshot protocol adds negotiated 2.1 while tunnel and existing authority operations remain 2.0.
- State schema 7 adds local fenced sync-lease persistence only; protocol remains unchanged.
- Protocol 2.0 first encoding is bounded length-prefixed JSON; raw full-duplex bytes begin after `OpenOK`.

### Fixed
- Authority-owned publication and expiry now advance persistent snapshot metadata atomically; reconciliation prunes only discovered cache rows and refresh retry deadlines prevent repeated authority dials.
- Configured relay connections now clear stale libp2p dial backoff and retry every five seconds after network interruption; authority/publisher reservations and records recover without process restart.
- Local pipe status now degrades on relay/session setup failure, `doctor` detects degraded/disconnected runtimes, and `get pipes` retains verified cached inventory during discovery outages.

### Compatibility
- Product version: `0.1.0-dev`
- Protocol version: `2.1` for negotiated authority snapshots; tunnel and existing authority operations remain `2.0`
- State schema version: `10`
- Protocol version: `2.1` for tunnel resume; tunnel open remains `2.0`
- Mixed-version behavior: schema-8/9 binaries reject schema-10 state; session resume requires both peers to advertise `session-resume-v1`; protocol 1.x is rejected
- Compatibility with previous Tubo versions: intentionally unsupported
- Rollout: back up state, deploy schema-10 binary, restart. Resume sessions work only when both peers are upgraded
- Rollback: stop schema-10 binaries and restore pre-schema-10 backup; no downgrade migration exists
- Validation: `make verify-local`; no three-machine campaign run
- Operator action: use isolated state and do not point this binary at an existing Tubo home
