#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
E2E="$ROOT_DIR/tests/e2e/three-machine"
SCENARIO="$E2E/scenarios/periodic-sync.sh"
SSH_SMOKE="$E2E/ssh-smoke.sh"

bash -n "$E2E/lib.sh" "$E2E/run.sh" "$E2E/scenarios/private-http.sh" "$SCENARIO" "$SSH_SMOKE"
grep -F 'private-http|periodic-sync' "$E2E/run.sh" >/dev/null
grep -F 'finish_mutating_run' "$E2E/run.sh" >/dev/null
grep -F 'record_local_runner sync-a' "$SCENARIO" >/dev/null
grep -F 'record_local_runner sync-b' "$SCENARIO" >/dev/null
grep -F 'E2E_MUTATION_STARTED=1' "$E2E/run.sh" >/dev/null
python3 - "$E2E/run.sh" <<'PY'
import sys
text=open(sys.argv[1],encoding='utf-8').read()
segment=text[text.index('run_preflight()'):text.index('run_mutating()')]
assert text.index('trap on_exit EXIT INT TERM') < text.index('run_preflight()')
assert segment.index('install_preflight_trap') < segment.index('\n  preflight')
PY
grep -F 'validate_e2e_config' "$E2E/lib.sh" >/dev/null
grep -F 'load_e2e_env' "$E2E/lib.sh" >/dev/null
! grep -F 'source "$E2E_DIR/.env.local"' "$E2E/lib.sh"
for option in 'StrictHostKeyChecking=yes' 'UpdateHostKeys=no' 'CheckHostIP=no' 'ControlMaster=no' 'ControlPath=none' 'ControlPersist=no'; do
  grep -F "$option" "$E2E/lib.sh" >/dev/null
done
grep -F 'UserKnownHostsFile=$SSH_KNOWN_HOSTS' "$E2E/lib.sh" >/dev/null
grep -F 'HostKeyAlias=$host' "$E2E/lib.sh" >/dev/null
grep -F 'ssh-keygen -F "$host" -f "$SSH_KNOWN_HOSTS_SOURCE"' "$E2E/lib.sh" >/dev/null
! grep -F 'ssh-keyscan' "$E2E/lib.sh"
grep -F 'ssh_preflight_probe' "$E2E/lib.sh" >/dev/null
grep -F 'ssh_effective_settings' "$E2E/lib.sh" >/dev/null
grep -F -- '-G -- "$host"' "$E2E/lib.sh" >/dev/null
grep -F '2>"$stderr"' "$E2E/lib.sh" >/dev/null
grep -F 'run_step trust prepare_ssh_trust' "$SSH_SMOKE" >/dev/null
grep -F 'run_step "$host-deploy" remote_scp_to' "$SSH_SMOKE" >/dev/null
grep -F 'start_remote_bg_home' "$SSH_SMOKE" >/dev/null
grep -F 'kill_remote_exact' "$SSH_SMOKE" >/dev/null
grep -F 'cat "$STEP_ERR"' "$SSH_SMOKE" >/dev/null
# Trust comes only from already-verified alias entries and every transport call uses the run bundle.
ssh_contract_home="$(mktemp -d "${TMPDIR:-/tmp}/tubo-e2e-ssh-home.XXXXXX")"
ssh_contract_run="contract-ssh-$$"
mkdir -p "$ssh_contract_home/.ssh"
printf '%s\n' \
  'tuborelay ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' \
  'oripi ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA' \
  >"$ssh_contract_home/.ssh/known_hosts"
