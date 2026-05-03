#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE="${COMPOSE_CMD:-docker compose -f docker-compose.nat.yml}"
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

compose_build_serial() {
  if $COMPOSE build --help 2>/dev/null | grep -q -- "--no-parallel"; then
    $COMPOSE build --no-parallel
    return
  fi
  COMPOSE_PARALLEL_LIMIT=1 $COMPOSE build
}

echo "[smoke-nat] docker compose build"
compose_build_serial

echo "[smoke-nat] docker compose up -d"
$COMPOSE up -d --remove-orphans

echo "[smoke-nat] waiting for health endpoints"
wait_http_ok "http://127.0.0.1:8443/healthz"
wait_http_ok "http://127.0.0.1:8444/healthz"
wait_http_ok "http://127.0.0.1:8091/healthz"
wait_http_ok "http://127.0.0.1:8092/healthz"

echo "[smoke-nat] waiting for discovery cache and auto-route"
for i in $(seq 1 60); do
  services_json="$(curl -fsS http://127.0.0.1:8444/services || true)"
  routes_json="$(curl -fsS http://127.0.0.1:8444/routes || true)"

  if echo "$services_json" | grep -Eq '"count"[[:space:]]*:[[:space:]]*1' && \
     echo "$routes_json" | grep -q '"hostname":"myapi"'; then
    break
  fi

  if [[ "$i" == "60" ]]; then
    echo "[smoke-nat] discovery not ready"
    echo "services: $services_json"
    echo "routes:   $routes_json"
    exit 1
  fi
  sleep 1
done

echo "[smoke-nat] proving known-string fetch through relayed path"
known_body="$(mktemp)"
known_code="$(curl -sS -o "$known_body" -w "%{http_code}" \
  -H "Host: myapi" \
  "http://127.0.0.1:8443/known.txt")"

if [[ "$known_code" != "200" ]]; then
  echo "[smoke-nat] expected HTTP 200 for known string, got $known_code"
  cat "$known_body"
  exit 1
fi

if [[ "$(tr -d '\r\n' < "$known_body")" != "compose-nat-known-ok" ]]; then
  echo "[smoke-nat] known string mismatch"
  cat "$known_body"
  exit 1
fi

echo "[smoke-nat] running end-to-end request through tubo gateway"
payload="hello-relay-nat"
payload_b64="$(printf '%s' "$payload" | base64)"
resp_body="$(mktemp)"
http_code="$(curl -sS -o "$resp_body" -w "%{http_code}" \
  -H "Host: myapi" \
  -H "Content-Type: text/plain" \
  --data "$payload" \
  "http://127.0.0.1:8443/v1/dummy?from=relay-nat")"

if [[ "$http_code" != "200" ]]; then
  echo "[smoke-nat] expected HTTP 200, got $http_code"
  cat "$resp_body"
  exit 1
fi

if ! grep -q '"method":"POST"' "$resp_body"; then
  echo "[smoke-nat] response does not contain expected method"
  cat "$resp_body"
  exit 1
fi
if ! grep -q '"path":"/v1/dummy"' "$resp_body"; then
  echo "[smoke-nat] response does not contain expected path"
  cat "$resp_body"
  exit 1
fi
if ! grep -q '"raw_query":"from=relay-nat"' "$resp_body"; then
  echo "[smoke-nat] response does not contain expected query"
  cat "$resp_body"
  exit 1
fi
if ! grep -q "\"body_b64\":\"$payload_b64\"" "$resp_body"; then
  echo "[smoke-nat] response does not contain expected body payload"
  cat "$resp_body"
  exit 1
fi

echo "[smoke-nat] verifying relay fallback path from edge logs"
edge_logs="$($COMPOSE logs edge --no-color 2>/dev/null || true)"
if ! grep -q 'connection_path=relayed' <<<"$edge_logs"; then
  echo "[smoke-nat] expected relayed connection path in edge logs"
  printf '%s\n' "$edge_logs"
  exit 1
fi

echo "[smoke-nat] PASS: isolated-network relay scenario works end-to-end"
