#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

WORK_DIR="$(mktemp -d /tmp/tubo-cli-ux-smoke.XXXXXX)"
BIN="$WORK_DIR/tubo"
DUMMY_BIN="$WORK_DIR/dummy-api-server"
export XDG_CONFIG_HOME="$WORK_DIR/config"
export XDG_DATA_HOME="$WORK_DIR/data"
mkdir -p "$XDG_CONFIG_HOME" "$XDG_DATA_HOME"

connect_pid=""
fg_attach_pid=""
dummy_pid=""

free_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
}

wait_http_ok() {
  local url="$1"
  local tries="${2:-80}"
  local i
  for i in $(seq 1 "$tries"); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  echo "[smoke-cli-ux] timeout waiting for $url"
  return 1
}

wait_file_nonempty() {
  local path="$1"
  local tries="${2:-80}"
  local i
  for i in $(seq 1 "$tries"); do
    if [[ -s "$path" ]]; then
      return 0
    fi
    sleep 0.25
  done
  echo "[smoke-cli-ux] timeout waiting for non-empty file $path"
  return 1
}

assert_contains() {
  local needle="$1"
  local path="$2"
  if ! rg -F -- "$needle" "$path" >/dev/null 2>&1; then
    echo "[smoke-cli-ux] expected to find: $needle"
    echo "[smoke-cli-ux] in file: $path"
    echo "--- $path ---"
    cat "$path" || true
    exit 1
  fi
}

cleanup() {
  set +e
  if [[ -n "$connect_pid" ]]; then
    kill "$connect_pid" >/dev/null 2>&1 || true
    wait "$connect_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n "$fg_attach_pid" ]]; then
    kill "$fg_attach_pid" >/dev/null 2>&1 || true
    wait "$fg_attach_pid" >/dev/null 2>&1 || true
  fi
  for ref in process/gateway-default process/attach-lmstudio process/relay-default; do
    "$BIN" stop "$ref" >/dev/null 2>&1 || true
  done
  if [[ -n "$dummy_pid" ]]; then
    kill "$dummy_pid" >/dev/null 2>&1 || true
    wait "$dummy_pid" >/dev/null 2>&1 || true
  fi
  "$BIN" rm --stale >/dev/null 2>&1 || true
  if [[ "${KEEP_WORK:-0}" != "1" ]]; then
    rm -rf "$WORK_DIR"
  else
    echo "[smoke-cli-ux] preserved work dir: $WORK_DIR"
  fi
}
trap cleanup EXIT

echo "[smoke-cli-ux] building local binaries"
go build -o "$BIN" ./cmd/tubo
go build -o "$DUMMY_BIN" ./cmd/dummy-api-server

relay_port="$(free_port)"
relay_health_port="$(free_port)"
dummy_port="$(free_port)"
service_health_port="$(free_port)"
foreground_service_health_port="$(free_port)"
gateway_port="$(free_port)"
gateway_admin_port="$(free_port)"
connect_port="$(free_port)"
relay_seed="cli-ux-relay-seed"
relay_id="$("$BIN" id from-seed "$relay_seed" | tr -d '\n')"
relay_addr="/ip4/127.0.0.1/tcp/$relay_port/p2p/$relay_id"
swarm_key="$WORK_DIR/swarm.key"

echo "[smoke-cli-ux] generating swarm key"
"$BIN" keygen swarm --out "$swarm_key"

echo "[smoke-cli-ux] starting dummy local HTTP target"
DUMMY_API_LISTEN="127.0.0.1:$dummy_port" DUMMY_API_INSTANCE="lmstudio" "$DUMMY_BIN" >"$WORK_DIR/dummy.log" 2>&1 &
dummy_pid=$!
wait_http_ok "http://127.0.0.1:$dummy_port/healthz"

echo "[smoke-cli-ux] starting detached relay"
XDG_CONFIG_HOME="$WORK_DIR/relay-config" \
RELAY_HEALTH_LISTEN="127.0.0.1:$relay_health_port" \
  "$BIN" relay \
  --seed "$relay_seed" \
  --listen "/ip4/127.0.0.1/tcp/$relay_port" \
  --public-addr "/ip4/127.0.0.1/tcp/$relay_port" \
  --swarm-key "$swarm_key" \
  -d >"$WORK_DIR/relay.out"
assert_contains "relay running" "$WORK_DIR/relay.out"
assert_contains "process/relay-default" "$WORK_DIR/relay.out"
wait_http_ok "http://127.0.0.1:$relay_health_port/healthz"

