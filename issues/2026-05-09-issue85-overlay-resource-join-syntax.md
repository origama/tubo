# Issue #85 — Overlay resource join syntax and legacy overlay compatibility

Status: DONE
Updated: 2026-05-09 15:20 UTC

## Summary
- Added explicit overlay join forms:
  - `tubo join overlay/public`
  - `tubo join overlay/manual --relay ... --swarm-key ...`
- Preserved legacy manual join:
  - `tubo join --relay ... --swarm-key ...`
- Kept default public join compatibility for `tubo join` and `tubo join tubo-public`.
- Updated join output to clearly say whether the user joined a public overlay or a manual/legacy overlay.

## Validation
- `go test ./...`
- `./tests/smoke-compose.sh`
- `RUN_INTEGRATION=1 go test -v ./tests/integration`

## Notes
- No runtime behavior changes beyond clearer overlay selection and output.
- Config still writes both the new overlay model and legacy `network:` fields for compatibility.
