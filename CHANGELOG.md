# Changelog

All notable changes to `tubo` will be documented in this file.

This project follows the versioning policy in `docs/VERSIONING.md`.

## [Unreleased]

### Added
- Release/versioning policy in `docs/VERSIONING.md`.

### Changed
- None.

### Fixed
- None.

### Compatibility
- Product version: pending next release
- Protocol version: 1.0
- Protocol compatibility change: none
- Operator action required: none

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
