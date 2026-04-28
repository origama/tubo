#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE="${COMPOSE_CMD:-docker compose} -f docker-compose.tubo.yml"
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

mkdir -p generated/tubo-smoke

relay_id="$(go run ./cmd/tubo id from-seed relay-demo-seed)"
edge_id="$(go run ./cmd/tubo id from-seed edge-demo-seed)"
relay_addr="/dns4/tubo-relay/tcp/4002/p2p/${relay_id}"
edge_addr="/dns4/tubo-edge/tcp/4001/p2p/${edge_id}"

cat > generated/tubo-smoke/relay.yaml <<YAML
role: relay
node:
  seed: relay-demo-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4002
relay:
  health_listen: :8092
  enable_relay_service: true
  enable_autonat_service: true
  enable_discovery_pubsub: true
  force_reachability_public: true
  print_run_commands: false
YAML

cat > generated/tubo-smoke/edge.yaml <<YAML
role: edge
node:
  seed: edge-demo-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
network:
  relay_peers:
    - ${relay_addr}
edge:
  listen: :8443
  admin_listen: :8444
  direct_stream_timeout: 750ms
YAML

cat > generated/tubo-smoke/service.yaml <<YAML
role: service
node:
  seed: service-demo-seed
  p2p_listen: /ip4/0.0.0.0/tcp/40123
network:
  bootstrap_peers:
    - ${edge_addr}
    - ${relay_addr}
  relay_peers:
    - ${relay_addr}
  autorelay: true
  hole_punching: true
service:
  name: myapi
  target: http://tubo-dummy-api-server:8000
health_listen: :8091
heartbeat_interval: 5s
YAML

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
$COMPOSE up -d

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
