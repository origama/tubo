#!/usr/bin/env bash
set -eEuo pipefail

E2E_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export TUBO_E2E_RUN_ID="${TUBO_E2E_RUN_ID:-ssh-smoke-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
# shellcheck disable=SC1091
source "$E2E_DIR/lib.sh"
confirm_mutation

WORK="$(mktemp -d "${TMPDIR:-/tmp}/tubo-ssh-smoke.XXXXXX")"
FAILED_STEP=""
STEP_ERR=""
CLEANED=0

cleanup() {
  local status=$?
  if [[ "$CLEANED" != 1 ]]; then cleanup_run >/dev/null 2>&1 || true; fi
  rm -rf -- "$WORK" "$LOCAL_ROOT"
  exit "$status"
}
on_error() {
  local status=$?
  printf '[ssh-smoke] FAIL step=%s status=%s\n' "$FAILED_STEP" "$status" >&2
  [[ -n "$STEP_ERR" && -f "$STEP_ERR" ]] && cat "$STEP_ERR" >&2
  exit "$status"
}
trap on_error ERR
trap cleanup EXIT

run_step() {
  local name="$1"
  shift
  FAILED_STEP="$name"
  STEP_ERR="$WORK/$name.stderr"
  if ! "$@" >"$WORK/$name.stdout" 2>"$STEP_ERR"; then
    printf '[ssh-smoke] FAIL step=%s\n' "$name" >&2
    cat "$STEP_ERR" >&2
    return 1
  fi
  cat "$WORK/$name.stdout"
  FAILED_STEP=""
  STEP_ERR=""
}

PAYLOAD="$WORK/ssh-smoke"
printf '%s\n' '#!/usr/bin/env bash' 'exec -a "$0 $1" sleep 30' >"$PAYLOAD"
chmod 700 "$PAYLOAD"

run_step trust prepare_ssh_trust
for host in "$RELAY_HOST" "$PUBLISHER_HOST"; do
  run_step "$host-mkdir" remote "$host" "umask 077; mkdir -p '$REMOTE_ROOT/bin' '$REMOTE_ROOT/home' '$REMOTE_ROOT/logs' '$REMOTE_ROOT/pids'; chmod 700 '$REMOTE_ROOT'"
  run_step "$host-deploy" remote_scp_to "$PAYLOAD" "$host" "$REMOTE_ROOT/bin/ssh-smoke"
  run_step "$host-chmod" remote "$host" "chmod 700 '$REMOTE_ROOT/bin/ssh-smoke'; test -x '$REMOTE_ROOT/bin/ssh-smoke'"
  run_step "$host-start" start_remote_bg_home "$host" "$REMOTE_ROOT/home" ssh-smoke ssh-smoke-run "'$REMOTE_ROOT/bin/ssh-smoke' ssh-smoke-run"
  run_step "$host-running" remote "$host" "pid=\$(cat '$REMOTE_ROOT/pids/ssh-smoke.pid'); kill -0 \"\$pid\""
  run_step "$host-kill" kill_remote_exact "$host" ssh-smoke ssh-smoke-run
done
run_step exact-clean cleanup_run
CLEANED=1
printf '[ssh-smoke] PASS run_id=%s hosts=%s,%s\n' "$RUN_ID" "$RELAY_HOST" "$PUBLISHER_HOST"
