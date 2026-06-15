# Operability Runbook

This document is the canonical operational reference for:

1. starting the components;
2. creating a working P2P tunnel;
3. creating a **secure** P2P tunnel (private swarm PSK) between two or more services.

## 1) Prerequisites

- Go 1.24+
- Docker + Docker Compose plugin
- network reachability between nodes (at least outbound from service nodes to the edge/bootstrap node)

## 2) Real runtime components

Recommended new UX: explicit intent-based `tubo` commands:

```bash
tubo relay --config relay.yaml
tubo gateway --config edge.yaml
tubo attach --config service.yaml
```

Available roles through `tubo`:

- `relay` (bootstrap + relay v2 + health endpoint); today it is only transport/bootstrap, not a discovery authority or catalog of record
- `gateway` (HTTP ingress + discovery consumer)
- `attach` (publisher + stream handler toward the origin HTTP service or raw TCP, depending on the target)
- in collaborative configurations, `gateway`/`attach`/observer select an opaque Discovery V3 topic derived from the namespace discovery entry, not from public cluster/namespace identifiers alone
- the old discovery swarm `/discovery/v1.0` has been removed: only namespace-scoped Discovery V3 remains for collaborative ambient discovery
- `bridge` is still available as client-side logic, but the historical runtime command `bridge run` is no longer supported

## 3) Local quick start (Docker Compose)

From the repository root:

```bash
docker compose up -d --build
```

Minimum checks:

```bash
curl -fsS http://127.0.0.1:8443/healthz
curl -fsS http://127.0.0.1:8444/healthz
curl -fsS http://127.0.0.1:8091/healthz
curl -fsS http://127.0.0.1:8092/healthz
curl -fsS http://127.0.0.1:8444/services
curl -fsS http://127.0.0.1:8444/routes
```

Recommended end-to-end test:

```bash
./tests/smoke-compose.sh
```

## 4) Secure P2P tunnel (private swarm PSK)

### 4.1 Generate the swarm key

Generate `swarm.key` (libp2p pnet format):

```bash
# recommended new method
tubo keygen swarm --out swarm.key
chmod 600 swarm.key

# equivalent manual form
KEY_HEX="$(openssl rand -hex 32)"
cat > swarm.key <<EOF_KEY
/key/swarm/psk/1.0.0/
/base16/
${KEY_HEX}
EOF_KEY
chmod 600 swarm.key
```

Distribute `swarm.key` **only** to trusted nodes. Do not commit it to the repository.

For complete YAML examples (relay, edge, service, bridge) and `tests/e2e/compose/tubo/compose.yml`, see [`reference/cli.md`](../reference/cli.md). In collaborative setups, the recommended local flow is: `tubo create cluster/...`, `tubo create namespace/...`, `tubo create service/...`, then `tubo use ...`, `tubo share service/...` and `tubo attach ...` / `tubo connect --token ...`. If the service target is `tcp://host:port`, `attach` publishes `service_kind=tcp`, the share token preserves that kind, and `connect` exposes a local `tcp://127.0.0.1:PORT` endpoint instead of the HTTP bridge: this is the canonical TLS passthrough path. When the namespace is discovery-enabled (`connect_policy: namespace_members`), you can also invite Bob with `tubo share cluster/home --namespace <ns> --role member`, have him run `tubo join cluster/home --token ...`, then use `tubo get services` and `tubo connect <service>` by name in the same scope; a `--role viewer` invite can list but cannot obtain a connect lease. The public bundle does not contain namespace discovery entries, and its default `home/default` namespace remains `discovery: disabled` + `connect_policy: invite_only`; clean-config public flows stay invite/token driven, not ambient-discovery driven. `tubo get secrets`, `tubo describe secret/...`, and `tubo rotate secret/...` are the intended local management surface for namespace discovery entries. The share token is now a `ShareInvite` bearer connect-only token: it does not authorize generic listing, it resolves the exact `service_id`, it does not replace membership capability, and on the server side it is one-time at the first successful lease/session redemption (not at the first HTTP request). `tubo share revoke <share-invite>` can block its local redemption, while `tubo revoke invite|session|service-access|publish ...` updates the issuer-side revocation store used by `grants serve`. The current model assumes a single active issuer per scope; HA/consensus plans for issuers are deferred to a future design. For `get services -A` or additional namespaces, make sure each namespace has its own `membership_capability_file` (or a broad capability with namespace `*`). Invite-based joins also save a signed grant that authorizes queries on the remote node.

