#!/usr/bin/env bash
set -euo pipefail

source "${E2E_ROOT}/lib/common.sh"

write_report_json() {
  local path="$1"
  local scenario="$2"
  local network="$3"
  local service_name="$4"
  local alice_container="$5"
  local bob_container="$6"
  python3 - "$path" "$scenario" "$network" "$service_name" "$alice_container" "$bob_container" <<'PY'
import json
import sys

path, scenario, network, service_name, alice_container, bob_container = sys.argv[1:7]
report = {
    "scenario": scenario,
    "pass": True,
    "runtime": "docker",
    "network": network,
    "service_name": service_name,
    "actors": {
        "admin": {"container": "tubo-e2e-%s-admin" % scenario},
        "alice": {"container": alice_container, "published_service": service_name},
        "bob": {"container": bob_container, "connected_service": service_name},
    },
}
with open(path, "w", encoding="utf-8") as f:
    json.dump(report, f, indent=2, sort_keys=True)
    f.write("\n")
PY
}
