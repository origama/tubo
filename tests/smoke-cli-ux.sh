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
detached_connect_ref=""
fg_attach_pid=""
fg_gateway_pid=""
fg_relay_pid=""
grants_pid=""
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

wait_process_registered() {
  local name="$1"
  local tries="${2:-80}"
  local i
  for i in $(seq 1 "$tries"); do
    if "$BIN" ps | grep -F -- "$name" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  echo "[smoke-cli-ux] timeout waiting for process registration: $name"
  "$BIN" ps || true
  return 1
}

assert_contains() {
  local needle="$1"
  local path="$2"
  if ! grep -F -- "$needle" "$path" >/dev/null 2>&1; then
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
  if [[ -n "$fg_gateway_pid" ]]; then
    kill "$fg_gateway_pid" >/dev/null 2>&1 || true
    wait "$fg_gateway_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n "$fg_relay_pid" ]]; then
    kill "$fg_relay_pid" >/dev/null 2>&1 || true
    wait "$fg_relay_pid" >/dev/null 2>&1 || true
  fi
  if [[ -n "$grants_pid" ]]; then
    kill "$grants_pid" >/dev/null 2>&1 || true
    wait "$grants_pid" >/dev/null 2>&1 || true
  fi
  for ref in process/gateway-default process/attach-lmstudio process/relay-default process/grants-serve-lab; do
    "$BIN" stop "$ref" >/dev/null 2>&1 || true
  done
  if [[ -n "$detached_connect_ref" ]]; then
    "$BIN" stop "$detached_connect_ref" >/dev/null 2>&1 || true
  fi
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
detached_connect_port="$(free_port)"
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
mkdir -p "$WORK_DIR/relay-config/tubo"
: >"$WORK_DIR/relay-config/tubo/config.yaml"
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
if grep -F "joined swarm config" "$WORK_DIR/join.out" >/dev/null 2>&1; then
  true
else
  assert_contains "joined manual overlay" "$WORK_DIR/join.out"
fi
assert_contains "tubo get services" "$WORK_DIR/join.out"
[[ -f "$XDG_CONFIG_HOME/tubo/config.yaml" ]]
[[ -f "$XDG_CONFIG_HOME/tubo/swarm.key" ]]

echo "[smoke-cli-ux] creating local cluster metadata"
"$BIN" create cluster/lab >"$WORK_DIR/create-cluster.out"
assert_contains "created cluster \"lab\"" "$WORK_DIR/create-cluster.out"

echo "[smoke-cli-ux] restarting detached relay with cluster config"
"$BIN" stop process/relay-default >/dev/null 2>&1 || true
"$BIN" rm --stale >/dev/null 2>&1 || true
XDG_CONFIG_HOME="$WORK_DIR/config" \
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

echo "[smoke-cli-ux] starting detached cluster authority"
"$BIN" start cluster/lab >"$WORK_DIR/start-cluster.out"
assert_contains 'started cluster authority for cluster "lab"' "$WORK_DIR/start-cluster.out"
assert_contains 'process/grants-serve-lab' "$WORK_DIR/start-cluster.out"
wait_process_registered "grants-serve-lab"

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
for i in $(seq 1 20); do
  "$BIN" get services --system --timeout 8s >"$WORK_DIR/get-services-system.out" 2>&1
  if grep -F "grant-service" "$WORK_DIR/get-services-system.out" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
assert_contains "local discovery cache unavailable" "$WORK_DIR/get-services-system.out"
assert_contains "querying cluster discovery peer" "$WORK_DIR/get-services-system.out"
assert_contains "grant-service" "$WORK_DIR/get-services-system.out"
for i in $(seq 1 20); do
  "$BIN" get services --timeout 8s >"$WORK_DIR/get-services-live.out" 2>&1
  if grep -F "lmstudio" "$WORK_DIR/get-services-live.out" >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
assert_contains "local discovery cache unavailable" "$WORK_DIR/get-services-live.out"
assert_contains "querying cluster discovery peer" "$WORK_DIR/get-services-live.out"
assert_contains "received 2 records from cluster discovery authority" "$WORK_DIR/get-services-live.out"
assert_contains "lmstudio" "$WORK_DIR/get-services-live.out"
"$BIN" get service/lmstudio >"$WORK_DIR/get-service.out" 2>&1
assert_contains "querying cluster discovery peer" "$WORK_DIR/get-service.out"
assert_contains "received service lmstudio" "$WORK_DIR/get-service.out"
assert_contains "lmstudio" "$WORK_DIR/get-service.out"
"$BIN" describe service/lmstudio >"$WORK_DIR/describe-service.out" 2>&1
assert_contains "Name: lmstudio" "$WORK_DIR/describe-service.out"
assert_contains "Kind: service" "$WORK_DIR/describe-service.out"
assert_contains "Dial policy:" "$WORK_DIR/describe-service.out"
assert_contains "preferred: direct" "$WORK_DIR/describe-service.out"
assert_contains "fallback: relay" "$WORK_DIR/describe-service.out"
assert_contains "  Direct:" "$WORK_DIR/describe-service.out"
assert_contains "  Relayed:" "$WORK_DIR/describe-service.out"
assert_contains "Observed from:" "$WORK_DIR/describe-service.out"
assert_contains "querying cluster discovery peer" "$WORK_DIR/describe-service.out"
"$BIN" inspect service/lmstudio --json >"$WORK_DIR/inspect-service.json"
python3 - "$WORK_DIR/inspect-service.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    payload = json.load(f)
assert payload['mode'] == 'remote-query', payload
assert payload['metadata']['served_by_role'] == 'authority', payload
assert payload['item']['name'] == 'lmstudio', payload
assert payload['item']['kind'] == 'service', payload
assert payload['item']['status'] == 'online', payload
assert payload['item']['path'] == 'direct', payload
assert payload['item']['direct_addresses'], payload
assert payload['item']['relayed_addresses'], payload
PY

echo "[smoke-cli-ux] minting service share invite"
"$BIN" share service/lmstudio --json >"$WORK_DIR/share-service.json"
share_token="$(python3 - "$WORK_DIR/share-service.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    payload = json.load(f)
print(payload['token'])
PY
)"

