#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE="${COMPOSE_CMD:-docker compose} -f tests/e2e/compose/tubo/compose.yml"
export DOCKER_BUILDKIT="${DOCKER_BUILDKIT:-0}"
export COMPOSE_DOCKER_CLI_BUILD="${COMPOSE_DOCKER_CLI_BUILD:-0}"
export TUBO_REPO_ROOT="$ROOT_DIR"

GO_TEST="${GO_TEST_CMD:-go test}"
$GO_TEST ./tests/integration -run '^TestPrepareIntegrationComposeConfig$' -count=1 >/dev/null

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

if [[ "${SMOKE_FORCE_BUILD:-0}" == "1" ]]; then
  echo "[smoke-tubo] forcing image rebuild"
  compose_build_serial
fi

echo "[smoke-tubo] docker compose up -d"
$COMPOSE up -d --remove-orphans

echo "[smoke-tubo] waiting for health endpoints"
wait_http_ok "http://127.0.0.1:8000/healthz"
wait_http_ok "http://127.0.0.1:8443/healthz"
wait_http_ok "http://127.0.0.1:8444/healthz"
wait_http_ok "http://127.0.0.1:8091/healthz"
wait_http_ok "http://127.0.0.1:8092/healthz"

echo "[smoke-tubo] waiting for discovery cache and route"
for i in $(seq 1 75); do
  services_json="$(curl -fsS http://127.0.0.1:8444/services || true)"
  routes_json="$(curl -fsS http://127.0.0.1:8444/routes || true)"
  if echo "$services_json" | grep -Eq '"count"[[:space:]]*:[[:space:]]*1' && \
     echo "$routes_json" | grep -q '"hostname":"myapi"'; then
    break
  fi
  if [[ "$i" == "75" ]]; then
    echo "[smoke-tubo] discovery not ready"
    echo "services: $services_json"
    echo "routes:   $routes_json"
    exit 1
  fi
  sleep 1
done

echo "[smoke-tubo] running end-to-end request"
payload="hello-tubo-compose"
payload_b64="$(printf '%s' "$payload" | base64)"
resp_body="$(mktemp)"
http_code="$(curl -sS -o "$resp_body" -w "%{http_code}" \
  -H "Host: myapi" \
  -H "Content-Type: text/plain" \
  --data "$payload" \
  "http://127.0.0.1:8443/v1/dummy?from=tubo-compose")"

if [[ "$http_code" != "200" ]]; then
  echo "[smoke-tubo] expected HTTP 200, got $http_code"
  cat "$resp_body"
  exit 1
fi

grep -q '"method":"POST"' "$resp_body"
grep -q '"path":"/v1/dummy"' "$resp_body"
grep -q '"raw_query":"from=tubo-compose"' "$resp_body"
grep -q "\"body_b64\":\"$payload_b64\"" "$resp_body"

echo "[smoke-tubo] PASS: tubo compose stack works"
