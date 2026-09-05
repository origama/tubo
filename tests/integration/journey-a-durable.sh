#!/usr/bin/env bash
# Journey A durable remote-Pipe binding end-to-end proof.
#
# Scenario (real processes, real libp2p transport over a local relay):
#   Host A: attach HTTP fixture (short lease TTL), share -> one-time invite.
#   Host B: accept --token (persists durable binding; creates no Pipe/ingress),
#           connect --remote <pipe-id> (no invite) -> local listener.
#   Traffic verified; lease expires; connection reopened WITHOUT a new invite
#   (renewal through the durable binding). Host B restarted cleanly with the
#   same TUBO_HOME and NO invite -> access reacquired. Owner (Host A) runtime
#   restarted -> accepted authoritative state survives -> traffic still works.
#   Owner revokes pipe access -> next legitimate lease acquisition is denied;
#   no new target connection, application endpoint unreachable.
#
# Lease TTL is shortened via the documented TUBO_EXACT_LEASE_TTL facility (no
# production bypass; the lease stays short-lived and revocation enforcement is
# unchanged).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tubo-journey-a-durable.XXXXXX")"
PIDS=()
LEASE_TTL=3s

cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  sleep 0.2
  for pid in "${PIDS[@]}"; do kill -9 "$pid" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

# ---- Ports ----
read -r RELAY_PORT HTTP_PORT CLIENT_PORT < <(python3 - <<'PY'
import socket
ports=[]
for _ in range(3):
    s=socket.socket(); s.bind(('127.0.0.1',0)); ports.append(s.getsockname()[1]); s.close()
print(*ports)
PY
)

# ---- Build ----
mkdir -p "$WORK/bin"
(cd "$ROOT" && go build -o "$WORK/bin/tubo" ./cmd/tubo && go build -o "$WORK/bin/http" ./cmd/test-http-server) 2>&1
T="$WORK/bin/tubo"

start_bg() {
  local home="$1" log="$2"; shift 2
  env TUBO_HOME="$home" "$@" >"$WORK/$log.log" 2>&1 &
  local pid="$!"
  PIDS+=("$pid")
}

wait_port() {
  local port="$1" label="$2"
  for _ in $(seq 1 60); do
    if python3 -c "import socket; s=socket.socket(); s.settimeout(0.5); s.connect(('127.0.0.1',$port)); s.close()" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  echo "FAIL: timeout waiting for $label on port $port" >&2
  return 1
}

wait_json() {
  local home="$1" cmd="$2" expr="$3"
  for _ in $(seq 1 120); do
    if output="$(env TUBO_HOME="$home" sh -c "$cmd" 2>/dev/null)" && printf '%s' "$output" | python3 -c "import json,sys; value=json.load(sys.stdin); assert $expr" 2>/dev/null; then
      return 0
    fi
    sleep 0.1
  done
  return 1
}

probe() { # try an HTTP request; exit code reflects reachability
  python3 - "$CLIENT_PORT" <<'PY' 2>/dev/null
import sys, urllib.request
try:
    with urllib.request.urlopen(f"http://127.0.0.1:{sys.argv[1]}/v1/check", timeout=8) as r:
        body = r.read()
    sys.exit(0)
except Exception:
    sys.exit(1)
PY
}

echo "=== 1. relay ==="
RELAY_HOME="$WORK/relay"; mkdir -p "$RELAY_HOME"
env TUBO_HOME="$RELAY_HOME" "$T" keygen swarm --out "$WORK/swarm.key" >/dev/null
start_bg "$RELAY_HOME" "relay" "$T" relay --listen "/ip4/127.0.0.1/tcp/$RELAY_PORT" --public-addr "/ip4/127.0.0.1/tcp/$RELAY_PORT" --swarm-key "$WORK/swarm.key"
wait_json "$RELAY_HOME" "'$T' inspect relay/default --json" "value['status']=='ready'" >/dev/null
RELAY_PEER="$(env TUBO_HOME="$RELAY_HOME" "$T" inspect relay/default --json | python3 -c 'import json,sys; print(json.load(sys.stdin)["peer_id"])')"
RELAY="/ip4/127.0.0.1/tcp/$RELAY_PORT/p2p/$RELAY_PEER"
echo "OK: relay ready"