echo "[smoke-cli-ux] starting foreground connect command"
"$BIN" connect --token "$share_token" --local "127.0.0.1:$connect_port" >"$WORK_DIR/connect.out" 2>&1 &
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
assert_contains 'connected to service "lmstudio"' "$WORK_DIR/connect.out"
assert_contains "path: relayed" "$WORK_DIR/connect.out"
assert_contains "direct:" "$WORK_DIR/connect.out"
assert_contains "relay:" "$WORK_DIR/connect.out"
wait_process_registered "connect-lmstudio-$connect_port"
"$BIN" describe "process/connect-lmstudio-$connect_port" >"$WORK_DIR/describe-connect-process.out"
assert_contains 'Command: connect' "$WORK_DIR/describe-connect-process.out"
assert_contains 'Source: foreground' "$WORK_DIR/describe-connect-process.out"
assert_contains 'Connect access expires in:' "$WORK_DIR/describe-connect-process.out"
assert_contains 'Connect refresh expires in:' "$WORK_DIR/describe-connect-process.out"
connect_body="$WORK_DIR/connect-body.json"
connect_code="$(curl -sS -o "$connect_body" -w '%{http_code}' -X POST -d 'hello-connect' "http://127.0.0.1:$connect_port/v1/dummy?from=connect")"
if [[ "$connect_code" != "200" ]]; then
  echo "[smoke-cli-ux] expected HTTP 200 via connect, got $connect_code"
  cat "$connect_body" || true
  exit 1
fi
assert_contains '"instance":"lmstudio"' "$connect_body"
assert_contains '"raw_query":"from=connect"' "$connect_body"

echo "[smoke-cli-ux] minting detached-connect share invite"
"$BIN" share service/lmstudio --json >"$WORK_DIR/share-service-detached.json"
detached_share_token="$(python3 - "$WORK_DIR/share-service-detached.json" <<'PY'
import json, sys
with open(sys.argv[1], 'r', encoding='utf-8') as f:
    payload = json.load(f)
print(payload['token'])
PY
)"

echo "[smoke-cli-ux] starting detached connect command"
"$BIN" connect --token "$detached_share_token" --local "127.0.0.1:$detached_connect_port" -d >"$WORK_DIR/connect-detached.out"
detached_connect_ref="process/connect-lmstudio-$detached_connect_port"
assert_contains "$detached_connect_ref" "$WORK_DIR/connect-detached.out"
wait_http_ok "http://127.0.0.1:$detached_connect_port/healthz"
wait_process_registered "connect-lmstudio-$detached_connect_port"
detached_connect_body="$WORK_DIR/connect-detached-body.json"
detached_connect_code="$(curl -sS -o "$detached_connect_body" -w '%{http_code}' -X POST -d 'hello-connect-detached' "http://127.0.0.1:$detached_connect_port/v1/dummy?from=connect-detached")"
if [[ "$detached_connect_code" != "200" ]]; then
  echo "[smoke-cli-ux] expected HTTP 200 via detached connect, got $detached_connect_code"
  cat "$detached_connect_body" || true
  exit 1
fi
assert_contains '"raw_query":"from=connect-detached"' "$detached_connect_body"

echo "[smoke-cli-ux] starting detached gateway"
"$BIN" gateway --listen "127.0.0.1:$gateway_port" --admin-listen "127.0.0.1:$gateway_admin_port" -d >"$WORK_DIR/gateway.out"
assert_contains "gateway running" "$WORK_DIR/gateway.out"
assert_contains "process/gateway-default" "$WORK_DIR/gateway.out"
wait_http_ok "http://127.0.0.1:$gateway_admin_port/healthz"
wait_http_ok "http://127.0.0.1:$gateway_port/healthz"

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
assert_contains 'attached service "ollama"' "$WORK_DIR/attach-foreground.out"
wait_process_registered "attach-ollama"
"$BIN" describe process/attach-ollama >"$WORK_DIR/describe-foreground-attach.out"
assert_contains 'Command: attach' "$WORK_DIR/describe-foreground-attach.out"
assert_contains 'Source: foreground' "$WORK_DIR/describe-foreground-attach.out"
kill "$fg_attach_pid"
wait "$fg_attach_pid" >/dev/null 2>&1 || true
fg_attach_pid=""

