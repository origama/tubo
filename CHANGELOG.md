# Changelog

All notable changes to `tubo` will be documented in this file.

This project follows the versioning policy in `docs/VERSIONING.md`.

## [Unreleased]

### Added
- None.

### Changed
- None.

### Fixed
- None.

### Compatibility
- Product version: pending next release
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none

## [v0.4.0] - 2026-05-05

Repository/module rename release that aligns the project identity around `tubo`, updates operational paths and image names, and validates the renamed tree on the real 3-node Linode bench.

### Added
- No new runtime protocol features; this release is focused on project/repository identity alignment and operability validation.

### Changed
- GitHub repository moved from `origama/p2p-api-tunnel` to `origama/tubo`.
- Go module path changed from `p2p-api-tunnel` to `github.com/origama/tubo`.
- Internal imports, release ldflags, and source references now use the new module path.
- Operational/docs references were updated from `p2p-api-tunnel` to `tubo`.
- Remote runtime paths now use `/opt/tubo` and `/var/run/tubo`.
- Local compose image names now use `tubo` / `tubo-dummy-api-server`.
- Local checkout path is now `/root/tubo`.

### Fixed
- Rename fallout in Linode runtime scripts was corrected so remote pid/state paths no longer expand to invalid `github.com/origama/tubo` filesystem paths.
- The renamed tree was validated successfully on the real 3-node Linode setup after fixing those path regressions.

### Compatibility
- Product version: v0.4.0
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: if you consume the source tree directly, update Git remotes, Go module imports, local checkout path expectations, Docker image references, and remote runtime paths from `p2p-api-tunnel` to `tubo`

## [v0.3.0] - 2026-05-03

Remote discovery query release focused on making service discovery and connect resolution more reliable when clients arrive after the initial pubsub announcement window.

### Added
- New libp2p application protocol `/tubo/discovery/query/1.0` for querying remote discovery cache state.
- New package `internal/discovery/query` with request/response structs, JSON stream handlers, client helpers, and discovery DTO mapping.
- Discovery query serving on gateway/edge, relay, and attach/service roles.
- Relay-side discovery cache usable for remote query responses.
- Role-specific tests covering gateway, relay, and attach query serving.

### Changed
- CLI discovery resolution now prefers: local edge admin cache -> remote discovery query -> live pubsub observer fallback.
- Single-service lookups (`get service/...`, `describe service/...`, `inspect service/...`, `connect`) now use targeted remote `get_service` queries instead of depending only on list+filter or live observer timing.
- Human-readable CLI output now reports when discovery data came from remote query, and JSON output includes remote-query metadata.
- CLI UX smoke now validates the remote-query path before connect/service inspection.

### Fixed
- `get services` and related resource-oriented commands no longer depend solely on catching a fresh heartbeat during the observer timeout window when a bootstrap/relay peer already has the service in cache.
- `connect <service>` can now resolve services through remote cache query before falling back to live observation, reducing false negatives after late joins.
- Same-swarm service inspection is more reliable when no local edge cache exists but a relay already knows the service.

### Compatibility
- Product version: v0.3.0
- Protocol version: 1.1
- Protocol compatibility change: none; `/tubo/discovery/query/1.0` is an additive optional application protocol
- Operator action required: none; this is a backward-compatible feature release on top of protocol `1.1`

## [v0.2.0] - 2026-05-03

CLI UX v2 milestone release with the intent-based/resource-oriented command set, daemonless local process management, docs-driven happy-path smoke coverage, and direct-first service connection behavior when relay fallback is also available.

### Added
- Intent-based CLI surface for `relay`, `attach`, `gateway`, `join`, and `connect`.
- Resource-oriented swarm discovery commands: `get services`, `get service/<name>`, `describe service/<name>`, `inspect service/<name> --json`, and `watch services`.
- Local detached process management commands: `ps`, `get processes`, `logs`, `stop`, `rm --stale`, `describe process/...`, and `inspect process/... --json`.
- Docs-driven CLI happy-path smoke harness `tests/smoke-cli-ux.sh` plus dedicated CI coverage.
- Direct-vs-relayed address classification in service discovery output and inspect JSON.
- GitHub release workflow for publishing platform binaries and checksums to GitHub Releases.

### Changed
- `connect` now prefers direct service addresses when available and keeps relay addresses as explicit fallback.
- `describe service/...` now shows dial policy plus separate direct and relayed address sections.
- Default CLI workflow is now centered on `relay -> join -> attach -> get services -> connect`, while legacy role commands remain available as advanced compatibility commands.
- Local detached process state/log management now follows stable `process/...` resource IDs.
- Implicit local init for `attach`, `gateway`, and `relay` is now available outside CI, with `--no-init` and CI-safe fail-fast behavior.

### Fixed
- Same-machine relay/bootstrap setups no longer misleadingly prefer relayed service paths when a usable direct path is advertised.
- `connect` output now clearly reports whether direct was selected, unavailable, or being kept behind relay fallback.
- CLI UX smoke portability and CI reliability improved for local observer/cache discovery paths.
- Release asset publishing workflow issues discovered in live validation were fixed, and published releases can now be re-run successfully.

