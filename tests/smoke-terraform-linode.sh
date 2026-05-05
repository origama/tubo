#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TF_DIR="${TF_DIR:-$ROOT_DIR/infra/terraform/linode-distributed}"
RUN_DIR="${RUN_DIR:-$ROOT_DIR/generated/linode-terraform}"
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

info() {
  echo "[smoke-linode-terraform] $*"
}

die() {
  echo "[smoke-linode-terraform] ERROR: $*" >&2
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
  relay_peer_id="$($RUN_DIR/tubo id from-seed public-relay-seed | tr -d '\n')"
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
  ssh "${SSH_OPTS[@]}" "$host" "mkdir -p '$REMOTE_BASE_DIR/.upload' '$REMOTE_BASE_DIR/bin' /etc/tubo /var/log/tubo /var/run/tubo"
}

upload_artifacts() {
  info "uploading binaries and configs"
  for host in "$RELAY_HOST" "$EDGE_HOST" "$SERVICE_HOST"; do
    remote_prepare_dir "$host"
    scp "${SSH_OPTS[@]}" "$RUN_DIR/tubo" "$RUN_DIR/swarm.key" "$host:$REMOTE_BASE_DIR/.upload/" >/dev/null
    ssh "${SSH_OPTS[@]}" "$host" "mv -f '$REMOTE_BASE_DIR/.upload/tubo' '$REMOTE_BASE_DIR/tubo' && mv -f '$REMOTE_BASE_DIR/.upload/swarm.key' '$REMOTE_BASE_DIR/swarm.key'"
  done
  scp "${SSH_OPTS[@]}" "$RUN_DIR/dummy-api-server" "$SERVICE_HOST:$REMOTE_BASE_DIR/.upload/" >/dev/null
  ssh "${SSH_OPTS[@]}" "$SERVICE_HOST" "mv -f '$REMOTE_BASE_DIR/.upload/dummy-api-server' '$REMOTE_BASE_DIR/dummy-api-server'"
  scp "${SSH_OPTS[@]}" "$RUN_DIR/relay.yaml" "$RELAY_HOST:$REMOTE_BASE_DIR/.upload/relay.yaml" >/dev/null
  ssh "${SSH_OPTS[@]}" "$RELAY_HOST" "mv -f '$REMOTE_BASE_DIR/.upload/relay.yaml' /etc/tubo/relay.yaml"
  scp "${SSH_OPTS[@]}" "$RUN_DIR/edge.yaml" "$EDGE_HOST:$REMOTE_BASE_DIR/.upload/edge.yaml" >/dev/null
  ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "mv -f '$REMOTE_BASE_DIR/.upload/edge.yaml' /etc/tubo/edge.yaml"
  scp "${SSH_OPTS[@]}" "$RUN_DIR/service.yaml" "$SERVICE_HOST:$REMOTE_BASE_DIR/.upload/service.yaml" >/dev/null
  ssh "${SSH_OPTS[@]}" "$SERVICE_HOST" "mv -f '$REMOTE_BASE_DIR/.upload/service.yaml' /etc/tubo/service.yaml"
}

verify_swarm_key_sync() {
  info "verifying uploaded swarm.key checksum on all hosts"
  local expected actual host
  expected="$(sha256sum "$RUN_DIR/swarm.key" | awk '{print $1}')"
  for host in "$RELAY_HOST" "$EDGE_HOST" "$SERVICE_HOST"; do
    actual="$(ssh "${SSH_OPTS[@]}" "$host" "sha256sum '$REMOTE_BASE_DIR/swarm.key' | cut -d' ' -f1")"
    if [[ "$actual" != "$expected" ]]; then
      die "swarm.key checksum mismatch on $host: expected $expected got $actual"
    fi
  done
}

remote_stop() {
  local host="$1"
  ssh "${SSH_OPTS[@]}" "$host" '
    set -e
    for name in relay edge service dummy-api-server; do
      if [ -f "/var/run/github.com/origama/tubo/$name.pid" ]; then
        kill "$(cat "/var/run/github.com/origama/tubo/$name.pid")" >/dev/null 2>&1 || true
        rm -f "/var/run/github.com/origama/tubo/$name.pid"
      fi
    done
  ' >/dev/null 2>&1 || true
}

