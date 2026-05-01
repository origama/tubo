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
