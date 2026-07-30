# Changelog

## [Unreleased]

### Added
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
- State schema version: `7`
- Mixed-version behavior: schema-6 binaries reject schema-7 state; new consumers require authority 2.1 for deletion-capable `records.list`; 2.0 has no deletion fallback. Protocol 1.x is rejected
- Compatibility with previous Tubo versions: intentionally unsupported
- Rollout: back up state, deploy authority/origin with 2.1 before consumers, then deploy schema-7 consumers using isolated `TUBO_HOME`
- Rollback: uninstall/bootout exact registered launchd labels or stop exact systemd units before binary/state rollback; restore pre-schema-7 state backup because no downgrade migration exists
- Validation: `make verify-local`; prior isolated three-machine member/viewer HTTP run `20260728T151219Z-60255`; periodic-sync three-machine authority-2.1 campaign passed: `20260730T092006Z-83493` (`generated/e2e-three-machine/20260730T092006Z-83493/artifacts/report.json`)
- Operator action: use isolated state and do not point this binary at an existing Tubo home