cleanup() {
  if [[ "$KEEP_RUNNING" == "1" ]]; then
    info "KEEP_RUNNING=1, leaving remote processes up"
    return
  fi
  remote_stop "$RELAY_HOST"
  remote_stop "$EDGE_HOST"
  remote_stop "$SERVICE_HOST"
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

start_remote_processes() {
  info "starting relay"
  remote_stop "$RELAY_HOST"
  ssh "${SSH_OPTS[@]}" "$RELAY_HOST" "
    nohup '$REMOTE_BASE_DIR/tubo' relay run --config /etc/tubo/relay.yaml > /var/log/tubo/relay.log 2>&1 & echo \$! > /var/run/github.com/origama/tubo/relay.pid
  "

  info "starting edge"
  remote_stop "$EDGE_HOST"
  ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "
    nohup '$REMOTE_BASE_DIR/tubo' edge run --config /etc/tubo/edge.yaml > /var/log/tubo/edge.log 2>&1 & echo \$! > /var/run/github.com/origama/tubo/edge.pid
  "

  info "starting service + dummy origin"
  remote_stop "$SERVICE_HOST"
  ssh "${SSH_OPTS[@]}" "$SERVICE_HOST" "
    nohup env DUMMY_API_LISTEN='$DUMMY_API_LISTEN' DUMMY_API_INSTANCE='linode-terraform-remote' '$REMOTE_BASE_DIR/dummy-api-server' > /var/log/tubo/dummy-api-server.log 2>&1 & echo \$! > /var/run/github.com/origama/tubo/dummy-api-server.pid
    nohup '$REMOTE_BASE_DIR/tubo' service run --config /etc/tubo/service.yaml > /var/log/tubo/service.log 2>&1 & echo \$! > /var/run/github.com/origama/tubo/service.pid
  "
}

wait_readiness() {
  info "waiting for relay/service/edge health"
  wait_remote_http_ok "$RELAY_HOST" "http://$RELAY_HEALTH_LISTEN/healthz"
  wait_remote_http_ok "$SERVICE_HOST" "http://$SERVICE_HEALTH_LISTEN/healthz"
  wait_remote_http_ok "$EDGE_HOST" "http://$EDGE_HTTP_LISTEN/healthz"
  wait_remote_http_ok "$EDGE_HOST" "http://$EDGE_ADMIN_LISTEN/healthz"

  info "waiting for discovery and route on edge"
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
  info "running request from inside edge host"
  local payload payload_b64 response
  payload="hello-linode-terraform"
  payload_b64="$(printf '%s' "$payload" | base64)"
  response="$(ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "curl -sS -H 'Host: $SERVICE_NAME' -H 'Content-Type: text/plain' --data '$payload' 'http://$EDGE_HTTP_LISTEN/v1/dummy?from=linode-terraform'")"

  [[ "$response" == *'"instance":"linode-terraform-remote"'* ]]
  [[ "$response" == *'"method":"POST"'* ]]
  [[ "$response" == *'"path":"/v1/dummy"'* ]]
  [[ "$response" == *'"raw_query":"from=linode-terraform"'* ]]
  [[ "$response" == *"\"body_b64\":\"$payload_b64\""* ]]
}

verify_relay_path() {
  info "verifying relayed path"
  ssh "${SSH_OPTS[@]}" "$EDGE_HOST" "grep -q 'connection_path=relayed' /var/log/tubo/edge.log"
}

show_summary() {
  info "PASS"
  info "relay:   $RELAY_IP"
  info "edge:    $EDGE_IP"
  info "service: $SERVICE_IP"
}

trap cleanup EXIT
build_binaries
generate_swarm_key
fetch_hosts
generate_configs
upload_artifacts
verify_swarm_key_sync
start_remote_processes
wait_readiness
run_request
verify_relay_path
show_summary