Operational note for `tubo-public`: you can run multiple public relays without issues, but today it is recommended to have **only one authoritative Grant Service per public cluster/namespace** (for example `home/default`). Relays only handle reachability/transport; multiple grant services with independent stores can approve the same `service name` for different peers at the same time, creating split-brain and non-deterministic discovery results. In short: **multi-relay ok, single grant service per authority scope**.

Configuration precedence is:

```text
CLI flag > env var > config file > default > interactive prompt
```

### 4.2 Supported variables (implemented)

- `LIBP2P_PRIVATE_NETWORK_KEY=/path/to/swarm.key`
- `LIBP2P_PRIVATE_NETWORK_KEY_B64=<base64_32_bytes>`

If either is set, the libp2p host is created with a private network PSK.

## 5) Real 3-machine test (laptop NAT + edge NAT + public relay)

### 5.1 Start relay (stable public host)

```bash
NODE_SEED=public-relay-seed \
P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
RELAY_HEALTH_LISTEN=127.0.0.1:8092 \
ENABLE_RELAY_SERVICE=true \
ENABLE_AUTONAT_SERVICE=true \
ENABLE_DISCOVERY_PUBSUB=true \
FORCE_REACHABILITY_PUBLIC=true \
PRINT_RUN_COMMANDS=true \
RELAY_LIMIT_DATA_BYTES=0 \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
go run ./cmd/tubo relay
```

The relay prints in the logs:

1. its own `peer_id`;
2. the available libp2p addresses;
3. a `startup command hints` block with `BOOTSTRAP_PEERS` and `RELAY_PEERS` already filled in for `edge` and `service`.

If the public address is not inferred correctly, force it:

```bash
RELAY_PUBLIC_ADDR=/ip4/<RELAY_PUBLIC_IP>/tcp/4001
```

If `RELAY_PUBLIC_ADDR` does not include `/p2p/<PEER_ID>`, the relay automatically adds its own PeerID in the suggested commands.

`RELAY_LIMIT_DATA_BYTES=0` means no byte cap on the relayed circuit connection. Positive values cap cumulative bytes for the whole circuit, so small values can reset long raw TCP/TLS tunnels before an application request finishes.

Minimum firewall ports on the relay:

1. `tcp/4001` (required, bootstrap + relay circuit v2)
2. `tcp/8092` (optional, health check)
3. `tcp/22` (SSH management)

### 5.2 Start edge

For NAT/NAT, the edge must be able to use the public relay as its only static peer. It is not necessary to expose the edge as a bootstrap peer for services.

```bash
EDGE_LISTEN=:8443 \
EDGE_ADMIN_LISTEN=127.0.0.1:8444 \
EDGE_P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
EDGE_SEED=edge-seed \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
RELAY_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
go run ./cmd/tubo gateway
```

Retrieve the edge `peer_id` from the logs (`edge gateway peer_id=...`).

### 5.3 Start the service on the laptop (LM Studio)

For NAT/NAT, the service must use the public relay as both `BOOTSTRAP_PEERS` and `RELAY_PEERS`. Do not use the edge as a bootstrap peer if the edge is behind NAT.

```bash
SERVICE_NAME=lmstudio \
SERVICE_TARGET=http://192.168.1.28:1234 \
SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40123 \
NODE_SEED=service-lmstudio-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
RELAY_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
ENABLE_AUTORELAY=true \
ENABLE_HOLE_PUNCHING=true \
FORCE_REACHABILITY_PRIVATE=true \
HEARTBEAT_INTERVAL=5s \
go run ./cmd/tubo attach
```

### 5.4 Verify discovery and route on the edge node

```bash
curl -fsS http://127.0.0.1:8444/services
curl -fsS http://127.0.0.1:8444/routes
```

Expected: `count >= 1` and auto-created route for `lmstudio`.

### 5.5 Execute the real request from the client on the edge host

```bash
curl -sS \
  -H 'Host: lmstudio' \
  -H 'Content-Type: application/json' \
  -d '{"model":"google/gemma-4-e2b","system_prompt":"You answer only in rhymes.","input":"What is your favorite color?"}' \
  http://127.0.0.1:8443/api/v1/chat
```

