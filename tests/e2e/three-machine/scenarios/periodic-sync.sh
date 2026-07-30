#!/usr/bin/env bash
set -euo pipefail

PERIODIC_PHASE=bootstrap
PERIODIC_FAILURE_CLASS=""
PERIODIC_OWNER=""
PERIODIC_SUCCESSOR=""
PERIODIC_OLD_EXPIRY=""
PERIODIC_KILLED_AT=""
PERIODIC_ORIGIN_ID=""
PERIODIC_LATER_ID=""
PERIODIC_RELAY_ADDRESS=""
# Keep election bound aligned with runtime ScopeSyncCadence, jitter(cadence/10),
# and ScopeSyncAttemptTimeout. Poll grace bounds observation delay.
PERIODIC_SYNC_CADENCE_SECONDS=60
PERIODIC_SYNC_JITTER_SECONDS=6
PERIODIC_SYNC_ATTEMPT_TIMEOUT_SECONDS=20
PERIODIC_ELECTION_POLL_GRACE_SECONDS=4
PERIODIC_ELECTION_SUCCESS_TIMEOUT=$((PERIODIC_SYNC_CADENCE_SECONDS + PERIODIC_SYNC_JITTER_SECONDS + PERIODIC_SYNC_ATTEMPT_TIMEOUT_SECONDS + PERIODIC_ELECTION_POLL_GRACE_SECONDS))
PERIODIC_RELAY_RESTART_TIMEOUT=90

periodic_secret_record() {
  local value="$1"
  [[ -n "$value" ]] || periodic_fail internal_error 'empty generated secret'
  mkdir -p "$LOCAL_ROOT"
  (umask 077; printf '%s\n' "$value" >>"$SECRET_VALUES_FILE")
}