ssh_contract_calls="$(mktemp "${TMPDIR:-/tmp}/tubo-e2e-ssh-calls.XXXXXX")"
HOME="$ssh_contract_home" TUBO_E2E_RUN_ID="$ssh_contract_run" SSH_CONTRACT_CALLS="$ssh_contract_calls" bash -c '
  source "$1"
  prepare_ssh_trust
  ssh() { printf "ssh\\0" >>"$SSH_CONTRACT_CALLS"; printf "%s\\0" "$@" >>"$SSH_CONTRACT_CALLS"; }
  scp() { printf "scp\\0" >>"$SSH_CONTRACT_CALLS"; printf "%s\\0" "$@" >>"$SSH_CONTRACT_CALLS"; }
  remote tuborelay true
  remote_scp_to source.bin oripi /tmp/destination.bin
  remote_scp_from tuborelay /tmp/source.bin destination.bin
  python3 - "$SSH_KNOWN_HOSTS" <<'"'"'PY'"'"'
import os,sys
assert os.stat(sys.argv[1]).st_mode & 0o777 == 0o600
PY
  grep -F tuborelay "$SSH_KNOWN_HOSTS" >/dev/null
  grep -F oripi "$SSH_KNOWN_HOSTS" >/dev/null
' bash "$E2E/lib.sh"
python3 - "$ssh_contract_calls" <<'PY'
import pathlib,sys
parts=pathlib.Path(sys.argv[1]).read_bytes().split(b'\0')
parts=[x.decode() for x in parts if x]
required={'StrictHostKeyChecking=yes','UpdateHostKeys=no','CheckHostIP=no','ControlMaster=no','ControlPath=none','ControlPersist=no'}
for marker in ('ssh','scp'):
    start=[i for i,v in enumerate(parts) if v == marker]
    assert start, (marker,parts)
    for i in start:
        end=next((j for j in range(i+1,len(parts)) if parts[j] in {'ssh','scp'}),len(parts))
        call=parts[i+1:end]
        assert required <= set(call), (marker,call)
        assert any(v.startswith('UserKnownHostsFile=') for v in call), (marker,call)
        assert any(v.startswith('HostKeyAlias=') for v in call), (marker,call)
        if marker == 'scp' and any(v.startswith('tuborelay:/tmp/source.bin') for v in call):
            assert any('r' in option[1:] for option in call if option.startswith('-')), call
