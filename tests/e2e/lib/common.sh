#!/usr/bin/env bash
set -euo pipefail

E2E_ROOT="${E2E_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
E2E_REPO_ROOT="${E2E_REPO_ROOT:-$(cd "$E2E_ROOT/../.." && pwd)}"
E2E_SCENARIOS_DIR="$E2E_ROOT/scenarios"

E2E_WORK_DIR="${E2E_WORK_DIR:-}"
E2E_RUN_ID="${E2E_RUN_ID:-}"
E2E_SCENARIO="${E2E_SCENARIO:-}"
E2E_LOG_DIR="${E2E_LOG_DIR:-}"
E2E_ARTIFACTS_DIR="${E2E_ARTIFACTS_DIR:-}"
E2E_IMAGE_NAME="${E2E_IMAGE_NAME:-}"
E2E_NETWORK_NAME="${E2E_NETWORK_NAME:-}"

log() {
  echo "[e2e] $*"
}

fail() {
  echo "[e2e] $*" >&2
  exit 1
}

require_tools() {
  for tool in docker curl python3; do
    command -v "$tool" >/dev/null 2>&1 || fail "missing required tool: $tool"
  done
}

init_run_dirs() {
  [[ -n "$E2E_WORK_DIR" ]] || fail "E2E_WORK_DIR is required"
  mkdir -p "$E2E_WORK_DIR/bin" "$E2E_WORK_DIR/logs" "$E2E_WORK_DIR/artifacts" "$E2E_WORK_DIR/actors" \
    "$E2E_WORK_DIR/actors/admin/logs" "$E2E_WORK_DIR/actors/admin/config" "$E2E_WORK_DIR/actors/admin/data" "$E2E_WORK_DIR/actors/admin/cache" \
    "$E2E_WORK_DIR/actors/alice/logs" "$E2E_WORK_DIR/actors/alice/config" "$E2E_WORK_DIR/actors/alice/data" "$E2E_WORK_DIR/actors/alice/cache" \
    "$E2E_WORK_DIR/actors/bob/logs" "$E2E_WORK_DIR/actors/bob/config" "$E2E_WORK_DIR/actors/bob/data" "$E2E_WORK_DIR/actors/bob/cache"
  E2E_LOG_DIR="$E2E_WORK_DIR/logs"
  E2E_ARTIFACTS_DIR="$E2E_WORK_DIR/artifacts"
}

build_binaries() {
  log "building local binaries"
  (cd "$E2E_REPO_ROOT" && go build -o "$E2E_WORK_DIR/bin/tubo" ./cmd/tubo)
  (cd "$E2E_REPO_ROOT" && go build -o "$E2E_WORK_DIR/bin/dummy-api-server" ./cmd/dummy-api-server)
}

generate_swarm_key() {
  log "generating swarm key"
  "$E2E_WORK_DIR/bin/tubo" keygen swarm --out "$E2E_ARTIFACTS_DIR/swarm.key" >/dev/null
}

copy_swarm_key_to_actors() {
  for actor in admin alice bob; do
    cp "$E2E_ARTIFACTS_DIR/swarm.key" "$E2E_WORK_DIR/actors/$actor/swarm.key"
  done
}

actor_home() {
  local actor="$1"
  printf '%s' "$E2E_WORK_DIR/actors/$actor"
}
