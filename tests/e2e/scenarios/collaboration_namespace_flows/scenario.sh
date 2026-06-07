#!/usr/bin/env bash
set -euo pipefail

SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/docker.sh"
source "$E2E_ROOT/lib/assertions.sh"
source "$E2E_ROOT/lib/report.sh"

NAMESPACE="collab"
SERVICE_NAME="e2e-collab"
DUMMY_PORT="18010"
MEMBER_PORT="18881"
OUTSIDER_PORT="18882"

mkdir -p "$E2E_LOG_DIR" "$E2E_ARTIFACTS_DIR"

generate_swarm_key
copy_swarm_key_to_actors

cat > "$(actor_home admin)/config.yaml" <<EOF
role: relay
node:
  seed: collab-relay-seed
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
relay_peer_id="$($E2E_WORK_DIR/bin/tubo id from-seed collab-relay-seed | tr -d '\n')"
relay_addr="/ip4/${admin_ip}/tcp/4001/p2p/${relay_peer_id}"
log "relay addr: $relay_addr"

exec_actor_bg admin sh -lc "cd /work && exec tubo relay --config /work/config.yaml > /work/logs/admin-relay.out 2>&1"
wait_http_ok_in_actor admin http://127.0.0.1:8092/healthz 90 || fail "admin relay did not become healthy"

start_actor alice
start_actor bob

exec_actor alice sh -lc "cd /work && tubo init service --out /work/config.yaml --force"
exec_actor alice sh -lc "cd /work && tubo create cluster/home --config /work/config.yaml"
exec_actor alice sh -lc "cd /work && tubo create namespace/${NAMESPACE} --config /work/config.yaml"
cp "$(actor_home alice)/config.yaml" "$E2E_ARTIFACTS_DIR/alice-cluster.yaml"
exec_actor alice sh -lc "cd /work && tubo join overlay/manual --config-dir /work --relay '$relay_addr' --swarm-key /work/swarm.key --force"
python3 - "$E2E_ARTIFACTS_DIR/alice-cluster.yaml" "$(actor_home alice)/config.yaml" <<'PY'
import sys
from pathlib import Path
import yaml
src = yaml.safe_load(Path(sys.argv[1]).read_text())
path = Path(sys.argv[2])
obj = yaml.safe_load(path.read_text())
obj['clusters'] = src['clusters']
obj['current_cluster'] = src.get('current_cluster', 'home')
obj['current_namespace'] = src.get('current_namespace', 'default')
path.write_text(yaml.safe_dump(obj, sort_keys=False))
PY

exec_actor_bg alice sh -lc "cd /work && DUMMY_API_LISTEN=127.0.0.1:${DUMMY_PORT} DUMMY_API_INSTANCE=alice exec dummy-api-server > /work/logs/alice-dummy-api.out 2>&1"
wait_http_ok_in_actor alice http://127.0.0.1:${DUMMY_PORT}/healthz 60 || fail "alice dummy api did not become healthy"
exec_actor_bg alice sh -lc "cd /work && exec tubo attach http://127.0.0.1:${DUMMY_PORT} --name ${SERVICE_NAME} --config /work/config.yaml --heartbeat-interval 1s > /work/logs/alice-attach.out 2>&1"
wait_http_ok_in_actor alice http://127.0.0.1:8091/healthz 90 || fail "alice attach runtime did not become healthy"

member_share="$(exec_actor alice sh -lc "cd /work && tubo share cluster/home --config /work/config.yaml --namespace ${NAMESPACE} --role member --expires 2h")"
viewer_share="$(exec_actor alice sh -lc "cd /work && tubo share cluster/home --config /work/config.yaml --namespace ${NAMESPACE} --role viewer --expires 2h")"
service_share="$(exec_actor alice sh -lc "cd /work && tubo share service/${SERVICE_NAME} --config /work/config.yaml --cluster home --namespace ${NAMESPACE} --expires 2h")"
member_token="$(printf '%s\n' "$member_share" | awk '/tubo-invite-v1\./ {print $NF; exit}')"
viewer_token="$(printf '%s\n' "$viewer_share" | awk '/tubo-invite-v1\./ {print $NF; exit}')"
share_token="$(printf '%s\n' "$service_share" | awk '/tubo-share-invite-v1\./ {print $NF; exit}')"
[[ -n "$member_token" ]] || fail "failed to extract member cluster invite"
[[ -n "$viewer_token" ]] || fail "failed to extract viewer cluster invite"
[[ -n "$share_token" ]] || fail "failed to extract service share invite"
printf '%s\n' "$member_share" > "$E2E_ARTIFACTS_DIR/alice-member-share.out"
printf '%s\n' "$viewer_share" > "$E2E_ARTIFACTS_DIR/alice-viewer-share.out"
printf '%s\n' "$service_share" > "$E2E_ARTIFACTS_DIR/alice-service-share.out"

