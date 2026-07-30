#!/usr/bin/env bash
set -euo pipefail

run_private_http() {
  build_and_deploy

  log 'generating isolated swarm key'
  client_sh "$CLIENT_BIN" keygen swarm --out "$LOCAL_ROOT/swarm.key"
  remote_scp_to "$LOCAL_ROOT/swarm.key" "$RELAY_HOST" "$REMOTE_ROOT/swarm.key"
  remote_scp_to "$LOCAL_ROOT/swarm.key" "$PUBLISHER_HOST" "$REMOTE_ROOT/swarm.key"
  remote "$RELAY_HOST" "chmod 600 '$REMOTE_ROOT/swarm.key'"
  remote "$PUBLISHER_HOST" "chmod 600 '$REMOTE_ROOT/swarm.key'"

  log 'starting transport-only relay on tuborelay'
  start_remote_bg "$RELAY_HOST" relay "'$REMOTE_ROOT/bin/tubo' relay --listen /ip4/0.0.0.0/tcp/$RELAY_PORT --public-addr /ip4/$RELAY_PUBLIC_IP/tcp/$RELAY_PORT --swarm-key '$REMOTE_ROOT/swarm.key'"
  relay_status="$(wait_remote_json "$RELAY_HOST" "'$REMOTE_ROOT/bin/tubo' inspect relay/default --json" "value['status']" ready 90)" || fail 'relay did not become ready'
  printf '%s\n' "$relay_status" >"$ARTIFACTS/relay-status.json"
  relay_peer_id="$(printf '%s' "$relay_status" | json_get "value['peer_id']")"
  validate_generated_value "$relay_peer_id"
  relay_addr="/ip4/$RELAY_PUBLIC_IP/tcp/$RELAY_PORT/p2p/$relay_peer_id"
  log "relay address: $relay_addr"

  log 'joining publisher to isolated overlay'
  remote "$PUBLISHER_HOST" "env TUBO_HOME='$REMOTE_ROOT/home' '$REMOTE_ROOT/bin/tubo' join overlay --relay '$relay_addr' --swarm-key '$REMOTE_ROOT/swarm.key' --force-reachability private --json" >"$ARTIFACTS/publisher-overlay.json"

  log 'creating private scope and starting its single authority'
  remote "$PUBLISHER_HOST" "env TUBO_HOME='$REMOTE_ROOT/home' '$REMOTE_ROOT/bin/tubo' scope create solar/default --json" >"$ARTIFACTS/scope-create.json"
  start_remote_bg "$PUBLISHER_HOST" authority "'$REMOTE_ROOT/bin/tubo' authority start solar --scope solar/default"
  authority_status="$(wait_remote_json "$PUBLISHER_HOST" "'$REMOTE_ROOT/bin/tubo' inspect authority/solar --json" "value['status']" ready 90)" || fail 'authority did not become ready'
  printf '%s\n' "$authority_status" >"$ARTIFACTS/authority-status.json"

  log 'starting local-only HTTP service on oripi'
  start_remote_bg "$PUBLISHER_HOST" test-http "'$REMOTE_ROOT/bin/test-http-server' --listen 127.0.0.1:$HTTP_PORT --instance oripi"
  for _ in $(seq 1 60); do
    remote "$PUBLISHER_HOST" "curl -fsS http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null 2>&1 && break
    sleep 1
  done
  remote "$PUBLISHER_HOST" "curl -fsS http://127.0.0.1:$HTTP_PORT/healthz" >/dev/null || fail 'origin HTTP service did not become healthy'

  log 'creating local service and published origin pipe'
  remote "$PUBLISHER_HOST" "env TUBO_HOME='$REMOTE_ROOT/home' '$REMOTE_ROOT/bin/tubo' service create echo --target http://127.0.0.1:$HTTP_PORT --json" >"$ARTIFACTS/service-create.json"
  pipe_create="$(remote "$PUBLISHER_HOST" "env TUBO_HOME='$REMOTE_ROOT/home' '$REMOTE_ROOT/bin/tubo' pipe create echo-team --ingress tubo --target service/echo --scope solar/default --visibility discoverable --access members --json")"
  printf '%s\n' "$pipe_create" >"$ARTIFACTS/pipe-create.json"
  origin_pipe_id="$(printf '%s' "$pipe_create" | json_get "value['id']")"
  start_remote_bg "$PUBLISHER_HOST" origin-pipe "'$REMOTE_ROOT/bin/tubo' start pipe/echo-team"
  origin_status="$(wait_remote_json "$PUBLISHER_HOST" "'$REMOTE_ROOT/bin/tubo' inspect pipe/echo-team --json" "value['status']" ready 120)" || fail 'origin pipe did not become ready'
  printf '%s\n' "$origin_status" >"$ARTIFACTS/origin-pipe-status.json"

  log 'creating member and viewer invitations'
  member_invite="$(remote "$PUBLISHER_HOST" "env TUBO_HOME='$REMOTE_ROOT/home' '$REMOTE_ROOT/bin/tubo' scope invite solar/default --role member --expires 2h --json")"
  viewer_invite="$(remote "$PUBLISHER_HOST" "env TUBO_HOME='$REMOTE_ROOT/home' '$REMOTE_ROOT/bin/tubo' scope invite solar/default --role viewer --expires 2h --json")"
  # Invite values remain shell-local; artifacts never receive bearer material.
  member_token="$(printf '%s' "$member_invite" | json_get "value['token']")"
  viewer_token="$(printf '%s' "$viewer_invite" | json_get "value['token']")"

  log 'joining clean macb state as member'
  mkdir -p "$CLIENT_HOME"
  client_sh "$CLIENT_BIN" join overlay --relay "$relay_addr" --swarm-key "$LOCAL_ROOT/swarm.key" --force-reachability private --json >"$ARTIFACTS/client-overlay.json"
  client_sh "$CLIENT_BIN" scope join --token "$member_token" --json >"$ARTIFACTS/client-scope.json"

  log 'waiting for verified pipe discovery on macb'
  pipes="$(wait_client_json "'$CLIENT_BIN' get pipes --scope solar/default --json" "any(item.get('id') == '$origin_pipe_id' for item in value['items'])" True 120)" || fail 'client did not discover origin pipe'
  printf '%s\n' "$pipes" >"$ARTIFACTS/client-pipes.json"
  if printf '%s' "$pipes" | grep -F '127.0.0.1:' >/dev/null; then
    fail 'remote pipe listing leaked a local service target'
  fi

  log 'verifying viewer discovery and connect denial from separate clean state'
  mkdir -p "$VIEWER_HOME"
  env TUBO_HOME="$VIEWER_HOME" "$CLIENT_BIN" join overlay --relay "$relay_addr" --swarm-key "$LOCAL_ROOT/swarm.key" --force-reachability private --json >"$ARTIFACTS/viewer-overlay.json"
  env TUBO_HOME="$VIEWER_HOME" "$CLIENT_BIN" scope join --token "$viewer_token" --json >"$ARTIFACTS/viewer-scope.json"
  viewer_pipes=''
  viewer_found=0
  for _ in $(seq 1 60); do
    if viewer_pipes="$(env TUBO_HOME="$VIEWER_HOME" "$CLIENT_BIN" get pipes --scope solar/default --json 2>/dev/null)" && printf '%s' "$viewer_pipes" | python3 -c "import json,sys; value=json.load(sys.stdin); assert any(item.get('id') == '$origin_pipe_id' for item in value['items'])" 2>/dev/null; then
      viewer_found=1
      break
    fi
    sleep 1
  done
  [[ "$viewer_found" == 1 ]] || fail 'viewer could not discover origin pipe'
  printf '%s\n' "$viewer_pipes" >"$ARTIFACTS/viewer-pipes.json"
  if printf '%s' "$viewer_pipes" | grep -F '127.0.0.1:' >/dev/null; then fail 'viewer listing leaked a local service target'; fi
  if env TUBO_HOME="$VIEWER_HOME" "$CLIENT_BIN" connect "pipe/$origin_pipe_id" --local "127.0.0.1:$VIEWER_PORT" -d --json >"$ARTIFACTS/viewer-connect.json" 2>"$ARTIFACTS/viewer-connect.err"; then
    viewer_pid="$(json_get "value['pid']" <"$ARTIFACTS/viewer-connect.json")"
    record_local_runner viewer "$viewer_pid" '_run-pipe'
    fail 'viewer connect unexpectedly succeeded'
  fi
  grep -F 'authorization_denied' "$ARTIFACTS/viewer-connect.err" >/dev/null || fail 'viewer connect failed without authorization_denied class'

  log 'creating local consumer pipe through connect preset'
  client_sh "$CLIENT_BIN" connect "pipe/$origin_pipe_id" --local "127.0.0.1:$CLIENT_PORT" -d --json >"$ARTIFACTS/client-connect.json"
  client_pid="$(json_get "value['pid']" <"$ARTIFACTS/client-connect.json")"
  client_pipe_ref="$(json_get "value['ref']" <"$ARTIFACTS/client-connect.json")"
  record_local_runner client "$client_pid" "_run-pipe $client_pipe_ref"
  response=''
  for _ in $(seq 1 90); do
    if response="$(curl -fsS -X POST -H 'Content-Type: text/plain' --data 'hello-from-macb' "http://127.0.0.1:$CLIENT_PORT/v1/dummy?from=macb")"; then
      break
    fi
    sleep 1
  done
  [[ -n "$response" ]] || fail 'macb did not receive HTTP response through the pipe graph'
  printf '%s\n' "$response" >"$ARTIFACTS/http-response.json"
  grep -F '"instance":"oripi"' <<<"$response" >/dev/null || fail 'response did not come from oripi origin'
  grep -F '"raw_query":"from=macb"' <<<"$response" >/dev/null || fail 'query was not preserved'
  grep -F '"method":"POST"' <<<"$response" >/dev/null || fail 'method was not preserved'

  log 'asserting rebuilt process ontology'
  client_sh "$CLIENT_BIN" ps --json >"$ARTIFACTS/client-ps.json"
  remote "$PUBLISHER_HOST" "env TUBO_HOME='$REMOTE_ROOT/home' '$REMOTE_ROOT/bin/tubo' ps --json" >"$ARTIFACTS/publisher-ps.json"
  python3 - "$ARTIFACTS/client-ps.json" "$ARTIFACTS/publisher-ps.json" <<'PY'
import json, sys
for path in sys.argv[1:]:
    value = json.load(open(path, encoding='utf-8'))
    for item in value['items']:
        assert item.get('kind') not in {'attach', 'connect', 'service'}, (path, item)
        assert item.get('primary_ref', '').split('/', 1)[0] in {'pipe', 'relay', 'authority', 'gateway'}, (path, item)
PY

  client_sh "$CLIENT_BIN" inspect "pipe/$origin_pipe_id" --json >"$ARTIFACTS/client-origin-inspect.json"
  client_sh "$CLIENT_BIN" inspect "$client_pipe_ref" --json >"$ARTIFACTS/client-local-pipe-inspect.json"
  python3 - "$ARTIFACTS/client-local-pipe-inspect.json" <<'PY'
import json, sys
value = json.load(open(sys.argv[1], encoding='utf-8'))
sessions = value.get('sessions', [])
assert sessions, value
assert any(item.get('initial_transport_path') == 'relayed' for item in sessions), value
PY

  cat >"$ARTIFACTS/report.json" <<EOF
{"scenario":"private-http","run_id":"$RUN_ID","result":"pass","relay":"$RELAY_HOST","publisher":"$PUBLISHER_HOST","client":"macb","origin_pipe_id":"$origin_pipe_id"}
EOF
  log "PASS: three-machine private HTTP pipe works; artifacts: $ARTIFACTS"
}
