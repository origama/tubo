# Issue #90 — Data-plane authorization with connect proof enforcement

Status: DONE
Updated: 2026-05-09 20:35 UTC

## Summary
- Added protocol-level connect proof frames and encoding/decoding support.
- Service-side stream handling now requires a connect proof before forwarding upstream in namespace-v2 / cluster mode.
- Bridge/client flows build and send proofs from connect grants.
- Proof validation is bound to cluster/namespace/service, subject peer, expiry, and replay protection.
- Invalid, expired, missing, mismatched, and replayed proofs are rejected before upstream proxying.

## Validation
- `go test ./...`
- `./tests/smoke-compose.sh`
- `RUN_INTEGRATION=1 go test -v ./tests/integration`

## Notes
- Legacy non-cluster flows remain unchanged.
- The service-share token is now consumed as a connect grant that materializes into an on-stream proof for the bridge path.