periodic_phase() {
  PERIODIC_PHASE="$1"
  mkdir -p "$ARTIFACTS"
  python3 - "$ARTIFACTS/phases.jsonl" "$PERIODIC_PHASE" <<'PY'
import datetime, json, sys
with open(sys.argv[1], "a", encoding="utf-8") as out:
    out.write(json.dumps({"at": datetime.datetime.now(datetime.UTC).isoformat(), "phase": sys.argv[2]}) + "\n")
PY
}
periodic_fail() { PERIODIC_FAILURE_CLASS="$1"; fail "$2"; }
periodic_assert() {
  local message="$1"
  shift
  python3 - "$@" || periodic_fail assertion_failed "$message"
}
periodic_client_scope() { client_sh "$CLIENT_BIN" get scopes --json; }
periodic_capture_scope() {
  local label="$1"
  periodic_client_scope >"$ARTIFACTS/client-scope-$label.json"
  remote_home_sh "$PUBLISHER_HOST" "$AUTHORITY_HOME" "'$REMOTE_ROOT/bin/tubo' get scopes --json" >"$ARTIFACTS/authority-scope-$label.json"
  remote_home_sh "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' get scopes --json" >"$ARTIFACTS/publisher-scope-$label.json"
}
periodic_ps_projection() {
  python3 -c '
import json,sys
value=json.load(sys.stdin)
items=value.get("items")
assert isinstance(items,list), value
projection=[]
for item in items:
    assert isinstance(item,dict) and isinstance(item.get("primary_ref"),str) and isinstance(item.get("status"),str), item
    projection.append({"primary_ref":item["primary_ref"],"status":item["status"]})
print(json.dumps({"items":projection},sort_keys=True))
'
}
periodic_capture_ps() {
  local label="$1"
  # PID/resource validation remains in private $LOCAL_PID_DIR manifests; artifacts retain safe resource/status proof only.
  client_sh "$CLIENT_BIN" ps --json | periodic_ps_projection >"$ARTIFACTS/client-ps-$label.json"
  remote_home_sh "$PUBLISHER_HOST" "$AUTHORITY_HOME" "'$REMOTE_ROOT/bin/tubo' ps --json" | periodic_ps_projection >"$ARTIFACTS/authority-ps-$label.json"
  remote_home_sh "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' ps --json" | periodic_ps_projection >"$ARTIFACTS/publisher-ps-$label.json"
}
periodic_scope_sample() {
  local path="$1"
  periodic_client_scope | python3 -c 'import datetime,json,sys; v=json.load(sys.stdin); print(json.dumps({"at":datetime.datetime.now(datetime.UTC).isoformat(),"sync":v["items"][0].get("sync",{})}))' >>"$path"
}
periodic_wait_python() {
  local tries="$1" script="$2"
  shift 2
  for _ in $(seq 1 "$tries"); do
    if periodic_client_scope | python3 -c "$script" "$@"; then return 0; fi
    sleep 1
  done
  return 1
}
periodic_wait_new_success() {
  local baseline="$1"
  periodic_wait_python "$PERIODIC_ELECTION_SUCCESS_TIMEOUT" 'import datetime,json,sys; baseline=datetime.datetime.fromisoformat(sys.argv[1].replace("Z","+00:00")); p=json.load(sys.stdin)["items"][0].get("sync",{}); assert p.get("owner_ref") in {"pipe/sync-a","pipe/sync-b"} and p.get("last_success") and datetime.datetime.fromisoformat(p["last_success"].replace("Z","+00:00")) > baseline' "$baseline"
}
periodic_wait_successor_success() {
  local baseline="$1" successor="$2"
  periodic_wait_python "$PERIODIC_ELECTION_SUCCESS_TIMEOUT" 'import datetime,json,sys; baseline=datetime.datetime.fromisoformat(sys.argv[1].replace("Z","+00:00")); p=json.load(sys.stdin)["items"][0].get("sync",{}); assert p.get("owner_ref") == sys.argv[2] and p.get("last_success") and datetime.datetime.fromisoformat(p["last_success"].replace("Z","+00:00")) > baseline' "$baseline" "$successor"
}
periodic_wait_restarted_relay() {
  local pre_kill_started_at="$1" expected_pid="$2" observed
  [[ "$expected_pid" =~ ^[0-9]+$ ]] || return 1
  for _ in $(seq 1 "$PERIODIC_RELAY_RESTART_TIMEOUT"); do
    if observed="$(remote_home_sh "$RELAY_HOST" "$REMOTE_ROOT/home" "'$REMOTE_ROOT/bin/tubo' inspect relay/default --json" 2>/dev/null)" &&
      printf '%s' "$observed" | python3 -c 'import datetime,json,sys; before=datetime.datetime.fromisoformat(sys.argv[1].replace("Z","+00:00")); value=json.load(sys.stdin); started=datetime.datetime.fromisoformat(value["started_at"].replace("Z","+00:00")); assert str(value.get("pid")) == sys.argv[2] and value.get("status") == "ready" and started > before' "$pre_kill_started_at" "$expected_pid"; then
      if remote "$RELAY_HOST" "test -r '/proc/$expected_pid/cmdline'; cmd=\$(tr '\\000' ' ' <'/proc/$expected_pid/cmdline'); case \"\$cmd\" in *'$REMOTE_ROOT'*relay*) exit 0;; *) exit 1;; esac" 2>/dev/null; then
        printf '%s' "$observed"
        return 0
      fi
    fi
    sleep 1
  done
  return 1
}
periodic_wait_publisher_relay_probe() {
  local relay_started_at="$1" observed
  for _ in $(seq 1 "$PERIODIC_RELAY_RESTART_TIMEOUT"); do
    if observed="$(remote_home_sh "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' get pipes --scope solar/default --refresh --json" 2>/dev/null)" &&
      printf '%s' "$observed" | python3 -c 'import datetime,json,sys; restarted=datetime.datetime.fromisoformat(sys.argv[1].replace("Z","+00:00")); value=json.load(sys.stdin); scope=next(item for item in value["scopes"] if item.get("scope") == "solar/default"); last=datetime.datetime.fromisoformat(scope["last_success"].replace("Z","+00:00")); assert scope.get("refresh_attempted") is True and not scope.get("refresh_error") and last > restarted' "$relay_started_at"; then
        printf '%s' "$observed"
        return 0
    fi
    sleep 1
  done
  return 1
}
periodic_redact_ps_artifacts() {
  python3 - "$ARTIFACTS" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1])
for path in root.rglob('*-ps-*.json'):
    value=json.loads(path.read_text(encoding='utf-8'))
    items=value.get('items')
    if not isinstance(items,list): raise SystemExit('invalid process artifact: '+str(path))
    projection=[]
    for item in items:
        if not isinstance(item,dict) or not isinstance(item.get('primary_ref'),str) or not isinstance(item.get('status'),str):
            raise SystemExit('invalid process artifact item: '+str(path))
        projection.append({'primary_ref':item['primary_ref'],'status':item['status']})
    path.write_text(json.dumps({'items':projection},sort_keys=True)+'\n',encoding='utf-8')