PY
rm -rf "$ROOT_DIR/generated/e2e-three-machine/$ssh_contract_run" "$ssh_contract_home"
rm -f "$ssh_contract_calls"
grep -F 'periodic_secret_record' "$SCENARIO" >/dev/null
grep -F 'secret value in structured artifact' "$SCENARIO" >/dev/null
grep -F 'pipe create sync-a' "$SCENARIO" >/dev/null
grep -F 'pipe create sync-b' "$SCENARIO" >/dev/null
grep -F 'PERIODIC_SYNC_CADENCE_SECONDS=60' "$SCENARIO" >/dev/null
grep -F 'PERIODIC_SYNC_JITTER_SECONDS=6' "$SCENARIO" >/dev/null
grep -F 'PERIODIC_SYNC_ATTEMPT_TIMEOUT_SECONDS=20' "$SCENARIO" >/dev/null
grep -F 'PERIODIC_ELECTION_SUCCESS_TIMEOUT=$((PERIODIC_SYNC_CADENCE_SECONDS + PERIODIC_SYNC_JITTER_SECONDS + PERIODIC_SYNC_ATTEMPT_TIMEOUT_SECONDS + PERIODIC_ELECTION_POLL_GRACE_SECONDS))' "$SCENARIO" >/dev/null
grep -F 'periodic_wait_new_success "$pre_contender_success"' "$SCENARIO" >/dev/null
grep -F 'periodic_wait_successor_success "$pre_crash_success" "$PERIODIC_SUCCESSOR"' "$SCENARIO" >/dev/null
grep -F 'datetime.datetime.fromisoformat(p["last_success"].replace("Z","+00:00")) > baseline' "$SCENARIO" >/dev/null
grep -F 'periodic_wait_restarted_relay "$pre_kill_relay_started_at" "$restarted_relay_pid"' "$SCENARIO" >/dev/null
grep -F "cat '\$REMOTE_ROOT/pids/relay.pid'" "$SCENARIO" >/dev/null
grep -F "/proc/\$expected_pid/cmdline" "$SCENARIO" >/dev/null
grep -F "value.get(\"status\") == \"ready\" and started > before" "$SCENARIO" >/dev/null
grep -F "get pipes --scope solar/default --refresh --json" "$SCENARIO" >/dev/null
grep -F 'publisher-pipes-relay-restart-probe.json' "$SCENARIO" >/dev/null
grep -F 'publisher-ps-later-pipe-ready.json' "$SCENARIO" >/dev/null
grep -F 'periodic_redact_ps_artifacts' "$SCENARIO" >/dev/null
grep -F '"election_success":int(sys.argv[15])' "$SCENARIO" >/dev/null
grep -F 'periodic_fail assertion_failed' "$SCENARIO" >/dev/null
grep -F -- '--cached-only' "$SCENARIO" >/dev/null
# New success must be strictly later than baseline; stale setup success cannot win election.
bash -c '
  source "$1"; source "$2"
  sleep() { :; }
  PERIODIC_ELECTION_SUCCESS_TIMEOUT=2
  scope_json="{\"items\":[{\"sync\":{\"owner_ref\":\"pipe/sync-a\",\"last_success\":\"2026-07-29T15:48:27Z\"}}]}"
  periodic_client_scope() { printf "%s" "$scope_json"; }
  periodic_wait_new_success "2026-07-29T15:48:26Z"
  scope_json="{\"items\":[{\"sync\":{\"owner_ref\":\"pipe/sync-a\",\"last_success\":\"2026-07-29T15:48:26Z\"}}]}"
  if periodic_wait_new_success "2026-07-29T15:48:26Z" 2>/dev/null; then exit 1; fi
  scope_json="{\"items\":[{\"sync\":{\"owner_ref\":\"pipe/sync-b\",\"last_success\":\"2026-07-29T15:48:27Z\"}}]}"
  periodic_wait_successor_success "2026-07-29T15:48:26Z" pipe/sync-b
  scope_json="{\"items\":[{\"sync\":{\"owner_ref\":\"pipe/sync-a\",\"last_success\":\"2026-07-29T15:48:27Z\"}}]}"
  if periodic_wait_successor_success "2026-07-29T15:48:26Z" pipe/sync-b 2>/dev/null; then exit 1; fi
' bash "$E2E/lib.sh" "$SCENARIO"

# Relay restart readiness rejects stale state, PID mismatch, and missing fresh publisher handshake.
bash -c '
  source "$1"; source "$2"
  sleep() { :; }
  PERIODIC_RELAY_RESTART_TIMEOUT=2
  remote() { :; }
  relay_json="{\"pid\":42,\"status\":\"ready\",\"started_at\":\"2026-07-30T08:00:00Z\"}"
  remote_home_sh() { printf "%s" "$relay_json"; }
  if periodic_wait_restarted_relay "2026-07-30T08:00:00Z" 42 2>/dev/null; then exit 1; fi
  relay_json="{\"pid\":41,\"status\":\"ready\",\"started_at\":\"2026-07-30T08:00:01Z\"}"
  if periodic_wait_restarted_relay "2026-07-30T08:00:00Z" 42 2>/dev/null; then exit 1; fi
  relay_json="{\"pid\":42,\"status\":\"ready\",\"started_at\":\"2026-07-30T08:00:01Z\"}"
  periodic_wait_restarted_relay "2026-07-30T08:00:00Z" 42 >/dev/null
  relay_json="{\"scopes\":[{\"scope\":\"solar/default\",\"refresh_attempted\":true,\"last_success\":\"2026-07-30T08:00:00Z\"}]}"
  if periodic_wait_publisher_relay_probe "2026-07-30T08:00:00Z" 2>/dev/null; then exit 1; fi
  relay_json="{\"scopes\":[{\"scope\":\"solar/default\",\"refresh_attempted\":true,\"last_success\":\"2026-07-30T08:00:01Z\",\"refresh_error\":{\"class\":\"discovery_unreachable\"}}]}"
  if periodic_wait_publisher_relay_probe "2026-07-30T08:00:00Z" 2>/dev/null; then exit 1; fi
  relay_json="{\"scopes\":[{\"scope\":\"solar/default\",\"refresh_attempted\":true,\"last_success\":\"2026-07-30T08:00:01Z\"}]}"
  periodic_wait_publisher_relay_probe "2026-07-30T08:00:00Z" >/dev/null
