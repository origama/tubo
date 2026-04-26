#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE="${COMPOSE_CMD:-docker compose}"
# Docker Desktop BuildKit has shown intermittent crashes in this environment.
# Default to legacy builder for stability; override by exporting DOCKER_BUILDKIT=1.
export DOCKER_BUILDKIT="${DOCKER_BUILDKIT:-0}"
export COMPOSE_DOCKER_CLI_BUILD="${COMPOSE_DOCKER_CLI_BUILD:-0}"

cleanup() {
  $COMPOSE down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_http_ok() {
  local url="$1"
  local tries="${2:-60}"
  local i
  for i in $(seq 1 "$tries"); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

echo "[smoke] docker compose up -d"
if [[ "${SMOKE_FORCE_BUILD:-0}" == "1" ]]; then
  echo "[smoke] forcing image rebuild (sequential)"
  build_ok=0
  for attempt in 1 2 3; do
    if $COMPOSE build --no-parallel; then
      build_ok=1
      break
    fi
    echo "[smoke] compose build attempt $attempt failed, retrying..."
    sleep 2
  done
  if [[ "$build_ok" != "1" ]]; then
    echo "[smoke] compose build failed after 3 attempts"
    exit 1
  fi
fi

up_ok=0
for attempt in 1 2 3; do
  if $COMPOSE up -d; then
    up_ok=1
    break
  fi
  echo "[smoke] compose up attempt $attempt failed, retrying..."
  sleep 2
done
if [[ "$up_ok" != "1" ]]; then
  echo "[smoke] compose up failed after 3 attempts"
  exit 1
fi

echo "[smoke] waiting for health endpoints"
wait_http_ok "http://127.0.0.1:8000/healthz"
wait_http_ok "http://127.0.0.1:8443/healthz"
wait_http_ok "http://127.0.0.1:8444/healthz"
wait_http_ok "http://127.0.0.1:8091/healthz"

echo "[smoke] waiting for discovery cache and auto-route"
for i in $(seq 1 60); do
  services_json="$(curl -fsS http://127.0.0.1:8444/services || true)"
  routes_json="$(curl -fsS http://127.0.0.1:8444/routes || true)"

  if echo "$services_json" | grep -Eq '"count"[[:space:]]*:[[:space:]]*1' && \
     echo "$routes_json" | grep -q '"hostname":"myapi"'; then
    break
  fi

  if [[ "$i" == "60" ]]; then
    echo "[smoke] discovery not ready"
    echo "services: $services_json"
    echo "routes:   $routes_json"
    exit 1
  fi
  sleep 1
done

echo "[smoke] running end-to-end request through edge gateway"
payload="hello-compose"
payload_b64="$(printf '%s' "$payload" | base64)"
resp_headers="$(mktemp)"
resp_body="$(mktemp)"

http_code="$(curl -sS -o "$resp_body" -D "$resp_headers" -w "%{http_code}" \
  -H "Host: myapi" \
  -H "Content-Type: text/plain" \
  --data "$payload" \
  "http://127.0.0.1:8443/v1/dummy?from=compose")"

if [[ "$http_code" != "200" ]]; then
  echo "[smoke] expected HTTP 200, got $http_code"
  echo "--- headers ---"
  cat "$resp_headers"
  echo "--- body ---"
  cat "$resp_body"
  exit 1
fi

if ! grep -q '"method":"POST"' "$resp_body"; then
  echo "[smoke] response does not contain expected method"
  cat "$resp_body"
  exit 1
fi
if ! grep -q '"path":"/v1/dummy"' "$resp_body"; then
  echo "[smoke] response does not contain expected path"
  cat "$resp_body"
  exit 1
fi
if ! grep -q '"raw_query":"from=compose"' "$resp_body"; then
  echo "[smoke] response does not contain expected query"
  cat "$resp_body"
  exit 1
fi
if ! grep -q "\"body_b64\":\"$payload_b64\"" "$resp_body"; then
  echo "[smoke] response does not contain expected body payload"
  cat "$resp_body"
  exit 1
fi

echo "[smoke] PASS: compose stack is runnable and end-to-end path works"
