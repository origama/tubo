# Issue #93 — Remove legacy swarm discovery mode; make cluster/namespace discovery the only supported path

Status: DONE
Updated: 2026-05-10 10:08 UTC

## Summary
- New breaking-change issue that removes legacy swarm-based discovery entirely.
- The product should rely only on capability-based overlay/cluster/namespace discovery.
- Legacy compatibility is intentionally not part of this issue.

## Notes
- Replace legacy discovery fallbacks with fail-fast errors.
- Update CLI help and docs to describe only the new supported model.
- Keep public overlays documented as transport only.
- Convert current tests, compose flows, and smoke coverage to the new cluster-aware-only path.
- Isolated-network NAT compose coverage is currently skipped in the integration suite while the relayed discovery path remains unsupported.
- Completed: runtime/docs now require cluster/namespace discovery V2 only; legacy swarm discovery is removed.
