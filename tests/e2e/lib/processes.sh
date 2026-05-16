#!/usr/bin/env bash
set -euo pipefail

source "${E2E_ROOT}/lib/common.sh"
source "${E2E_ROOT}/lib/docker.sh"

start_bg_process() {
  local actor="$1"
  local log_name="$2"
  shift 2
  local name
  name="$(actor_container_name "$actor")"
  docker exec -d \
    -e XDG_CONFIG_HOME=/work/config \
    -e XDG_DATA_HOME=/work/data \
    -e XDG_CACHE_HOME=/work/cache \
    -e TUBO_DEFAULT_PUBLIC_BUNDLE_URL="${TUBO_DEFAULT_PUBLIC_BUNDLE_URL:-}" \
    "$name" sh -lc "$* > /work/logs/${log_name}.out 2>&1"
}
