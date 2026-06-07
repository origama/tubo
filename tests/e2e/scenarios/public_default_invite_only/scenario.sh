#!/usr/bin/env bash
set -euo pipefail

SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/docker.sh"
source "$E2E_ROOT/lib/assertions.sh"
source "$E2E_ROOT/lib/report.sh"

SERVICE_NAME="e2e-public"
DUMMY_PORT="18000"
BOB_PORT="18888"

mkdir -p "$E2E_LOG_DIR" "$E2E_ARTIFACTS_DIR"

python3 - "$E2E_REPO_ROOT/docs/.well-known/tubo/networks/tubo-public.payload.json" "$E2E_ARTIFACTS_DIR/swarm.key" <<'PY'
import json
import sys
from pathlib import Path
payload = json.loads(Path(sys.argv[1]).read_text())
Path(sys.argv[2]).write_text(payload['swarm_key']['value'])
PY
copy_swarm_key_to_actors

cat > "$(actor_home admin)/config.yaml" <<EOF
role: relay
node:
  seed: public-relay-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
network:
  private_key_file: /work/swarm.key
  private_key_b64: ""
  allowed_peers: []
  bootstrap_peers: []
  relay_peers: []
  autorelay: true
  hole_punching: true
  force_reachability: public
service:
  name: ""
  target: ""
edge:
  listen: ""
  admin_listen: ""
  direct_stream_timeout: 250ms
relay:
  public_addr: ""
  health_listen: 127.0.0.1:8092
  enable_relay_service: true
  enable_autonat_service: true
  enable_discovery_pubsub: true
  force_reachability_public: true
  max_reservations: 256
  max_reservations_per_ip: 16
  max_reservations_per_asn: 64
  max_circuits_per_peer: 64
  buffer_size: 65536
  reservation_ttl: 1h
  limit_duration: 5m
  limit_data_bytes: 268435456
  print_run_commands: false
bridge:
  listen: ""
  service_addr: ""
  service_seed: ""
  service_p2p_listen: ""
health_listen: ""
heartbeat_interval: 15s
EOF

start_actor admin
admin_ip="$(actor_ip admin)"
relay_peer_id="$($E2E_WORK_DIR/bin/tubo id from-seed public-relay-seed | tr -d '\n')"
relay_addr="/ip4/${admin_ip}/tcp/4001/p2p/${relay_peer_id}"
log "relay addr: $relay_addr"

exec_actor_bg admin sh -lc "cd /work && exec tubo relay --config /work/config.yaml > /work/logs/admin-relay.out 2>&1"
wait_http_ok_in_actor admin http://127.0.0.1:8092/healthz 90 || fail "admin relay did not become healthy"

start_actor alice
start_actor bob

exec_actor alice sh -lc "cd /work && tubo init service --out /work/config.yaml --force"
exec_actor alice sh -lc "cd /work && tubo create cluster/home --config /work/config.yaml"
cp "$(actor_home alice)/config.yaml" "$E2E_ARTIFACTS_DIR/alice-cluster.yaml"
exec_actor alice sh -lc "cd /work && tubo join overlay/manual --config-dir /work --relay '$relay_addr' --swarm-key /work/swarm.key --force"
python3 - "$E2E_ARTIFACTS_DIR/alice-cluster.yaml" "$relay_addr" "$(actor_home alice)/config.yaml" <<'PY'
import sys
from pathlib import Path
import yaml
src = yaml.safe_load(Path(sys.argv[1]).read_text())
relay_addr = sys.argv[2]
path = Path(sys.argv[3])
obj = yaml.safe_load(path.read_text())
obj['clusters'] = src['clusters']
obj['current_cluster'] = src.get('current_cluster', 'home')
obj['current_namespace'] = src.get('current_namespace', 'default')
obj['current_overlay'] = 'tubo-public'
overlays = obj.setdefault('overlays', {})
overlays['tubo-public'] = {
    'kind': 'public-bundle',
    'public_default_cluster': 'home',
    'public_default_namespace': 'default',
}
obj.setdefault('network', {})['relay_peers'] = [relay_addr]
obj['network']['bootstrap_peers'] = [relay_addr]
ns = obj['clusters']['home']['namespaces']['default']
ns['discovery'] = 'disabled'
ns['connect_policy'] = 'invite_only'
path.write_text(yaml.safe_dump(obj, sort_keys=False))
PY

exec_actor_bg alice sh -lc "cd /work && DUMMY_API_LISTEN=127.0.0.1:${DUMMY_PORT} DUMMY_API_INSTANCE=alice exec dummy-api-server > /work/logs/alice-dummy-api.out 2>&1"
wait_http_ok_in_actor alice http://127.0.0.1:${DUMMY_PORT}/healthz 60 || fail "alice dummy api did not become healthy"
exec_actor_bg alice sh -lc "cd /work && exec tubo attach http://127.0.0.1:${DUMMY_PORT} --name ${SERVICE_NAME} --config /work/config.yaml --heartbeat-interval 1s > /work/logs/alice-attach.out 2>&1"
wait_http_ok_in_actor alice http://127.0.0.1:8091/healthz 90 || fail "alice attach runtime did not become healthy"

attach_log="$(exec_actor alice sh -lc 'cat /work/logs/alice-attach.out')"
printf '%s\n' "$attach_log" > "$E2E_ARTIFACTS_DIR/alice-attach.out"
assert_contains "$attach_log" 'visibility: unlisted' 'attach output missing unlisted visibility marker'
assert_contains "$attach_log" 'access: invite token required' 'attach output missing invite-only marker'