echo "[smoke-cli-ux] validating foreground grants serve registration"
"$BIN" stop process/grants-serve-lab >"$WORK_DIR/stop-detached-grants.out"
assert_contains 'stopped process/grants-serve-lab' "$WORK_DIR/stop-detached-grants.out"
"$BIN" grants serve --cluster lab --namespace default --p2p-listen /ip4/0.0.0.0/tcp/0 >"$WORK_DIR/grants-foreground.out" 2>&1 &
grants_pid=$!
wait_file_nonempty "$WORK_DIR/grants-foreground.out"
wait_process_registered "grants-serve-lab"
"$BIN" describe process/grants-serve-lab >"$WORK_DIR/describe-grants-process.out"
assert_contains 'Command: grants serve' "$WORK_DIR/describe-grants-process.out"
assert_contains 'Source: foreground' "$WORK_DIR/describe-grants-process.out"
kill "$grants_pid"
wait "$grants_pid" >/dev/null 2>&1 || true
grants_pid=""

echo "[smoke-cli-ux] validating foreground gateway registration"
"$BIN" stop process/gateway-default >"$WORK_DIR/stop-detached-gateway.out"
assert_contains 'stopped process/gateway-default' "$WORK_DIR/stop-detached-gateway.out"
FG_GATEWAY_PORT="$(free_port)"
FG_GATEWAY_ADMIN_PORT="$(free_port)"
"$BIN" gateway --listen "127.0.0.1:$FG_GATEWAY_PORT" --admin-listen "127.0.0.1:$FG_GATEWAY_ADMIN_PORT" >"$WORK_DIR/gateway-foreground.out" 2>&1 &
fg_gateway_pid=$!
wait_http_ok "http://127.0.0.1:$FG_GATEWAY_ADMIN_PORT/healthz"
wait_http_ok "http://127.0.0.1:$FG_GATEWAY_PORT/healthz"
wait_process_registered "gateway-default"
"$BIN" describe process/gateway-default >"$WORK_DIR/describe-foreground-gateway.out"
assert_contains 'Command: gateway' "$WORK_DIR/describe-foreground-gateway.out"
assert_contains 'Source: foreground' "$WORK_DIR/describe-foreground-gateway.out"
kill "$fg_gateway_pid"
wait "$fg_gateway_pid" >/dev/null 2>&1 || true
fg_gateway_pid=""

echo "[smoke-cli-ux] validating foreground relay registration"
"$BIN" stop process/relay-default >"$WORK_DIR/stop-detached-relay.out"
assert_contains 'stopped process/relay-default' "$WORK_DIR/stop-detached-relay.out"
fg_relay_port="$(free_port)"
fg_relay_health_port="$(free_port)"
TUBO_PROCESS_SOURCE="systemd" \
RELAY_HEALTH_LISTEN="127.0.0.1:$fg_relay_health_port" \
  "$BIN" relay \
  --seed cli-ux-foreground-relay-seed \
  --listen "/ip4/127.0.0.1/tcp/$fg_relay_port" \
  --public-addr "/ip4/127.0.0.1/tcp/$fg_relay_port" >"$WORK_DIR/relay-foreground.out" 2>&1 &
fg_relay_pid=$!
wait_http_ok "http://127.0.0.1:$fg_relay_health_port/healthz"
wait_process_registered "relay-default"
"$BIN" describe process/relay-default >"$WORK_DIR/describe-foreground-relay.out"
assert_contains 'Command: relay' "$WORK_DIR/describe-foreground-relay.out"
assert_contains 'Source: systemd' "$WORK_DIR/describe-foreground-relay.out"
kill "$fg_relay_pid"
wait "$fg_relay_pid" >/dev/null 2>&1 || true
fg_relay_pid=""

echo "[smoke-cli-ux] stopping detached attach and cleaning stale state"
"$BIN" stop process/attach-lmstudio >"$WORK_DIR/stop-attach.out"
assert_contains "stopped process/attach-lmstudio" "$WORK_DIR/stop-attach.out"
if [[ -n "$detached_connect_ref" ]]; then
  "$BIN" stop "$detached_connect_ref" >"$WORK_DIR/stop-connect-detached.out"
  assert_contains "stopped $detached_connect_ref" "$WORK_DIR/stop-connect-detached.out"
  detached_connect_ref=""
fi
"$BIN" rm --stale >"$WORK_DIR/rm-stale.out"
assert_contains "removed " "$WORK_DIR/rm-stale.out"

echo "[smoke-cli-ux] PASS"
