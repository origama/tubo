#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

COMPOSE="${COMPOSE_CMD:-docker compose} -f tests/e2e/compose/tubo-workflow/compose.yml"
export DOCKER_BUILDKIT="${DOCKER_BUILDKIT:-0}"
export COMPOSE_DOCKER_CLI_BUILD="${COMPOSE_DOCKER_CLI_BUILD:-0}"

BIN_DIR="$(mktemp -d "${ROOT_DIR}/.tmp-smoke-workflow-bin.XXXXXX")"
TUBO_BIN="$BIN_DIR/tubo"
connect_process_ref=""
connect_log_path=""

tubo() {
  "$TUBO_BIN" "$@"
}

cleanup() {
  set +e
  if [[ -n "$connect_process_ref" ]]; then
    tubo stop "$connect_process_ref" >/dev/null 2>&1 || true
    tubo rm --stale >/dev/null 2>&1 || true
    connect_process_ref=""
  fi
  pkill -f 'generated/tubo-workflow/tubo/client.yaml' >/dev/null 2>&1 || true
  for _ in $(seq 1 40); do
    if ! pgrep -f 'generated/tubo-workflow/tubo/client.yaml' >/dev/null 2>&1; then
      break
    fi
    sleep 0.25
  done
  $COMPOSE down --remove-orphans >/dev/null 2>&1 || true
  rm -rf "$BIN_DIR"
}
trap cleanup EXIT INT TERM

wait_http_ok() {
  local url="$1"
  local tries="${2:-90}"
  for _ in $(seq 1 "$tries"); do
    if curl -fsS "$url" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  return 1
}

assert_no_workflow_connect_leaks() {
  if pgrep -af 'generated/tubo-workflow/tubo/client.yaml' >/dev/null 2>&1; then
    echo "[smoke-tubo-workflow] leaked host-side tubo connect process"
    pgrep -af 'generated/tubo-workflow/tubo/client.yaml' || true
    return 1
  fi
}

free_tcp_port() {
  python3 - <<'PY'
import socket
s = socket.socket()
s.bind(('127.0.0.1', 0))
print(s.getsockname()[1])
s.close()
PY
}

extract_field() {
  local prefix="$1"
  local text="$2"
  printf '%s\n' "$text" | awk -v p="$prefix" 'index($0, p) == 1 {sub(p, "", $0); sub(/^: /, "", $0); print; exit}'
}

service_id_from_owner_key() {
  local key_file="$1"
  local workdir
  workdir="$(mktemp -d "${ROOT_DIR}/.tmp-smoke-workflow.XXXXXX")"
  trap 'rm -rf "$workdir"' RETURN
  cat > "$workdir/service-id-from-owner-key.go" <<'EOF'
package main

import (
  "fmt"
  "os"

  "github.com/origama/tubo/internal/serviceidentity"
)

func main() {
  if len(os.Args) != 2 {
    panic("usage: service-id-from-owner-key <key-file>")
  }
  identity, _, err := serviceidentity.Load(os.Args[1])
  if err != nil {
    panic(err)
  }
  fmt.Println(identity.ServiceID)
}
EOF
  go run "$workdir/service-id-from-owner-key.go" "$key_file"
}