' bash "$E2E/lib.sh" "$SCENARIO"

# Persisted process artifacts expose resource/status only; PID and network/process identity fields never survive.
bash -c '
  source "$1"; source "$2"
  ARTIFACTS="$(mktemp -d "${TMPDIR:-/tmp}/tubo-e2e-ps.XXXXXX")"
  raw="{\"items\":[{\"primary_ref\":\"pipe/sync-a\",\"status\":\"ready\",\"pid\":42,\"peer_id\":\"peer\",\"address\":\"127.0.0.1:1\",\"process_identity\":\"identity\"}]}"
  client_sh() { printf "%s" "$raw"; }
  remote_home_sh() { printf "%s" "$raw"; }
  periodic_capture_ps election
  periodic_redact_ps_artifacts
  python3 - "$ARTIFACTS" <<'"'"'PY'"'"'
import json,pathlib,sys
for path in pathlib.Path(sys.argv[1]).glob("*-ps-*.json"):
    value=json.load(open(path,encoding="utf-8"))
    assert set(value)=={"items"}, (path,value)
    for item in value["items"]:
        assert set(item) <= {"primary_ref","status"}, (path,item)
        assert not ({"pid","peer_id","address","process_identity"} & set(item)), (path,item)
        assert item == {"primary_ref":"pipe/sync-a","status":"ready"}, (path,item)
PY
  rm -rf "$ARTIFACTS"
' bash "$E2E/lib.sh" "$SCENARIO"

# Relay artifacts retain only safe status projection; raw relay logs have no allowlisted schema.
bash -c '
  source "$1"; source "$2"
  ARTIFACTS="$(mktemp -d "${TMPDIR:-/tmp}/tubo-e2e-relay.XXXXXX")"
  mkdir -p "$ARTIFACTS/relay"
  raw="{\"primary_ref\":\"relay/default\",\"status\":\"ready\",\"pid\":42,\"peer_id\":\"peer\",\"address\":\"127.0.0.1:1\",\"process_identity\":\"identity\"}"
  printf "%s\\n" "$raw" >"$ARTIFACTS/relay-initial-status.json"
  printf "%s\\n" "$raw" >"$ARTIFACTS/relay-recovered-status.json"
  printf "pid=42 peer_id=peer address=127.0.0.1:1 process_identity=identity\\n" >"$ARTIFACTS/relay/relay.log"
  periodic_redact_relay_artifacts
  python3 - "$ARTIFACTS" <<'"'"'PY'"'"'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
for path in root.glob("relay-*-status.json"):
    value=json.load(open(path,encoding="utf-8"))
    assert value == {"primary_ref":"relay/default","status":"ready"}, (path,value)
    assert not ({"pid","peer_id","address","process_identity"} & set(value)), (path,value)
assert not (root/"relay").exists(), "raw relay log survived"
PY
  rm -rf "$ARTIFACTS"
' bash "$E2E/lib.sh" "$SCENARIO"
if grep -REn --include='*.sh' '(pkill|killall|systemctl|launchctl|/usr/local/bin/tubo|~/.config/tubo|~/.local/share/tubo|rm -rf /tmp|rm -rf \$HOME)' "$E2E"; then
  echo '[contract] forbidden installed-state or broad-process operation' >&2
  exit 1
fi
python3 - "$SCENARIO" <<'PY'
import sys
text=open(sys.argv[1], encoding='utf-8').read()
setup=text.index('periodic_phase election')
for line in text[setup:].splitlines():
    if 'get pipes' in line and '--cached-only' not in line:
        raise SystemExit('non-cached pipe listing after election: '+line)
