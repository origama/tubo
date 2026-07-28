#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/tubo-private-http.XXXXXX")"
PIDS=()
cleanup() {
  for pid in "${PIDS[@]}"; do kill "$pid" 2>/dev/null || true; done
  sleep 0.2
  for pid in "${PIDS[@]}"; do kill -9 "$pid" 2>/dev/null || true; done
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

read -r RELAY_PORT HTTP_PORT CLIENT_PORT VIEWER_PORT < <(python3 - <<'PY'
import socket
sockets=[]; ports=[]
for _ in range(4):
    s=socket.socket(); s.bind(('127.0.0.1',0)); sockets.append(s); ports.append(s.getsockname()[1])
print(*ports)
PY
)
mkdir -p "$WORK/bin"
(cd "$ROOT" && go build -o "$WORK/bin/tubo" ./cmd/tubo && go build -o "$WORK/bin/http" ./cmd/test-http-server)
T="$WORK/bin/tubo"

start() {
  local home="$1" log="$2"; shift 2
  env TUBO_HOME="$home" "$@" >"$WORK/$log.log" 2>&1 &
  LAST_PID="$!"
  PIDS+=("$LAST_PID")
}
wait_json() {
  local home="$1" command="$2" expression="$3"
  for _ in $(seq 1 100); do
    if output="$(env TUBO_HOME="$home" sh -c "$command" 2>/dev/null)" && printf '%s' "$output" | python3 -c "import json,sys; value=json.load(sys.stdin); assert $expression" 2>/dev/null; then
      printf '%s' "$output"; return 0
    fi
    sleep 0.1
  done
  return 1
}

RELAY_HOME="$WORK/relay"; AUTH_HOME="$WORK/authority"; PUB_HOME="$WORK/publisher"; CLIENT_HOME="$WORK/client"; VIEWER_HOME="$WORK/viewer"
env TUBO_HOME="$RELAY_HOME" "$T" keygen swarm --out "$WORK/swarm.key" >/dev/null
start "$RELAY_HOME" relay "$T" relay --listen "/ip4/127.0.0.1/tcp/$RELAY_PORT" --public-addr "/ip4/127.0.0.1/tcp/$RELAY_PORT" --swarm-key "$WORK/swarm.key"
relay_pid="$LAST_PID"
relay="$(wait_json "$RELAY_HOME" "'$T' inspect relay/default --json" "value['status']=='ready'")"
relay_peer="$(printf '%s' "$relay" | python3 -c 'import json,sys;print(json.load(sys.stdin)["peer_id"])')"
relay_addr="/ip4/127.0.0.1/tcp/$RELAY_PORT/p2p/$relay_peer"

env TUBO_HOME="$AUTH_HOME" "$T" join overlay --relay "$relay_addr" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
env TUBO_HOME="$AUTH_HOME" "$T" scope create solar/default --json >/dev/null
start "$AUTH_HOME" authority "$T" authority start solar --scope solar/default
wait_json "$AUTH_HOME" "'$T' inspect authority/solar --json" "value['status']=='ready'" >/dev/null
publisher="$(env TUBO_HOME="$AUTH_HOME" "$T" scope invite solar/default --role publisher --expires 1h --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
env TUBO_HOME="$PUB_HOME" "$T" join overlay --relay "$relay_addr" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
env TUBO_HOME="$PUB_HOME" "$T" scope join --token "$publisher" --json >/dev/null
start "$PUB_HOME" http "$WORK/bin/http" --listen "127.0.0.1:$HTTP_PORT" --instance integration-origin
for _ in $(seq 1 50); do curl -fsS "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null 2>&1 && break; sleep 0.1; done
curl -fsS "http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null

env TUBO_HOME="$PUB_HOME" "$T" service create echo --target "http://127.0.0.1:$HTTP_PORT" --json >/dev/null
pipe="$(env TUBO_HOME="$PUB_HOME" "$T" pipe create echo-team --ingress tubo --target service/echo --scope solar/default --visibility discoverable --access members --json)"
pipe_id="$(printf '%s' "$pipe" | python3 -c 'import json,sys;print(json.load(sys.stdin)["id"])')"
start "$PUB_HOME" pipe "$T" start pipe/echo-team
wait_json "$PUB_HOME" "'$T' inspect pipe/echo-team --json" "value['status']=='ready'" >/dev/null
env TUBO_HOME="$PUB_HOME" "$T" pipe create hidden --ingress tubo --target service/echo --scope solar/default --visibility unlisted --access invite_only --json >/dev/null
start "$PUB_HOME" hidden "$T" start pipe/hidden
wait_json "$PUB_HOME" "'$T' inspect pipe/hidden --json" "value['status']=='ready'" >/dev/null
member="$(env TUBO_HOME="$AUTH_HOME" "$T" scope invite solar/default --role member --expires 1h --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"
viewer="$(env TUBO_HOME="$AUTH_HOME" "$T" scope invite solar/default --role viewer --expires 1h --json | python3 -c 'import json,sys;print(json.load(sys.stdin)["token"])')"

for tuple in "$CLIENT_HOME:$member" "$VIEWER_HOME:$viewer"; do
  home="${tuple%%:*}"; token="${tuple#*:}"
  env TUBO_HOME="$home" "$T" join overlay --relay "$relay_addr" --swarm-key "$WORK/swarm.key" --force-reachability private --json >/dev/null
  env TUBO_HOME="$home" "$T" scope join --token "$token" --json >/dev/null
  listing="$(wait_json "$home" "'$T' get pipes --scope solar/default --json" "any(x['id']=='$pipe_id' for x in value['items'])")"
  printf '%s' "$listing" | python3 -c "import json,sys; value=json.load(sys.stdin); assert '127.0.0.1:$HTTP_PORT' not in str(value); assert not any(x.get('name')=='hidden' for x in value['items']); item=next(x for x in value['items'] if x['id']=='$pipe_id'); assert item['path']=='relayed'; assert item['path_source']=='advertised'; assert item['status']=='available'"
done

connection=''
for _ in $(seq 1 20); do
  if connection="$(env TUBO_HOME="$CLIENT_HOME" "$T" connect "pipe/$pipe_id" --local "127.0.0.1:$CLIENT_PORT" -d --json 2>"$WORK/member-connect.err")"; then break; fi
  sleep 0.2
done
[[ -n "$connection" ]] || { cat "$WORK/member-connect.err" >&2; exit 1; }
PIDS+=("$(printf '%s' "$connection" | python3 -c 'import json,sys;print(json.load(sys.stdin)["pid"])')")
response=''
for _ in $(seq 1 20); do
  if response="$(curl -fsS -X POST -H 'X-Tubo-Test: integration' --data 'stream-body' "http://127.0.0.1:$CLIENT_PORT/v1/check?from=integration" 2>/dev/null)"; then break; fi
  sleep 0.2
done
[[ -n "$response" ]] || { echo 'member tunnel request failed' >&2; exit 1; }
printf '%s' "$response" | python3 -c "import json,sys,base64; value=json.load(sys.stdin); assert value['instance']=='integration-origin'; assert value['method']=='POST'; assert value['raw_query']=='from=integration'; assert value['headers']['X-Tubo-Test']==['integration']; assert base64.b64decode(value['body_b64'])==b'stream-body'"
python3 - "$CLIENT_PORT" <<'PY'
import base64, json, sys, urllib.request
payload = bytes(range(256)) * 1024
request = urllib.request.Request(f"http://127.0.0.1:{sys.argv[1]}/large", data=payload, method="POST")
with urllib.request.urlopen(request, timeout=20) as response:
    value = json.load(response)
assert base64.b64decode(value["body_b64"]) == payload
PY
python3 - "$CLIENT_PORT" <<'PY'
import socket, sys
s = socket.create_connection(("127.0.0.1", int(sys.argv[1])), timeout=10)
s.sendall(b"GET /ws HTTP/1.1\r\nHost: tubo.test\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dHViby10ZXN0LWtleQ==\r\n\r\n")
headers = b""
while b"\r\n\r\n" not in headers:
    headers += s.recv(4096)
assert headers.startswith(b"HTTP/1.1 101"), headers
payload = b"websocket-through-tubo"
mask = b"TUBO"
frame = bytes([0x82, 0x80 | len(payload)]) + mask + bytes(value ^ mask[i % 4] for i, value in enumerate(payload))
s.sendall(frame)
received = b""
while len(received) < len(frame):
    received += s.recv(len(frame) - len(received))
assert received == frame, (received, frame)
s.close()
PY
relay_top="$(wait_json "$RELAY_HOME" "'$T' top --json" "value['items'][0]['network_bytes_in']>0 and value['items'][0]['network_bytes_out']>0 and (value['items'][0]['network_rate_in']>0 or value['items'][0]['network_rate_out']>0) and value['items'][0]['connected_peers']>0")"
printf '%s' "$relay_top" | python3 -c "import json,sys; item=json.load(sys.stdin)['items'][0]; assert item['kind']=='relay'; assert item['open_connections']>0"
publisher_peers="$(wait_json "$PUB_HOME" "'$T' get peers --json" "any(x['connected'] for x in value['items'])")"
printf '%s' "$publisher_peers" | python3 -c "import json,sys; value=json.load(sys.stdin); assert any('pipe/echo-team' in x.get('observed_by',[]) for x in value['items'])"
ref="$(printf '%s' "$connection" | python3 -c 'import json,sys;print(json.load(sys.stdin)["ref"])')"
inspection="$(env TUBO_HOME="$CLIENT_HOME" "$T" inspect "$ref" --json)"
printf '%s' "$inspection" | python3 -c "import json,sys; value=json.load(sys.stdin); assert value['sessions']; assert value['sessions'][0]['initial_transport_path']=='relayed'; assert value['transport_summary']['current']=='relayed'; assert value['transport_summary']['source']=='last_session'; assert value['logical_path']==[value['ref'],'pipe/$pipe_id']"
restart="$(env TUBO_HOME="$CLIENT_HOME" "$T" restart "$ref" --json)"
restart_pid="$(printf '%s' "$restart" | python3 -c 'import json,sys;print(json.load(sys.stdin)["pid"])')"
PIDS+=("$restart_pid")
restart_ok=0
for _ in $(seq 1 30); do
  if curl -fsS --max-time 5 "http://127.0.0.1:$CLIENT_PORT/healthz" >/dev/null 2>&1; then restart_ok=1; break; fi
  sleep 0.2
done
[[ "$restart_ok" == 1 ]] || { echo 'restarted local pipe did not recover' >&2; exit 1; }

viewer_denied=0
for _ in $(seq 1 10); do
  if env TUBO_HOME="$VIEWER_HOME" "$T" connect "pipe/$pipe_id" --local "127.0.0.1:$VIEWER_PORT" -d --json >"$WORK/viewer-connect.out" 2>&1; then
    echo 'viewer connect unexpectedly succeeded' >&2; exit 1
  fi
  if grep -F 'authorization_denied' "$WORK/viewer-connect.out" >/dev/null; then viewer_denied=1; break; fi
  sleep 0.2
done
[[ "$viewer_denied" == 1 ]] || { cat "$WORK/viewer-connect.out" >&2; exit 1; }

kill "$relay_pid"
for _ in $(seq 1 100); do kill -0 "$relay_pid" 2>/dev/null || break; sleep 0.1; done
cached="$(env TUBO_HOME="$CLIENT_HOME" "$T" get pipes --scope solar/default --json 2>/dev/null)"
printf '%s' "$cached" | python3 -c "import json,sys; value=json.load(sys.stdin); assert value.get('discovery_errors'); assert any(x['id']=='$pipe_id' for x in value['items'])"
start "$RELAY_HOME" relay-restarted "$T" relay --listen "/ip4/127.0.0.1/tcp/$RELAY_PORT" --public-addr "/ip4/127.0.0.1/tcp/$RELAY_PORT" --swarm-key "$WORK/swarm.key"
relay_pid="$LAST_PID"
recovered=0
for _ in $(seq 1 300); do
  auth_status="$(env TUBO_HOME="$AUTH_HOME" "$T" inspect authority/solar --json 2>/dev/null || true)"
  pub_status="$(env TUBO_HOME="$PUB_HOME" "$T" inspect pipe/echo-team --json 2>/dev/null || true)"
  client_status="$(env TUBO_HOME="$CLIENT_HOME" "$T" inspect "$ref" --json 2>/dev/null || true)"
  if printf '%s\n%s\n%s\n' "$auth_status" "$pub_status" "$client_status" | python3 -c 'import json,sys; values=[json.loads(x) for x in sys.stdin if x.strip()]; assert len(values)==3 and all(x["status"]=="ready" for x in values)' 2>/dev/null && curl -fsS --max-time 3 "http://127.0.0.1:$CLIENT_PORT/healthz" >/dev/null 2>&1; then recovered=1; break; fi
  sleep 0.1
done
[[ "$recovered" == 1 ]] || { echo 'runtime did not self-heal after relay outage' >&2; exit 1; }
new_relay="$(env TUBO_HOME="$RELAY_HOME" "$T" inspect relay/default --json)"
printf '%s' "$new_relay" | python3 -c "import json,sys; value=json.load(sys.stdin); assert value['peer_id']=='$relay_peer'; assert value['status']=='ready'"

kill -9 "$restart_pid" 2>/dev/null || true
sleep 0.2
stale="$(env TUBO_HOME="$CLIENT_HOME" "$T" rm --stale --json)"
printf '%s' "$stale" | python3 -c "import json,sys; value=json.load(sys.stdin); assert '$ref' in value['removed']"

echo '[integration] remote publisher/member/viewer HTTP PASS'