echo "[smoke-cli-ux] joining swarm config"
"$BIN" join --relay "$relay_addr" --swarm-key "$swarm_key" >"$WORK_DIR/join.out"
assert_contains "joined swarm config" "$WORK_DIR/join.out"
assert_contains "tubo get services" "$WORK_DIR/join.out"
[[ -f "$XDG_CONFIG_HOME/tubo/config.yaml" ]]
[[ -f "$XDG_CONFIG_HOME/tubo/swarm.key" ]]

echo "[smoke-cli-ux] starting detached attach publisher"
FORCE_REACHABILITY_PRIVATE=true \
SERVICE_HEALTH_LISTEN="127.0.0.1:$service_health_port" \
  "$BIN" attach "http://127.0.0.1:$dummy_port" \
  --name lmstudio \
  --seed cli-ux-lmstudio-seed \
  --p2p-listen /ip4/127.0.0.1/tcp/0 \
  --heartbeat-interval 1s \
  -d >"$WORK_DIR/attach.out"
assert_contains "attached service \"lmstudio\"" "$WORK_DIR/attach.out"
assert_contains "process/attach-lmstudio" "$WORK_DIR/attach.out"
wait_http_ok "http://127.0.0.1:$service_health_port/healthz"

# process management happy path
"$BIN" ps >"$WORK_DIR/ps.out"
assert_contains "attach-lmstudio" "$WORK_DIR/ps.out"
assert_contains "relay-default" "$WORK_DIR/ps.out"
"$BIN" get processes --json >"$WORK_DIR/get-processes.json"
python3 - "$WORK_DIR/get-processes.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    payload = json.load(f)
assert payload['count'] >= 2, payload
names = {item['name'] for item in payload['items']}
assert 'attach-lmstudio' in names, payload
PY
"$BIN" describe process/attach-lmstudio >"$WORK_DIR/describe-process.out"
assert_contains "Name: attach-lmstudio" "$WORK_DIR/describe-process.out"
assert_contains "Command: attach" "$WORK_DIR/describe-process.out"
assert_contains "Service: lmstudio" "$WORK_DIR/describe-process.out"
"$BIN" inspect process/attach-lmstudio --json >"$WORK_DIR/inspect-process.json"
python3 - "$WORK_DIR/inspect-process.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    payload = json.load(f)
assert payload['status'] == 'running', payload
assert payload['state']['id'] == 'process/attach-lmstudio', payload
assert payload['state']['service'] == 'lmstudio', payload
PY
wait_file_nonempty "$XDG_DATA_HOME/tubo/logs/attach-lmstudio.log"
"$BIN" logs process/attach-lmstudio >"$WORK_DIR/logs-process.out"

# resource discovery without a local gateway cache
"$BIN" get services --timeout 8s >"$WORK_DIR/get-services-live.out"
assert_contains "no local cache found" "$WORK_DIR/get-services-live.out"
assert_contains "starting temporary observer" "$WORK_DIR/get-services-live.out"
assert_contains "lmstudio" "$WORK_DIR/get-services-live.out"
"$BIN" get service/lmstudio >"$WORK_DIR/get-service.out"
assert_contains "lmstudio" "$WORK_DIR/get-service.out"
"$BIN" describe service/lmstudio >"$WORK_DIR/describe-service.out"
assert_contains "Name: lmstudio" "$WORK_DIR/describe-service.out"
assert_contains "Kind: service" "$WORK_DIR/describe-service.out"
assert_contains "Observed from:" "$WORK_DIR/describe-service.out"
"$BIN" inspect service/lmstudio --json >"$WORK_DIR/inspect-service.json"
python3 - "$WORK_DIR/inspect-service.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    payload = json.load(f)
assert payload['item']['name'] == 'lmstudio', payload
assert payload['item']['kind'] == 'service', payload
assert payload['item']['status'] == 'online', payload
PY

echo "[smoke-cli-ux] starting foreground connect command"
"$BIN" connect lmstudio --local "127.0.0.1:$connect_port" >"$WORK_DIR/connect.out" 2>&1 &
connect_pid=$!
for i in $(seq 1 80); do
  if curl -fsS "http://127.0.0.1:$connect_port/healthz" >/dev/null 2>&1; then
    break
  fi
  if ! kill -0 "$connect_pid" >/dev/null 2>&1; then
    echo "[smoke-cli-ux] connect exited early"
    cat "$WORK_DIR/connect.out" || true
    exit 1
  fi
  sleep 0.25