crash=text[text.index('periodic_phase crash_takeover'):text.index('periodic_phase relay_outage')]
assert crash.index('takeover_observed=1') < crash.index('periodic_wait_successor_success') < crash.index('periodic_capture_scope takeover'), crash
assert "samples[-1]['sync']['last_success']" not in crash, crash
PY

# Assertion helpers produce stable report classes instead of generic shell errors.
assertion_class="$(bash -c '
  source "$1"; source "$2"
  fail() { return 1; }
  if periodic_assert test <<'"'"'PY'"'"'
assert False
PY
  then exit 1; fi
  printf "%s" "$PERIODIC_FAILURE_CLASS"
' bash "$E2E/lib.sh" "$SCENARIO" 2>/dev/null)"
[[ "$assertion_class" == assertion_failed ]] || { echo '[contract] assertion failure class is unstable' >&2; exit 1; }
bash -c '
  source "$1"; source "$2"
  mkdir -p "$ARTIFACTS"; PERIODIC_FAILURE_CLASS=assertion_failed
  periodic_write_report 1
  python3 - "$ARTIFACTS/report.json" <<'"'"'PY'"'"'
import json,sys
report=json.load(open(sys.argv[1]))
assert report["failure_class"] == "assertion_failed", report
assert report["bounds_seconds"]["election_success"] == 90, report
PY
  rm -rf "$LOCAL_ROOT"
' bash "$E2E/lib.sh" "$SCENARIO"

# Invalid environment must fail before preflight can reach SSH/build. Sentinel proves no shell payload ran.
sentinel="$(mktemp "${TMPDIR:-/tmp}/tubo-e2e-contract.XXXXXX")"
rm -f "$sentinel"
for assignment in \
  "TUBO_E2E_RUN_ID=bad;touch-$sentinel" \
  'TUBO_E2E_RELAY_PORT=1;touch /tmp/never' \
  'TUBO_E2E_HTTP_PORT=70000' \
  'TUBO_E2E_RELAY_PUBLIC_IP=127.0.0.1;id' \
  'TUBO_E2E_RELAY_HOST=-oProxyCommand=x' \
  'TUBO_E2E_REMOTE_GOARCH=amd64;id' \
  'KEEP_WORK=unsafe'; do
  key="${assignment%%=*}"; value="${assignment#*=}"
  if env "$key=$value" "$E2E/run.sh" preflight >/dev/null 2>&1; then
    echo "[contract] invalid environment accepted: $key" >&2; exit 1
  fi
done
[[ ! -e "$sentinel" ]] || { echo '[contract] environment payload executed' >&2; exit 1; }

# Finalizer failure cannot skip cleanup; original failure status wins.
set +e
bash -c '
  source "$1"
  marker="$2"
  collect_artifacts() { return 1; }
  periodic_finalize() { return 1; }
  periodic_write_report() { :; }
  cleanup_run() { : >"$marker"; return 0; }
  E2E_MUTATION_STARTED=1; PERIODIC_FAILURE_CLASS=sync_takeover_timeout
  finish_mutating_run 7
' bash "$E2E/lib.sh" "$sentinel.cleanup"
status=$?
set -e
[[ "$status" -eq 7 && -f "$sentinel.cleanup" ]] || { echo '[contract] finalizer failure skipped cleanup or changed original status' >&2; exit 1; }
rm -f "$sentinel.cleanup"

# Preflight failure writes stable phase/class report and never invokes cleanup.
set +e
bash -c '
  source "$1"; source "$2"
  E2E_SCENARIO_NAME=periodic-sync; E2E_MUTATION_STARTED=0; PERIODIC_PHASE=preflight; PERIODIC_FAILURE_CLASS=preflight_failure
  cleanup_run() { : >"$3"; return 0; }
  finish_mutating_run 9