exec_actor bob sh -lc "mkdir -p /work/member /work/viewer /work/outsider"
exec_actor bob sh -lc "cd /work && tubo join overlay/manual --config-dir /work/member --relay '$relay_addr' --swarm-key /work/swarm.key --force"
exec_actor bob sh -lc "cd /work && tubo join cluster/home --token '$member_token' --config-dir /work/member --force > /work/logs/bob-member-join.out 2>&1"
exec_actor bob sh -lc "cd /work && tubo join overlay/manual --config-dir /work/viewer --relay '$relay_addr' --swarm-key /work/swarm.key --force"
exec_actor bob sh -lc "cd /work && tubo join cluster/home --token '$viewer_token' --config-dir /work/viewer --force > /work/logs/bob-viewer-join.out 2>&1"
exec_actor bob sh -lc "cd /work && tubo join overlay/manual --config-dir /work/outsider --relay '$relay_addr' --swarm-key /work/swarm.key --force"

member_cfg="/work/member/config.yaml"
viewer_cfg="/work/viewer/config.yaml"
outsider_cfg="/work/outsider/config.yaml"

for i in $(seq 1 60); do
  if exec_actor bob sh -lc "cd /work && tubo get services --config ${member_cfg}" > "$E2E_ARTIFACTS_DIR/bob-member-services.out" 2>&1; then
    if grep -Fq "$SERVICE_NAME" "$E2E_ARTIFACTS_DIR/bob-member-services.out"; then
      break
    fi
  fi
  sleep 1
done
assert_file_contains "$E2E_ARTIFACTS_DIR/bob-member-services.out" "$SERVICE_NAME" 'member invite should discover the collaboration service'

if ! exec_actor bob sh -lc "cd /work && tubo get services --config ${viewer_cfg}" > "$E2E_ARTIFACTS_DIR/bob-viewer-services.out" 2>&1; then
  fail "viewer invite should allow service listing"
fi
assert_file_contains "$E2E_ARTIFACTS_DIR/bob-viewer-services.out" "$SERVICE_NAME" 'viewer invite should list the collaboration service'

if exec_actor bob sh -lc "cd /work && tubo connect ${SERVICE_NAME} --config ${viewer_cfg}" > "$E2E_ARTIFACTS_DIR/bob-viewer-connect.out" 2>&1; then
  fail "viewer invite unexpectedly allowed connect"
fi
assert_file_contains "$E2E_ARTIFACTS_DIR/bob-viewer-connect.out" 'connect permission' 'viewer connect denial should mention missing connect permission'

exec_actor_bg bob sh -lc "cd /work && exec tubo connect ${SERVICE_NAME} --config ${member_cfg} --local 127.0.0.1:${MEMBER_PORT} > /work/logs/bob-member-connect.out 2>&1"
member_response=""
for i in $(seq 1 90); do
  if member_response="$(exec_actor bob sh -lc "curl -fsS http://127.0.0.1:${MEMBER_PORT}/v1/dummy?from=member")"; then
    break
  fi
  sleep 1
done
[[ -n "$member_response" ]] || fail "member invite did not yield a working collaboration tunnel"
printf '%s\n' "$member_response" > "$E2E_ARTIFACTS_DIR/bob-member-response.json"
assert_contains "$member_response" '"instance":"alice"' 'member connect response missing alice instance marker'
assert_contains "$member_response" '"raw_query":"from=member"' 'member connect response missing query marker'

member_log="$(exec_actor bob sh -lc 'cat /work/logs/bob-member-connect.out')"
printf '%s\n' "$member_log" > "$E2E_ARTIFACTS_DIR/bob-member-connect.out"
alice_attach_log="$(exec_actor alice sh -lc 'cat /work/logs/alice-attach.out')"
printf '%s\n' "$alice_attach_log" > "$E2E_ARTIFACTS_DIR/alice-attach.out"

exec_actor_bg bob sh -lc "cd /work && exec tubo connect --token '$share_token' --config ${outsider_cfg} --local 127.0.0.1:${OUTSIDER_PORT} > /work/logs/bob-outsider-connect.out 2>&1"
outsider_response=""
for i in $(seq 1 90); do
  if outsider_response="$(exec_actor bob sh -lc "curl -fsS http://127.0.0.1:${OUTSIDER_PORT}/v1/dummy?from=outsider")"; then
    break
  fi
  sleep 1
done
[[ -n "$outsider_response" ]] || fail "share invite did not work cross-scope without namespace membership"
printf '%s\n' "$outsider_response" > "$E2E_ARTIFACTS_DIR/bob-outsider-response.json"
assert_contains "$outsider_response" '"instance":"alice"' 'outsider connect response missing alice instance marker'
assert_contains "$outsider_response" '"raw_query":"from=outsider"' 'outsider connect response missing query marker'
outsider_log="$(exec_actor bob sh -lc 'cat /work/logs/bob-outsider-connect.out')"
printf '%s\n' "$outsider_log" > "$E2E_ARTIFACTS_DIR/bob-outsider-connect.out"

write_report_json "$E2E_ARTIFACTS_DIR/report.json" "$E2E_SCENARIO" "$E2E_NETWORK_NAME" "$SERVICE_NAME" "$(actor_container_name alice)" "$(actor_container_name bob)"

echo "[e2e] PASS: collaboration namespace member/viewer/invite flows work end-to-end"
