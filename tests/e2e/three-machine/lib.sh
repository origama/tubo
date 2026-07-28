#!/usr/bin/env bash
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../../.." && pwd)"

if [[ -f "$E2E_DIR/.env.local" ]]; then
  # shellcheck disable=SC1091
  source "$E2E_DIR/.env.local"
fi

RELAY_HOST="${TUBO_E2E_RELAY_HOST:-tuborelay}"
PUBLISHER_HOST="${TUBO_E2E_PUBLISHER_HOST:-oripi}"
CLIENT_HOST="${TUBO_E2E_CLIENT_HOST:-local}"
RELAY_PUBLIC_IP="${TUBO_E2E_RELAY_PUBLIC_IP:-172.232.189.160}"
REMOTE_GOARCH="${TUBO_E2E_REMOTE_GOARCH:-amd64}"
RELAY_PORT="${TUBO_E2E_RELAY_PORT:-44001}"
HTTP_PORT="${TUBO_E2E_HTTP_PORT:-18080}"
CLIENT_PORT="${TUBO_E2E_CLIENT_PORT:-18999}"
VIEWER_PORT="${TUBO_E2E_VIEWER_PORT:-$((CLIENT_PORT + 1))}"
RUN_ID="${TUBO_E2E_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
LOCAL_ROOT="$REPO_ROOT/generated/e2e-three-machine/$RUN_ID"
REMOTE_ROOT="/tmp/tubo-rebuilt-e2e-$RUN_ID"
CLIENT_HOME="$LOCAL_ROOT/client-home"
VIEWER_HOME="$LOCAL_ROOT/viewer-home"
CLIENT_BIN="$LOCAL_ROOT/bin/tubo"
ARTIFACTS="$LOCAL_ROOT/artifacts"

log() { printf '[three-machine] %s\n' "$*"; }
fail() { printf '[three-machine] ERROR: %s\n' "$*" >&2; exit 1; }

require_local_tools() {
  for tool in go ssh scp curl python3; do
    command -v "$tool" >/dev/null 2>&1 || fail "missing local tool: $tool"
  done
}

confirm_mutation() {
  [[ "${TUBO_E2E_CONFIRM:-}" == "three-machine" ]] || fail 'set TUBO_E2E_CONFIRM=three-machine after reading docs/runbooks/THREE_MACHINE_E2E.md'
}

remote() {
  local host="$1"
  shift
  ssh -o BatchMode=yes -o ConnectTimeout=10 "$host" "$@"
}

remote_sh() {
  local host="$1"
  shift
  remote "$host" sh -lc "$(printf '%q ' "$@")"
}

client_sh() {
  TUBO_HOME="$CLIENT_HOME" "$@"
}

json_get() {
  local expression="$1"
  python3 -c "import json,sys; value=json.load(sys.stdin); print($expression)"
}

wait_remote_json() {
  local host="$1" command="$2" expression="$3" expected="$4" tries="${5:-60}"
  local output value
  for _ in $(seq 1 "$tries"); do
    if output="$(remote "$host" "env TUBO_HOME='$REMOTE_ROOT/home' $command" 2>/dev/null)"; then
      value="$(printf '%s' "$output" | json_get "$expression" 2>/dev/null || true)"
      if [[ "$value" == "$expected" ]]; then
        printf '%s' "$output"
        return 0
      fi
    fi
    sleep 1
  done
  return 1
}

wait_client_json() {
  local command="$1" expression="$2" expected="$3" tries="${4:-60}"
  local output value
  for _ in $(seq 1 "$tries"); do
    if output="$(env TUBO_HOME="$CLIENT_HOME" sh -lc "$command" 2>/dev/null)"; then
      value="$(printf '%s' "$output" | json_get "$expression" 2>/dev/null || true)"
      if [[ "$value" == "$expected" ]]; then
        printf '%s' "$output"
        return 0
      fi
    fi
    sleep 1
  done
  return 1
}

start_remote_bg() {
  local host="$1" name="$2"
  shift 2
  local command="$*"
  remote "$host" "mkdir -p '$REMOTE_ROOT/logs' '$REMOTE_ROOT/pids'; nohup sh -lc \"exec env TUBO_HOME='$REMOTE_ROOT/home' $command\" >'$REMOTE_ROOT/logs/$name.log' 2>&1 </dev/null & echo \$! >'$REMOTE_ROOT/pids/$name.pid'"
}