### Compatibility
- Product version: v0.2.0
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none; this is a backward-compatible CLI/runtime feature release on top of protocol `1.1`

## [v0.1.3] - 2026-05-02

Service-restart recovery release focused on reducing relayed traffic disruption during service restarts, especially on the real 3-host Linode bench.

### Added
- Edge regression coverage for coordinated relay recovery, retry-time discovery re-resolution, guarded last-known entry use, and recovery-end route handling.

### Changed
- Edge now coordinates relay recovery per service as a bounded single-flight flow instead of letting each failing request independently thrash the retry path.
- Stream-open retries now re-resolve discovery state on each attempt and only rely on last-known service entries inside the active recovery window.
- Requests arriving during active relay recovery now briefly wait for the in-flight recovery/announcement refresh instead of failing immediately while the service is still republishing its relay path.

### Fixed
- Relayed traffic during service restart now recovers cleanly on the validated 3-host Linode restart stress, with zero request failures on the final release-candidate rerun.
- Route expiry/removal no longer races a successful coordinated recovery completion.

### Compatibility
- Product version: v0.1.3
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none; this is a backward-compatible recovery and latency improvement on top of the existing protocol `1.1` + legacy fallback behavior

## [v0.1.2] - 2026-05-01

Relay-first recovery release focused on making traffic recover cleanly after relay restarts in both NAT compose tests and the real Linode multi-host bench.

### Added
- NAT integration regression test: `TestRelayNATTrafficRecoversAfterRelayRestart`.
- Service regression tests for synthesized relay-circuit announcements and tracked reservation readiness.
- Edge regression tests for relay-recovery gating behavior.

### Changed
- Service now clears tracked reservation state when the relay peer disconnects and forces a fresh reservation on reconnect.
- Service republishes usable relay-circuit announcement addresses immediately after reservation refresh, even when host-reported `/p2p-circuit` addresses lag.
- Edge now seeds its peerstore from discovery announcements and prefers explicitly announced relayed addresses when the service is operating on a relay-first path.
- Edge no longer treats stale direct addresses as first-class recovery candidates when the service is clearly advertising a relay-only recovery path.

### Fixed
- Relay-first traffic now recovers after relay process restart instead of remaining wedged in repeated `502`, `dial backoff`, and `NO_RESERVATION` failures.
- Relay restart no longer causes the service announcement to drift into stale reservation state after the relay disconnects and reconnects.

### Compatibility
- Product version: v0.1.2
- Protocol version: 1.1
- Protocol compatibility change: none
- Operator action required: none; this is a backward-compatible reliability fix on top of the existing `1.1` + legacy fallback behavior

## [v0.1.1] - 2026-05-01

First clean versioned release with explicit product/protocol versioning, protocol negotiation, and real mixed-version compatibility evidence on the Linode multi-host bench.

### Added
- Release/versioning policy in `docs/VERSIONING.md`.
- `tubo version` command with product version, protocol version, commit, and build date output.
- Root `VERSION` file, `CHANGELOG.md`, and manual release checklist in `docs/RELEASING.md`.
- Protocol 1.1 hello handshake carrying `protocol major.minor`, role, and capabilities.
- Edge/service protocol debug endpoints exposing negotiated protocol state.
- Real mixed-version Linode compatibility smoke harness: `tests/smoke-terraform-linode-mixed-version.sh`.

### Changed
- Stream negotiation now prefers `/p2p-tunnel/1.1` and falls back to legacy `/p2p-tunnel/1.0`.
- Service accepts both `/p2p-tunnel/1.1` and legacy `/p2p-tunnel/1.0` stream protocol IDs during rollout.
- Linode testbench docs now include the mixed-version compatibility workflow.

### Fixed
- Mixed-version same-major stream setup now has an explicit fast-fail path for incompatible protocol-major peers.
- Selected stream protocol is now visible in runtime logs even on the legacy fallback path.

### Compatibility
- Product version: v0.1.1
- Protocol version: 1.1
- Protocol compatibility change: backward-compatible addition; legacy `/p2p-tunnel/1.0` remains accepted
- Operator action required: none for same-major upgrades; old/new nodes can mix during rollout through the legacy fallback path

## [v0.1.0] - 2026-05-01

Initial tagged baseline for the unified `tubo` binary after relay-first NAT validation, distributed Linode testbench rollout, and the first stabilized relay large-payload fixes.

### Added
- Unified `tubo` CLI with role subcommands for edge, relay, service, and bridge.
- Binary framing protocol with streamed request/response bodies.
- Discovery via signed pubsub announcements with TTL cache.
- Relay-first NAT/isolated-network test coverage and Linode Terraform distributed testbench.

### Changed
- Relay and edge runtime defaults were hardened for relayed large-body traffic.
- Performance baselines are now saved under `tests/perf/results/linode-terraform/`.

### Fixed
- Partial frame writes now flush fully during protocol encoding.
- Relayed large-payload traffic stability improved under mixed and burst load.
- Stale routes are evicted earlier after hard stream-open failures.

### Compatibility
- Product version: v0.1.0
- Protocol version: 1.0
- Protocol compatibility change: none
- Operator action required: none