generate_membership_cap() {
  local private_key_file="$1"
  local cluster_id="$2"
  local namespace="$3"
  local subject_peer_id="$4"
  local out_file="$5"
  mkdir -p "$(dirname "$out_file")"
  local workdir
  workdir="$(mktemp -d "${ROOT_DIR}/.tmp-smoke-workflow.XXXXXX")"
  trap 'rm -rf "$workdir"' RETURN
  cat > "$workdir/gen-membership-cap.go" <<'EOF'
//go:build tools
// +build tools

package main

import (
  "crypto/ed25519"
  "crypto/x509"
  "encoding/json"
  "encoding/pem"
  "fmt"
  "os"
  "time"

  capability "github.com/origama/tubo/internal/capability"
)

func loadKey(path string) (ed25519.PrivateKey, error) {
  b, err := os.ReadFile(path)
  if err != nil { return nil, err }
  block, _ := pem.Decode(b)
  if block == nil { return nil, fmt.Errorf("private key is not PEM encoded") }
  key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
  if err != nil { return nil, err }
  switch k := key.(type) {
  case ed25519.PrivateKey:
    return k, nil
  case *ed25519.PrivateKey:
    return *k, nil
  default:
    return nil, fmt.Errorf("unsupported private key type %T", key)
  }
}

func main() {
  if len(os.Args) != 6 {
    panic("usage: gen-membership-cap <private-key> <cluster-id> <namespace> <subject-peer-id> <out-file>")
  }
  priv, err := loadKey(os.Args[1])
  if err != nil { panic(err) }
  cap, err := capability.SignMembershipCapability(capability.MembershipCapability{
    ClusterID:     os.Args[2],
    NamespaceID:   os.Args[3],
    SubjectPeerID: os.Args[4],
    Permissions: []string{
      capability.PermissionSubscribe,
      capability.PermissionList,
      capability.PermissionPublish,
      capability.PermissionConnect,
    },
    ExpiresAt: time.Now().Add(time.Hour),
  }, priv)
  if err != nil { panic(err) }
  b, err := json.MarshalIndent(cap, "", "  ")
  if err != nil { panic(err) }
  b = append(b, '\n')
  if err := os.WriteFile(os.Args[5], b, 0600); err != nil { panic(err) }
}
EOF
  go run -tags tools "$workdir/gen-membership-cap.go" "$private_key_file" "$cluster_id" "$namespace" "$subject_peer_id" "$out_file"
}

compose_build_serial() {
  if $COMPOSE build --help 2>/dev/null | grep -q -- "--no-parallel"; then
    $COMPOSE build --no-parallel
    return
  fi
  COMPOSE_PARALLEL_LIMIT=1 $COMPOSE build
}

echo "[smoke-tubo-workflow] building local tubo binary"
go build -o "$TUBO_BIN" ./cmd/tubo

config_dir="generated/tubo-workflow/tubo"
container_root="/home/nonroot/.config/tubo"
config_path="${config_dir}/config.yaml"
rm -rf generated/tubo-workflow
mkdir -p "$config_dir"
cat > "$config_path" <<YAML
role: service
current_overlay: public
overlays:
  public: {}
YAML
export XDG_CONFIG_HOME="${ROOT_DIR}/generated/tubo-workflow"

cluster_out="$(tubo create cluster/home --config "$config_path")"
cluster_id="$(extract_field "cluster id" "$cluster_out")"
authority_public_key="$(extract_field "authority public key" "$cluster_out")"
host_authority_key_file="${config_dir}/clusters/home/authority.key"
host_tenant_a_cluster_cap_file="${config_dir}/clusters/home/namespaces/tenant-a/cluster.membership.cap.json"
host_tenant_a_namespace_cap_file="${config_dir}/clusters/home/namespaces/tenant-a/membership.cap.json"
host_tenant_b_cluster_cap_file="${config_dir}/clusters/home/namespaces/tenant-b/cluster.membership.cap.json"
host_tenant_b_namespace_cap_file="${config_dir}/clusters/home/namespaces/tenant-b/membership.cap.json"
host_swarm_key_file="${config_dir}/swarm.key"
authority_key_file="${container_root}/clusters/home/authority.key"
tenant_a_cluster_cap_file="${container_root}/clusters/home/namespaces/tenant-a/cluster.membership.cap.json"
tenant_a_namespace_cap_file="${container_root}/clusters/home/namespaces/tenant-a/membership.cap.json"
tenant_b_cluster_cap_file="${container_root}/clusters/home/namespaces/tenant-b/cluster.membership.cap.json"
tenant_b_namespace_cap_file="${container_root}/clusters/home/namespaces/tenant-b/membership.cap.json"
swarm_key_file="${container_root}/swarm.key"

