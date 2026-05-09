# Issue #86 — Discovery V2 runtime on namespace-scoped topics

Status: DONE
Updated: 2026-05-09 15:43 UTC

## Summary
- Added cluster-aware discovery topic selection.
- Gateway, attach, and observer flows now use an opaque `/discovery/v2/...` topic when the current config has cluster identity metadata.
- Legacy configs continue using `/discovery/v1.0`.
- Service-side V2 announcements now carry encrypted metadata, including membership capability bytes when available.

## Validation
- `go test ./...`
- `./tests/smoke-compose.sh`
- `RUN_INTEGRATION=1 go test -v ./tests/integration`

## Notes
- Legacy overlay configs remain on the v1 topic.
- V2 topics do not leak cluster/namespace names in the topic string.