Expected: `HTTP 200` and a JSON body returned by LM Studio.

Note: for TLS passthrough or other TCP protocols, use `SERVICE_TARGET=tcp://127.0.0.1:<PORT>` (or `tubo attach tcp://127.0.0.1:<PORT> --name ...`). In that case `connect` exposes a local raw TCP listener and you can verify the service with native TLS/TCP clients instead of the HTTP gateway.

### 5.6 Distributed smoke on Terraform/Linode (3 hosts, multi-region)

For a repeatable distributed cloud bench, there is also a Terraform stack + smoke harness:

- Terraform: `infra/terraform/linode-distributed/`
- doc: `docs/runbooks/LINODE_TERRAFORM_TESTBENCH.md`
- smoke: `./tests/smoke-terraform-linode.sh`

The topology uses:

- public `relay`
- NAT-like `edge` (SSH-only, ingress closed)
- NAT-like `service` (SSH-only, ingress closed)

Because edge and service are intentionally closed to inbound traffic, HTTP verification is executed from inside the edge host over SSH.

### 5.7 Distributed smoke with only 2 machines

If you only have 2 real machines available, the recommended operational compromise is:

- `edge` on machine A;
- `relay` on machine B (public, required);
- `service` + example service on the same machine B;
- `service` bound to loopback (`/ip4/127.0.0.1/tcp/40123`) with `force_reachability: private` to prevent public direct dialing.

This still produces a useful **distributed relay-first** bench, even though it is not a pure 3-host setup.

Details: `tests/distributed-two-host.md`

## 6) Add another service on the same tunnel

1. create a new `service` with a unique `SERVICE_NAME`;
2. use the same `LIBP2P_PRIVATE_NETWORK_KEY` as the swarm;
3. point `BOOTSTRAP_PEERS` to the public relay;
4. point `RELAY_PEERS` to the public relay and set `ENABLE_AUTORELAY=true`;
5. verify a route appears in `GET /routes`;
6. call the edge with `Host: <SERVICE_NAME>`.

Example additional service:

```bash
SERVICE_NAME=internal-api \
SERVICE_TARGET=http://127.0.0.1:9000 \
SERVICE_P2P_LISTEN=/ip4/0.0.0.0/tcp/40124 \
NODE_SEED=service-internal-api-seed \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
BOOTSTRAP_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
RELAY_PEERS=/ip4/<RELAY_PUBLIC_IP>/tcp/4001/p2p/<RELAY_PEER_ID> \
ENABLE_AUTORELAY=true \
ENABLE_HOLE_PUNCHING=true \
FORCE_REACHABILITY_PRIVATE=true \
HEARTBEAT_INTERVAL=5s \
go run ./cmd/tubo attach
```

## 7) Security state: implemented vs target

Implemented today:

- pubsub discovery;
- signed announcements;
- relayed traffic;
- private swarm PSK;
- per-service discovery and routing;
- reachability-aware diagnostics and recovery wakeups;
- `tubo top` live local traffic stats for registered processes;
- multi-host smoke tests.

Still target / in progress:

- stronger peer authorization end-to-end;
- full hole punching robustness;
- richer route introspection;
- more complete error taxonomy for 502 cases;
- policy-driven TLS passthrough and TCP/UDP generalization.

## 8) Troubleshooting

### `service` not discovered

1. check that `BOOTSTRAP_PEERS` and `RELAY_PEERS` are correct;
2. check that the service has heartbeated at least once;
3. verify the libp2p host is using the same PSK;
4. check the edge discovery topic matches the service scope;
5. inspect logs for invalid signatures or lease expiration.

### `curl` returns 502

Verify:

1. service is present in `GET /services`;
2. the route exists in `GET /routes`;
3. the service is still alive and heartbeating;
4. `BOOTSTRAP_PEERS` and `EDGE_PEER_ID` are correct;
5. all nodes use the same PSK (or no PSK in local dev).

### Useful expected logs

- relay: bootstrap/relay readiness and health check success
- edge: discovery add/remove events and route creation/removal
- service: heartbeat sent and upstream forwarding successful
- bridge/connect: network degraded/recovered notices and retry wakeups when reachability returns

### Hung GET or no-body request

If a `GET` or a request without a body hangs, make sure the client is using a version with a final empty body chunk: the service should see `service stream completed`, not only `service upstream request`.
