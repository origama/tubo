#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${RUN_DIR:-$ROOT_DIR/generated/distributed-two-host}"
ARTIFACT_DIR="${ARTIFACT_DIR:-$ROOT_DIR/generated/failure-campaign-two-host}"
REMOTE_HOST="${REMOTE_HOST:-root@172-232-189-160.ip.linodeusercontent.com}"
REMOTE_DIR="${REMOTE_DIR:-/tmp/tubo-distributed-smoke}"
EDGE="${EDGE:-http://127.0.0.1:18443}"
ADMIN="${ADMIN:-http://127.0.0.1:18444}"
SERVICE_NAME="${SERVICE_NAME:-myapi}"
SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new)
mkdir -p "$ARTIFACT_DIR"
REPORT="$ARTIFACT_DIR/report.md"

info() { echo "[failure-campaign] $*"; }
req() {
  local query="$1"
  local payload="${2:-x}"
  local body="$ARTIFACT_DIR/resp.tmp"
  local code
  code="$(curl -sS --max-time 20 -o "$body" -w '%{http_code}' -H "Host: $SERVICE_NAME" --data-binary "$payload" "$EDGE/v1/dummy?$query" || true)"
  printf '%s\n' "$code"
}
req_body() { cat "$ARTIFACT_DIR/resp.tmp" 2>/dev/null || true; }
admin_get() { curl -fsS "$ADMIN/$1"; }
remote_exec() { ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "$1"; }
remote_kill_ports() {
  local ports="$1"
  remote_exec "for port in $ports; do for pid in \$(lsof -tiTCP:\$port -sTCP:LISTEN 2>/dev/null | sort -u); do kill \$pid >/dev/null 2>&1 || true; done; done; sleep 1; for port in $ports; do for pid in \$(lsof -tiTCP:\$port -sTCP:LISTEN 2>/dev/null | sort -u); do kill -9 \$pid >/dev/null 2>&1 || true; done; done"
}
stop_relay() { remote_exec "rm -f '$REMOTE_DIR/relay.pid'"; remote_kill_ports "4001 18092"; }
start_relay() { stop_relay; remote_exec "cd '$REMOTE_DIR' && python3 -c \"import subprocess; f=open('relay.log','ab',0); p=subprocess.Popen(['./tubo','relay','run','--config','relay.yaml'], stdin=subprocess.DEVNULL, stdout=f, stderr=subprocess.STDOUT, start_new_session=True); open('relay.pid','w').write(str(p.pid))\""; }
stop_service() { remote_exec "rm -f '$REMOTE_DIR/service.pid'"; remote_kill_ports "40123 18091"; }
start_service() { stop_service; remote_exec "cd '$REMOTE_DIR' && python3 -c \"import subprocess; f=open('service.log','ab',0); p=subprocess.Popen(['./tubo','service','run','--config','service.yaml'], stdin=subprocess.DEVNULL, stdout=f, stderr=subprocess.STDOUT, start_new_session=True); open('service.pid','w').write(str(p.pid))\""; }
stop_dummy() { remote_exec "rm -f '$REMOTE_DIR/dummy-api-server.pid'"; remote_kill_ports "18000"; }
start_dummy() { stop_dummy; remote_exec "cd '$REMOTE_DIR' && python3 -c \"import os, subprocess; f=open('dummy-api-server.log','ab',0); p=subprocess.Popen(['./dummy-api-server'], stdin=subprocess.DEVNULL, stdout=f, stderr=subprocess.STDOUT, start_new_session=True, env=dict(os.environ, DUMMY_API_LISTEN='127.0.0.1:18000', DUMMY_API_INSTANCE='distributed-remote')); open('dummy-api-server.pid','w').write(str(p.pid))\""; }
stop_edge() { kill "$(cat "$RUN_DIR/edge.pid")" >/dev/null 2>&1 || true; for port in 18443 18444 4001; do for pid in $(lsof -tiTCP:"$port" -sTCP:LISTEN 2>/dev/null | sort -u); do kill "$pid" >/dev/null 2>&1 || true; done; done; }
start_edge() { stop_edge; nohup "$RUN_DIR/tubo" edge run --config "$RUN_DIR/edge.yaml" > "$RUN_DIR/edge.log" 2>&1 < /dev/null & echo $! > "$RUN_DIR/edge.pid"; }
wait_http() { local url="$1"; local tries="${2:-60}"; for i in $(seq 1 "$tries"); do curl -fsS "$url" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }
wait_remote_http() { local url="$1"; local tries="${2:-60}"; for i in $(seq 1 "$tries"); do remote_exec "curl -fsS '$url' >/dev/null" >/dev/null 2>&1 && return 0; sleep 1; done; return 1; }
wait_route() { for i in $(seq 1 90); do local s r; s="$(admin_get services || true)"; r="$(admin_get routes || true)"; if echo "$s" | grep -Eq '"count"[[:space:]]*:[[:space:]]*1' && echo "$r" | grep -q '"hostname":"'$SERVICE_NAME'"'; then return 0; fi; sleep 1; done; return 1; }
setup_bench() { KEEP_RUNNING=1 "$ROOT_DIR/tests/smoke-distributed-two-host.sh" >/dev/null; }
teardown_bench() {
  stop_edge || true
  rm -f "$RUN_DIR/edge.pid" >/dev/null 2>&1 || true
  remote_exec "cd '$REMOTE_DIR' 2>/dev/null || exit 0; rm -f ./*.pid" >/dev/null 2>&1 || true
  remote_kill_ports "18000 18091 18092 4001 40123" >/dev/null 2>&1 || true
}
append() { printf '%s\n' "$*" >> "$REPORT"; }
collect_logs() {
  local name="$1"
  cp "$RUN_DIR/edge.log" "$ARTIFACT_DIR/${name}-edge.log" 2>/dev/null || true
  remote_exec "cd '$REMOTE_DIR' && for f in relay.log service.log dummy-api-server.log; do [ -f \"\$f\" ] && cat \"\$f\"; echo '__FILE_SPLIT__'\"\$f\"; done" > "$ARTIFACT_DIR/${name}-remote-combined.log" || true
}
measure_recovery() {
  local prefix="$1"; local tries="${2:-40}"
  for i in $(seq 1 "$tries"); do
    local code; code="$(req "$prefix=$i")"
    if [ "$code" = "200" ]; then echo "$i"; return 0; fi
    sleep 1
  done
  echo "timeout"
  return 1
}
scenario_relay_restart_idle() {
  append "## relay_restart_idle"
  local before down_code recover_steps route_after
  before="$(req 'scenario=relay-restart-before')"
  down_code="pending"
  stop_relay; sleep 1
  down_code="$(req 'scenario=relay-restart-down')"
  start_relay
  wait_remote_http "http://127.0.0.1:18092/healthz" 30 || true
  recover_steps="$(measure_recovery 'scenario=relay-restart-recover' 50 || true)"
  route_after="$(admin_get routes || true)"
  append "- baseline status: $before"
  append "- while relay down: $down_code"
  append "- recovery steps to 200: $recover_steps"
  append "- routes after recovery: \
\`\`\`json
$route_after
\`\`\`"
  append "- notable log pattern: $(grep -E 'incoming message was too large|trailing newline' -m 2 "$RUN_DIR/edge.log" 2>/dev/null | tr '\n' '; ' || true)"
}
scenario_edge_restart() {
  append "## edge_restart"
  local down_err recover_steps services_json
  stop_edge; sleep 1
  down_err="$(curl -sS --max-time 5 -H "Host: $SERVICE_NAME" --data 'x' "$EDGE/v1/dummy?scenario=edge-down" 2>&1 || true)"
  start_edge
  wait_http "$EDGE/healthz" 30 || true
  wait_http "$ADMIN/healthz" 30 || true
  wait_route || true
  recover_steps="$(measure_recovery 'scenario=edge-recover' 20 || true)"
  services_json="$(admin_get services || true)"
  append "- request while edge down: \`$down_err\`"
  append "- recovery steps to 200 after restart: $recover_steps"
  append "- services after recovery: \`$services_json\`"
}
scenario_origin_down_up() {
  append "## origin_down_up"
  local down_code down_body up_code
  stop_dummy; sleep 1
  down_code="$(req 'scenario=origin-down')"
  down_body="$(req_body | tr '\n' ' ')"
  start_dummy
  wait_remote_http "http://127.0.0.1:18000/healthz" 20 || true
  up_code="$(measure_recovery 'scenario=origin-recover' 10 || true)"
  append "- while origin down status: $down_code"
  append "- while origin down body: \`$down_body\`"
  append "- recovery steps to 200 after origin restart: $up_code"
}
scenario_service_restart_staleness() {
  append "## service_restart_staleness"
  local t5 t15 t35 recover_steps
  stop_service
  sleep 5; t5="$(admin_get services || true) | $(admin_get routes || true)"
  sleep 10; t15="$(admin_get services || true) | $(admin_get routes || true)"
  sleep 20; t35="$(admin_get services || true) | $(admin_get routes || true)"
  start_service
  wait_remote_http "http://127.0.0.1:18091/healthz" 30 || true
  recover_steps="$(measure_recovery 'scenario=service-recover' 30 || true)"
  append "- after 5s down: \`$t5\`"
  append "- after 15s down: \`$t15\`"
  append "- after 35s down: \`$t35\`"
  append "- recovery steps to 200 after service restart: $recover_steps"
}
scenario_relay_kill_during_large_burst() {
  append "## relay_kill_during_large_burst"
  local payload codes
  payload="$(python3 - <<'PY'
print('L' * (512 * 1024))
PY
)"
  : > "$ARTIFACT_DIR/burst.codes"
  for i in $(seq 1 8); do
    (
      code="$(req "scenario=relay-burst-$i" "$payload")"
      printf '%s\n' "$code" >> "$ARTIFACT_DIR/burst.codes"
    ) &
  done
  sleep 1
  stop_relay
  wait || true
  start_relay
  wait_remote_http "http://127.0.0.1:18092/healthz" 30 || true
  codes="$(sort "$ARTIFACT_DIR/burst.codes" | uniq -c | tr '\n' '; ')"
  append "- burst status histogram after killing relay mid-flight: \`$codes\`"
  append "- edge stream errors: $(grep -E 'stream reset|unexpected EOF|stream_forward_failed|relay_unavailable' "$RUN_DIR/edge.log" | tail -n 6 | tr '\n' '; ' || true)"
}

cat > "$REPORT" <<'EOF'
# Two-host failure campaign

This report was generated against the real 2-host distributed bench.
EOF

for scenario in relay_restart_idle edge_restart origin_down_up service_restart_staleness relay_kill_during_large_burst; do
  info "scenario: $scenario"
  rm -f "$ARTIFACT_DIR"/*.log "$ARTIFACT_DIR"/burst.codes "$ARTIFACT_DIR"/resp.tmp 2>/dev/null || true
  teardown_bench || true
  setup_bench
  wait_route || true
  collect_logs "pre-${scenario}"
  "scenario_${scenario}"
  collect_logs "$scenario"
  teardown_bench || true
  append ""
done

info "report: $REPORT"
