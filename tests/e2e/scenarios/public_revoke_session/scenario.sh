#!/usr/bin/env bash
set -euo pipefail

SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/report.sh"

mkdir -p "$E2E_LOG_DIR" "$E2E_ARTIFACTS_DIR"

log "validating connect session revocation"
(
  cd "$E2E_REPO_ROOT"
  go test -v ./internal/grants -run TestGrantServerRevokedSessionAndServiceAccessCannotRefresh
) | tee "$E2E_LOG_DIR/revoke-session.out"

cat > "$E2E_ARTIFACTS_DIR/report.json" <<EOF
{
  "scenario": "$E2E_SCENARIO",
  "result": "pass",
  "validated": [
    "revoked session cannot refresh",
    "service access epoch invalidates old refresh leases"
  ]
}
EOF

echo "[e2e] PASS: revoked connect session cannot refresh"
