#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${RUN_DIR:-$ROOT_DIR/generated/distributed-two-host}"
REMOTE_HOST="${REMOTE_HOST:-root@172-232-189-160.ip.linodeusercontent.com}"
REMOTE_RELAY_IP="${REMOTE_RELAY_IP:-172.232.189.160}"
EDGE_HOST_IP="${EDGE_HOST_IP:-172.236.202.99}"
REMOTE_BASE_DIR="${REMOTE_BASE_DIR:-/tmp/p2p-api-tunnel-distributed-smoke}"
SERVICE_NAME="${SERVICE_NAME:-myapi}"
EDGE_HTTP_LISTEN="${EDGE_HTTP_LISTEN:-127.0.0.1:18443}"
EDGE_ADMIN_LISTEN="${EDGE_ADMIN_LISTEN:-127.0.0.1:18444}"
EDGE_P2P_LISTEN="${EDGE_P2P_LISTEN:-/ip4/0.0.0.0/tcp/4001}"
REMOTE_RELAY_P2P_LISTEN="${REMOTE_RELAY_P2P_LISTEN:-/ip4/0.0.0.0/tcp/4001}"
REMOTE_SERVICE_P2P_LISTEN="${REMOTE_SERVICE_P2P_LISTEN:-/ip4/127.0.0.1/tcp/40123}"
REMOTE_DUMMY_LISTEN="${REMOTE_DUMMY_LISTEN:-127.0.0.1:18000}"
REMOTE_SERVICE_HEALTH="${REMOTE_SERVICE_HEALTH:-127.0.0.1:18091}"
REMOTE_RELAY_HEALTH="${REMOTE_RELAY_HEALTH:-127.0.0.1:18092}"
KEEP_RUNNING="${KEEP_RUNNING:-0}"
SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new)

mkdir -p "$RUN_DIR"

info() {
  echo "[smoke-distributed] $*"
}

cleanup_local() {
  if [[ -f "$RUN_DIR/edge.pid" ]]; then
    kill "$(cat "$RUN_DIR/edge.pid")" >/dev/null 2>&1 || true
    rm -f "$RUN_DIR/edge.pid"
  fi
}

