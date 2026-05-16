#!/usr/bin/env bash
set -euo pipefail

source "${E2E_ROOT}/lib/common.sh"

actor_container_name() {
  local actor="$1"
  printf 'tubo-e2e-%s-%s' "$E2E_RUN_ID" "$actor"
}

build_e2e_image() {
  local image_ctx="$E2E_WORK_DIR/image"
  mkdir -p "$image_ctx/bin"
  cp "$E2E_WORK_DIR/bin/tubo" "$image_ctx/bin/tubo"
  cp "$E2E_WORK_DIR/bin/dummy-api-server" "$image_ctx/bin/dummy-api-server"
  docker build -t "$E2E_IMAGE_NAME" -f "$E2E_ROOT/Dockerfile" "$image_ctx" >/dev/null
}

create_network() {
  docker network create "$E2E_NETWORK_NAME" >/dev/null
}

remove_network() {
  docker network rm "$E2E_NETWORK_NAME" >/dev/null 2>&1 || true
}

start_actor() {
  local actor="$1"
  local name
  name="$(actor_container_name "$actor")"
  docker run -d \
    --name "$name" \
    --hostname "$actor" \
    --network "$E2E_NETWORK_NAME" \
    -v "$(actor_home "$actor"):/work" \
    -w /work \
    "$E2E_IMAGE_NAME" >/dev/null
}

stop_actor() {
  local actor="$1"
  local name
  name="$(actor_container_name "$actor")"
  docker rm -f "$name" >/dev/null 2>&1 || true
}

actor_ip() {
  local actor="$1"
  local name
  name="$(actor_container_name "$actor")"
  docker inspect -f '{{range.NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name"
}

exec_actor() {
  local actor="$1"
  shift
  docker exec \
    -e XDG_CONFIG_HOME=/work/config \
    -e XDG_DATA_HOME=/work/data \
    -e XDG_CACHE_HOME=/work/cache \
    "$(actor_container_name "$actor")" "$@"
}

exec_actor_bg() {
  local actor="$1"
  shift
  docker exec -d \
    -e XDG_CONFIG_HOME=/work/config \
    -e XDG_DATA_HOME=/work/data \
    -e XDG_CACHE_HOME=/work/cache \
    "$(actor_container_name "$actor")" "$@"
}

container_logs() {
  local actor="$1"
  local name
  name="$(actor_container_name "$actor")"
  docker logs "$name" 2>&1 || true
}

save_container_logs() {
  local actor="$1"
  local prefix="$2"
  mkdir -p "$E2E_LOG_DIR"
  container_logs "$actor" >"$E2E_LOG_DIR/${prefix}.container.log" || true
}

cleanup_containers() {
  for actor in bob alice admin; do
    stop_actor "$actor"
  done
}
