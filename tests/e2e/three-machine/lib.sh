#!/usr/bin/env bash
set -euo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$E2E_DIR/../../.." && pwd)"
log() { printf '[three-machine] %s\n' "$*"; }
fail() { printf '[three-machine] ERROR: %s\n' "$*" >&2; exit 1; }

# .env.local is data, never shell input. Values remain subject to strict validation.
load_e2e_env() {
  local file="$E2E_DIR/.env.local" line key value
  [[ -f "$file" ]] || return 0
  while IFS= read -r line || [[ -n "$line" ]]; do
    [[ -z "$line" || "$line" == \#* ]] && continue
    [[ "$line" == TUBO_E2E_*=* ]] || fail "invalid .env.local line"
    key="${line%%=*}"; value="${line#*=}"
    case "$key" in
      TUBO_E2E_RELAY_HOST|TUBO_E2E_PUBLISHER_HOST|TUBO_E2E_CLIENT_HOST|TUBO_E2E_RELAY_PUBLIC_IP|TUBO_E2E_REMOTE_GOARCH|TUBO_E2E_RELAY_PORT|TUBO_E2E_HTTP_PORT|TUBO_E2E_CLIENT_PORT|TUBO_E2E_VIEWER_PORT|TUBO_E2E_SYNC_A_PORT|TUBO_E2E_SYNC_B_PORT|TUBO_E2E_SYNC_TAKEOVER_TIMEOUT|TUBO_E2E_SYNC_OUTAGE_TIMEOUT|TUBO_E2E_SYNC_RECOVERY_TIMEOUT|TUBO_E2E_RUN_ID) ;;
      *) fail "unsupported .env.local key: $key" ;;
    esac
    export "$key=$value"
  done <"$file"
}
load_e2e_env

RELAY_HOST="${TUBO_E2E_RELAY_HOST:-tuborelay}"
PUBLISHER_HOST="${TUBO_E2E_PUBLISHER_HOST:-oripi}"
CLIENT_HOST="${TUBO_E2E_CLIENT_HOST:-local}"
RELAY_PUBLIC_IP="${TUBO_E2E_RELAY_PUBLIC_IP:-172.232.189.160}"
REMOTE_GOARCH="${TUBO_E2E_REMOTE_GOARCH:-amd64}"
RELAY_PORT="${TUBO_E2E_RELAY_PORT:-44001}"
HTTP_PORT="${TUBO_E2E_HTTP_PORT:-18080}"
CLIENT_PORT="${TUBO_E2E_CLIENT_PORT:-18999}"
VIEWER_PORT="${TUBO_E2E_VIEWER_PORT:-}"
SYNC_A_PORT="${TUBO_E2E_SYNC_A_PORT:-}"
SYNC_B_PORT="${TUBO_E2E_SYNC_B_PORT:-}"
SYNC_TAKEOVER_TIMEOUT="${TUBO_E2E_SYNC_TAKEOVER_TIMEOUT:-65}"
SYNC_OUTAGE_TIMEOUT="${TUBO_E2E_SYNC_OUTAGE_TIMEOUT:-120}"
SYNC_RECOVERY_TIMEOUT="${TUBO_E2E_SYNC_RECOVERY_TIMEOUT:-180}"
KEEP_WORK="${KEEP_WORK:-0}"
RUN_ID="${TUBO_E2E_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}"