PY
}
periodic_redact_relay_artifacts() {
  python3 - "$ARTIFACTS" <<'PY'
import json,pathlib,shutil,sys
root=pathlib.Path(sys.argv[1])
for path in root.glob('relay-*-status.json'):
    value=json.loads(path.read_text(encoding='utf-8'))
    if not isinstance(value,dict) or not isinstance(value.get('primary_ref'),str) or not isinstance(value.get('status'),str):
        raise SystemExit('invalid relay status artifact: '+str(path))
    path.write_text(json.dumps({'primary_ref':value['primary_ref'],'status':value['status']},sort_keys=True)+'\n',encoding='utf-8')
# Relay logs have no safe schema. Retain only allowlisted relay status projections.
shutil.rmtree(root/'relay', ignore_errors=True)
PY
}
periodic_assert_redacted() {
  periodic_assert 'artifact redaction assertion failed' "$@" <<'PY'
import json,sys
def walk(value):
    if isinstance(value,dict):
        assert not ({"token","process_identity"} & set(value)), value
        for child in value.values(): walk(child)
    elif isinstance(value,list):
        for child in value: walk(child)
for path in sys.argv[1:]: walk(json.load(open(path,encoding="utf-8")))
PY
}
periodic_write_report() {
  local status="$1"
  mkdir -p "$ARTIFACTS"
  python3 - "$ARTIFACTS" "$RUN_ID" "$PERIODIC_PHASE" "${PERIODIC_FAILURE_CLASS:-internal_error}" "$PERIODIC_OWNER" "$PERIODIC_SUCCESSOR" "$PERIODIC_ORIGIN_ID" "$PERIODIC_LATER_ID" "$PERIODIC_KILLED_AT" "$PERIODIC_OLD_EXPIRY" "$status" "$SYNC_TAKEOVER_TIMEOUT" "$SYNC_OUTAGE_TIMEOUT" "$SYNC_RECOVERY_TIMEOUT" "$PERIODIC_ELECTION_SUCCESS_TIMEOUT" "$RELAY_HOST" "$PUBLISHER_HOST" "$CLIENT_HOST" "$PERIODIC_RELAY_ADDRESS" <<'PY'
import json,pathlib,sys
root=pathlib.Path(sys.argv[1]); status=int(sys.argv[11])
report={"scenario":"periodic-sync","run_id":sys.argv[2],"result":"pass" if status == 0 else "fail","phase":sys.argv[3],"failure_class":sys.argv[4] if status else "","owner_ref":sys.argv[5],"successor_ref":sys.argv[6],"origin_pipe_id":sys.argv[7],"new_pipe_id":sys.argv[8],"killed_at":sys.argv[9],"old_lease_expires_at":sys.argv[10],"hosts":{"relay":sys.argv[16],"publisher":sys.argv[17],"client":sys.argv[18]},"relay_address":sys.argv[19],"bounds_seconds":{"takeover":int(sys.argv[12]),"outage":int(sys.argv[13]),"recovery":int(sys.argv[14]),"election_success":int(sys.argv[15])},"artifacts":{"source":"source.json","phases":"phases.jsonl","relay_initial":"relay-initial-status.json","relay_recovered":"relay-recovered-status.json","relay_restart_probe":"publisher-pipes-relay-restart-probe.json","later_pipe_ready":"publisher-ps-later-pipe-ready.json","takeover_samples":"takeover-samples.jsonl","scope_prefixes":["authority-scope-","publisher-scope-","client-scope-"],"process_prefixes":["authority-ps-","publisher-ps-","client-ps-"],"cached_listings":["client-pipes-setup.json","client-pipes-before-outage.json","client-pipes-during-outage.json","client-pipes-convergence.json"]}}
(root/'report.json').write_text(json.dumps(report,sort_keys=True)+"\n",encoding="utf-8")
PY
}
periodic_finalize() {
  local status="$1"
  mkdir -p "$ARTIFACTS"
  # Remove complete secret-bearing log lines. JSON/artifact records containing a full known secret fail closed.
  python3 - "$ARTIFACTS" "$SECRET_VALUES_FILE" <<'PY'
import pathlib,sys
root=pathlib.Path(sys.argv[1]); secret_file=pathlib.Path(sys.argv[2])
secrets=[line for line in secret_file.read_text(encoding="utf-8").splitlines() if line] if secret_file.exists() else []
rejected=[]
for path in root.rglob('*'):
    if not path.is_file(): continue
    try: text=path.read_text(encoding="utf-8")
    except UnicodeDecodeError: raise SystemExit("non-text artifact: "+str(path))
    if not any(secret in text for secret in secrets): continue
    if path.suffix in {".json", ".jsonl"}:
        path.unlink(); rejected.append(str(path)); continue
    kept=[line for line in text.splitlines(keepends=True) if not any(secret in line for secret in secrets)]
    path.write_text(''.join(kept),encoding="utf-8")
for path in root.rglob('*'):
    if path.is_file():
        text=path.read_text(encoding="utf-8")
        if any(secret in text for secret in secrets): raise SystemExit("secret value remained: "+str(path))
if rejected: raise SystemExit("secret value in structured artifact: "+", ".join(rejected))
PY
  [[ $? -eq 0 ]] || return 1
  periodic_redact_ps_artifacts || return 1
  periodic_redact_relay_artifacts || return 1
  periodic_write_report "$status"
}

