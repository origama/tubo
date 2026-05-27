#!/usr/bin/env bash
set -euo pipefail

SCENARIO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$E2E_ROOT/lib/common.sh"
source "$E2E_ROOT/lib/docker.sh"
source "$E2E_ROOT/lib/assertions.sh"
source "$E2E_ROOT/lib/report.sh"

SERVICE_NAME="e2e-echo"
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

docker run -d \
  --name bundle-server \
  --network "$E2E_NETWORK_NAME" \
  -v "$E2E_REPO_ROOT/docs/.well-known/tubo/networks:/srv:ro" \
  -w /srv \
  python:3-alpine python -m http.server 8080 --bind 0.0.0.0 >/dev/null
export TUBO_DEFAULT_PUBLIC_BUNDLE_URL="http://bundle-server:8080/tubo-public.bundle"
for i in $(seq 1 30); do
  if docker exec bundle-server sh -lc 'wget -qO- http://127.0.0.1:8080/tubo-public.bundle >/dev/null'; then
    break
  fi
  sleep 1
done

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

export E2E_EXTRA_HOSTS="relay.tubo.click:${admin_ip} grants.tubo.click:${admin_ip}"
start_actor alice
start_actor bob

exec_actor alice sh -lc "cd /work && tubo init service --out /work/config.yaml --force"
exec_actor alice sh -lc "cd /work && tubo create cluster/home --config /work/config.yaml"
cp "$(actor_home alice)/config.yaml" "$E2E_ARTIFACTS_DIR/alice-cluster.yaml"
exec_actor alice sh -lc "cd /work && tubo join overlay/manual --config-dir /work --relay '$relay_addr' --swarm-key /work/swarm.key --force"
python3 - "$E2E_ARTIFACTS_DIR/alice-cluster.yaml" "$(actor_home alice)/config.yaml" <<'PY'
import sys
from pathlib import Path
import yaml
src = yaml.safe_load(Path(sys.argv[1]).read_text())
dst_path = Path(sys.argv[2])
dst = yaml.safe_load(dst_path.read_text())
dst['current_cluster'] = src.get('current_cluster', dst.get('current_cluster'))
dst['current_namespace'] = src.get('current_namespace', dst.get('current_namespace'))
dst.setdefault('clusters', {})['home'] = src['clusters']['home']
dst_path.write_text(yaml.safe_dump(dst, sort_keys=False))
PY
exec_actor_bg alice sh -lc "cd /work && DUMMY_API_LISTEN=127.0.0.1:${DUMMY_PORT} DUMMY_API_INSTANCE=alice exec dummy-api-server > /work/logs/alice-dummy-api.out 2>&1"
wait_http_ok_in_actor alice http://127.0.0.1:${DUMMY_PORT}/healthz 60 || fail "alice dummy api did not become healthy"
exec_actor_bg alice sh -lc "cd /work && exec tubo attach http://127.0.0.1:${DUMMY_PORT} --name ${SERVICE_NAME} --config /work/config.yaml --heartbeat-interval 1s > /work/logs/alice-attach.out 2>&1"

alice_services=""
for i in $(seq 1 60); do
  if alice_services="$(exec_actor alice sh -lc "cd /work && tubo get services --config /work/config.yaml --timeout 5s --json")" && grep -Fq '"name":"${SERVICE_NAME}"' <<<"$alice_services"; then
    break
  fi
  sleep 1
done
[[ -n "$alice_services" ]] || fail "alice did not see published service"

share_output="$(exec_actor alice sh -lc "cd /work && tubo share service/${SERVICE_NAME} --config /work/config.yaml --cluster home --namespace default --expires 2h")"
share_token="$(printf '%s\n' "$share_output" | awk '/tubo-share-invite-v1\./ {print $NF; exit}')"
[[ -n "$share_token" ]] || fail "failed to extract share invite token"

mkdir -p "$(actor_home bob)/config/tubo" "$(actor_home bob)/clusters"
cp "$(actor_home alice)/config.yaml" "$(actor_home bob)/config/tubo/config.yaml"
rm -rf "$(actor_home bob)/clusters/home"
cp -a "$(actor_home alice)/clusters/home" "$(actor_home bob)/clusters/"

exec_actor_bg bob sh -lc "cd /work && exec tubo connect --token '$share_token' --local 127.0.0.1:${BOB_PORT} > /work/logs/bob-connect.out 2>&1"

response=""
for i in $(seq 1 60); do
  if response="$(exec_actor bob sh -lc "curl -fsS -X POST -H 'Content-Type: text/plain' --data 'hello-from-bob' http://127.0.0.1:${BOB_PORT}/v1/dummy?from=bob")"; then
    break
  fi
  sleep 1
done
[[ -n "$response" ]] || fail "bob did not receive a response from alice service"
printf '%s\n' "$response" > "$E2E_ARTIFACTS_DIR/bob-response.json"
assert_contains "$response" '"instance":"alice"' "response missing alice instance marker"
assert_contains "$response" '"raw_query":"from=bob"' "response missing from=bob query marker"
assert_contains "$response" '"method":"POST"' "response missing POST marker"
assert_contains "$response" '"path":"/v1/dummy"' "response missing path marker"

write_report_json "$E2E_ARTIFACTS_DIR/report.json" "$E2E_SCENARIO" "$E2E_NETWORK_NAME" "$SERVICE_NAME" "$(actor_container_name alice)" "$(actor_container_name bob)"

echo "[e2e] PASS: default cluster/default namespace happy path works"
