#!/usr/bin/env bash
set -euo pipefail

source "${E2E_ROOT}/lib/common.sh"

E2E_RESOURCE_LABEL_KEY="io.origama.tubo.e2e"
E2E_RESOURCE_LABEL_VALUE="1"
E2E_RUN_LABEL_KEY="io.origama.tubo.e2e.run_id"

e2e_resource_label() {
  printf '%s=%s' "$E2E_RESOURCE_LABEL_KEY" "$E2E_RESOURCE_LABEL_VALUE"
}

e2e_run_label() {
  printf '%s=%s' "$E2E_RUN_LABEL_KEY" "$E2E_RUN_ID"
}

remove_docker_names() {
  local kind="$1"
  shift
  local names=()
  while IFS= read -r name; do
    [[ -n "$name" ]] || continue
    names+=("$name")
  done
  if [[ ${#names[@]} -eq 0 ]]; then
    return
  fi
  case "$kind" in
    container)
      docker rm -f "${names[@]}" >/dev/null 2>&1 || true
      ;;
    network)
      docker network rm "${names[@]}" >/dev/null 2>&1 || true
      ;;
  esac
}

cleanup_stale_e2e_resources() {
  remove_docker_names container < <(docker ps -a --filter "label=$(e2e_resource_label)" --format '{{.Names}}')
  remove_docker_names container < <(docker ps -a --format '{{.Names}}' | rg '^tubo-e2e-' || true)
  docker rm -f bundle-server >/dev/null 2>&1 || true
  remove_docker_names network < <(docker network ls --filter "label=$(e2e_resource_label)" --format '{{.Name}}')
  remove_docker_names network < <(docker network ls --format '{{.Name}}' | rg '^tubo-e2e-' || true)
}

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
  docker network create \
    --label "$(e2e_resource_label)" \
    --label "$(e2e_run_label)" \
    "$E2E_NETWORK_NAME" >/dev/null
}

remove_network() {
  docker network rm "$E2E_NETWORK_NAME" >/dev/null 2>&1 || true
}

start_actor() {
  local actor="$1"
  local name
  name="$(actor_container_name "$actor")"
  local run_args=(
    -d
    --name "$name"
    --hostname "$actor"
    --network "$E2E_NETWORK_NAME"
    --label "$(e2e_resource_label)"
    --label "$(e2e_run_label)"
    -e "TUBO_DEFAULT_PUBLIC_BUNDLE_URL=${TUBO_DEFAULT_PUBLIC_BUNDLE_URL:-}"
    -v "$(actor_home "$actor"):/work"
    -w /work
  )
  if [[ -n "${E2E_EXTRA_HOSTS:-}" ]]; then
    for host in ${E2E_EXTRA_HOSTS}; do
      run_args+=(--add-host "$host")
    done
  fi
  run_args+=("$E2E_IMAGE_NAME")
  docker run "${run_args[@]}" >/dev/null
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
    -e TUBO_DEFAULT_PUBLIC_BUNDLE_URL="${TUBO_DEFAULT_PUBLIC_BUNDLE_URL:-}" \
    "$(actor_container_name "$actor")" "$@"
}

exec_actor_bg() {
  local actor="$1"
  shift
  docker exec -d \
    -e XDG_CONFIG_HOME=/work/config \
    -e XDG_DATA_HOME=/work/data \
    -e XDG_CACHE_HOME=/work/cache \
    -e TUBO_DEFAULT_PUBLIC_BUNDLE_URL="${TUBO_DEFAULT_PUBLIC_BUNDLE_URL:-}" \
    "$(actor_container_name "$actor")" sh -lc "$*"
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
  docker rm -f bundle-server >/dev/null 2>&1 || true
}