tubo keygen swarm --out "$host_swarm_key_file" >/dev/null

if [[ -z "$cluster_id" || -z "$authority_public_key" ]]; then
  echo "[smoke-tubo-workflow] failed to parse cluster metadata"
  echo "$cluster_out"
  exit 1
fi

tubo create namespace/tenant-a --config "$config_path" >/dev/null
tenant_a_out="$(tubo create service/myapi --config "$config_path")"
service_a_id="$(extract_field "service id" "$tenant_a_out")"
service_a_seed="$(extract_field "service seed" "$tenant_a_out")"
service_a_owner_key_file="$(extract_field "service owner key file" "$tenant_a_out")"
host_service_a_owner_key_file="${config_dir}/clusters/home/namespaces/tenant-a/services/myapi.owner.key"
host_service_a_claim_file="${config_dir}/clusters/home/namespaces/tenant-a/services/myapi.claim.json"
host_service_a_publish_lease_file="${config_dir}/clusters/home/namespaces/tenant-a/services/myapi.publish-lease.json"
service_a_owner_key_file_container="${container_root}/clusters/home/namespaces/tenant-a/services/myapi.owner.key"
service_a_claim_file="${container_root}/clusters/home/namespaces/tenant-a/services/myapi.claim.json"
service_a_publish_lease_file="${container_root}/clusters/home/namespaces/tenant-a/services/myapi.publish-lease.json"
service_a_peer_id="$(tubo id from-seed "$service_a_seed" | tr -d '\n')"
generate_membership_cap "$host_authority_key_file" "$cluster_id" tenant-a "$service_a_peer_id" "$host_tenant_a_cluster_cap_file"
generate_membership_cap "$host_authority_key_file" "$cluster_id" tenant-a "$service_a_peer_id" "$host_tenant_a_cluster_cap_file"
generate_membership_cap "$host_authority_key_file" "$cluster_id" tenant-a "$cluster_id" "$host_tenant_a_namespace_cap_file"

tubo create namespace/tenant-b --config "$config_path" >/dev/null
tubo use namespace/tenant-b --config "$config_path" >/dev/null
service_b_out="$(tubo create service/myapi --config "$config_path")"
service_b_id="$(extract_field "service id" "$service_b_out")"
service_b_seed="$(extract_field "service seed" "$service_b_out")"
service_b_owner_key_file="$(extract_field "service owner key file" "$service_b_out")"
host_service_b_owner_key_file="${config_dir}/clusters/home/namespaces/tenant-b/services/myapi.owner.key"
host_service_b_claim_file="${config_dir}/clusters/home/namespaces/tenant-b/services/myapi.claim.json"
host_service_b_publish_lease_file="${config_dir}/clusters/home/namespaces/tenant-b/services/myapi.publish-lease.json"
service_b_owner_key_file_container="${container_root}/clusters/home/namespaces/tenant-b/services/myapi.owner.key"
service_b_claim_file="${container_root}/clusters/home/namespaces/tenant-b/services/myapi.claim.json"
service_b_publish_lease_file="${container_root}/clusters/home/namespaces/tenant-b/services/myapi.publish-lease.json"
service_b_peer_id="$(tubo id from-seed "$service_b_seed" | tr -d '\n')"
generate_membership_cap "$host_authority_key_file" "$cluster_id" tenant-b "$service_b_peer_id" "$host_tenant_b_cluster_cap_file"
generate_membership_cap "$host_authority_key_file" "$cluster_id" tenant-b "$service_b_peer_id" "$host_tenant_b_cluster_cap_file"
generate_membership_cap "$host_authority_key_file" "$cluster_id" tenant-b "$cluster_id" "$host_tenant_b_namespace_cap_file"

