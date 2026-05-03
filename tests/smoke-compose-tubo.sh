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
  local tries="${2:-90}"
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

mkdir -p generated/tubo-smoke
rm -rf generated/tubo-smoke/relay-config generated/tubo-smoke/attach-config generated/tubo-smoke/connect-config generated/tubo-smoke/swarm.key
mkdir -p generated/tubo-smoke/relay-config

echo "[smoke-tubo] generating compose UX config dirs"
go run ./cmd/tubo keygen swarm --out generated/tubo-smoke/swarm.key >/dev/null
relay_id="$(go run ./cmd/tubo id from-seed relay-demo-seed | tr -d '\n')"
relay_addr="/dns4/tubo-relay/tcp/4002/p2p/${relay_id}"

cp generated/tubo-smoke/swarm.key generated/tubo-smoke/relay-config/swarm.key
cat > generated/tubo-smoke/relay-config/config.yaml <<'YAML'
role: relay
network:
  private_key_file: /etc/xdg/tubo/swarm.key
YAML

go run ./cmd/tubo join \
  --force \
  --config-dir generated/tubo-smoke/attach-config \
  --relay "$relay_addr" \
  --swarm-key generated/tubo-smoke/swarm.key >/dev/null

go run ./cmd/tubo join \
  --force \
  --config-dir generated/tubo-smoke/connect-config \
  --relay "$relay_addr" \
  --swarm-key generated/tubo-smoke/swarm.key >/dev/null

sed -i 's|private_key_file: .*|private_key_file: /etc/xdg/tubo/swarm.key|' generated/tubo-smoke/attach-config/config.yaml
sed -i 's|private_key_file: .*|private_key_file: /etc/xdg/tubo/swarm.key|' generated/tubo-smoke/connect-config/config.yaml
chmod 755 generated/tubo-smoke/relay-config generated/tubo-smoke/attach-config generated/tubo-smoke/connect-config
chmod 644 generated/tubo-smoke/relay-config/config.yaml generated/tubo-smoke/relay-config/swarm.key
chmod 644 generated/tubo-smoke/attach-config/config.yaml generated/tubo-smoke/attach-config/swarm.key
chmod 644 generated/tubo-smoke/connect-config/config.yaml generated/tubo-smoke/connect-config/swarm.key

echo "[smoke-tubo] docker compose build"
compose_build_serial

echo "[smoke-tubo] docker compose up -d"
$COMPOSE up -d --remove-orphans

echo "[smoke-tubo] waiting for health endpoints"
wait_http_ok "http://127.0.0.1:8000/healthz"
wait_http_ok "http://127.0.0.1:8091/healthz"
wait_http_ok "http://127.0.0.1:8092/healthz"
wait_http_ok "http://127.0.0.1:18081/healthz"

echo "[smoke-tubo] proving known-string fetch through tubo connect"
known_body="$(mktemp)"
known_code="$(curl -sS -o "$known_body" -w "%{http_code}" "http://127.0.0.1:18081/known.txt")"
if [[ "$known_code" != "200" ]]; then
  echo "[smoke-tubo] expected HTTP 200 for known string, got $known_code"
  cat "$known_body"
  exit 1
fi
if [[ "$(tr -d '\r\n' < "$known_body")" != "tubo-compose-connect-known-ok" ]]; then
  echo "[smoke-tubo] known string mismatch"
  cat "$known_body"
  exit 1
fi

echo "[smoke-tubo] running end-to-end request through tubo connect"
payload="hello-tubo-compose-connect"
payload_b64="$(printf '%s' "$payload" | base64)"
resp_body="$(mktemp)"
http_code="$(curl -sS -o "$resp_body" -w "%{http_code}" \
  -H "Content-Type: text/plain" \
  --data "$payload" \
  "http://127.0.0.1:18081/v1/dummy?from=tubo-compose-connect")"

if [[ "$http_code" != "200" ]]; then
  echo "[smoke-tubo] expected HTTP 200, got $http_code"
  cat "$resp_body"
  exit 1
fi

grep -q '"instance":"myapi"' "$resp_body"
grep -q '"method":"POST"' "$resp_body"
grep -q '"path":"/v1/dummy"' "$resp_body"
grep -q '"raw_query":"from=tubo-compose-connect"' "$resp_body"
grep -q "\"body_b64\":\"$payload_b64\"" "$resp_body"

echo "[smoke-tubo] PASS: compose UX stack works via relay + attach + connect"
