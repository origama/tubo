#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
export E2E_ROOT="$ROOT_DIR/tests/e2e"
export E2E_REPO_ROOT="$ROOT_DIR"

source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/docker.sh"
source "$E2E_ROOT/lib/assertions.sh"
source "$E2E_ROOT/lib/processes.sh"
source "$E2E_ROOT/lib/report.sh"
source "$E2E_ROOT/lib/ports.sh"

scenario_arg="${1:-}"
if [[ -z "$scenario_arg" ]]; then
  fail "usage: tests/e2e/run.sh <scenario|all|clean>"
fi

if [[ "$scenario_arg" == "clean" ]]; then
  rm -rf "$ROOT_DIR/generated/e2e"
  log "removed generated/e2e"
  exit 0
fi

require_tools

run_id="$(date +%Y%m%d%H%M%S)-$RANDOM"
export E2E_RUN_ID="$run_id"
export E2E_WORK_DIR="$ROOT_DIR/generated/e2e/${scenario_arg}-${run_id}"
export E2E_LOG_DIR="$E2E_WORK_DIR/logs"
export E2E_ARTIFACTS_DIR="$E2E_WORK_DIR/artifacts"
export E2E_IMAGE_NAME="tubo-e2e:${run_id}"
export E2E_NETWORK_NAME="tubo-e2e-${run_id}-public"

mkdir -p "$E2E_WORK_DIR"
init_run_dirs

cleanup() {
  if [[ "${KEEP_WORK:-0}" == "1" ]]; then
    log "KEEP_WORK=1; leaving workdir at $E2E_WORK_DIR"
    return
  fi
  cleanup_containers
  remove_network
}
trap cleanup EXIT

run_one() {
  local scenario="$1"
  local scenario_dir="$E2E_ROOT/scenarios/$scenario"
  [[ -d "$scenario_dir" ]] || fail "unknown scenario: $scenario"
  log "running scenario $scenario"
  (cd "$scenario_dir" && ./scenario.sh)
  log "scenario $scenario completed"
}

build_binaries
build_e2e_image
create_network

if [[ "$scenario_arg" == "all" ]]; then
  for scenario_dir in "$E2E_ROOT"/scenarios/*; do
    [[ -d "$scenario_dir" ]] || continue
    cleanup_containers
    rm -rf "$E2E_WORK_DIR/actors" "$E2E_WORK_DIR/logs" "$E2E_WORK_DIR/artifacts"
    init_run_dirs
    scenario="$(basename "$scenario_dir")"
    E2E_SCENARIO="$scenario"
    run_one "$scenario"
    cleanup_containers
  done
else
  E2E_SCENARIO="$scenario_arg"
  run_one "$scenario_arg"
fi

log "scenario workdir: $E2E_WORK_DIR"
