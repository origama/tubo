#!/usr/bin/env bash
# Journey A canonical integration test: Host A shares HTTP fixture via exact
# invite; Host B connects and verifies connectivity through the local listener.
# No Scope, no discovery, no authority process. Only a local relay for
# connectivity.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tubo-journey-a.XXXXXX")"
PIDS=()
FAILED=0

cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  sleep 0.2
  for pid in "${PIDS[@]}"; do kill -9 "$pid" 2>/dev/null || true; done
  rm -rf "$WORK"
}

trap cleanup EXIT INT TERM

# ---- Port allocation ----
read -r RELAY_PORT HTTP_PORT CLIENT_PORT < <(python3 - <<'PY'
import socket
ports=[]
for _ in range(3):
    s=socket.socket(); s.bind(('127.0.0.1',0)); ports.append(s.getsockname()[1]); s.close()
print(*ports)
PY
)

# ---- Build binaries ----
mkdir -p "$WORK/bin"
(cd "$ROOT" && go build -o "$WORK/bin/tubo" ./cmd/tubo && go build -o "$WORK/bin/http" ./cmd/test-http-server) 2>&1
T="$WORK/bin/tubo"

# ---- Helper: start a background process ----
start_bg() {
  local home="$1" log="$2"; shift 2
  env TUBO_HOME="$home" "$@" >"$WORK/$log.log" 2>&1 &
  local pid="$!"
  PIDS+=("$pid")
}

# ---- Helper: wait for a TCP port ----
wait_port() {
  local port="$1" label="$2"
  for _ in $(seq 1 50); do
    if python3 -c "import socket; s=socket.socket(); s.settimeout(0.5); s.connect(('127.0.0.1',$port)); s.close()" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  echo "FAIL: timeout waiting for $label on port $port" >&2
  return 1
}

# ---- Helper: wait for a JSON condition ----
wait_json() {
  local home="$1" cmd="$2" expr="$3"
  for _ in $(seq 1 100); do
    if output="$(env TUBO_HOME="$home" sh -c "$cmd" 2>/dev/null)" && printf '%s' "$output" | python3 -c "import json,sys; value=json.load(sys.stdin); assert $expr" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

# ============================================================
# 1. Generate swarm key and start relay
# ============================================================
echo "--- Step 1: Start relay"
RELAY_HOME="$WORK/relay"
mkdir -p "$RELAY_HOME"
env TUBO_HOME="$RELAY_HOME" "$T" keygen swarm --out "$WORK/swarm.key" >/dev/null
start_bg "$RELAY_HOME" "relay" "$T" relay --listen "/ip4/127.0.0.1/tcp/$RELAY_PORT" --public-addr "/ip4/127.0.0.1/tcp/$RELAY_PORT" --swarm-key "$WORK/swarm.key"
wait_json "$RELAY_HOME" "'$T' inspect relay/default --json" "value['status']=='ready'" >/dev/null
RELAY_PEER="$(env TUBO_HOME="$RELAY_HOME" "$T" inspect relay/default --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["peer_id"])')"
RELAY="/ip4/127.0.0.1/tcp/$RELAY_PORT/p2p/$RELAY_PEER"
echo "OK: relay ready ($RELAY)"

# ============================================================
# 2. Host A: join overlay, start HTTP fixture, attach, share
# ============================================================
echo "--- Step 2: Host A setup"
HOST_A="$WORK/host-a"
mkdir -p "$HOST_A"

# Join overlay (no scope, no discovery - just libp2p connectivity).
env TUBO_HOME="$HOST_A" "$T" join overlay --relay "$RELAY" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
echo "OK: Host A joined overlay"

# Start HTTP fixture.
start_bg "$HOST_A" "http" "$WORK/bin/http" --listen "127.0.0.1:$HTTP_PORT" --instance journey-a-origin
wait_port "$HTTP_PORT" "HTTP fixture"
echo "OK: HTTP fixture ready on port $HTTP_PORT"

# Attach with -d (creates service + pipe + starts daemon).
env TUBO_HOME="$HOST_A" "$T" attach "http://127.0.0.1:$HTTP_PORT" --name "origin" -d 2>/dev/null
wait_json "$HOST_A" "'$T' inspect pipe/origin --json" "value['status']=='ready'" >/dev/null
echo "OK: Host A attached and origin pipe ready"

# Share mints exact invite.
INVITE="$(env TUBO_HOME="$HOST_A" "$T" share pipe/origin 2>/dev/null)"
if [[ -z "$INVITE" ]]; then
  echo "FAIL: empty invite from share" >&2; exit 1
fi
echo "OK: Host A shared invite (len=${#INVITE})"

# ============================================================
# 3. Host B: connect via exact invite
# ============================================================
echo "--- Step 3: Host B connect"
HOST_B="$WORK/host-b"
mkdir -p "$HOST_B"

# Join overlay (no scope).
env TUBO_HOME="$HOST_B" "$T" join overlay --relay "$RELAY" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
echo "OK: Host B joined overlay"

# Connect redeems invite with a friendly local Pipe name, creates local
# pipe, starts daemon.
# stderr (progress/hints) is captured separately: --json stdout must be exactly
# one parseable JSON value.
OUTPUT="$(env TUBO_HOME="$HOST_B" "$T" connect --token "$INVITE" --local "127.0.0.1:$CLIENT_PORT" --name west -d --json 2>"$WORK/connect-hb.err" || true)"
if [[ -z "$OUTPUT" ]]; then
  echo "FAIL: connect produced no output" >&2
  ls -la "$HOST_B/logs/" 2>/dev/null >&2 || true
  cat "$HOST_B/logs/"*.log 2>/dev/null | head -40 >&2 || true
  exit 1
fi
if ! printf '%s' "$OUTPUT" | python3 -c 'import json,sys; json.load(sys.stdin)' 2>/dev/null; then
  echo "FAIL: connect output not JSON: $OUTPUT" >&2
  ls -la "$HOST_B/logs/" 2>/dev/null >&2 || true
  cat "$HOST_B/logs/"*.log 2>/dev/null | head -40 >&2 || true
  exit 1
fi
CONNECT_PID="$(printf '%s' "$OUTPUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["pid"])')"
PIDS+=("$CONNECT_PID")
# The friendly name must produce a stable local Pipe ref.
ref="$(printf '%s' "$OUTPUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["ref"])')"
if [[ "$ref" != "pipe/west" ]]; then
  echo "FAIL: connect --name ref = $ref, want pipe/west" >&2; exit 1
