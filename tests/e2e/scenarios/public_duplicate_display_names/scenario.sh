#!/usr/bin/env bash
set -euo pipefail

SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/report.sh"

mkdir -p "$E2E_LOG_DIR" "$E2E_ARTIFACTS_DIR"

log "validating service_id-first discovery accepts duplicate display names"
(
  cd "$E2E_REPO_ROOT"
  go test -v ./internal/discovery -run 'TestPubSubSubscriberV2AcceptsDuplicateDisplayNamesWithDifferentServiceIDs|TestPubSubSubscriberV2RejectsDuplicateServiceIDFromWrongKey|TestPubSubSubscriberV2RejectsPublishLeaseForDifferentServiceID|TestPubSubSubscriberV2RejectsPublishLeaseFromUntrustedIssuer|TestPubSubSubscriberV2RejectsExpiredPublishLease'
) | tee "$E2E_LOG_DIR/discovery-service-id-first.out"

cat > "$E2E_ARTIFACTS_DIR/report.json" <<EOF
{
  "scenario": "$E2E_SCENARIO",
  "result": "pass",
  "validated": [
    "duplicate display names accepted as distinct service_id records",
    "wrong service public key rejected",
    "publish lease for different service_id rejected",
    "untrusted publish lease issuer rejected",
    "expired publish lease rejected"
  ]
}
EOF

echo "[e2e] PASS: service_id-first discovery accepts duplicate display names and rejects invalid leases"
