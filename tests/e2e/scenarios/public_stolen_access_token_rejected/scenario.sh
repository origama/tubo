#!/usr/bin/env bash
set -euo pipefail

SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/report.sh"

mkdir -p "$E2E_LOG_DIR" "$E2E_ARTIFACTS_DIR"

log "validating stolen/replayed/scope-mismatched connect proof rejection"
(
  cd "$E2E_REPO_ROOT"
  RUN_INTEGRATION=1 go test -v ./tests/integration -run TestConnectProofAuthorizesBridgeAndRejectsReplayAndScope
) | tee "$E2E_LOG_DIR/connect-proof-rejection.out"

cat > "$E2E_ARTIFACTS_DIR/report.json" <<EOF
{
  "scenario": "$E2E_SCENARIO",
  "result": "pass",
  "validated": [
    "valid connect proof accepted",
    "missing connect proof rejected",
    "expired connect proof rejected",
    "scope-mismatched connect proof rejected",
    "replayed connect proof rejected"
  ]
}
EOF

echo "[e2e] PASS: stolen/replayed access proof attempts are rejected"
