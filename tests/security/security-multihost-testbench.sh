#!/usr/bin/env bash
set -u -o pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
COMPOSE_FILE="$ROOT_DIR/tests/security/docker-compose.security-multihost.yml"
COMPOSE="${COMPOSE_CMD:-docker compose -f "$COMPOSE_FILE"}"
ARTIFACT_DIR="$ROOT_DIR/tests/security/artifacts"
REPORT="$ARTIFACT_DIR/security-multihost-report.txt"
KEEP_STACK="${KEEP_STACK:-0}"

mkdir -p "$ARTIFACT_DIR"
: > "$REPORT"

log() {
  printf '[security-multihost] %s\n' "$*" | tee -a "$REPORT"
}

record() {
  local id="$1"
  local severity="$2"
  local status="$3"
  local detail="$4"
  printf '%s | %s | %s | %s\n' "$id" "$severity" "$status" "$detail" | tee -a "$REPORT"
}

cleanup() {
  if [[ "$KEEP_STACK" != "1" ]]; then
    # shellcheck disable=SC2086
    $COMPOSE --profile rogue down -v --remove-orphans >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

compose_exec_curl() {
  # shellcheck disable=SC2086
  $COMPOSE exec -T curl-client curl -fsS "$@"
}

wait_until() {
  local label="$1"
  local timeout="$2"
  local cmd="$3"
  local start now
  start="$(date +%s)"
  while true; do
    if eval "$cmd" >/dev/null 2>&1; then
      log "ready: $label"
      return 0
    fi
    now="$(date +%s)"
    if (( now - start >= timeout )); then
      log "timeout waiting for: $label"
      return 1
    fi
    sleep 2
  done
}

request_host() {
  local host="$1"
  local path="${2:-/echo}"
  compose_exec_curl -H "Host: $host" "http://edge:8080$path"
}

log "starting security multihost testbench"
log "compose file: $COMPOSE_FILE"

log "building images serially to avoid parallel tag races"
docker build -t p2p-api-tunnel-tubo:security-multihost -f "$ROOT_DIR/deploy/Dockerfile.tubo" "$ROOT_DIR" >>"$REPORT" 2>&1 || { log "tubo image build failed"; exit 1; }
docker build -t p2p-api-tunnel-dummy-api-server:security-multihost -f "$ROOT_DIR/deploy/Dockerfile.dummy-api-server" "$ROOT_DIR" >>"$REPORT" 2>&1 || { log "dummy-api-server image build failed"; exit 1; }
# shellcheck disable=SC2086
$COMPOSE up -d --no-build relay edge curl-client dummy-api-server-one service-one dummy-api-server-two service-two dummy-api-server-attacker attacker-service >>"$REPORT" 2>&1 || { log "compose up failed"; exit 1; }

wait_until "edge admin" 90 'compose_exec_curl http://edge:8444/healthz' || exit 1
wait_until "routes for svc-one, svc-two and attacker" 120 'routes="$(compose_exec_curl http://edge:8444/routes)" && echo "$routes" | grep -q "\"hostname\":\"svc-one\"" && echo "$routes" | grep -q "\"hostname\":\"svc-two\"" && echo "$routes" | grep -q "\"hostname\":\"attacker\""' || exit 1

log "baseline routing checks"
svc_one_response="$(request_host svc-one /v1/dummy?baseline=1 || true)"
svc_two_response="$(request_host svc-two /v1/dummy?baseline=1 || true)"
if [[ "$svc_one_response" == *"svc-one-legit"* && "$svc_two_response" == *"svc-two-legit"* ]]; then
  record "SEC-BENCH-000" "info" "pass" "baseline multi-service routing works"
else
  record "SEC-BENCH-000" "info" "fail" "baseline routing failed; svc-one=$svc_one_response svc-two=$svc_two_response"
  exit 1
fi

log "SEC-001: unauthenticated admin API route injection"
add_route_status="$(compose_exec_curl -o /dev/null -w '%{http_code}' -X POST -d 'hostname=admin-hijack&path_prefix=/&service=attacker' http://edge:8444/add_route || true)"
hijack_response="$(request_host admin-hijack /v1/dummy?attack=admin_route || true)"
if [[ "$add_route_status" == "201" && "$hijack_response" == *"attacker-controlled"* ]]; then
  record "SEC-001" "high" "vulnerable" "edge admin API accepts unauthenticated POST /add_route and exposes attacker service under arbitrary Host"
else
  record "SEC-001" "high" "not-reproduced" "admin route injection did not succeed; status=$add_route_status response=$hijack_response"
fi

log "SEC-002: admin API exposed on host port"
host_admin_status="$(curl -fsS -o /dev/null -w '%{http_code}' http://127.0.0.1:18444/routes 2>/dev/null || true)"
if [[ "$host_admin_status" == "200" ]]; then
  record "SEC-002" "high" "vulnerable" "admin API is reachable from Docker host on 127.0.0.1:18444 without auth"
else
  record "SEC-002" "high" "not-reproduced" "host admin API was not reachable; status=$host_admin_status"
fi

log "SEC-003: duplicate service-name takeover by swarm insider"
# shellcheck disable=SC2086
$COMPOSE --profile rogue up -d rogue-svc-one >>"$REPORT" 2>&1 || record "SEC-003" "high" "setup-failed" "could not start rogue duplicate service"
rogue_observed="no"
for _ in $(seq 1 40); do
  response="$(request_host svc-one /v1/dummy?attack=duplicate_service || true)"
  if [[ "$response" == *"attacker-controlled"* ]]; then
    rogue_observed="yes"
    break
  fi
  sleep 2
done
if [[ "$rogue_observed" == "yes" ]]; then
  record "SEC-003" "high" "vulnerable" "a peer with the private swarm key can announce an existing service name and take over traffic for that service"
else
  record "SEC-003" "high" "not-reproduced" "svc-one did not route to rogue service during observation window"
fi

log "SEC-004: ingress has no client authentication"
anonymous_status="$(curl -fsS -o /dev/null -w '%{http_code}' -H 'Host: svc-two' http://127.0.0.1:18080/v1/dummy?anonymous=1 2>/dev/null || true)"
if [[ "$anonymous_status" == "200" ]]; then
  record "SEC-004" "medium" "vulnerable" "public edge ingress accepts unauthenticated HTTP requests for published services"
else
  record "SEC-004" "medium" "not-reproduced" "host ingress did not accept anonymous request; status=$anonymous_status"
fi

log "SEC-005: no enforced ServiceName -> PeerID binding"
routes_after="$(compose_exec_curl http://edge:8444/routes || true)"
if [[ "$routes_after" == *"svc-one"* ]]; then
  record "SEC-005" "high" "observed-design-gap" "routes and discovery are keyed by service name; cache keeps one mutable registration per name and does not enforce a pinned peer identity"
else
  record "SEC-005" "high" "inconclusive" "could not inspect routes after duplicate service test"
fi

log "report written to $REPORT"
log "done"
