#!/usr/bin/env bash
set -euo pipefail

SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/docker.sh"
source "$E2E_ROOT/lib/assertions.sh"
source "$E2E_ROOT/lib/report.sh"

CLUSTER_NAME="teamhome"
NAMESPACE="team"
SERVICE_NAME="e2e-v3-alice"
DUMMY_PORT="18020"
ROTATION_GRACE="6s"

mkdir -p "$E2E_LOG_DIR" "$E2E_ARTIFACTS_DIR"

generate_swarm_key
copy_swarm_key_to_actors

cat > "$(actor_home admin)/config.yaml" <<EOF
role: relay
node:
  seed: secret-v3-relay-seed
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
relay_peer_id="$($E2E_WORK_DIR/bin/tubo id from-seed secret-v3-relay-seed | tr -d '\n')"
relay_addr="/ip4/${admin_ip}/tcp/4001/p2p/${relay_peer_id}"
log "relay addr: $relay_addr"

exec_actor_bg admin sh -lc "cd /work && exec tubo relay --config /work/config.yaml > /work/logs/admin-relay.out 2>&1"
wait_http_ok_in_actor admin http://127.0.0.1:8092/healthz 90 || fail "admin relay did not become healthy"

start_actor alice
start_actor bob

exec_actor alice sh -lc "cd /work && tubo init service --out /work/config.yaml --force"
exec_actor alice sh -lc "cd /work && tubo join overlay/manual --config-dir /work --relay '$relay_addr' --swarm-key /work/swarm.key --force"
exec_actor alice sh -lc "cd /work && tubo create cluster/${CLUSTER_NAME} --config /work/config.yaml"
exec_actor alice sh -lc "cd /work && tubo create namespace/${NAMESPACE} --config /work/config.yaml"

exec_actor_bg alice sh -lc "cd /work && DUMMY_API_LISTEN=127.0.0.1:${DUMMY_PORT} DUMMY_API_INSTANCE=alice exec dummy-api-server > /work/logs/alice-dummy-api.out 2>&1"
wait_http_ok_in_actor alice http://127.0.0.1:${DUMMY_PORT}/healthz 60 || fail "alice dummy api did not become healthy"
exec_actor_bg alice sh -lc "cd /work && exec tubo attach http://127.0.0.1:${DUMMY_PORT} --name ${SERVICE_NAME} --config /work/config.yaml --heartbeat-interval 1s > /work/logs/alice-attach.out 2>&1"
wait_http_ok_in_actor alice http://127.0.0.1:8091/healthz 90 || fail "alice attach runtime did not become healthy"

member_share="$(exec_actor alice sh -lc "cd /work && tubo share cluster/${CLUSTER_NAME} --config /work/config.yaml --namespace ${NAMESPACE} --role member --expires 2h")"
member_token="$(printf '%s\n' "$member_share" | awk '/tubo-invite-v1\./ {print $NF; exit}')"
[[ -n "$member_token" ]] || fail "failed to extract member cluster invite"
printf '%s\n' "$member_share" > "$E2E_ARTIFACTS_DIR/alice-member-share.out"

exec_actor bob sh -lc "mkdir -p /work/member /work/mismatch"
exec_actor bob sh -lc "cd /work && tubo join overlay/manual --config-dir /work/member --relay '$relay_addr' --swarm-key /work/swarm.key --force"
exec_actor bob sh -lc "cd /work && tubo join cluster/${CLUSTER_NAME} --token '$member_token' --config-dir /work/member --force > /work/logs/bob-member-join.out 2>&1"
exec_actor bob sh -lc "cd /work && tubo use cluster/${CLUSTER_NAME} --config /work/member/config.yaml >/dev/null && tubo use namespace/${NAMESPACE} --config /work/member/config.yaml >/dev/null"

member_cfg="/work/member/config.yaml"

for i in $(seq 1 60); do
  if exec_actor bob sh -lc "cd /work && tubo get services --config ${member_cfg} --timeout 5s" > "$E2E_ARTIFACTS_DIR/bob-member-services.out" 2>&1; then
    if grep -Fq "$SERVICE_NAME" "$E2E_ARTIFACTS_DIR/bob-member-services.out"; then
      break
    fi
  fi
  sleep 1
done
assert_file_contains "$E2E_ARTIFACTS_DIR/bob-member-services.out" "$SERVICE_NAME" 'member invite should discover the Discovery V3 service'

write_report_json "$E2E_ARTIFACTS_DIR/report.json" "$E2E_SCENARIO" "$E2E_NETWORK_NAME" "$SERVICE_NAME" "$(actor_container_name alice)" "$(actor_container_name bob)"

echo "[e2e] PASS: secret-backed namespace discovery member join/discover flow works end-to-end"