if [[ -z "$service_a_id" || -z "$service_a_seed" || -z "$service_a_owner_key_file" ]]; then
  echo "[smoke-tubo-workflow] tenant-a service metadata incomplete"
  echo "$tenant_a_out"
  exit 1
fi
if [[ -z "$service_b_id" || -z "$service_b_seed" || -z "$service_b_owner_key_file" ]]; then
  echo "[smoke-tubo-workflow] tenant-b service metadata incomplete"
  echo "$service_b_out"
  exit 1
fi
if [[ ! -f "$service_a_owner_key_file" || ! -f "$service_b_owner_key_file" ]]; then
  echo "[smoke-tubo-workflow] missing service owner key file"
  exit 1
fi
expected_a_id="$(service_id_from_owner_key "$service_a_owner_key_file" | tr -d '\n')"
expected_b_id="$(service_id_from_owner_key "$service_b_owner_key_file" | tr -d '\n')"
if [[ "$service_a_id" != "$expected_a_id" ]]; then
  echo "[smoke-tubo-workflow] tenant-a service id does not match owner key"
  echo "got id=$service_a_id want=$expected_a_id"
  exit 1
fi
if [[ "$service_b_id" != "$expected_b_id" ]]; then
  echo "[smoke-tubo-workflow] tenant-b service id does not match owner key"
  echo "got id=$service_b_id want=$expected_b_id"
  exit 1
fi
if [[ "$service_a_id" == "$service_b_id" ]]; then
  echo "[smoke-tubo-workflow] service id should differ across namespaces"
  exit 1
fi
if [[ "$service_a_seed" == "$service_b_seed" ]]; then
  echo "[smoke-tubo-workflow] service seed should differ across namespaces"
  exit 1
fi

relay_peer_id="$(tubo id from-seed relay-demo-seed | tr -d '\n')"
edge_peer_id="$(tubo id from-seed edge-demo-seed | tr -d '\n')"
relay_addr="/dns4/tubo-relay/tcp/4002/p2p/${relay_peer_id}"
host_relay_addr="/ip4/127.0.0.1/tcp/4002/p2p/${relay_peer_id}"
edge_addr="/dns4/tubo-edge/tcp/4001/p2p/${edge_peer_id}"

cat > "${config_dir}/relay.yaml" <<YAML
role: relay
node:
  seed: relay-demo-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4002
network:
  private_key_file: ${swarm_key_file}
relay:
  health_listen: :8092
  enable_relay_service: true
  enable_autonat_service: true
  enable_discovery_pubsub: true
  force_reachability_public: true
  print_run_commands: false
YAML

cat > "${config_dir}/edge.yaml" <<YAML
role: edge
node:
  seed: edge-demo-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
network:
  private_key_file: ${swarm_key_file}
  relay_peers:
    - ${relay_addr}
edge:
  listen: :8443
  admin_listen: :8444
  direct_stream_timeout: 750ms
current_cluster: home
current_namespace: tenant-a
clusters:
  home:
    cluster_id: ${cluster_id}
    authority_public_key: ${authority_public_key}
    authority_private_key_file: ${authority_key_file}
    membership_capability_file: ${tenant_a_cluster_cap_file}
    namespaces:
      tenant-a:
        membership_capability_file: ${tenant_a_namespace_cap_file}
        services:
          myapi:
            service_id: ${service_a_id}
            service_seed: ${service_a_seed}
            service_owner_key_file: ${service_a_owner_key_file_container}
            service_claim_file: ${service_a_claim_file}
            service_publish_lease_file: ${service_a_publish_lease_file}
      tenant-b:
        membership_capability_file: ${tenant_b_namespace_cap_file}
        services:
          myapi:
            service_id: ${service_b_id}
            service_seed: ${service_b_seed}
            service_owner_key_file: ${service_b_owner_key_file_container}
            service_claim_file: ${service_b_claim_file}
            service_publish_lease_file: ${service_b_publish_lease_file}
YAML

