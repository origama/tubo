# Issue #87 — Harden Discovery V2 PubSub validation and replay protection

Status: DONE
Updated: 2026-05-09 16:25 UTC

## Summary
- Added opaque-topic checks for Discovery V2 subscribers.
- Added authority-backed membership capability validation for namespace-scoped announcements.
- Added optional service-claim validation when claim bytes are present.
- Added announcement expiry checks and bounded replay protection on nonce/topic/peer tuple.
- Added unit coverage for valid/invalid topic, replay, expiry, capability, claim, and ciphertext failures.

## Validation
- `go test ./...`
- `./tests/smoke-compose.sh`
- `RUN_INTEGRATION=1 go test -v ./tests/integration`

## Notes
- Legacy Discovery V1 remains unchanged.
- Replay cache is bounded so message dedup state cannot grow without limit.
