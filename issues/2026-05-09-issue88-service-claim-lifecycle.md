# Issue #88 — Service claim lifecycle for namespace-scoped service publishing

Status: DONE
Updated: 2026-05-09 17:15 UTC

## Summary
- Added `tubo create service/<name>` for namespace-scoped service identity creation.
- Service IDs are derived deterministically from `(cluster_id, namespace_id, display name)` so duplicate names are stable within a namespace and distinct across namespaces.
- Local service creation now signs and stores a `ServiceClaim` on disk for reuse by cluster-aware `attach`/Discovery V2 publishing.
- Service Discovery V2 payloads now carry `service_id`, and validation checks the claim against that service identity.
- Legacy attach/runtime behavior remains unchanged when no cluster-aware service metadata is present.

## Validation
- `go test ./...`
- `./tests/smoke-compose.sh`
- `RUN_INTEGRATION=1 go test -v ./tests/integration`

## Notes
- Existing configs without a stored service identity continue to work; the runtime derives the same deterministic service ID when needed.
- Duplicate service creation in the same namespace reuses the same identity/claim path instead of generating a new one.