cleanup_remote() {
  ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "
    set -e
    cd '$REMOTE_BASE_DIR' 2>/dev/null || exit 0
    for name in edge relay service dummy-api-server; do
      if [ -f \"\$name.pid\" ]; then
        kill \"\$(cat \"\$name.pid\")\" >/dev/null 2>&1 || true
        rm -f \"\$name.pid\"
      fi
    done
    pkill -f '$REMOTE_BASE_DIR/tubo .*run --config' >/dev/null 2>&1 || true
    pkill -f '$REMOTE_BASE_DIR/dummy-api-server' >/dev/null 2>&1 || true
    for _ in 1 2 3 4 5; do
      if ! lsof '$REMOTE_BASE_DIR/tubo' >/dev/null 2>&1 && ! lsof '$REMOTE_BASE_DIR/dummy-api-server' >/dev/null 2>&1; then
        break
      fi
      sleep 1
    done
  " >/dev/null 2>&1 || true
}

cleanup() {
  if [[ "$KEEP_RUNNING" == "1" ]]; then
    info "KEEP_RUNNING=1, leaving processes up"
    return
  fi
  cleanup_local
  cleanup_remote
}
trap cleanup EXIT

wait_http_ok() {
  local url="$1"
  local tries="${2:-60}"
  local i
  for i in $(seq 1 "$tries"); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

wait_remote_http_ok() {
  local url="$1"
  local tries="${2:-60}"
  local i
  for i in $(seq 1 "$tries"); do
    if ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "curl -fsS '$url' >/dev/null" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

assert_process_started() {
  local pid_file="$1"
  local name="$2"
  sleep 1
  if [[ ! -f "$pid_file" ]] || ! kill -0 "$(cat "$pid_file")" >/dev/null 2>&1; then
    info "$name failed to stay up"
    return 1
  fi
}

assert_remote_process_started() {
  local name="$1"
  sleep 1
  if ! ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "kill -0 \$(cat '$REMOTE_BASE_DIR/$name.pid') >/dev/null 2>&1"; then
    info "remote $name failed to stay up"
    return 1
  fi
}

build_binaries() {
  info "building local binaries"
  (cd "$ROOT_DIR" && go build -o "$RUN_DIR/tubo" ./cmd/tubo)
  (cd "$ROOT_DIR" && go build -o "$RUN_DIR/dummy-api-server" ./cmd/dummy-api-server)
}

generate_swarm_key() {
  info "generating ephemeral swarm key"
  rm -f "$RUN_DIR/swarm.key"
  "$RUN_DIR/tubo" keygen swarm --out "$RUN_DIR/swarm.key" >/dev/null
}

generate_configs() {
  local relay_peer_id bootstrap
  relay_peer_id="$($RUN_DIR/tubo id from-seed public-relay-seed | tr -d '\n')"
  bootstrap="/ip4/$REMOTE_RELAY_IP/tcp/4001/p2p/$relay_peer_id"

  cat > "$RUN_DIR/edge.yaml" <<EOF
role: edge
node:
  seed: edge-distributed-smoke-seed
  p2p_listen: $EDGE_P2P_LISTEN
network:
  private_key_file: $RUN_DIR/swarm.key
  private_key_b64: ""
  allowed_peers: []
  bootstrap_peers:
    - $bootstrap
  relay_peers:
    - $bootstrap
  autorelay: true
  hole_punching: true
  force_reachability: ""
service:
  name: ""
  target: ""
edge:
  listen: $EDGE_HTTP_LISTEN
  admin_listen: $EDGE_ADMIN_LISTEN
  direct_stream_timeout: 750ms
relay:
  public_addr: ""
  health_listen: ""
  enable_relay_service: false
  enable_autonat_service: false
  enable_discovery_pubsub: false
  force_reachability_public: false
  max_reservations: 0
  max_reservations_per_ip: 0
  max_reservations_per_asn: 0
  max_circuits_per_peer: 0
  buffer_size: 0
  reservation_ttl: ""
  limit_duration: ""
  limit_data_bytes: 0
  print_run_commands: false
bridge:
  listen: ""
  service_addr: ""
  service_seed: ""
  service_p2p_listen: ""
health_listen: ""
heartbeat_interval: 15s
EOF

  cat > "$RUN_DIR/relay.yaml" <<EOF
role: relay
node:
  seed: public-relay-seed
  p2p_listen: $REMOTE_RELAY_P2P_LISTEN
network:
  private_key_file: $REMOTE_BASE_DIR/swarm.key
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
  direct_stream_timeout: ""
relay:
  public_addr: /ip4/$REMOTE_RELAY_IP/tcp/4001
  health_listen: $REMOTE_RELAY_HEALTH
  enable_relay_service: true
  enable_autonat_service: true
  enable_discovery_pubsub: true
  force_reachability_public: true
  max_reservations: 256
  max_reservations_per_ip: 16
  max_reservations_per_asn: 64
  max_circuits_per_peer: 64
  buffer_size: 4096
  reservation_ttl: 1h0m0s
  limit_duration: 5m0s
  limit_data_bytes: 16777216
  print_run_commands: true
bridge:
  listen: ""
  service_addr: ""
  service_seed: ""
  service_p2p_listen: ""
health_listen: ""
heartbeat_interval: 15s
EOF

  cat > "$RUN_DIR/service.yaml" <<EOF
role: service
node:
  seed: service-distributed-smoke-seed
  p2p_listen: $REMOTE_SERVICE_P2P_LISTEN
network:
  private_key_file: $REMOTE_BASE_DIR/swarm.key
  private_key_b64: ""
  allowed_peers: []
  bootstrap_peers:
    - $bootstrap
  relay_peers:
    - $bootstrap
  autorelay: true
  hole_punching: true
  force_reachability: private
service:
  name: $SERVICE_NAME
  target: http://$REMOTE_DUMMY_LISTEN
edge:
  listen: ""
  admin_listen: ""
  direct_stream_timeout: ""
relay:
  public_addr: ""
  health_listen: ""
  enable_relay_service: false
  enable_autonat_service: false
  enable_discovery_pubsub: false
  force_reachability_public: false
  max_reservations: 0
  max_reservations_per_ip: 0
  max_reservations_per_asn: 0
  max_circuits_per_peer: 0
  buffer_size: 0
  reservation_ttl: ""
  limit_duration: ""
  limit_data_bytes: 0
  print_run_commands: false
bridge:
  listen: ""
  service_addr: ""
  service_seed: ""
  service_p2p_listen: ""
health_listen: $REMOTE_SERVICE_HEALTH
heartbeat_interval: 5s
EOF
}

upload_remote() {
  info "uploading binaries and configs to $REMOTE_HOST"
  ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "mkdir -p '$REMOTE_BASE_DIR/.upload' '$REMOTE_BASE_DIR'"
  scp "${SSH_OPTS[@]}" \
    "$RUN_DIR/tubo" \
    "$RUN_DIR/dummy-api-server" \
    "$RUN_DIR/swarm.key" \
    "$RUN_DIR/relay.yaml" \
    "$RUN_DIR/service.yaml" \
    "$REMOTE_HOST:$REMOTE_BASE_DIR/.upload/" >/dev/null
  ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "
    set -e
    cd '$REMOTE_BASE_DIR'
    mv -f .upload/tubo ./tubo
    mv -f .upload/dummy-api-server ./dummy-api-server
    mv -f .upload/swarm.key ./swarm.key
    mv -f .upload/relay.yaml ./relay.yaml
    mv -f .upload/service.yaml ./service.yaml
  "
}

start_remote() {
  info "starting relay + dummy origin + service on remote host"
  cleanup_remote
  ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "cd '$REMOTE_BASE_DIR' && nohup env DUMMY_API_LISTEN='$REMOTE_DUMMY_LISTEN' DUMMY_API_INSTANCE='distributed-remote' ./dummy-api-server > dummy-api-server.log 2>&1 < /dev/null & echo \$! > dummy-api-server.pid"
  ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "cd '$REMOTE_BASE_DIR' && nohup ./tubo relay run --config relay.yaml > relay.log 2>&1 < /dev/null & echo \$! > relay.pid"
  ssh "${SSH_OPTS[@]}" "$REMOTE_HOST" "cd '$REMOTE_BASE_DIR' && nohup ./tubo service run --config service.yaml > service.log 2>&1 < /dev/null & echo \$! > service.pid"
  assert_remote_process_started dummy-api-server
  assert_remote_process_started relay
  assert_remote_process_started service
}

start_local_edge() {
  info "starting edge on $EDGE_HOST_IP"
  cleanup_local
  nohup "$RUN_DIR/tubo" edge run --config "$RUN_DIR/edge.yaml" > "$RUN_DIR/edge.log" 2>&1 &
  echo $! > "$RUN_DIR/edge.pid"
  assert_process_started "$RUN_DIR/edge.pid" edge
}

wait_readiness() {
  info "waiting for remote relay/service health"
  wait_remote_http_ok "http://$REMOTE_RELAY_HEALTH/healthz" 90
  wait_remote_http_ok "http://$REMOTE_SERVICE_HEALTH/healthz" 90

  info "waiting for local edge health"
  wait_http_ok "http://$EDGE_HTTP_LISTEN/healthz" 90
  wait_http_ok "http://$EDGE_ADMIN_LISTEN/healthz" 90

  info "waiting for discovery and route on edge admin"
  local services_json routes_json i
  for i in $(seq 1 90); do
    services_json="$(curl -fsS "http://$EDGE_ADMIN_LISTEN/services" || true)"
    routes_json="$(curl -fsS "http://$EDGE_ADMIN_LISTEN/routes" || true)"
    if echo "$services_json" | grep -Eq '"count"[[:space:]]*:[[:space:]]*1' && \
       echo "$routes_json" | grep -q '"hostname":"'$SERVICE_NAME'"'; then
      return 0
    fi
    sleep 1
  done

  info "discovery not ready"
  echo "services: $services_json"
  echo "routes:   $routes_json"
  return 1
}

run_request() {
  info "running end-to-end request through distributed edge"
  local payload payload_b64 resp_body http_code
  payload="hello-distributed-relay"
  payload_b64="$(printf '%s' "$payload" | base64)"
  resp_body="$(mktemp)"
  http_code="$(curl -sS -o "$resp_body" -w "%{http_code}" \
    -H "Host: $SERVICE_NAME" \
    -H "Content-Type: text/plain" \
    --data "$payload" \
    "http://$EDGE_HTTP_LISTEN/v1/dummy?from=distributed-two-host")"

  if [[ "$http_code" != "200" ]]; then
    info "expected HTTP 200, got $http_code"
    cat "$resp_body"
    return 1
  fi
  grep -q '"instance":"distributed-remote"' "$resp_body"
  grep -q '"method":"POST"' "$resp_body"
  grep -q '"path":"/v1/dummy"' "$resp_body"
  grep -q '"raw_query":"from=distributed-two-host"' "$resp_body"
  grep -q "\"body_b64\":\"$payload_b64\"" "$resp_body"
}

verify_relay_path() {
  info "verifying relayed path from edge logs"
  if ! grep -q 'connection_path=relayed' "$RUN_DIR/edge.log"; then
    info "expected relayed connection path in edge log"
    cat "$RUN_DIR/edge.log"
    return 1
  fi
}

show_summary() {
  info "PASS"
  info "edge host:   $EDGE_HOST_IP"
  info "relay host:  $REMOTE_HOST"
  info "run dir:     $RUN_DIR"
  info "remote dir:  $REMOTE_BASE_DIR"
}

build_binaries
generate_swarm_key
generate_configs
upload_remote
start_remote
start_local_edge
wait_readiness
run_request
verify_relay_path
show_summary