fi
wait_json "$HOST_B" "'$T' inspect pipe/west --json" "value['status']=='ready'" >/dev/null
echo "OK: Host B connect daemon pid=$CONNECT_PID ref=$ref"

# Wait for the local listener to be ready.
wait_port "$CLIENT_PORT" "client listener" || {
  echo "--- Host B logs ---"
  ls -la "$HOST_B/logs/" 2>/dev/null || true
  cat "$HOST_B/logs/"*.log 2>/dev/null | head -50 || true
  exit 1
}
echo "OK: client listener ready on port $CLIENT_PORT"

# ============================================================
# 4. HTTP request through the tunnel
# ============================================================
echo "--- Step 4: HTTP request through tunnel"
RESPONSE="$(curl -fsS -X POST -H 'X-Tubo-Test: integration' --data 'hello-world' "http://127.0.0.1:$CLIENT_PORT/v1/check?from=journey-a" 2>/dev/null)"
if [[ -z "$RESPONSE" ]]; then
  echo "FAIL: tunnel request returned empty" >&2; exit 1
fi
printf '%s' "$RESPONSE" | python3 -c "
import json, sys, base64
value = json.load(sys.stdin)
assert value['instance'] == 'journey-a-origin', f'instance: {value[\"instance\"]}'
assert value['method'] == 'POST', f'method: {value[\"method\"]}'
assert value['raw_query'] == 'from=journey-a', f'query: {value[\"raw_query\"]}'
assert value['headers'].get('X-Tubo-Test') == ['integration'], f'headers: {value[\"headers\"]}'
assert base64.b64decode(value['body_b64']) == b'hello-world', f'body: {value[\"body_b64\"]}'
print('OK: HTTP request through tunnel verified')
"

# ============================================================
# 5. Large body test
# ============================================================
echo "--- Step 5: Large body through tunnel"
python3 - "$CLIENT_PORT" <<'PY'
import base64, json, sys, urllib.request
payload = bytes(range(256)) * 1024
request = urllib.request.Request(f"http://127.0.0.1:{sys.argv[1]}/large", data=payload, method="POST")
with urllib.request.urlopen(request, timeout=20) as response:
    value = json.load(response)
assert base64.b64decode(value["body_b64"]) == payload, "large body mismatch"
print("OK: large body verified")
PY

# ============================================================
# 6. Negative: invalid invite
# ============================================================
echo "--- Step 6: Negative - invalid invite"
HOST_BAD="$WORK/host-bad"
mkdir -p "$HOST_BAD"
env TUBO_HOME="$HOST_BAD" "$T" join overlay --relay "$RELAY" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
if env TUBO_HOME="$HOST_BAD" "$T" connect --token "AAAA" --local "127.0.0.1:0" -d --json 2>"$WORK/bad-connect.err"; then
  echo "FAIL: invalid invite accepted" >&2; exit 1
fi
if ! grep -q "authorization_denied" "$WORK/bad-connect.err" 2>/dev/null && ! grep -q "invalid" "$WORK/bad-connect.err" 2>/dev/null; then
  echo "FAIL: unexpected error for invalid invite: $(cat "$WORK/bad-connect.err")" >&2; exit 1
fi
echo "OK: invalid invite rejected"

# ============================================================
# 7. Negative: tampered invite
# ============================================================
echo "--- Step 7: Negative - tampered invite"
TAMPERED="${INVITE:0:${#INVITE}-1}X"
HOST_TAMPER="$WORK/host-tamper"
mkdir -p "$HOST_TAMPER"
env TUBO_HOME="$HOST_TAMPER" "$T" join overlay --relay "$RELAY" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
if env TUBO_HOME="$HOST_TAMPER" "$T" connect --token "$TAMPERED" --local "127.0.0.1:0" -d --json 2>"$WORK/tamper-connect.err"; then
  echo "FAIL: tampered invite accepted" >&2; exit 1
fi
if ! grep -q "authorization_denied" "$WORK/tamper-connect.err" 2>/dev/null && ! grep -q "invalid" "$WORK/tamper-connect.err" 2>/dev/null; then
  echo "FAIL: unexpected error for tampered invite: $(cat "$WORK/tamper-connect.err")" >&2; exit 1
fi
echo "OK: tampered invite rejected"

# ============================================================
# 8. Cleanup
# ============================================================
echo "--- Step 8: Cleanup"
kill -9 "$CONNECT_PID" 2>/dev/null || true
sleep 0.2
echo "OK: cleanup done"

echo ""
echo "[integration] Journey A private-http PASS"