run_periodic_sync() {
  periodic_phase bootstrap
  build_and_deploy
  # Local target values are privacy-sensitive artifact values too.
  periodic_secret_record "http://127.0.0.1:$HTTP_PORT"
  periodic_secret_record "127.0.0.1:$HTTP_PORT"
  source_head="$(git -C "$REPO_ROOT" rev-parse HEAD)"
  source_dirty="$(git -C "$REPO_ROOT" status --short)"
  source_local_sha="$(sha256_file "$CLIENT_BIN")"

  client_sh "$CLIENT_BIN" keygen swarm --out "$LOCAL_ROOT/swarm.key" >/dev/null
  periodic_secret_record "$(<"$LOCAL_ROOT/swarm.key")"
  remote_scp_to "$LOCAL_ROOT/swarm.key" "$RELAY_HOST" "$REMOTE_ROOT/swarm.key"
  remote_scp_to "$LOCAL_ROOT/swarm.key" "$PUBLISHER_HOST" "$REMOTE_ROOT/swarm.key"
  remote "$RELAY_HOST" "chmod 600 '$REMOTE_ROOT/swarm.key'"
  source_remote_sha="$(remote "$PUBLISHER_HOST" "chmod 600 '$REMOTE_ROOT/swarm.key'; sha256sum '$REMOTE_ROOT/bin/tubo'" | awk '{print $1}')"
  python3 - "$ARTIFACTS/source.json" "$source_head" "$source_dirty" "$source_local_sha" "$source_remote_sha" <<'PY'
import json,sys
paths=[line for line in sys.argv[3].splitlines() if line]
value={"head":sys.argv[2],"dirty":bool(paths),"dirty_paths":paths,"local_binary_sha256":sys.argv[4],"remote_binary_sha256":sys.argv[5]}
open(sys.argv[1],"w",encoding="utf-8").write(json.dumps(value,sort_keys=True)+"\n")
PY

  start_remote_bg_home "$RELAY_HOST" "$REMOTE_ROOT/home" relay relay "'$REMOTE_ROOT/bin/tubo' relay --listen /ip4/0.0.0.0/tcp/$RELAY_PORT --public-addr /ip4/$RELAY_PUBLIC_IP/tcp/$RELAY_PORT --swarm-key '$REMOTE_ROOT/swarm.key'"
  relay_status="$(wait_remote_home_json "$RELAY_HOST" "$REMOTE_ROOT/home" "'$REMOTE_ROOT/bin/tubo' inspect relay/default --json" "value['status']" ready 90)" || periodic_fail relay_unreachable 'relay did not become ready'
  printf '%s\n' "$relay_status" >"$ARTIFACTS/relay-initial-status.json"
  relay_peer_id="$(printf '%s' "$relay_status" | json_get "value['peer_id']")"
  validate_generated_value "$relay_peer_id"
  relay_addr="/ip4/$RELAY_PUBLIC_IP/tcp/$RELAY_PORT/p2p/$relay_peer_id"
  PERIODIC_RELAY_ADDRESS="$relay_addr"

  periodic_phase authority
  remote_home_sh "$PUBLISHER_HOST" "$AUTHORITY_HOME" "'$REMOTE_ROOT/bin/tubo' join overlay --relay '$relay_addr' --swarm-key '$REMOTE_ROOT/swarm.key' --force-reachability private --json" >/dev/null
  remote_home_sh "$PUBLISHER_HOST" "$AUTHORITY_HOME" "'$REMOTE_ROOT/bin/tubo' scope create solar/default --json" >/dev/null
  start_remote_bg_home "$PUBLISHER_HOST" "$AUTHORITY_HOME" authority authority "'$REMOTE_ROOT/bin/tubo' authority start solar --scope solar/default"
  wait_remote_home_json "$PUBLISHER_HOST" "$AUTHORITY_HOME" "'$REMOTE_ROOT/bin/tubo' inspect authority/solar --json" "value['status']" ready 90 >/dev/null || periodic_fail authority_unreachable 'authority did not become ready'
  publisher_invite="$(remote_home_sh "$PUBLISHER_HOST" "$AUTHORITY_HOME" "'$REMOTE_ROOT/bin/tubo' scope invite solar/default --role publisher --expires 2h --json")"
  publisher_token="$(printf '%s' "$publisher_invite" | json_get "value['token']")"
  validate_generated_value "$publisher_token"; periodic_secret_record "$publisher_token"
  remote_home_sh "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' join overlay --relay '$relay_addr' --swarm-key '$REMOTE_ROOT/swarm.key' --force-reachability private --json" >/dev/null
  remote_home_sh "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' scope join --token '$publisher_token' --json" >/dev/null
  unset publisher_invite publisher_token
  periodic_capture_scope authority
  periodic_assert 'authority scope assertion failed' "$ARTIFACTS/authority-scope-authority.json" <<'PY'
import json,sys
row=json.load(open(sys.argv[1]))['items'][0]
assert row['authority'] is True and row.get('sync',{}).get('owner_ref') is None, row
PY

  periodic_phase origin
  start_remote_bg_home "$PUBLISHER_HOST" "$PUBLISHER_HOME" test-http test-http-server "'$REMOTE_ROOT/bin/test-http-server' --listen 127.0.0.1:$HTTP_PORT --instance oripi"
  for _ in $(seq 1 60); do remote "$PUBLISHER_HOST" "curl -fsS http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null 2>&1 && break; sleep 1; done
  remote "$PUBLISHER_HOST" "curl -fsS http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null || periodic_fail pipe_readiness_timeout 'origin HTTP service did not become healthy'
  remote_home_sh "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' service create echo --target http://127.0.0.1:$HTTP_PORT --json" >/dev/null
  origin_create="$(remote_home_sh "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' pipe create echo-team --ingress tubo --target service/echo --scope solar/default --visibility discoverable --access members --json")"
  PERIODIC_ORIGIN_ID="$(printf '%s' "$origin_create" | json_get "value['id']")"
  validate_generated_value "$PERIODIC_ORIGIN_ID"
  later_create="$(remote_home_sh "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' pipe create echo-later --ingress tubo --target service/echo --scope solar/default --visibility discoverable --access members --json")"
  PERIODIC_LATER_ID="$(printf '%s' "$later_create" | json_get "value['id']")"
  validate_generated_value "$PERIODIC_LATER_ID"
  start_remote_bg_home "$PUBLISHER_HOST" "$PUBLISHER_HOME" origin-pipe pipe/echo-team "'$REMOTE_ROOT/bin/tubo' start pipe/echo-team"
  wait_remote_home_json "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' ps --json" "any(item.get('primary_ref') == 'pipe/echo-team' and item.get('status') == 'ready' for item in value['items'])" True 120 >/dev/null || periodic_fail pipe_readiness_timeout 'origin pipe did not become ready'
  wait_remote_home_json "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' get scopes --json" "value['items'][0].get('sync',{}).get('owner_ref')" pipe/echo-team 90 >/dev/null || periodic_fail sync_takeover_timeout 'publisher runtime did not become its local sync owner'
  periodic_capture_scope origin
  periodic_assert 'origin scope assertion failed' "$ARTIFACTS/authority-scope-origin.json" "$ARTIFACTS/publisher-scope-origin.json" <<'PY'
import json,sys
authority=json.load(open(sys.argv[1]))['items'][0].get('sync',{})
publisher=json.load(open(sys.argv[2]))['items'][0].get('sync',{})
assert authority.get('owner_ref') is None, authority
assert publisher.get('owner_ref') == 'pipe/echo-team', publisher
PY
  member_invite="$(remote_home_sh "$PUBLISHER_HOST" "$AUTHORITY_HOME" "'$REMOTE_ROOT/bin/tubo' scope invite solar/default --role member --expires 2h --json")"
  member_token="$(printf '%s' "$member_invite" | json_get "value['token']")"
  validate_generated_value "$member_token"; periodic_secret_record "$member_token"
  client_sh "$CLIENT_BIN" join overlay --relay "$relay_addr" --swarm-key "$LOCAL_ROOT/swarm.key" --force-reachability private --json >/dev/null
  client_sh "$CLIENT_BIN" scope join --token "$member_token" --json >/dev/null
  unset member_invite member_token
  client_sh "$CLIENT_BIN" get pipes --scope solar/default --refresh --json >"$ARTIFACTS/client-pipes-setup.json"
  periodic_client_scope >"$ARTIFACTS/client-scope-before-contenders.json"
  periodic_assert 'setup discovery assertion failed' "$ARTIFACTS/client-pipes-setup.json" "$PERIODIC_ORIGIN_ID" "$PERIODIC_LATER_ID" <<'PY'
import json,sys
value=json.load(open(sys.argv[1])); ids={item['id'] for item in value['items']}
assert sys.argv[2] in ids and sys.argv[3] not in ids, value
PY

  periodic_phase election
  client_sh "$CLIENT_BIN" pipe create sync-a --ingress "local://127.0.0.1:$SYNC_A_PORT" --target "pipe/$PERIODIC_ORIGIN_ID" --json >/dev/null
  client_sh "$CLIENT_BIN" pipe create sync-b --ingress "local://127.0.0.1:$SYNC_B_PORT" --target "pipe/$PERIODIC_ORIGIN_ID" --json >/dev/null
  sync_a_start="$(client_sh "$CLIENT_BIN" start pipe/sync-a -d --json)"
  sync_a_pid="$(printf '%s' "$sync_a_start" | json_get "value['pid']")"
  record_local_runner sync-a "$sync_a_pid" '_run-pipe pipe/sync-a'
  sync_b_start="$(client_sh "$CLIENT_BIN" start pipe/sync-b -d --json)"
  sync_b_pid="$(printf '%s' "$sync_b_start" | json_get "value['pid']")"
  record_local_runner sync-b "$sync_b_pid" '_run-pipe pipe/sync-b'
  pre_contender_success="$(json_get "value['items'][0]['sync']['last_success']" <"$ARTIFACTS/client-scope-before-contenders.json")" || periodic_fail assertion_failed 'pre-contender scope has no sync success baseline'
  [[ -n "$pre_contender_success" ]] || periodic_fail assertion_failed 'pre-contender scope has empty sync success baseline'
  periodic_wait_new_success "$pre_contender_success" || periodic_fail sync_takeover_timeout 'no periodic owner with post-contender sync success before bounded election deadline'
  periodic_capture_scope election
  periodic_capture_ps election
  periodic_assert 'election assertion failed' "$ARTIFACTS/client-scope-before-contenders.json" "$ARTIFACTS/client-scope-election.json" "$ARTIFACTS/client-ps-election.json" <<'PY'
import datetime,json,sys
before=json.load(open(sys.argv[1]))['items'][0].get('sync',{}); scope=json.load(open(sys.argv[2]))['items'][0]['sync']; runs=json.load(open(sys.argv[3]))['items']
assert set(scope) <= {'owner_ref','lease_expires_at','last_success','next_retry','error_class'}, scope
assert scope['owner_ref'] in {'pipe/sync-a','pipe/sync-b'}, scope
assert datetime.datetime.fromisoformat(scope['last_success'].replace('Z','+00:00')) > datetime.datetime.fromisoformat(before['last_success'].replace('Z','+00:00')), (before, scope)
live={x['primary_ref']:x for x in runs if x['primary_ref'] in {'pipe/sync-a','pipe/sync-b'}}
assert set(live)=={'pipe/sync-a','pipe/sync-b'} and all(x['status']=='ready' for x in live.values()), live
PY
  PERIODIC_OWNER="$(json_get "value['items'][0]['sync']['owner_ref']" <"$ARTIFACTS/client-scope-election.json")"
  periodic_client_scope >"$ARTIFACTS/client-scope-pre-crash.json"
  pre_crash_owner="$(json_get "value['items'][0]['sync']['owner_ref']" <"$ARTIFACTS/client-scope-pre-crash.json")"
  [[ "$pre_crash_owner" == "$PERIODIC_OWNER" ]] || periodic_fail sync_takeover_timeout 'owner changed before exact crash sample'
  PERIODIC_OLD_EXPIRY="$(json_get "value['items'][0]['sync']['lease_expires_at']" <"$ARTIFACTS/client-scope-pre-crash.json")"
  owner_pid="$sync_a_pid"; [[ "$PERIODIC_OWNER" == pipe/sync-b ]] && owner_pid="$sync_b_pid"
  PERIODIC_SUCCESSOR="pipe/sync-b"; [[ "$PERIODIC_OWNER" == pipe/sync-b ]] && PERIODIC_SUCCESSOR="pipe/sync-a"
  pre_crash_success="$(json_get "value['items'][0]['sync']['last_success']" <"$ARTIFACTS/client-scope-pre-crash.json")" || periodic_fail assertion_failed 'pre-crash scope has no sync success baseline'
  [[ -n "$pre_crash_success" ]] || periodic_fail assertion_failed 'pre-crash scope has empty sync success baseline'

  periodic_phase crash_takeover
  PERIODIC_KILLED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  kill_local_exact "$owner_pid" "_run-pipe $PERIODIC_OWNER" || periodic_fail unsafe_pid_rejected 'owner PID/root/resource validation rejected SIGKILL'
  samples="$ARTIFACTS/takeover-samples.jsonl"
  takeover_observed=0
  for _ in $(seq 1 "$SYNC_TAKEOVER_TIMEOUT"); do
    periodic_scope_sample "$samples"
    if periodic_client_scope | python3 -c "import json,sys; p=json.load(sys.stdin)['items'][0].get('sync',{}); assert p.get('owner_ref') == '$PERIODIC_SUCCESSOR'"; then takeover_observed=1; break; fi
    sleep 1
  done
  [[ "$takeover_observed" == 1 ]] || periodic_fail sync_takeover_timeout 'successor did not take over stored lease before bounded takeover deadline'
  periodic_wait_successor_success "$pre_crash_success" "$PERIODIC_SUCCESSOR" || periodic_fail sync_takeover_timeout 'successor did not record post-crash sync success before cadence/jitter/attempt deadline'
  periodic_capture_scope takeover
  periodic_capture_ps takeover
  periodic_assert 'crash takeover assertion failed' "$samples" "$ARTIFACTS/client-scope-pre-crash.json" "$ARTIFACTS/client-scope-takeover.json" "$PERIODIC_OWNER" "$PERIODIC_SUCCESSOR" "$PERIODIC_OLD_EXPIRY" <<'PY'
import datetime,json,sys
before=json.load(open(sys.argv[2]))['items'][0]['sync']; after=json.load(open(sys.argv[3]))['items'][0]['sync']; old,new,expiry=sys.argv[4:]; expiry=datetime.datetime.fromisoformat(sys.argv[6].replace('Z','+00:00'))
samples=[json.loads(x) for x in open(sys.argv[1])]
assert samples and samples[-1]['sync'].get('owner_ref') == new, samples[-1] if samples else None
for sample in samples:
    at=datetime.datetime.fromisoformat(sample['at'].replace('Z','+00:00'))
    if at < expiry: assert sample['sync'].get('owner_ref') != new, sample
assert after['owner_ref'] == new, after
assert datetime.datetime.fromisoformat(after['lease_expires_at'].replace('Z','+00:00')) > expiry
assert datetime.datetime.fromisoformat(after['last_success'].replace('Z','+00:00')) > datetime.datetime.fromisoformat(before['last_success'].replace('Z','+00:00'))
PY
  curl -fsS "http://127.0.0.1:${SYNC_A_PORT}/healthz" >/dev/null 2>&1 || curl -fsS "http://127.0.0.1:${SYNC_B_PORT}/healthz" >/dev/null 2>&1 || periodic_fail data_plane_mismatch 'surviving local listener did not serve origin HTTP'

  periodic_phase relay_outage
  client_sh "$CLIENT_BIN" get pipes --scope solar/default --cached-only --json >"$ARTIFACTS/client-pipes-before-outage.json"
  pre_kill_relay_started_at="$(printf '%s' "$relay_status" | json_get "value['started_at']")" || periodic_fail assertion_failed 'initial relay status has no started_at'
  [[ -n "$pre_kill_relay_started_at" ]] || periodic_fail assertion_failed 'initial relay status has empty started_at'
  kill_remote_exact "$RELAY_HOST" relay relay || periodic_fail unsafe_pid_rejected 'relay PID/root/resource validation rejected SIGKILL'
  outage_ok=0
  for _ in $(seq 1 "$SYNC_OUTAGE_TIMEOUT"); do
    periodic_capture_scope outage
    if python3 - "$ARTIFACTS/client-scope-outage.json" "$PERIODIC_SUCCESSOR" <<'PY'
import json,sys
p=json.load(open(sys.argv[1]))['items'][0].get('sync',{})
assert p.get('owner_ref') == sys.argv[2] and p.get('error_class') in {'authority_unreachable','discovery_unreachable'} and p.get('next_retry')
PY
    then outage_ok=1; break; fi
    sleep 1
  done
  [[ "$outage_ok" == 1 ]] || periodic_fail authority_unreachable 'periodic owner did not record bounded relay outage'
  client_sh "$CLIENT_BIN" get pipes --scope solar/default --cached-only --json >"$ARTIFACTS/client-pipes-during-outage.json"
  periodic_assert 'outage cache assertion failed' "$ARTIFACTS/client-pipes-during-outage.json" "$PERIODIC_ORIGIN_ID" <<'PY'
import json,sys
assert sys.argv[2] in {item['id'] for item in json.load(open(sys.argv[1]))['items']}
PY

  periodic_phase recovery
  start_remote_bg_home "$RELAY_HOST" "$REMOTE_ROOT/home" relay relay "'$REMOTE_ROOT/bin/tubo' relay --listen /ip4/0.0.0.0/tcp/$RELAY_PORT --public-addr /ip4/$RELAY_PUBLIC_IP/tcp/$RELAY_PORT --swarm-key '$REMOTE_ROOT/swarm.key'"
  restarted_relay_pid="$(remote "$RELAY_HOST" "cat '$REMOTE_ROOT/pids/relay.pid'")" || periodic_fail relay_unreachable 'restarted relay PID file could not be read'
  [[ "$restarted_relay_pid" =~ ^[0-9]+$ ]] || periodic_fail unsafe_pid_rejected 'restarted relay PID file is invalid'
  recovered_relay="$(periodic_wait_restarted_relay "$pre_kill_relay_started_at" "$restarted_relay_pid")" || periodic_fail relay_unreachable 'relay did not become a fresh live ready process after restart'
  printf '%s\n' "$recovered_relay" >"$ARTIFACTS/relay-recovered-status.json"
  [[ "$(printf '%s' "$recovered_relay" | json_get "value['peer_id']")" == "$relay_peer_id" ]] || periodic_fail relay_identity_changed 'isolated relay peer ID changed after restart'
  periodic_wait_python "$SYNC_RECOVERY_TIMEOUT" "import json,sys; p=json.load(sys.stdin)['items'][0].get('sync',{}); assert p.get('owner_ref') == '$PERIODIC_SUCCESSOR' and p.get('last_success') and not p.get('error_class') and not p.get('next_retry')" || periodic_fail sync_recovery_timeout 'periodic owner did not recover authority sync'
  periodic_capture_scope recovery
  periodic_capture_ps recovery
  periodic_assert 'recovery assertion failed' "$ARTIFACTS/client-scope-outage.json" "$ARTIFACTS/client-scope-recovery.json" "$ARTIFACTS/authority-ps-recovery.json" "$ARTIFACTS/publisher-ps-recovery.json" "$ARTIFACTS/client-ps-recovery.json" "$PERIODIC_SUCCESSOR" <<'PY'
import datetime,json,sys
outage=json.load(open(sys.argv[1]))['items'][0]['sync']; recovered=json.load(open(sys.argv[2]))['items'][0]['sync']
assert datetime.datetime.fromisoformat(recovered['last_success'].replace('Z','+00:00')) > datetime.datetime.fromisoformat(outage['last_success'].replace('Z','+00:00')), (outage,recovered)
for path, ref in zip(sys.argv[3:6], ('authority/solar','pipe/echo-team',sys.argv[6])):
    rows=json.load(open(path))['items']
    assert any(x.get('primary_ref') == ref and x.get('status') == 'ready' for x in rows), (path,rows)
PY
  publisher_relay_probe="$(periodic_wait_publisher_relay_probe "$(printf '%s' "$recovered_relay" | json_get "value['started_at']")")" || periodic_fail relay_unreachable 'fresh publisher relay handshake probe did not refresh after restart'
  printf '%s\n' "$publisher_relay_probe" >"$ARTIFACTS/publisher-pipes-relay-restart-probe.json"
  start_remote_bg_home "$PUBLISHER_HOST" "$PUBLISHER_HOME" later-pipe pipe/echo-later "'$REMOTE_ROOT/bin/tubo' start pipe/echo-later"
  later_pipe_ready="$(wait_remote_home_json "$PUBLISHER_HOST" "$PUBLISHER_HOME" "'$REMOTE_ROOT/bin/tubo' ps --json" "any(item.get('primary_ref') == 'pipe/echo-later' and item.get('status') == 'ready' for item in value['items'])" True 120)" || periodic_fail pipe_readiness_timeout 'new published pipe did not become ready'
  printf '%s\n' "$later_pipe_ready" >"$ARTIFACTS/publisher-ps-later-pipe-ready.json"
  converged=0
  for _ in $(seq 1 "$SYNC_RECOVERY_TIMEOUT"); do
    client_sh "$CLIENT_BIN" get pipes --scope solar/default --cached-only --json >"$ARTIFACTS/client-pipes-convergence.json"
    periodic_client_scope >"$ARTIFACTS/client-scope-convergence.json"
    if python3 - "$ARTIFACTS/client-pipes-convergence.json" "$ARTIFACTS/client-scope-recovery.json" "$ARTIFACTS/client-scope-convergence.json" "$PERIODIC_ORIGIN_ID" "$PERIODIC_LATER_ID" <<'PY'
import datetime,json,sys
ids={item['id'] for item in json.load(open(sys.argv[1]))['items']}
before=json.load(open(sys.argv[2]))['items'][0]['sync']; after=json.load(open(sys.argv[3]))['items'][0]['sync']
assert set(sys.argv[4:]) <= ids
assert datetime.datetime.fromisoformat(after['last_success'].replace('Z','+00:00')) > datetime.datetime.fromisoformat(before['last_success'].replace('Z','+00:00'))
PY
    then converged=1; break; fi
    sleep 1
  done
  [[ "$converged" == 1 ]] || periodic_fail discovery_timeout 'cached-only listing did not converge to new exact pipe ID'
  periodic_assert_redacted "$ARTIFACTS/client-pipes-setup.json" "$ARTIFACTS/client-pipes-before-outage.json" "$ARTIFACTS/client-pipes-during-outage.json" "$ARTIFACTS/client-pipes-convergence.json" "$ARTIFACTS/client-scope-election.json" "$ARTIFACTS/client-scope-takeover.json" "$ARTIFACTS/client-scope-outage.json" "$ARTIFACTS/client-scope-recovery.json" "$ARTIFACTS/client-scope-convergence.json"
  periodic_phase complete
  log "PASS: periodic local sync election/recovery; artifacts: $ARTIFACTS"
}