cat > "${config_dir}/service-tenant-a.yaml" <<YAML
role: service
node:
  seed: ${service_a_seed}
  p2p_listen: /ip4/0.0.0.0/tcp/40123
network:
  private_key_file: ${swarm_key_file}
  bootstrap_peers:
    - ${edge_addr}
    - ${relay_addr}
  relay_peers:
    - ${relay_addr}
  autorelay: true
  hole_punching: true
service:
  name: myapi
  target: http://tubo-dummy-api-server:8000
health_listen: :8091
heartbeat_interval: 5s
current_cluster: home
current_namespace: tenant-a
clusters:
  home:
    cluster_id: ${cluster_id}
    authority_public_key: ${authority_public_key}
    authority_private_key_file: ${authority_key_file}
    membership_capability_file: ${tenant_a_cluster_cap_file}
    namespaces:
      tenant-a:
        membership_capability_file: ${tenant_a_namespace_cap_file}
        services:
          myapi:
            service_id: ${service_a_id}
            service_seed: ${service_a_seed}
            service_owner_key_file: ${service_a_owner_key_file_container}
            service_claim_file: ${service_a_claim_file}
            service_publish_lease_file: ${service_a_publish_lease_file}
      tenant-b:
        membership_capability_file: ${tenant_b_namespace_cap_file}
        services:
          myapi:
            service_id: ${service_b_id}
            service_seed: ${service_b_seed}
            service_owner_key_file: ${service_b_owner_key_file_container}
            service_claim_file: ${service_b_claim_file}
            service_publish_lease_file: ${service_b_publish_lease_file}
YAML

cat > "${config_dir}/service-tenant-b.yaml" <<YAML
role: service
node:
  seed: ${service_b_seed}
  p2p_listen: /ip4/0.0.0.0/tcp/40124
network:
  private_key_file: ${swarm_key_file}
  bootstrap_peers:
    - ${edge_addr}
    - ${relay_addr}
  relay_peers:
    - ${relay_addr}
  autorelay: true
  hole_punching: true
service:
  name: myapi
  target: http://tubo-dummy-api-server:8000
health_listen: :8093
heartbeat_interval: 5s
current_cluster: home
current_namespace: tenant-b
clusters:
  home:
    cluster_id: ${cluster_id}
    authority_public_key: ${authority_public_key}
    authority_private_key_file: ${authority_key_file}
    membership_capability_file: ${tenant_b_cluster_cap_file}
    namespaces:
      tenant-a:
        membership_capability_file: ${tenant_a_namespace_cap_file}
        services:
          myapi:
            service_id: ${service_a_id}
            service_seed: ${service_a_seed}
            service_owner_key_file: ${service_a_owner_key_file_container}
            service_claim_file: ${service_a_claim_file}
            service_publish_lease_file: ${service_a_publish_lease_file}
      tenant-b:
        membership_capability_file: ${tenant_b_namespace_cap_file}
        services:
          myapi:
            service_id: ${service_b_id}
            service_seed: ${service_b_seed}
            service_owner_key_file: ${service_b_owner_key_file_container}
            service_claim_file: ${service_b_claim_file}
            service_publish_lease_file: ${service_b_publish_lease_file}
YAML

cat > "${config_dir}/client.yaml" <<YAML
role: service
node:
  seed: connect-client-seed
  p2p_listen: /ip4/127.0.0.1/tcp/0
network:
  private_key_file: ${host_swarm_key_file}
  relay_peers:
    - ${host_relay_addr}
edge:
  admin_listen: 127.0.0.1:8444
current_cluster: home
current_namespace: tenant-a
clusters:
  home:
    cluster_id: ${cluster_id}
    authority_public_key: ${authority_public_key}
    authority_private_key_file: ${authority_key_file}
    membership_capability_file: ${tenant_a_namespace_cap_file}
YAML

cat > "$config_path" <<YAML
role: service
current_overlay: public
current_cluster: home
current_namespace: tenant-a
network:
  private_key_file: ${host_swarm_key_file}
  bootstrap_peers:
    - ${host_relay_addr}
  relay_peers:
    - ${host_relay_addr}
