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

### Changed
- This codebase has no compatibility relationship with the previous Tubo implementation.
- State schema 4 adds run source and supervisor registration metadata; schema 3 upgrades in place, and older binaries reject schema 4.
- Protocol 2.0 first encoding is bounded length-prefixed JSON; raw full-duplex bytes begin after `OpenOK`.

### Fixed
- Configured relay connections now clear stale libp2p dial backoff and retry every five seconds after network interruption; authority/publisher reservations and records recover without process restart.
- Local pipe status now degrades on relay/session setup failure, `doctor` detects degraded/disconnected runtimes, and `get pipes` retains verified cached inventory during discovery outages.

### Compatibility
- Product version: `0.1.0-dev`
- Protocol version: `2.0` (initial implementation; no version bump)
- State schema version: `4`
- Mixed-version behavior: no older rebuilt runtime exists; protocol 1.x is rejected
- Compatibility with previous Tubo versions: intentionally unsupported
- Rollout: deploy relay, authority/origin, then consumers from same checkout using isolated `TUBO_HOME`
- Rollback: uninstall/bootout exact registered launchd labels or stop exact systemd units before binary/state rollback; restore the pre-schema-4 state backup because no downgrade migration exists
- Validation: `make verify-local`; isolated three-machine member/viewer HTTP run `20260728T151219Z-60255`; live protocol-2.0 cutover `live-v2-20260728T153922Z`; operations deployment `easy-features-20260728T163758Z`; telemetry deployment `runtime-telemetry-20260728T173834Z`; live 20-second relay outage `self-heal-20260728T183109Z` recovered in 8 seconds with unchanged edge PIDs and HTTP/TCP pass
- Operator action: use isolated state and do not point this binary at an existing Tubo home