done
wait_http_ok "http://127.0.0.1:$connect_port/healthz"
connect_body="$WORK_DIR/connect-body.json"
connect_code="$(curl -sS -o "$connect_body" -w '%{http_code}' -X POST -d 'hello-connect' "http://127.0.0.1:$connect_port/v1/dummy?from=connect")"
if [[ "$connect_code" != "200" ]]; then
  echo "[smoke-cli-ux] expected HTTP 200 via connect, got $connect_code"
  cat "$connect_body" || true
  exit 1
fi
assert_contains '"instance":"lmstudio"' "$connect_body"
assert_contains '"raw_query":"from=connect"' "$connect_body"

echo "[smoke-cli-ux] starting detached gateway"
"$BIN" gateway --listen "127.0.0.1:$gateway_port" --admin-listen "127.0.0.1:$gateway_admin_port" -d >"$WORK_DIR/gateway.out"
assert_contains "gateway running" "$WORK_DIR/gateway.out"
assert_contains "process/gateway-default" "$WORK_DIR/gateway.out"
wait_http_ok "http://127.0.0.1:$gateway_admin_port/healthz"
wait_http_ok "http://127.0.0.1:$gateway_port/healthz"

for i in $(seq 1 80); do
  services_json="$(curl -fsS "http://127.0.0.1:$gateway_admin_port/services" || true)"
  if printf '%s' "$services_json" | rg -F '"name":"lmstudio"' >/dev/null 2>&1; then
    break
  fi
  sleep 0.25
done
services_json="$(curl -fsS "http://127.0.0.1:$gateway_admin_port/services")"
printf '%s' "$services_json" | rg -F '"name":"lmstudio"' >/dev/null

cat >"$WORK_DIR/gateway-cache-config.yaml" <<EOF
network:
  private_key_file: $XDG_CONFIG_HOME/tubo/swarm.key
  bootstrap_peers:
    - $relay_addr
  relay_peers:
    - $relay_addr
  autorelay: true
  hole_punching: true
edge:
  admin_listen: 127.0.0.1:$gateway_admin_port
EOF

"$BIN" get services --config "$WORK_DIR/gateway-cache-config.yaml" >"$WORK_DIR/get-services-cache.out"
assert_contains "using local cache from edge admin at 127.0.0.1:$gateway_admin_port" "$WORK_DIR/get-services-cache.out"
assert_contains "lmstudio" "$WORK_DIR/get-services-cache.out"

gateway_body="$WORK_DIR/gateway-body.json"
gateway_headers="$WORK_DIR/gateway-headers.txt"
gateway_code="$(curl -sS -o "$gateway_body" -D "$gateway_headers" -w '%{http_code}' -H 'Host: lmstudio' -X POST -d 'hello-gateway' "http://127.0.0.1:$gateway_port/v1/dummy?from=gateway")"
if [[ "$gateway_code" != "200" ]]; then
  echo "[smoke-cli-ux] expected HTTP 200 via gateway, got $gateway_code"
  echo '--- headers ---'
  cat "$gateway_headers" || true
  echo '--- body ---'
  cat "$gateway_body" || true
  exit 1
fi
assert_contains '"instance":"lmstudio"' "$gateway_body"
assert_contains '"raw_query":"from=gateway"' "$gateway_body"

echo "[smoke-cli-ux] validating foreground-by-default attach"
FORCE_REACHABILITY_PRIVATE=true \
SERVICE_HEALTH_LISTEN="127.0.0.1:$foreground_service_health_port" \
  "$BIN" attach "http://127.0.0.1:$dummy_port" \
  --name ollama \
  --seed cli-ux-ollama-seed \
  --p2p-listen /ip4/127.0.0.1/tcp/0 \
  --heartbeat-interval 1s >"$WORK_DIR/attach-foreground.out" 2>&1 &
fg_attach_pid=$!
wait_http_ok "http://127.0.0.1:$foreground_service_health_port/healthz"
wait_file_nonempty "$WORK_DIR/attach-foreground.out"
assert_contains 'service agent config service="ollama"' "$WORK_DIR/attach-foreground.out"
kill "$fg_attach_pid"
wait "$fg_attach_pid" >/dev/null 2>&1 || true
fg_attach_pid=""

echo "[smoke-cli-ux] stopping detached attach and cleaning stale state"
"$BIN" stop process/attach-lmstudio >"$WORK_DIR/stop-attach.out"
assert_contains "stopped process/attach-lmstudio" "$WORK_DIR/stop-attach.out"
"$BIN" rm --stale >"$WORK_DIR/rm-stale.out"
assert_contains "removed 1 stale process artifacts" "$WORK_DIR/rm-stale.out"

echo "[smoke-cli-ux] PASS"