' bash "$E2E/lib.sh" "$SCENARIO" "$sentinel.preflight-cleanup"
status=$?
set -e
[[ "$status" -eq 9 && ! -e "$sentinel.preflight-cleanup" ]] || { echo '[contract] preflight failure mutated cleanup state' >&2; exit 1; }
preflight_root="$(python3 - "$ROOT_DIR/generated/e2e-three-machine" <<'PY'
import json,pathlib,sys
reports=sorted(pathlib.Path(sys.argv[1]).glob('*/artifacts/report.json'),key=lambda p:p.stat().st_mtime)
value=json.load(open(reports[-1]))
assert value['phase']=='preflight' and value['failure_class']=='preflight_failure',value
print(reports[-1].parent.parent)
PY
)"
rm -f "$sentinel.preflight-cleanup"
rm -rf "$preflight_root"

# Exact cleanup removes secret/local homes but retains redacted artifact directory.
bash -c '
  source "$1"
  remote() { :; }
  mkdir -p "$CLIENT_HOME" "$VIEWER_HOME" "$LOCAL_ROOT/authority-home" "$LOCAL_ROOT/publisher-home" "$LOCAL_ROOT/bin" "$ARTIFACTS"
  : >"$LOCAL_ROOT/swarm.key"; : >"$SECRET_VALUES_FILE"; : >"$ARTIFACTS/keep.json"
  printf "#!/usr/bin/env bash\\nsleep 60\\n" >"$LOCAL_ROOT/bin/tubo"; chmod 700 "$LOCAL_ROOT/bin/tubo"
  "$LOCAL_ROOT/bin/tubo" _run-pipe pipe/sync-a & runner_a=$!
  "$LOCAL_ROOT/bin/tubo" _run-pipe pipe/sync-b & runner_b=$!
  record_local_runner sync-a "$runner_a" "_run-pipe pipe/sync-a"
  record_local_runner sync-b "$runner_b" "_run-pipe pipe/sync-b"
  cleanup_run
  ! kill -0 "$runner_a" 2>/dev/null && ! kill -0 "$runner_b" 2>/dev/null
  test ! -e "$CLIENT_HOME" && test ! -e "$VIEWER_HOME" && test ! -e "$LOCAL_ROOT/authority-home" && test ! -e "$LOCAL_ROOT/publisher-home" && test ! -e "$LOCAL_ROOT/swarm.key" && test ! -e "$SECRET_VALUES_FILE" && test -f "$ARTIFACTS/keep.json"
  rm -rf "$LOCAL_ROOT"
' bash "$E2E/lib.sh"

# KEEP_WORK preserves exact isolated roots while default cleanup remains separately tested above.
bash -c '
  source "$1"
  mkdir -p "$LOCAL_ROOT"; : >"$LOCAL_ROOT/state-marker"
  cleanup_marker="$LOCAL_ROOT/cleanup-marker"
  collect_artifacts() { :; }
  periodic_finalize() { :; }
  cleanup_run() { : >"$cleanup_marker"; rm -rf "$LOCAL_ROOT"; }
  E2E_MUTATION_STARTED=1; KEEP_WORK=1; PERIODIC_FAILURE_CLASS=
  finish_mutating_run 0
  test -f "$LOCAL_ROOT/state-marker" && test ! -e "$cleanup_marker"
  rm -rf "$LOCAL_ROOT"
' bash "$E2E/lib.sh"

# Known full secret values redact text lines and reject structured artifact leakage.
bash -c '
  source "$1"; source "$2"
  mkdir -p "$ARTIFACTS"; periodic_secret_record "secret-value-123"
  printf "safe\nsecret-value-123\n" >"$ARTIFACTS/runtime.log"
  periodic_finalize 1
  ! grep -F "secret-value-123" "$ARTIFACTS/runtime.log"
  printf "{\"leak\":\"secret-value-123\"}\n" >"$ARTIFACTS/leak.json"
  set +e; periodic_finalize 1; status=$?; set -e
  test "$status" -ne 0
  rm -rf "$LOCAL_ROOT"
' bash "$E2E/lib.sh" "$SCENARIO"

echo '[contract] three-machine periodic-sync safety contract PASS'