is_port() { [[ "$1" =~ ^[0-9]{1,5}$ ]] && ((10#$1 >= 1024 && 10#$1 <= 65535)); }
is_seconds() { [[ "$1" =~ ^[0-9]{1,3}$ ]] && ((10#$1 >= 1 && 10#$1 <= 600)); }
is_host() { [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9.-]{0,252}$ ]] && [[ "$1" != *..* ]]; }
is_id() { [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$ ]]; }
validate_generated_value() { [[ "$1" =~ ^[A-Za-z0-9._~-]+$ ]] || fail 'generated protocol value has unsafe syntax'; }
validate_resource() { [[ "$1" =~ ^[A-Za-z0-9_./-]+([[:space:]][A-Za-z0-9_./-]+)*$ ]]; }
validate_e2e_config() {
  is_id "$RUN_ID" || fail 'invalid TUBO_E2E_RUN_ID'
  [[ -z "${TUBO_E2E_CONFIRM:-}" || "${TUBO_E2E_CONFIRM:-}" == three-machine ]] || fail 'invalid TUBO_E2E_CONFIRM'
  is_host "$RELAY_HOST" || fail 'invalid TUBO_E2E_RELAY_HOST'
  is_host "$PUBLISHER_HOST" || fail 'invalid TUBO_E2E_PUBLISHER_HOST'
  [[ "$CLIENT_HOST" == local ]] || fail 'TUBO_E2E_CLIENT_HOST must be local'
  [[ "$REMOTE_GOARCH" == amd64 || "$REMOTE_GOARCH" == arm64 ]] || fail 'invalid TUBO_E2E_REMOTE_GOARCH'
  python3 - "$RELAY_PUBLIC_IP" <<'PY' || fail 'invalid TUBO_E2E_RELAY_PUBLIC_IP'
import ipaddress, sys
value=ipaddress.ip_address(sys.argv[1])
assert value.version == 4 and not value.is_unspecified
PY
  is_port "$RELAY_PORT" || fail 'invalid TUBO_E2E_RELAY_PORT'
  is_port "$HTTP_PORT" || fail 'invalid TUBO_E2E_HTTP_PORT'
  is_port "$CLIENT_PORT" || fail 'invalid TUBO_E2E_CLIENT_PORT'
  if [[ -z "$VIEWER_PORT" ]]; then VIEWER_PORT="$((10#$CLIENT_PORT + 1))"; fi
  if [[ -z "$SYNC_A_PORT" ]]; then SYNC_A_PORT="$((10#$CLIENT_PORT + 2))"; fi
  if [[ -z "$SYNC_B_PORT" ]]; then SYNC_B_PORT="$((10#$CLIENT_PORT + 3))"; fi
  is_port "$VIEWER_PORT" || fail 'invalid TUBO_E2E_VIEWER_PORT'
  is_port "$SYNC_A_PORT" || fail 'invalid TUBO_E2E_SYNC_A_PORT'
  is_port "$SYNC_B_PORT" || fail 'invalid TUBO_E2E_SYNC_B_PORT'
  [[ "$CLIENT_PORT,$VIEWER_PORT,$SYNC_A_PORT,$SYNC_B_PORT" != *",,"* ]] || fail 'invalid local port configuration'
  [[ "$CLIENT_PORT" != "$VIEWER_PORT" && "$CLIENT_PORT" != "$SYNC_A_PORT" && "$CLIENT_PORT" != "$SYNC_B_PORT" && "$VIEWER_PORT" != "$SYNC_A_PORT" && "$VIEWER_PORT" != "$SYNC_B_PORT" && "$SYNC_A_PORT" != "$SYNC_B_PORT" ]] || fail 'local E2E ports must be distinct'
  is_seconds "$SYNC_TAKEOVER_TIMEOUT" || fail 'invalid TUBO_E2E_SYNC_TAKEOVER_TIMEOUT'
  is_seconds "$SYNC_OUTAGE_TIMEOUT" || fail 'invalid TUBO_E2E_SYNC_OUTAGE_TIMEOUT'
  is_seconds "$SYNC_RECOVERY_TIMEOUT" || fail 'invalid TUBO_E2E_SYNC_RECOVERY_TIMEOUT'
  [[ "$KEEP_WORK" == 0 || "$KEEP_WORK" == 1 ]] || fail 'invalid KEEP_WORK; expected 0 or 1'
}
validate_e2e_config

LOCAL_ROOT="$REPO_ROOT/generated/e2e-three-machine/$RUN_ID"
REMOTE_ROOT="/tmp/tubo-rebuilt-e2e-$RUN_ID"
AUTHORITY_HOME="$REMOTE_ROOT/authority-home"
PUBLISHER_HOME="$REMOTE_ROOT/publisher-home"
CLIENT_HOME="$LOCAL_ROOT/client-home"
VIEWER_HOME="$LOCAL_ROOT/viewer-home"
CLIENT_BIN="$LOCAL_ROOT/bin/tubo"
ARTIFACTS="$LOCAL_ROOT/artifacts"
SECRET_VALUES_FILE="$LOCAL_ROOT/.periodic-secret-values"
LOCAL_PID_DIR="$LOCAL_ROOT/pids"
SSH_KNOWN_HOSTS="$LOCAL_ROOT/ssh-known_hosts"
SSH_KNOWN_HOSTS_SOURCE="$HOME/.ssh/known_hosts"
SSH_COMMON_OPTIONS=(
  -o BatchMode=yes
  -o ConnectTimeout=10
  -o StrictHostKeyChecking=yes
  -o "UserKnownHostsFile=$SSH_KNOWN_HOSTS"
  -o UpdateHostKeys=no
  -o CheckHostIP=no
  -o ControlMaster=no
  -o ControlPath=none
  -o ControlPersist=no
)
E2E_MUTATION_STARTED=0

require_local_tools() {
  for tool in go ssh scp ssh-keygen curl python3; do command -v "$tool" >/dev/null 2>&1 || fail "missing local tool: $tool"; done
  command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1 || fail 'missing local SHA-256 tool'
}
prepare_ssh_trust() {
  local host entries
  [[ -f "$SSH_KNOWN_HOSTS" ]] && return 0
  command -v ssh-keygen >/dev/null 2>&1 || fail 'missing local tool: ssh-keygen'
  [[ -r "$SSH_KNOWN_HOSTS_SOURCE" ]] || fail "missing operator known_hosts: $SSH_KNOWN_HOSTS_SOURCE"
  (umask 077; mkdir -p "$LOCAL_ROOT"; : >"$SSH_KNOWN_HOSTS")
  chmod 600 "$SSH_KNOWN_HOSTS"
  for host in "$RELAY_HOST" "$PUBLISHER_HOST"; do
    entries="$(ssh-keygen -F "$host" -f "$SSH_KNOWN_HOSTS_SOURCE" 2>/dev/null | awk '!/^#/')"
    [[ -n "$entries" ]] || fail "no verified SSH host-key entry for alias $host in $SSH_KNOWN_HOSTS_SOURCE"
    printf '%s\n' "$entries" >>"$SSH_KNOWN_HOSTS"
  done
}
ssh_effective_settings() {
  local host="$1"
  ssh "${SSH_COMMON_OPTIONS[@]}" -o "HostKeyAlias=$host" -G -- "$host" 2>&1 | awk '
    $1 == "host" || $1 == "hostname" || $1 == "hostkeyalias" ||
    $1 == "stricthostkeychecking" || $1 == "updatehostkeys" ||
    $1 == "checkhostip" || $1 == "controlmaster" ||
    $1 == "controlpath" || $1 == "controlpersist" ||
    $1 == "batchmode" || $1 == "connecttimeout" ||
    $1 == "userknownhostsfile" {
      if ($1 == "userknownhostsfile") print $1 " <run-bundle>"; else print
    }
  '
}
ssh_preflight_probe() {
  local host="$1" stdout="$LOCAL_ROOT/ssh-probe-$1.stdout" stderr="$LOCAL_ROOT/ssh-probe-$1.stderr" settings
  prepare_ssh_trust
  settings="$(ssh_effective_settings "$host")" || fail "could not read effective SSH settings for $host"
  log "SSH settings for $host: $(printf '%s' "$settings" | tr '\n' ';')"
  if ! ssh "${SSH_COMMON_OPTIONS[@]}" -o "HostKeyAlias=$host" -- "$host" 'printf "host=%s os=%s arch=%s\n" "$(hostname)" "$(uname -s)" "$(uname -m)"; command -v curl; command -v sha256sum; test -w /tmp' >"$stdout" 2>"$stderr"; then
    cat "$stderr" >&2
    fail "SSH probe failed for $host; full stderr captured in $stderr"
  fi
  cat "$stdout"
  rm -f "$stdout" "$stderr"
}
sha256_file() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'; else shasum -a 256 "$1" | awk '{print $1}'; fi; }
confirm_mutation() { [[ "${TUBO_E2E_CONFIRM:-}" == three-machine ]] || fail 'set TUBO_E2E_CONFIRM=three-machine after reading docs/runbooks/THREE_MACHINE_E2E.md'; }
# Keep remote(host, command...) compatibility while forcing per-run non-multiplexed SSH trust.
remote() { local host="$1"; shift; prepare_ssh_trust; ssh "${SSH_COMMON_OPTIONS[@]}" -o "HostKeyAlias=$host" -- "$host" "$@"; }
remote_scp_to() { local source="$1" host="$2" destination="$3"; prepare_ssh_trust; scp "${SSH_COMMON_OPTIONS[@]}" -o "HostKeyAlias=$host" -- "$source" "$host:$destination"; }
remote_scp_from() { local host="$1" source="$2" destination="$3"; prepare_ssh_trust; scp -qr "${SSH_COMMON_OPTIONS[@]}" -o "HostKeyAlias=$host" -- "$host:$source" "$destination"; }
client_sh() { TUBO_HOME="$CLIENT_HOME" "$@"; }
# Callers may interpolate only validated configuration or validate_generated_value output.
remote_home_sh() { local host="$1" home="$2"; shift 2; remote "$host" "env TUBO_HOME='$home' $*"; }
json_get() { local expression="$1"; python3 -c "import json,sys; value=json.load(sys.stdin); print($expression)"; }

wait_remote_home_json() {
  local host="$1" home="$2" command="$3" expression="$4" expected="$5" tries="${6:-60}" output value
  for _ in $(seq 1 "$tries"); do
    if output="$(remote_home_sh "$host" "$home" "$command" 2>/dev/null)"; then
      value="$(printf '%s' "$output" | json_get "$expression" 2>/dev/null || true)"
      [[ "$value" == "$expected" ]] && { printf '%s' "$output"; return 0; }
    fi
    sleep 1
  done
  return 1
}
wait_remote_json() { wait_remote_home_json "$1" "$REMOTE_ROOT/home" "$2" "$3" "$4" "${5:-60}"; }
wait_client_json() {
  local command="$1" expression="$2" expected="$3" tries="${4:-60}" output value
  for _ in $(seq 1 "$tries"); do
    if output="$(env TUBO_HOME="$CLIENT_HOME" sh -lc "$command" 2>/dev/null)"; then
      value="$(printf '%s' "$output" | json_get "$expression" 2>/dev/null || true)"
      [[ "$value" == "$expected" ]] && { printf '%s' "$output"; return 0; }
    fi
    sleep 1
  done
  return 1
}

start_remote_bg_home() {
  local host="$1" home="$2" name="$3" expected="$4"; shift 4
  is_id "$name" || fail 'unsafe remote process name'
  validate_resource "$expected" || fail 'unsafe expected remote resource'
  local command="$*"
  remote "$host" "umask 077; mkdir -p '$REMOTE_ROOT/logs' '$REMOTE_ROOT/pids'; printf '%s\\n' '$expected' >'$REMOTE_ROOT/pids/$name.expected'; nohup sh -lc \"exec env TUBO_HOME='$home' $command\" >'$REMOTE_ROOT/logs/$name.log' 2>&1 </dev/null & echo \$! >'$REMOTE_ROOT/pids/$name.pid'"
}
start_remote_bg() {
  local host="$1" name="$2"; shift 2
  local command="$*" expected="$name"
  if [[ "$command" =~ pipe/[A-Za-z0-9_-]+ ]]; then expected="${BASH_REMATCH[0]}"
  elif [[ "$command" == *' authority '* ]]; then expected=authority
  elif [[ "$command" == *' relay '* ]]; then expected=relay
  elif [[ "$command" == *test-http-server* ]]; then expected=test-http-server
  elif [[ "$command" == *test-tcp-server* ]]; then expected=test-tcp-server
  fi
  start_remote_bg_home "$host" "$REMOTE_ROOT/home" "$name" "$expected" "$command"
}
kill_remote_exact() {
  local host="$1" name="$2" expected="$3"
  is_id "$name" || return 41; validate_resource "$expected" || return 44
  remote "$host" "pidfile='$REMOTE_ROOT/pids/$name.pid'; test -r \"\$pidfile\" || exit 41; pid=\$(cat \"\$pidfile\"); case \"\$pid\" in ''|*[!0-9]*) exit 42;; esac; test -r \"/proc/\$pid/cmdline\" || exit 43; cmd=\$(tr '\\000' ' ' <\"/proc/\$pid/cmdline\"); case \"\$cmd\" in *'$REMOTE_ROOT'*'$expected'*) kill -KILL \"\$pid\";; *) exit 44;; esac"
}
kill_local_exact() {
  local pid="$1" expected="$2" command
  [[ "$pid" =~ ^[0-9]+$ ]] || return 41; validate_resource "$expected" || return 44
  command="$(ps -p "$pid" -o command= 2>/dev/null || true)"
  [[ "$command" == *"$LOCAL_ROOT"* && "$command" == *"$expected"* ]] || return 44
  kill -KILL "$pid"
}

preflight() {
  validate_e2e_config; require_local_tools
  log "local: $(uname -s)/$(uname -m)"
  prepare_ssh_trust
  for host in "$RELAY_HOST" "$PUBLISHER_HOST"; do
    log "checking SSH host $host"
    ssh_preflight_probe "$host"
  done
  if remote "$RELAY_HOST" "ss -ltn 2>/dev/null | grep -Eq '[:.]$RELAY_PORT[[:space:]]'"; then fail "relay test port $RELAY_PORT is already in use on $RELAY_HOST"; fi
  if remote "$PUBLISHER_HOST" "ss -ltn 2>/dev/null | grep -Eq '[:.]$HTTP_PORT[[:space:]]'"; then fail "HTTP test port $HTTP_PORT is already in use on $PUBLISHER_HOST"; fi
  python3 - "$CLIENT_PORT" "$VIEWER_PORT" "$SYNC_A_PORT" "$SYNC_B_PORT" <<'PY' || fail 'local test port is already in use'
import socket, sys
sockets=[]
try:
    for value in sys.argv[1:]:
        sock=socket.socket(); sock.bind(("127.0.0.1", int(value))); sockets.append(sock)
finally:
    for sock in sockets: sock.close()
PY
  log "preflight PASS; ports relay=$RELAY_PORT http=$HTTP_PORT client=$CLIENT_PORT sync=$SYNC_A_PORT,$SYNC_B_PORT"
}

build_and_deploy() {
  validate_e2e_config
  (umask 077; mkdir -p "$LOCAL_ROOT/bin" "$ARTIFACTS" "$CLIENT_HOME")
  chmod 700 "$LOCAL_ROOT"
  log 'building local client binaries'
  (cd "$REPO_ROOT" && go build -o "$CLIENT_BIN" ./cmd/tubo && go build -o "$LOCAL_ROOT/bin/test-http-server" ./cmd/test-http-server && go build -o "$LOCAL_ROOT/bin/test-tcp-server" ./cmd/test-tcp-server)
  log "building linux/$REMOTE_GOARCH binaries"
  (cd "$REPO_ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH="$REMOTE_GOARCH" go build -o "$LOCAL_ROOT/bin/tubo-linux" ./cmd/tubo && CGO_ENABLED=0 GOOS=linux GOARCH="$REMOTE_GOARCH" go build -o "$LOCAL_ROOT/bin/test-http-server-linux" ./cmd/test-http-server && CGO_ENABLED=0 GOOS=linux GOARCH="$REMOTE_GOARCH" go build -o "$LOCAL_ROOT/bin/test-tcp-server-linux" ./cmd/test-tcp-server)
  for host in "$RELAY_HOST" "$PUBLISHER_HOST"; do
    remote "$host" "umask 077; mkdir -p '$REMOTE_ROOT/bin' '$REMOTE_ROOT/home' '$AUTHORITY_HOME' '$PUBLISHER_HOME' '$REMOTE_ROOT/logs' '$REMOTE_ROOT/pids'; chmod 700 '$REMOTE_ROOT'"
    remote_scp_to "$LOCAL_ROOT/bin/tubo-linux" "$host" "$REMOTE_ROOT/bin/tubo"
    remote "$host" "chmod 700 '$REMOTE_ROOT/bin/tubo'"
  done
  remote_scp_to "$LOCAL_ROOT/bin/test-http-server-linux" "$PUBLISHER_HOST" "$REMOTE_ROOT/bin/test-http-server"
  remote_scp_to "$LOCAL_ROOT/bin/test-tcp-server-linux" "$PUBLISHER_HOST" "$REMOTE_ROOT/bin/test-tcp-server"
  remote "$PUBLISHER_HOST" "chmod 700 '$REMOTE_ROOT/bin/test-http-server' '$REMOTE_ROOT/bin/test-tcp-server'"
}

clean_remote_host() {
  local host="$1"
  remote "$host" "result=0; if [ -d '$REMOTE_ROOT/pids' ]; then for file in '$REMOTE_ROOT'/pids/*.pid; do [ -f \"\$file\" ] || continue; expected_file=\"\${file%.pid}.expected\"; [ -r \"\$expected_file\" ] || { result=1; continue; }; expected=\$(cat \"\$expected_file\"); case \"\$expected\" in ''|*[!A-Za-z0-9_./-]*) result=1; continue;; esac; pid=\$(cat \"\$file\"); case \"\$pid\" in ''|*[!0-9]*) result=1; continue;; esac; [ -r \"/proc/\$pid/cmdline\" ] || continue; cmd=\$(tr '\\000' ' ' <\"/proc/\$pid/cmdline\"); case \"\$cmd\" in *'$REMOTE_ROOT/bin/'*\"\$expected\"*) kill -KILL \"\$pid\" 2>/dev/null || result=1;; *) result=1;; esac; done; fi; [ \"\$result\" -eq 0 ] || exit 1; rm -rf '$REMOTE_ROOT'"
}
record_local_runner() {
  local name="$1" pid="$2" expected="$3"
  is_id "$name" || fail 'unsafe local runner name'
  [[ "$pid" =~ ^[0-9]+$ ]] || fail 'invalid detached runner PID'
  validate_resource "$expected" || fail 'unsafe local runner resource'
  mkdir -p "$LOCAL_PID_DIR"
  printf '%s\n' "$pid" >"$LOCAL_PID_DIR/$name.pid"
  printf '%s\n' "$expected" >"$LOCAL_PID_DIR/$name.expected"
}
cleanup_local_state() {
  local result=0 file pid expected
  for file in "$LOCAL_PID_DIR"/*.pid; do
    [[ -f "$file" ]] || continue
    pid="$(cat "$file")"; expected_file="${file%.pid}.expected"
    if [[ ! "$pid" =~ ^[0-9]+$ ]] || [[ ! -r "$expected_file" ]]; then result=1; continue; fi
    expected="$(cat "$expected_file")"
    validate_resource "$expected" || { result=1; continue; }
    if kill -0 "$pid" 2>/dev/null; then
      kill_local_exact "$pid" "$expected" 2>/dev/null || { result=1; continue; }
      for _ in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
      kill -0 "$pid" 2>/dev/null && result=1
    fi
  done
  rm -rf "$CLIENT_HOME" "$VIEWER_HOME" "$LOCAL_ROOT/authority-home" "$LOCAL_ROOT/publisher-home" "$LOCAL_PID_DIR" || result=1
  rm -f "$LOCAL_ROOT/swarm.key" "$SECRET_VALUES_FILE" "$SSH_KNOWN_HOSTS" "$LOCAL_ROOT"/ssh-probe-*.stdout "$LOCAL_ROOT"/ssh-probe-*.stderr || result=1
  return "$result"
}
cleanup_run() {
  local result=0
  log "cleaning isolated run $RUN_ID"
  clean_remote_host "$PUBLISHER_HOST" || result=1
  clean_remote_host "$RELAY_HOST" || result=1
  cleanup_local_state || result=1
  return "$result"
}
collect_artifacts() {
  mkdir -p "$ARTIFACTS/relay" "$ARTIFACTS/publisher"
  remote_scp_from "$RELAY_HOST" "$REMOTE_ROOT/logs/." "$ARTIFACTS/relay/" 2>/dev/null || return 1
  remote_scp_from "$PUBLISHER_HOST" "$REMOTE_ROOT/logs/." "$ARTIFACTS/publisher/" 2>/dev/null || return 1
}
write_preflight_report() {
  local status="$1"
  mkdir -p "$ARTIFACTS"
  python3 - "$ARTIFACTS/report.json" "${E2E_SCENARIO_NAME:-unknown}" "$RUN_ID" "$status" <<'PY'
import json,sys
value={"scenario":sys.argv[2],"run_id":sys.argv[3],"result":"pass" if int(sys.argv[4]) == 0 else "fail","phase":"preflight","failure_class":"" if int(sys.argv[4]) == 0 else "preflight_failure"}
open(sys.argv[1],"w",encoding="utf-8").write(json.dumps(value,sort_keys=True)+"\n")
PY
}

# Always attempt artifacts, finalization, and cleanup. Original run failure wins.
finish_mutating_run() {
  local original="$1" result="$1" artifact_result=0 cleanup_result=0
  set +e
  # Preflight has no owned processes or remote root. Report only; never collect/cleanup.
  if [[ "$E2E_MUTATION_STARTED" != 1 ]]; then
    if declare -F periodic_write_report >/dev/null; then periodic_write_report "$original" || true; else write_preflight_report "$original" || true; fi
    return "$original"
  fi
  collect_artifacts || artifact_result=1
  if declare -F periodic_finalize >/dev/null; then
    [[ "$original" -eq 0 && "$artifact_result" -ne 0 && -z "${PERIODIC_FAILURE_CLASS:-}" ]] && PERIODIC_FAILURE_CLASS=artifact_finalization_failed
    periodic_finalize "$original" || artifact_result=1
  fi
  if [[ "$KEEP_WORK" == 1 ]]; then
    log "KEEP_WORK=1; preserving isolated state local=$LOCAL_ROOT remote=$REMOTE_ROOT"
  else
    cleanup_run || cleanup_result=1
  fi
  if [[ "$original" -ne 0 ]]; then
    if [[ "$artifact_result" -ne 0 || "$cleanup_result" -ne 0 ]]; then declare -F periodic_write_report >/dev/null && periodic_write_report "$original" || true; fi
    return "$original"
  fi
  if [[ "$artifact_result" -ne 0 ]]; then PERIODIC_FAILURE_CLASS=artifact_finalization_failed; declare -F periodic_write_report >/dev/null && periodic_write_report 1 || true; return 1; fi
  if [[ "$cleanup_result" -ne 0 ]]; then PERIODIC_FAILURE_CLASS=cleanup_failure; declare -F periodic_write_report >/dev/null && periodic_write_report 1 || true; return 1; fi
  return "$result"
}
