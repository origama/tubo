#!/usr/bin/env bash
set -euo pipefail

source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/report.sh"

mkdir -p "$E2E_LOG_DIR" "$E2E_ARTIFACTS_DIR"

log "validating pinned service_id rebind after service restart"
(
  cd "$E2E_REPO_ROOT"
  RUN_INTEGRATION=1 go test -v ./tests/integration -run TestTCPBridgeRebindsPinnedServiceAfterRestart -count=1
) | tee "$E2E_LOG_DIR/connect-rebind.out"

cat > "$E2E_ARTIFACTS_DIR/report.json" <<EOF
{
  "scenario": "$E2E_SCENARIO",
  "result": "pass",
  "validated": [
    "bridge reconnects after the original service peer stops",
    "rebind resolver selects the restarted peer for the same pinned service_id",
    "second request succeeds against the replacement service"
  ]
}
EOF

echo "[e2e] PASS: pinned service_id rebinds after service restart"
