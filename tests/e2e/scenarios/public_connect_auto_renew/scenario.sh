#!/usr/bin/env bash
set -euo pipefail

SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/report.sh"

mkdir -p "$E2E_LOG_DIR" "$E2E_ARTIFACTS_DIR"

log "validating connect lease redemption and auto-renew"
(
  cd "$E2E_REPO_ROOT"
  RUN_INTEGRATION=1 go test -v ./tests/integration -run TestConnectLeaseRedeemAndRefreshKeepsBridgeAlive
) | tee "$E2E_LOG_DIR/connect-auto-renew.out"

cat > "$E2E_ARTIFACTS_DIR/report.json" <<EOF
{
  "scenario": "$E2E_SCENARIO",
  "result": "pass",
  "validated": [
    "share invite redeemed into connect access and refresh leases",
    "access TTL is short and expires during the test window",
    "bridge refreshes access lease before expiry",
    "requests continue after original access lease expiry"
  ]
}
EOF

echo "[e2e] PASS: connect access lease auto-renew keeps bridge alive"
