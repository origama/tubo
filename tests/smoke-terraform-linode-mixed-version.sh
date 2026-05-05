#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TF_DIR="${TF_DIR:-$ROOT_DIR/infra/terraform/linode-distributed}"
RUN_DIR="${RUN_DIR:-$ROOT_DIR/generated/linode-terraform-mixed-version}"
LEGACY_REF="${LEGACY_REF:-c9bbb1f}"
SSH_KEY_PATH="${SSH_KEY_PATH:-}"
SERVICE_NAME="${SERVICE_NAME:-myapi}"
REMOTE_BASE_DIR="${REMOTE_BASE_DIR:-/opt/tubo}"
EDGE_HTTP_LISTEN="${EDGE_HTTP_LISTEN:-127.0.0.1:8443}"
EDGE_ADMIN_LISTEN="${EDGE_ADMIN_LISTEN:-127.0.0.1:8444}"
RELAY_HEALTH_LISTEN="${RELAY_HEALTH_LISTEN:-127.0.0.1:8092}"
SERVICE_HEALTH_LISTEN="${SERVICE_HEALTH_LISTEN:-127.0.0.1:8091}"
DUMMY_API_LISTEN="${DUMMY_API_LISTEN:-127.0.0.1:18000}"
KEEP_RUNNING="${KEEP_RUNNING:-0}"
SSH_OPTS=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new)
LEGACY_WORKTREE=""
RELAY_IP=""
EDGE_IP=""
SERVICE_IP=""
RELAY_HOST=""
EDGE_HOST=""
SERVICE_HOST=""

info() {
  echo "[smoke-linode-mixed-version] $*"
}

die() {
  echo "[smoke-linode-mixed-version] ERROR: $*" >&2
  exit 1
}

need() {
  command -v "$1" >/dev/null 2>&1 || die "missing command: $1"
}

need terraform
need go
need curl
need ssh
need scp
need git

mkdir -p "$RUN_DIR"

terraform_output() {
  terraform -chdir="$TF_DIR" output -raw "$1"
}

if [[ -z "$SSH_KEY_PATH" ]]; then
  if [[ -f "$TF_DIR/terraform.tfvars" ]]; then
    SSH_KEY_PATH="$(python3 - "$TF_DIR/terraform.tfvars" <<'PY'
import re, sys
text = open(sys.argv[1], 'r', encoding='utf-8').read()
m = re.search(r'^ssh_private_key_path\s*=\s*"([^"]+)"', text, re.M)
print(m.group(1) if m else "")
PY
)"
  fi
fi

[[ -n "$SSH_KEY_PATH" ]] || die "set SSH_KEY_PATH or put ssh_private_key_path in terraform.tfvars"
SSH_KEY_PATH="${SSH_KEY_PATH/#\~/$HOME}"
[[ -f "$SSH_KEY_PATH" ]] || die "SSH private key not found: $SSH_KEY_PATH"
SSH_OPTS+=( -i "$SSH_KEY_PATH" )