share_output="$(exec_actor alice sh -lc "cd /work && tubo share service/${SERVICE_NAME} --config /work/config.yaml --cluster home --namespace default --expires 2h")"
share_token="$(printf '%s\n' "$share_output" | awk '/tubo-share-invite-v1\./ {print $NF; exit}')"
[[ -n "$share_token" ]] || fail "failed to extract share invite token"
printf '%s\n' "$share_output" > "$E2E_ARTIFACTS_DIR/alice-share.out"

python3 - "$share_token" "$E2E_ARTIFACTS_DIR/share-token.json" <<'PY'
import base64
import json
import sys

token = sys.argv[1].strip()
out = sys.argv[2]
prefix = 'tubo-share-invite-v1.'
if not token.startswith(prefix):
    raise SystemExit('unexpected token prefix')
payload_b64 = token[len(prefix):].split('.', 1)[0]
payload = json.loads(base64.urlsafe_b64decode(payload_b64 + '=' * (-len(payload_b64) % 4)))
endpoint = payload.get('service_endpoint') or {}
addrs = endpoint.get('addresses') or []
if not endpoint.get('peer_id') or not addrs:
    raise SystemExit('missing service_endpoint metadata')
for addr in addrs:
    lowered = addr.lower()
    for bad in ('127.0.0.1', '0.0.0.0', '::1', '/ip6/::/', 'localhost'):
        if bad in lowered:
            raise SystemExit(f'local-only service endpoint leaked into token: {addr}')
with open(out, 'w', encoding='utf-8') as fh:
    json.dump(payload, fh, indent=2)
    fh.write('\n')
PY

cp "$(actor_home alice)/config.yaml" "$(actor_home bob)/config.yaml"

for name in get-services get-services-all watch-services-all connect-name; do
  : >"$E2E_ARTIFACTS_DIR/${name}.out"
done

if exec_actor bob sh -lc "cd /work && tubo get services --config /work/config.yaml" >"$E2E_ARTIFACTS_DIR/get-services.out" 2>&1; then
  fail "ambient get services unexpectedly succeeded in public default"
fi
assert_file_contains "$E2E_ARTIFACTS_DIR/get-services.out" 'tubo connect --token <invite>' 'get services missing discovery-disabled guidance'

if exec_actor bob sh -lc "cd /work && tubo get services -A --config /work/config.yaml" >"$E2E_ARTIFACTS_DIR/get-services-all.out" 2>&1; then
  fail "ambient get services -A unexpectedly succeeded in public default"
fi
assert_file_contains "$E2E_ARTIFACTS_DIR/get-services-all.out" 'tubo connect --token <invite>' 'get services -A missing discovery-disabled guidance'

if exec_actor bob sh -lc "cd /work && tubo watch services -A --timeout 1s --config /work/config.yaml" >"$E2E_ARTIFACTS_DIR/watch-services-all.out" 2>&1; then
  fail "ambient watch services -A unexpectedly succeeded in public default"
fi
assert_file_contains "$E2E_ARTIFACTS_DIR/watch-services-all.out" 'tubo connect --token <invite>' 'watch services -A missing discovery-disabled guidance'

if exec_actor bob sh -lc "cd /work && tubo connect ${SERVICE_NAME} --config /work/config.yaml" >"$E2E_ARTIFACTS_DIR/connect-name.out" 2>&1; then
  fail "ambient connect <name> unexpectedly succeeded in public default"
fi
assert_file_contains "$E2E_ARTIFACTS_DIR/connect-name.out" 'tubo connect --token <invite>' 'connect <name> missing discovery-disabled guidance'

exec_actor_bg bob sh -lc "cd /work && exec tubo connect --token '$share_token' --config /work/config.yaml --local 127.0.0.1:${BOB_PORT} > /work/logs/bob-connect.out 2>&1"

response=""
for i in $(seq 1 90); do
  if response="$(exec_actor bob sh -lc "curl -fsS http://127.0.0.1:${BOB_PORT}/v1/dummy?from=public-default")"; then
    break
  fi
  sleep 1
done
[[ -n "$response" ]] || fail "bob did not receive a response from the invite-only public-default service"
printf '%s\n' "$response" > "$E2E_ARTIFACTS_DIR/bob-response.json"
assert_contains "$response" '"instance":"alice"' 'response missing alice instance marker'
assert_contains "$response" '"raw_query":"from=public-default"' 'response missing query marker'

bob_connect_log="$(exec_actor bob sh -lc 'cat /work/logs/bob-connect.out')"
printf '%s\n' "$bob_connect_log" > "$E2E_ARTIFACTS_DIR/bob-connect.out"
assert_contains "$bob_connect_log" 'connected to service "e2e-public"' 'expected invite-based connect success summary'
assert_contains "$bob_connect_log" 'path: relayed' 'expected relayed invite path in connect output'
if grep -Fq 'service not found' <<<"$bob_connect_log"; then
  fail "bob connect log suggests ambient discovery fallback instead of invite path"
fi

write_report_json "$E2E_ARTIFACTS_DIR/report.json" "$E2E_SCENARIO" "$E2E_NETWORK_NAME" "$SERVICE_NAME" "$(actor_container_name alice)" "$(actor_container_name bob)"

echo "[e2e] PASS: public default stays invite-only while connect --token works end-to-end"