clusters:
  home:
    cluster_id: ${cluster_id}
    authority_public_key: ${authority_public_key}
    authority_private_key_file: ${host_authority_key_file}
    membership_capability_file: ${host_tenant_a_cluster_cap_file}
    namespaces:
      tenant-a:
        membership_capability_file: ${host_tenant_a_namespace_cap_file}
        services:
          myapi:
            service_id: ${service_a_id}
            service_seed: ${service_a_seed}
            service_owner_key_file: ${host_service_a_owner_key_file}
            service_claim_file: ${host_service_a_claim_file}
            service_publish_lease_file: ${host_service_a_publish_lease_file}
      tenant-b:
        membership_capability_file: ${host_tenant_b_namespace_cap_file}
        services:
          myapi:
            service_id: ${service_b_id}
            service_seed: ${service_b_seed}
            service_owner_key_file: ${host_service_b_owner_key_file}
            service_claim_file: ${host_service_b_claim_file}
            service_publish_lease_file: ${host_service_b_publish_lease_file}
YAML

find "$config_dir" -type d -exec chmod 755 {} +
find "$config_dir" -type f -exec chmod 644 {} +
chmod 644 "$host_authority_key_file" "$host_tenant_a_cluster_cap_file" "$host_tenant_a_namespace_cap_file" "$host_tenant_b_cluster_cap_file" "$host_tenant_b_namespace_cap_file" "$host_service_a_owner_key_file" "$host_service_a_claim_file" "$host_service_b_owner_key_file" "$host_service_b_claim_file" "$host_swarm_key_file"

if [[ "${SMOKE_FORCE_BUILD:-0}" == "1" ]]; then
  echo "[smoke-tubo-workflow] forcing image rebuild"
  compose_build_serial
fi

echo "[smoke-tubo-workflow] docker compose up -d"
$COMPOSE up -d --remove-orphans

echo "[smoke-tubo-workflow] waiting for health endpoints"
wait_http_ok "http://127.0.0.1:8091/healthz"
wait_http_ok "http://127.0.0.1:8093/healthz"
wait_http_ok "http://127.0.0.1:8443/healthz"
wait_http_ok "http://127.0.0.1:8444/healthz"
wait_http_ok "http://127.0.0.1:8092/healthz"

echo "[smoke-tubo-workflow] waiting for tenant-a discovery cache and route"
for i in $(seq 1 90); do
  services_json="$(curl -fsS http://127.0.0.1:8444/services || true)"
  routes_json="$(curl -fsS http://127.0.0.1:8444/routes || true)"
  if echo "$services_json" | grep -Eq '"count"[[:space:]]*:[[:space:]]*1' && echo "$routes_json" | grep -q '"hostname":"myapi"'; then
    break
  fi
  if [[ "$i" == "90" ]]; then
    echo "[smoke-tubo-workflow] discovery not ready"
    echo "services: $services_json"
    echo "routes:   $routes_json"
    exit 1
  fi
  sleep 1
done

if ! echo "$services_json" | grep -q 'myapi'; then
  echo "[smoke-tubo-workflow] expected tenant-a service in edge cache"
  echo "$services_json"
  exit 1
fi

if ! tubo get clusters --config "$config_path" >/dev/null; then
  echo "[smoke-tubo-workflow] get clusters failed"
  exit 1
fi
if ! tubo get namespaces --config "$config_path" >/dev/null; then
  echo "[smoke-tubo-workflow] get namespaces failed"
  exit 1
fi
if ! tubo describe cluster/home --config "$config_path" >/dev/null; then
  echo "[smoke-tubo-workflow] describe cluster failed"
  exit 1
fi
if ! tubo describe namespace/tenant-a --config "$config_path" >/dev/null; then
  echo "[smoke-tubo-workflow] describe namespace failed"
  exit 1