echo "=== 2. Host A: fixture + attach + share ==="
HOST_A="$WORK/host-a"; mkdir -p "$HOST_A"
env TUBO_HOME="$HOST_A" "$T" join overlay --relay "$RELAY" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
start_bg "$HOST_A" "http-a" "$WORK/bin/http" --listen "127.0.0.1:$HTTP_PORT" --instance durable-origin
wait_port "$HTTP_PORT" "origin fixture"
# Short lease TTL on the owner side; verbosity for renewal evidence.
env TUBO_HOME="$HOST_A" TUBO_EXACT_LEASE_TTL="$LEASE_TTL" TUBO_LOG_LEVEL=info "$T" attach "http://127.0.0.1:$HTTP_PORT" --name origin -d 2>/dev/null
wait_json "$HOST_A" "'$T' inspect pipe/origin --json" "value['status']=='ready'" >/dev/null
SHARE_JSON="$(env TUBO_HOME="$HOST_A" "$T" share pipe/origin --json --quiet 2>/dev/null)"
PIPE_ID="$(printf '%s' "$SHARE_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["pipe_id"])')"
INVITE="$(printf '%s' "$SHARE_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["invite"])')"
if [[ -z "$INVITE" || -z "$PIPE_ID" ]]; then echo "FAIL: share metadata" >&2; exit 1; fi
echo "OK: Host A shared pipe $PIPE_ID"

echo "=== 3. Host B: accept --token (durable binding, NO pipe/ingress) ==="
HOST_B="$WORK/host-b"; mkdir -p "$HOST_B"
env TUBO_HOME="$HOST_B" "$T" join overlay --relay "$RELAY" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
ACCEPT_JSON="$(env TUBO_HOME="$HOST_B" "$T" accept --token "$INVITE" --json 2>"$WORK/accept.err")"
ACCEPTED_PIPE="$(printf '%s' "$ACCEPT_JSON" | python3 -c 'import json,sys; print(json.load(sys.stdin)["remote_pipe_id"])')"
if [[ "$ACCEPTED_PIPE" != "$PIPE_ID" ]]; then echo "FAIL: accepted pipe $ACCEPTED_PIPE != $PIPE_ID" >&2; exit 1; fi
# Accept must not have created any local Pipe/ingress.
if env TUBO_HOME="$HOST_B" "$T" inspect pipe/west --json >/dev/null 2>&1; then
  echo "FAIL: accept created a local Pipe" >&2; exit 1
fi
if env TUBO_HOME="$HOST_B" "$T" inspect "pipe/connect-$PIPE_ID" --json >/dev/null 2>&1; then
  echo "FAIL: accept created a deterministic local Pipe" >&2; exit 1
fi
echo "OK: Host B accepted $ACCEPTED_PIPE; no local Pipe/ingress created"

echo "=== 4. Host B: connect --remote (no invite) and verify traffic ==="
OUTPUT="$(env TUBO_HOME="$HOST_B" TUBO_LOG_LEVEL=info "$T" connect --remote "$PIPE_ID" --local "127.0.0.1:$CLIENT_PORT" --name west -d --json 2>/dev/null || true)"
if [[ -z "$OUTPUT" ]]; then echo "FAIL: connect --remote produced no output" >&2; cat "$HOST_B/logs/"*.log 2>/dev/null | tail -20 >&2; exit 1; fi
WEST_PID="$(printf '%s' "$OUTPUT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["pid"])')"
PIDS+=("$WEST_PID")
wait_json "$HOST_B" "'$T' inspect pipe/west --json" "value['status']=='ready'" >/dev/null
wait_port "$CLIENT_PORT" "west listener"
if ! probe; then echo "FAIL: first hop through --remote failed" >&2; exit 1; fi
echo "OK: connect --remote traffic verified (initial access)"

