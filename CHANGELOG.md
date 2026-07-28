# Changelog

## [Unreleased]

### Added
- Greenfield repository contract for Tubo's service/pipe architecture.
- Initial domain invariants and deterministic local verification gate.
- Isolated three-machine E2E harness contract for `tuborelay`, `oripi`, and `macb`.

### Changed
- This codebase has no compatibility relationship with the previous Tubo implementation.

### Fixed
- None.

### Compatibility
- Product version: `0.1.0-dev`
- Protocol version: `2.0`
- Compatibility with previous Tubo versions: intentionally unsupported
- Operator action: use isolated state and do not point this binary at an existing Tubo home
