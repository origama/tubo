# Issue #89 — Service sharing: connect-only grants for service access

Status: DONE
Updated: 2026-05-09 18:10 UTC

## Summary
- Added `tubo share service/<name>` for namespace-scoped service sharing.
- Service share tokens are versioned, signed bearer tokens carrying a connect-only `ConnectCapability` scoped to cluster/namespace/service.
- Added `tubo connect --token <service-share>` so a recipient can resolve the intended service without first listing services.
- Service share validation rejects expired, tampered, or non-connect-only tokens.
- Existing `tubo connect <service>` and legacy discovery behavior remain unchanged.

## Validation
- `go test ./...`
- `./tests/smoke-compose.sh`
- `RUN_INTEGRATION=1 go test -v ./tests/integration`

## Notes
- Token sharing is authorization-layer only; no data-plane connect proof enforcement yet.
- The token carries cluster/namespace/service identity and a connect-only grant, but the service bridge path still uses the existing discovery/connect flow.