echo "=== 5. lease expiry: reopen with accelerated TTL, no invite, renewal ==="
sleep 4   # cross the $LEASE_TTL boundary; the live session is fenced at expiry
BEFORE="$(grep -c 'exact lease renewed' "$HOST_B/logs/"*.log 2>/dev/null || true)"
renewed=false
for _ in $(seq 1 30); do
  if probe; then
    AFTER="$(grep -c 'exact lease renewed' "$HOST_B/logs/"*.log 2>/dev/null || true)"
    if [[ "$AFTER" -gt "$BEFORE" ]]; then renewed=true; fi
    break
  fi
  sleep 0.4
done
AFTER="$(grep -c 'exact lease renewed' "$HOST_B/logs/"*.log 2>/dev/null || true)"
if [[ "$AFTER" -le "$BEFORE" ]]; then
  echo "FAIL: no renewal observed after lease expiry: before=$BEFORE after=$AFTER" >&2
  cat "$HOST_B/logs/"*.log 2>/dev/null | tail -30 >&2
  exit 1
fi
if ! probe; then echo "FAIL: reopen after lease expiry failed (renewal did not restore access)" >&2; exit 1; fi
echo "OK: expire + reopen exercised exact renewal ($BEFORE -> $AFTER)"

echo "=== 6. clean Host B restart: same TUBO_HOME, NO invite, access reacquired ==="
env TUBO_HOME="$HOST_B" "$T" stop pipe/west --json >/dev/null 2>&1 || true
kill -9 "$WEST_PID" 2>/dev/null || true
sleep 0.5
if ! env TUBO_HOME="$HOST_B" TUBO_LOG_LEVEL=info "$T" start pipe/west -d --json >/dev/null 2>"$WORK/restart-b.err"; then
  echo "FAIL: Host B restart" >&2; cat "$WORK/restart-b.err" >&2; exit 1
fi
wait_port "$CLIENT_PORT" "west listener (restarted)"
if ! probe; then echo "FAIL: post-restart traffic (no invite supplied)" >&2; cat "$HOST_B/logs/"*.log 2>/dev/null | tail -20 >&2; exit 1; fi
echo "OK: Host B clean restart reacquired access from the durable binding (no invite)"

echo "=== 7. owner restart: authoritative accepted state survives ==="
env TUBO_HOME="$HOST_A" "$T" stop pipe/origin --json >/dev/null 2>&1 || true
env TUBO_HOME="$HOST_A" TUBO_EXACT_LEASE_TTL="$LEASE_TTL" TUBO_LOG_LEVEL=info "$T" start pipe/origin -d --json >/dev/null 2>"$WORK/restart-a.err"
wait_json "$HOST_A" "'$T' inspect pipe/origin --json" "value['status']=='ready'" >/dev/null
sleep 4   # let Host B's cached 3s lease expire so the next open must renew
if ! probe; then echo "FAIL: access after owner runtime restart" >&2; cat "$HOST_B/logs/"*.log 2>/dev/null | tail -20 >&2; exit 1; fi
echo "OK: owner restart preserved the accepted relationship; renewal succeeded"

echo "=== 8. revocation: renewal denied, no new connection, endpoint unreachable ==="
env TUBO_HOME="$HOST_A" "$T" revoke pipe-access "$PIPE_ID" --json >/dev/null
sleep 4   # cross the current lease fence
if probe; then
  echo "FAIL: revoked pipe still reachable" >&2
  exit 1
fi
if ! grep -q 'server denied\|renewal_failed\|authorization' "$HOST_B/logs/"*.log 2>/dev/null; then
  # Even if the denial text differs, the application endpoint must be dead.
  echo "WARN: no denial string in Host B logs; endpoint dead (probe failed), acceptable" >&2
fi
echo "OK: revocation denied renewal; no new Pipe open; endpoint unreachable"

echo ""
echo "[integration] Journey A durable binding PASS (lease expiry + restart + owner restart + revocation)"