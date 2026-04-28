#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE="${COMPOSE_CMD:-docker compose -f docker-compose.private-overlay-multi-service.yml}"
export DOCKER_BUILDKIT="${DOCKER_BUILDKIT:-0}"
export COMPOSE_DOCKER_CLI_BUILD="${COMPOSE_DOCKER_CLI_BUILD:-0}"

cleanup() {
  $COMPOSE down --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_healthy() {
  local service="$1"
  local tries="${2:-90}"
  local cid status i

  for i in $(seq 1 "$tries"); do
    cid="$($COMPOSE ps -q "$service" 2>/dev/null || true)"
    if [[ -n "$cid" ]]; then
      status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$cid" 2>/dev/null || true)"
      if [[ "$status" == "healthy" || "$status" == "running" ]]; then
        return 0
      fi
    fi
    sleep 1
  done
  return 1
}

assert_contains() {
  local needle="$1"
  local haystack="$2"
  local message="$3"
  if ! grep -Fq "$needle" <<<"$haystack"; then
    echo "[smoke-private-multi] $message"
    echo "$haystack"
    exit 1
  fi
}

request_from_edge() {
  local host="$1"
  local payload="$2"
  local query="$3"

  $COMPOSE exec -T curl-client sh -lc \
    "curl --fail-with-body -sS -H 'Host: $host' -H 'Content-Type: text/plain' --data '$payload' 'http://edge-gateway:8443/v1/dummy?$query'"
}

edge_admin_get() {
  local path="$1"
  $COMPOSE exec -T curl-client sh -lc "curl --fail-with-body -sS 'http://edge-gateway:8444$path'"
}

echo "[smoke-private-multi] docker compose up -d"
$COMPOSE up -d --build

echo "[smoke-private-multi] waiting for healthy services"
for service in \
  p2p-relay \
  edge-gateway \
  dummy-api-server-one service-agent-one \
  dummy-api-server-two service-agent-two \
  dummy-api-server-three service-agent-three; do
  wait_healthy "$service" || {
    echo "[smoke-private-multi] service not healthy: $service"
    $COMPOSE ps
    exit 1
  }
done

echo "[smoke-private-multi] waiting for discovery cache and auto-routes"
for i in $(seq 1 90); do
  services_json="$(edge_admin_get /services || true)"
  routes_json="$(edge_admin_get /routes || true)"

  if echo "$services_json" | grep -Eq '"count"[[:space:]]*:[[:space:]]*3' && \
     echo "$routes_json" | grep -q '"hostname":"svc-one"' && \
     echo "$routes_json" | grep -q '"hostname":"svc-two"' && \
     echo "$routes_json" | grep -q '"hostname":"svc-three"'; then
    break
  fi

  if [[ "$i" == "90" ]]; then
    echo "[smoke-private-multi] discovery not ready"
    echo "services: $services_json"
    echo "routes:   $routes_json"
    exit 1
  fi
  sleep 1
done

echo "[smoke-private-multi] running curl client from edge network against a single edge endpoint"
payload="hello-private-overlay"
payload_b64="$(printf '%s' "$payload" | base64 | tr -d '\n')"

for host in svc-one svc-two svc-three; do
  response="$(request_from_edge "$host" "$payload" "from=private-multi&service=$host")"
  assert_contains '"method":"POST"' "$response" "expected POST response for $host"
  assert_contains '"path":"/v1/dummy"' "$response" "expected /v1/dummy path for $host"
  if [[ "$response" != *"\"raw_query\":\"from=private-multi&service=$host\""* && \
        "$response" != *"\"raw_query\":\"from=private-multi\\u0026service=$host\""* ]]; then
    echo "[smoke-private-multi] expected query echo for $host"
    echo "$response"
    exit 1
  fi
  assert_contains "\"instance\":\"$host\"" "$response" "expected backend instance $host"
  assert_contains "\"body_b64\":\"$payload_b64\"" "$response" "expected payload echo for $host"
done

echo "[smoke-private-multi] PASS: private overlay with 3 services is reachable via one edge endpoint using Host routing"