preflight() {
  require_local_tools
  log "local: $(uname -s)/$(uname -m)"
  for host in "$RELAY_HOST" "$PUBLISHER_HOST"; do
    log "checking SSH host $host"
    remote "$host" 'printf "host=%s os=%s arch=%s\n" "$(hostname)" "$(uname -s)" "$(uname -m)"; command -v curl; test -w /tmp'
  done
  if remote "$RELAY_HOST" "ss -ltn 2>/dev/null | grep -Eq '[:.]$RELAY_PORT[[:space:]]'"; then
    fail "relay test port $RELAY_PORT is already in use on $RELAY_HOST"
  fi
  if remote "$PUBLISHER_HOST" "ss -ltn 2>/dev/null | grep -Eq '[:.]$HTTP_PORT[[:space:]]'"; then
    fail "HTTP test port $HTTP_PORT is already in use on $PUBLISHER_HOST"
  fi
  log "relay port $RELAY_PORT and publisher HTTP port $HTTP_PORT are free; the operator must also allow inbound TCP/$RELAY_PORT in host/cloud firewalls"
  [[ "$CLIENT_HOST" == "local" ]] || fail 'the bootstrap harness currently requires TUBO_E2E_CLIENT_HOST=local'
  python3 - "$CLIENT_PORT" "$VIEWER_PORT" <<'PY' || fail "client test port is already in use locally"
import socket, sys
sockets = []
try:
    for value in sys.argv[1:]:
        sock = socket.socket()
        sock.bind(("127.0.0.1", int(value)))
        sockets.append(sock)
finally:
    for sock in sockets:
        sock.close()
PY
  log 'preflight PASS'
}

build_and_deploy() {
  mkdir -p "$LOCAL_ROOT/bin" "$ARTIFACTS" "$CLIENT_HOME"
  log 'building local client binaries'
  (cd "$REPO_ROOT" && go build -o "$CLIENT_BIN" ./cmd/tubo && go build -o "$LOCAL_ROOT/bin/test-http-server" ./cmd/test-http-server && go build -o "$LOCAL_ROOT/bin/test-tcp-server" ./cmd/test-tcp-server)
  log "building linux/$REMOTE_GOARCH binaries"
  (cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$REMOTE_GOARCH" go build -o "$LOCAL_ROOT/bin/tubo-linux" ./cmd/tubo && CGO_ENABLED=0 GOOS=linux GOARCH="$REMOTE_GOARCH" go build -o "$LOCAL_ROOT/bin/test-http-server-linux" ./cmd/test-http-server && CGO_ENABLED=0 GOOS=linux GOARCH="$REMOTE_GOARCH" go build -o "$LOCAL_ROOT/bin/test-tcp-server-linux" ./cmd/test-tcp-server)
  for host in "$RELAY_HOST" "$PUBLISHER_HOST"; do
    remote "$host" "mkdir -p '$REMOTE_ROOT/bin' '$REMOTE_ROOT/home' '$REMOTE_ROOT/logs' '$REMOTE_ROOT/pids'"
    scp -q "$LOCAL_ROOT/bin/tubo-linux" "$host:$REMOTE_ROOT/bin/tubo"
    remote "$host" "chmod 700 '$REMOTE_ROOT/bin/tubo'"
  done
  scp -q "$LOCAL_ROOT/bin/test-http-server-linux" "$PUBLISHER_HOST:$REMOTE_ROOT/bin/test-http-server"
  scp -q "$LOCAL_ROOT/bin/test-tcp-server-linux" "$PUBLISHER_HOST:$REMOTE_ROOT/bin/test-tcp-server"
  remote "$PUBLISHER_HOST" "chmod 700 '$REMOTE_ROOT/bin/test-http-server' '$REMOTE_ROOT/bin/test-tcp-server'"
}

clean_remote_host() {
  local host="$1"
  remote "$host" "if [ -d '$REMOTE_ROOT/pids' ]; then
    for file in '$REMOTE_ROOT'/pids/*.pid; do
      [ -f \"\$file\" ] || continue
      pid=\$(cat \"\$file\")
      cmd=''
      [ ! -r \"/proc/\$pid/cmdline\" ] || cmd=\$(tr '\\000' ' ' <\"/proc/\$pid/cmdline\")
      case \"\$cmd\" in *'$REMOTE_ROOT'*) kill \"\$pid\" 2>/dev/null || true;; esac
    done
    sleep 1
    for file in '$REMOTE_ROOT'/pids/*.pid; do
      [ -f \"\$file\" ] || continue
      pid=\$(cat \"\$file\")
      cmd=''
      [ ! -r \"/proc/\$pid/cmdline\" ] || cmd=\$(tr '\\000' ' ' <\"/proc/\$pid/cmdline\")
      case \"\$cmd\" in *'$REMOTE_ROOT'*) kill -9 \"\$pid\" 2>/dev/null || true;; esac
    done
  fi
  rm -rf '$REMOTE_ROOT'" || true
}

cleanup_run() {
  log "cleaning isolated run $RUN_ID"
  clean_remote_host "$PUBLISHER_HOST"
  clean_remote_host "$RELAY_HOST"
  local file pid command
  for file in "$LOCAL_ROOT"/*.pid; do
    [[ -f "$file" ]] || continue
    pid="$(cat "$file")"
    command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
    case "$command" in *"$LOCAL_ROOT/bin/tubo _run-pipe"*)
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
      command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
      case "$command" in *"$LOCAL_ROOT/bin/tubo _run-pipe"*) kill -9 "$pid" 2>/dev/null || true;; esac
    esac
  done
}

collect_artifacts() {
  mkdir -p "$ARTIFACTS/relay" "$ARTIFACTS/publisher"
  scp -qr "$RELAY_HOST:$REMOTE_ROOT/logs/." "$ARTIFACTS/relay/" 2>/dev/null || true
  scp -qr "$PUBLISHER_HOST:$REMOTE_ROOT/logs/." "$ARTIFACTS/publisher/" 2>/dev/null || true
}