fi

share_output="$(tubo share service/myapi --config "$config_path" --cluster home --namespace tenant-a --expires 2h)"
share_token="$(printf '%s\n' "$share_output" | grep -o 'tubo-share-invite-v1\.[^[:space:]]*' | head -n1)"
if [[ -z "$share_token" ]]; then
  echo "[smoke-tubo-workflow] failed to extract share invite token"
  echo "$share_output"
  exit 1
fi

connect_port="$(free_tcp_port)"
bad_connect_port="$(free_tcp_port)"
connect_resp="$(mktemp)"
connect_output="$(tubo connect --token "$share_token" --config "${config_dir}/client.yaml" --local 127.0.0.1:${connect_port} -d)"
connect_process_ref="$(extract_field "id" "$connect_output")"
connect_log_path="$(extract_field "logs" "$connect_output")"
if [[ -z "$connect_process_ref" || -z "$connect_log_path" ]]; then
  echo "[smoke-tubo-workflow] failed to parse detached connect process metadata"
  echo "$connect_output"
  exit 1
fi
if ! tubo ps --kind connect | grep -q "${connect_process_ref#process/}"; then
  echo "[smoke-tubo-workflow] detached connect process not visible in tubo ps"
  tubo ps --all --kind connect || true
  exit 1
fi

for i in $(seq 1 60); do
  if curl -fsS -o "$connect_resp" -H 'Content-Type: text/plain' --data 'hello-service-share' "http://127.0.0.1:${connect_port}/v1/dummy?from=service-share" >/dev/null 2>&1; then
    break
  fi
  sleep 1
  if [[ "$i" == "60" ]]; then
    echo "[smoke-tubo-workflow] connect token tunnel did not become ready"
    cat "$connect_log_path"
    exit 1
  fi
done

if ! grep -q '"path":"/v1/dummy"' "$connect_resp"; then
  echo "[smoke-tubo-workflow] connect response missing expected path"
  cat "$connect_resp"
  exit 1
fi

if tubo connect --token "$share_token" --namespace tenant-b --config "${config_dir}/client.yaml" --local 127.0.0.1:${bad_connect_port} >/tmp/tubo-connect-bad.log 2>&1; then
  echo "[smoke-tubo-workflow] mismatched namespace unexpectedly succeeded"
  cat /tmp/tubo-connect-bad.log
  exit 1
fi

if ! tubo get service/myapi --config "$config_path" >/dev/null; then
  echo "[smoke-tubo-workflow] get service/myapi failed"
  exit 1
fi

payload="hello-tubo-workflow"
payload_b64="$(printf '%s' "$payload" | base64)"
resp_body="$(mktemp)"
http_code="$(curl -sS -o "$resp_body" -w "%{http_code}" -H "Host: myapi" -H "Content-Type: text/plain" --data "$payload" "http://127.0.0.1:8443/v1/dummy?from=tubo-workflow")"
if [[ "$http_code" != "200" ]]; then
  echo "[smoke-tubo-workflow] expected HTTP 200, got $http_code"
  cat "$resp_body"
  exit 1
fi
if ! grep -q '"method":"POST"' "$resp_body"; then
  echo "[smoke-tubo-workflow] missing method in edge response"
  cat "$resp_body"
  exit 1
fi
if ! grep -q '"raw_query":"from=tubo-workflow"' "$resp_body"; then
  echo "[smoke-tubo-workflow] missing query in edge response"
  cat "$resp_body"
  exit 1
fi
if ! grep -q "\"body_b64\":\"$payload_b64\"" "$resp_body"; then
  echo "[smoke-tubo-workflow] missing body payload in edge response"
  cat "$resp_body"
  exit 1
fi

cleanup
trap - EXIT INT TERM
assert_no_workflow_connect_leaks

echo "[smoke-tubo-workflow] PASS: cluster/namespace/service workflow works and namespace isolation is preserved"