cleanup() {
  if [[ -n "$LEGACY_WORKTREE" ]] && [[ -d "$LEGACY_WORKTREE" ]]; then
    git -C "$ROOT_DIR" worktree remove --force "$LEGACY_WORKTREE" >/dev/null 2>&1 || true
  fi
  if [[ "$KEEP_RUNNING" == "1" ]]; then
    info "KEEP_RUNNING=1, leaving remote processes up"
    return
  fi
  for host in "$RELAY_HOST" "$EDGE_HOST" "$SERVICE_HOST"; do
    [[ -n "$host" ]] || continue
    ssh "${SSH_OPTS[@]}" "$host" '
      set -e
      for name in relay edge service dummy-api-server; do
        if [ -f "/var/run/tubo/$name.pid" ]; then
          kill "$(cat "/var/run/tubo/$name.pid")" >/dev/null 2>&1 || true
          rm -f "/var/run/tubo/$name.pid"
        fi
      done
    ' >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

build_binaries() {
  info "building current binary"
  (cd "$ROOT_DIR" && go build -o "$RUN_DIR/tubo-current" ./cmd/tubo)
  (cd "$ROOT_DIR" && go build -o "$RUN_DIR/dummy-api-server" ./cmd/dummy-api-server)

  info "building legacy binary from $LEGACY_REF"
  LEGACY_WORKTREE="$(mktemp -d /tmp/tubo-legacy-XXXXXX)"
  git -C "$ROOT_DIR" worktree add --detach "$LEGACY_WORKTREE" "$LEGACY_REF" >/dev/null
  (cd "$LEGACY_WORKTREE" && go build -o "$RUN_DIR/tubo-legacy" ./cmd/tubo)
}

generate_swarm_key() {
  info "generating ephemeral swarm key"
  rm -f "$RUN_DIR/swarm.key"
  "$RUN_DIR/tubo-current" keygen swarm --out "$RUN_DIR/swarm.key" >/dev/null
}

fetch_hosts() {
  RELAY_IP="$(terraform_output relay_public_ip)"
  EDGE_IP="$(terraform_output edge_public_ip)"
  SERVICE_IP="$(terraform_output service_public_ip)"
  RELAY_HOST="root@$RELAY_IP"
  EDGE_HOST="root@$EDGE_IP"
  SERVICE_HOST="root@$SERVICE_IP"
}

generate_configs() {
  local relay_peer_id bootstrap
  relay_peer_id="$($RUN_DIR/tubo-current id from-seed public-relay-seed | tr -d '\n')"
  bootstrap="/ip4/$RELAY_IP/tcp/4001/p2p/$relay_peer_id"

  cat > "$RUN_DIR/relay.yaml" <<EOF
role: relay
node:
  seed: public-relay-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
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
  public_addr: /ip4/$RELAY_IP/tcp/4001
  health_listen: $RELAY_HEALTH_LISTEN
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
  limit_data_bytes: 268435456
  print_run_commands: true
bridge:
  listen: ""
  service_addr: ""
  service_seed: ""
  service_p2p_listen: ""
health_listen: ""
heartbeat_interval: 15s
EOF

  cat > "$RUN_DIR/edge.yaml" <<EOF
role: edge
node:
  seed: edge-terraform-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
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

  cat > "$RUN_DIR/service.yaml" <<EOF
role: service
node:
  seed: service-terraform-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
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
  target: http://$DUMMY_API_LISTEN
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
health_listen: $SERVICE_HEALTH_LISTEN
heartbeat_interval: 5s
EOF
}

remote_prepare_dir() {
  local host="$1"
  ssh "${SSH_OPTS[@]}" "$host" "mkdir -p '$REMOTE_BASE_DIR' /etc/tubo /var/log/tubo /var/run/tubo"
}

upload_common_artifacts() {
  for host in "$RELAY_HOST" "$EDGE_HOST" "$SERVICE_HOST"; do
    remote_prepare_dir "$host"
    scp "${SSH_OPTS[@]}" "$RUN_DIR/swarm.key" "$host:$REMOTE_BASE_DIR/swarm.key.tmp" >/dev/null
    ssh "${SSH_OPTS[@]}" "$host" "mv '$REMOTE_BASE_DIR/swarm.key.tmp' '$REMOTE_BASE_DIR/swarm.key'"
  done
  scp "${SSH_OPTS[@]}" "$RUN_DIR/dummy-api-server" "$SERVICE_HOST:$REMOTE_BASE_DIR/dummy-api-server.tmp" >/dev/null
  ssh "${SSH_OPTS[@]}" "$SERVICE_HOST" "mv '$REMOTE_BASE_DIR/dummy-api-server.tmp' '$REMOTE_BASE_DIR/dummy-api-server'"
  scp "${SSH_OPTS[@]}" "$RUN_DIR/relay.yaml" "$RELAY_HOST:/etc/tubo/relay.yaml.tmp" >/dev/null
  ssh "${SSH_OPTS[@]}" "$RELAY_HOST" "mv /etc/tubo/relay.yaml.tmp /etc/tubo/relay.yaml"
  scp "${SSH_OPTS[@]}" "$RUN_DIR/edge.yaml" "$EDGE_HOST:/etc/tubo/edge.yaml.tmp" >/dev/null
  ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "mv /etc/tubo/edge.yaml.tmp /etc/tubo/edge.yaml"
  scp "${SSH_OPTS[@]}" "$RUN_DIR/service.yaml" "$SERVICE_HOST:/etc/tubo/service.yaml.tmp" >/dev/null
  ssh "${SSH_OPTS[@]}" "$SERVICE_HOST" "mv /etc/tubo/service.yaml.tmp /etc/tubo/service.yaml"
}

install_binaries() {
  local edge_bin="$1"
  local service_bin="$2"
  info "installing edge binary: $(basename "$edge_bin") ; service binary: $(basename "$service_bin")"
  scp "${SSH_OPTS[@]}" "$RUN_DIR/tubo-current" "$RELAY_HOST:$REMOTE_BASE_DIR/tubo.tmp" >/dev/null
  ssh "${SSH_OPTS[@]}" "$RELAY_HOST" "mv '$REMOTE_BASE_DIR/tubo.tmp' '$REMOTE_BASE_DIR/tubo'"
  scp "${SSH_OPTS[@]}" "$edge_bin" "$EDGE_HOST:$REMOTE_BASE_DIR/tubo.tmp" >/dev/null
  ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "mv '$REMOTE_BASE_DIR/tubo.tmp' '$REMOTE_BASE_DIR/tubo'"
  scp "${SSH_OPTS[@]}" "$service_bin" "$SERVICE_HOST:$REMOTE_BASE_DIR/tubo.tmp" >/dev/null
  ssh "${SSH_OPTS[@]}" "$SERVICE_HOST" "mv '$REMOTE_BASE_DIR/tubo.tmp' '$REMOTE_BASE_DIR/tubo'"
}

remote_stop() {
  local host="$1"
  ssh "${SSH_OPTS[@]}" "$host" '
    set -e
    for name in relay edge service dummy-api-server; do
      if [ -f "/var/run/tubo/$name.pid" ]; then
        kill "$(cat "/var/run/tubo/$name.pid")" >/dev/null 2>&1 || true
        rm -f "/var/run/tubo/$name.pid"
      fi
    done
  ' >/dev/null 2>&1 || true
}

wait_remote_http_ok() {
  local host="$1"
  local url="$2"
  local tries="${3:-90}"
  local i
  for i in $(seq 1 "$tries"); do
    if ssh "${SSH_OPTS[@]}" "$host" "curl -fsS '$url' >/dev/null" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

start_processes() {
  remote_stop "$RELAY_HOST"
  remote_stop "$EDGE_HOST"
  remote_stop "$SERVICE_HOST"

  ssh "${SSH_OPTS[@]}" "$RELAY_HOST" "nohup '$REMOTE_BASE_DIR/tubo' relay run --config /etc/tubo/relay.yaml > /var/log/tubo/relay.log 2>&1 & echo \$! > /var/run/tubo/relay.pid"
  ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "nohup '$REMOTE_BASE_DIR/tubo' edge run --config /etc/tubo/edge.yaml > /var/log/tubo/edge.log 2>&1 & echo \$! > /var/run/tubo/edge.pid"
  ssh "${SSH_OPTS[@]}" "$SERVICE_HOST" "nohup env DUMMY_API_LISTEN='$DUMMY_API_LISTEN' DUMMY_API_INSTANCE='linode-terraform-mixed-version' '$REMOTE_BASE_DIR/dummy-api-server' > /var/log/tubo/dummy-api-server.log 2>&1 & echo \$! > /var/run/tubo/dummy-api-server.pid; nohup '$REMOTE_BASE_DIR/tubo' service run --config /etc/tubo/service.yaml > /var/log/tubo/service.log 2>&1 & echo \$! > /var/run/tubo/service.pid"
}

wait_readiness() {
  wait_remote_http_ok "$RELAY_HOST" "http://$RELAY_HEALTH_LISTEN/healthz"
  wait_remote_http_ok "$SERVICE_HOST" "http://$SERVICE_HEALTH_LISTEN/healthz"
  wait_remote_http_ok "$EDGE_HOST" "http://$EDGE_HTTP_LISTEN/healthz"
  wait_remote_http_ok "$EDGE_HOST" "http://$EDGE_ADMIN_LISTEN/healthz"

  local i services_json routes_json
  for i in $(seq 1 90); do
    services_json="$(ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "curl -fsS http://$EDGE_ADMIN_LISTEN/services" || true)"
    routes_json="$(ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "curl -fsS http://$EDGE_ADMIN_LISTEN/routes" || true)"
    if echo "$services_json" | grep -Eq '"count"[[:space:]]*:[[:space:]]*1' && \
       echo "$routes_json" | grep -q '"hostname":"'$SERVICE_NAME'"'; then
      return 0
    fi
    sleep 1
  done
  echo "services: $services_json"
  echo "routes:   $routes_json"
  return 1
}

run_request() {
  local payload payload_b64 response
  payload="hello-linode-mixed-version"
  payload_b64="$(printf '%s' "$payload" | base64)"
  response="$(ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "curl -sS -H 'Host: $SERVICE_NAME' -H 'Content-Type: text/plain' --data '$payload' 'http://$EDGE_HTTP_LISTEN/v1/dummy?from=linode-mixed-version'")"
  [[ "$response" == *'"instance":"linode-terraform-mixed-version"'* ]]
  [[ "$response" == *'"method":"POST"'* ]]
  [[ "$response" == *'"path":"/v1/dummy"'* ]]
  [[ "$response" == *'"raw_query":"from=linode-mixed-version"'* ]]
  [[ "$response" == *"\"body_b64\":\"$payload_b64\""* ]]
}

assert_log_contains() {
  local host="$1"
  local log_path="$2"
  local pattern="$3"
  ssh "${SSH_OPTS[@]}" "$host" "grep -q '$pattern' '$log_path'"
}

assert_admin_protocol_contains() {
  local host="$1"
  local url="$2"
  local pattern="$3"
  ssh "${SSH_OPTS[@]}" "$host" "curl -fsS '$url' | grep -q '$pattern'"
}

run_scenario() {
  local name="$1"
  local edge_bin="$2"
  local service_bin="$3"
  local expect_stream_protocol="$4"
  local stream_protocol_log_host="${5:-edge}"
  local expect_edge_protocol_json="${6:-}"
  local expect_service_protocol_json="${7:-}"

  info "scenario: $name"
  install_binaries "$edge_bin" "$service_bin"
  start_processes
  wait_readiness
  run_request
  assert_log_contains "$EDGE_HOST" /var/log/tubo/edge.log 'connection_path=relayed'

  if [[ "$stream_protocol_log_host" == "edge" ]]; then
    assert_log_contains "$EDGE_HOST" /var/log/tubo/edge.log "stream_protocol_id=$expect_stream_protocol"
  else
    assert_log_contains "$SERVICE_HOST" /var/log/tubo/service.log "stream_protocol_id=$expect_stream_protocol"
  fi
  if [[ "$expect_stream_protocol" == "/p2p-tunnel/1.1" ]]; then
    assert_log_contains "$EDGE_HOST" /var/log/tubo/edge.log 'client protocol negotiated'
    assert_log_contains "$SERVICE_HOST" /var/log/tubo/service.log 'service protocol negotiated'
  fi
  if [[ -n "$expect_edge_protocol_json" ]]; then
    assert_admin_protocol_contains "$EDGE_HOST" "http://$EDGE_ADMIN_LISTEN/protocol" "$expect_edge_protocol_json"
  fi
  if [[ -n "$expect_service_protocol_json" ]]; then
    assert_admin_protocol_contains "$SERVICE_HOST" "http://$SERVICE_HEALTH_LISTEN/debug/protocol" "$expect_service_protocol_json"
  fi
}

show_summary() {
  info "PASS"
  info "relay:   $RELAY_IP"
  info "edge:    $EDGE_IP"
  info "service: $SERVICE_IP"
  info "legacy ref: $LEGACY_REF"
}

build_binaries
generate_swarm_key
fetch_hosts
generate_configs
upload_common_artifacts
run_scenario "current edge -> legacy service (legacy fallback)" "$RUN_DIR/tubo-current" "$RUN_DIR/tubo-legacy" "/p2p-tunnel/1.0" edge 'protocol_version":"1.1'
run_scenario "legacy edge -> current service (current service accepts legacy)" "$RUN_DIR/tubo-legacy" "$RUN_DIR/tubo-current" "/p2p-tunnel/1.0" service '' 'protocol_version":"1.1'
run_scenario "current edge -> current service (hello negotiation)" "$RUN_DIR/tubo-current" "$RUN_DIR/tubo-current" "/p2p-tunnel/1.1" edge 'stream_protocol_id":"/p2p-tunnel/1.1' 'stream_protocol_id":"/p2p-tunnel/1.1'
show_summary
