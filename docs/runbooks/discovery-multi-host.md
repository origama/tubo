# Discovery and Multi-Host Runbook

This runbook covers two distinct layers:

1. the current as-is state of the project, which now requires Discovery V3 with namespace discovery entries at the cluster/namespace level;
2. the recommended operational target for private NAT/NAT deployments (LM Studio on a laptop + Hermes/edge on a remote host).

For operational startup and secure P2P tunneling across 2+ services, use this as the primary reference:

- `docs/runbooks/OPERABILITY.md`

## 1) Discovery: current state (as-is)

### 1.1 Publication (service)

`tubo attach` today:

1. creates a libp2p host (`p2p.NewHostWithSeedAndPSK`);
2. publishes a signed and encrypted `AnnouncementV3` on the namespace V3 topic derived from the namespace discovery entry;
3. includes display name (`ServiceName`), `ServiceID`, service public key, `Addresses`, membership capability, and a valid `PublishLease` (with legacy `ServiceClaim` kept only for compatibility);
4. starts a heartbeat (`HEARTBEAT_INTERVAL`, default `15s`) that republishes the same announcement;
5. connects to the bootstrap peers (`BOOTSTRAP_PEERS`) and retries (`BOOTSTRAP_RETRY_INTERVAL`, default `5s`);
6. if configured, enables static AutoRelay toward `RELAY_PEERS` (`ENABLE_AUTORELAY`, `ENABLE_HOLE_PUNCHING`, `FORCE_REACHABILITY_PRIVATE`).

### 1.2 Subscription and validation (edge)

`tubo gateway` today:

1. creates a libp2p host;
2. joins the namespace Discovery V3 topic (current + non-expired previous when configured);
3. uses `PubSubSubscriber` to:
   - deserialize the announcement;
   - verify topic/cluster/namespace;
   - verify the namespace membership capability and replay nonce;
   - verify that `service_id` matches the service public key;
   - require and validate a `PublishLease` valid for `service_id`, peer, namespace/scope, and authority;
   - update the discovery cache keyed primarily by `service_id`.
4. if configured, connects to bootstrap peers (`BOOTSTRAP_PEERS`) and retries (`BOOTSTRAP_RETRY_INTERVAL`, default `5s`).

### 1.3 Cache and auto-routing

- The cache is keyed primarily by `service_id`; `serviceName`/display name remains a compatibility index and is not unique (`internal/discovery/cache.go`).
- Relays and edges update the cache through validated Discovery V3; they accept `announce_service_v3` on the query protocol only after verifying the signed `AnnouncementV3` against the current discovery scope, and the cached TTL is capped by the earliest relevant authorization expiry.
- Relays can keep a query/sync cache to support remote `get services`.
- Namespace-scoped services must arrive through validated Discovery V3; legacy `announce_service` DTOs without signed Discovery V3 authorization are rejected at the receiver.
- Namespace-scoped `get services` / `get service/...` now require valid membership or an accepted membership grant proof; if that proof is missing or expired, Tubo returns an authorization error instead of a blank namespace.
- The effective TTL of V2 announcements is bounded by the announcement TTL and the expiry of the embedded `PublishLease`/claim.
- On `added`, the gateway creates an auto-route:
  - `hostname = serviceName`
  - `pathPrefix = "/"`
- On `removed` (expiry), it removes the route.

So an HTTP request with `Host: <serviceName>` is forwarded to the discovered peer.

### 1.4 Important current limitations

1. Duplicate display names are accepted as separate records when the `service_id` differs; legacy HTTP routes based on hostname remain ambiguous if two services in the same scope use the same display name.
2. Relay query caches propagate `service_id` when available and do not replace Discovery V3 validation on edges.
3. If the announced addresses are not reachable, direct dialing fails.
4. Discovery V3 uses a real namespace discovery entry for topic derivation and payload protection; it no longer derives secrecy only from public `cluster_id` + `namespace_id`. This improves private-namespace metadata protection, but it still does not hide topic existence, timing, or message-size metadata from peers able to observe PubSub traffic. See `../reference/discovery-v3-threat-model.md`.
5. Hole punching/AutoNAT are still not complete in the project.
6. Private swarm PSK is supported through env (`LIBP2P_PRIVATE_NETWORK_KEY` or `LIBP2P_PRIVATE_NETWORK_KEY_B64`) on `edge`, `service`, `bridge`, and `relay`.
7. `LIBP2P_ALLOWED_PEERS` + connection gating are implemented in the `relay`, but not yet enforced end-to-end across all binaries.
8. The old swarm discovery `"/discovery/v1.0"` is no longer supported.

## 2) Operational target for private NAT/NAT deployment

### 2.1 Mandatory controlled public node

For deployments with nodes potentially behind NAT, there must be at least one stable public node managed by us. With Discovery V3 the public node still serves as bootstrap/relay transport, not as a namespace discovery authority.

Minimum requirements:

- static public IP or stable DNS;
- stable PeerID;
- open libp2p TCP port (for example `4001/tcp`);
- bootstrap peer for the network;
- relay circuit v2;
- the same private network config as the other peers.

Do not use third-party bootstrap peers or public relays for private traffic.

Typical role:

```text
public-node:
- bootstrap peer
- circuit relay v2
- optional AutoNAT service
- optional edge HTTP ingress
```

### 2.2 Bootstrap vs relay separation

- `bootstrap`: join the network and find peers (control plane).
- `relay`: carry traffic when direct dialing does not work (data plane).
- `hole punching`: optimizes toward a direct path when possible, but relay remains the fallback.

Operational rule:

1. our public bootstrap node = required for the control plane;
2. our public relay node = required for robust NAT/NAT data plane.

## 3) Private libp2p network (PSK) + peer authorization

### 3.1 Private swarm PSK (target)

Desired configuration:

- `LIBP2P_PRIVATE_NETWORK_KEY=/etc/hermes-p2p/swarm.key`
- or `LIBP2P_PRIVATE_NETWORK_KEY_B64=<secret>`

When present, the libp2p host must be created with private network (`libp2p.PrivateNetwork(psk)`).

Key policy:

- strong entropy;
- distribute only to trusted nodes;
- mount as a secret;
- never commit to the repository;
- rotate on compromise.

### 3.2 Peer allowlist (connection-level: implemented on relay/edge/service/bridge)

Desired configuration:

- `LIBP2P_ALLOWED_PEERS=<EDGE_PEER_ID>,<SERVICE_AGENT_PEER_ID>,<RELAY_PEER_ID>,<HERMES_PEER_ID>`

Required behavior:

1. reject inbound traffic from non-allowlisted PeerIDs;
2. reject outbound connections to non-allowlisted PeerIDs;
3. reject discovery announcements signed by non-allowlisted PeerIDs;
4. reject `ServiceName -> PeerID` mappings that are not expected.

Current implementation:

- `ConnectionGater` at the connection layer;
- `LIBP2P_ALLOWED_PEERS` parser and connection enforcement on `relay`, `edge`, `service`, and `bridge`.

Still needed:

- application-level checks in discovery handlers and stream handlers on gateway/agent;
- `ServiceName -> PeerID` binding beyond the simple connection gate.

### 3.3 ServiceName -> PeerID binding (target)

Example config:

- `SERVICE_AUTHZ_lmstudio=<SERVICE_AGENT_PEER_ID>`
- `SERVICE_AUTHZ_hermes=<HERMES_PEER_ID>`

Or a unified format:

- `DISCOVERY_SERVICE_ALLOWLIST=lmstudio:<SERVICE_AGENT_PEER_ID>,hermes:<HERMES_PEER_ID>`

Accept the announcement only if:

1. `Announcement.PeerID == sender peer`;
2. the signature is valid;
3. the `PeerID` is allowlisted;
4. the `ServiceName` is authorized for that `PeerID`.

## 4) Isolated discovery (no public discovery)

For this private deployment:

1. do not use the public DHT;
2. do not use random bootstrap peers;
3. do not use external public relays;
4. use only opaque Discovery V3 topics derived from namespace discovery entries;
5. configure the cluster authority peer explicitly for private clusters (persisted as `clusters.<name>.discovery_query_peers` by `tubo start cluster/<name>` on the authority node); `tubo get services` now fails clearly if that peer is missing instead of falling back to the public relay;
6. continue discovery with signed announcements and verified capabilities.

## 5) Private relay, AutoRelay, and NAT reachability

### 5.1 Recommended runtime config (target)

- `BOOTSTRAP_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>`
- `RELAY_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>`
- `ENABLE_RELAY=true`
- `ENABLE_RELAY_SERVICE=true|false`
- `ENABLE_AUTORELAY=true`
- `ENABLE_HOLE_PUNCHING=true`
- `FORCE_REACHABILITY_PRIVATE=true`

### 5.2 Node roles

Public node:

- `ENABLE_RELAY_SERVICE=true`
- `ENABLE_AUTONAT_SERVICE=true`

NATed nodes:

- `ENABLE_RELAY=true`
- `ENABLE_AUTORELAY=true`
- `ENABLE_HOLE_PUNCHING=true`
- `FORCE_REACHABILITY_PRIVATE=true`

### 5.3 Static relays (target)

For private environments use configured static relays, not generic relay discovery.

Example:

- `RELAY_PEERS=/ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>`

## 6) Reachability diagnostics (target)

Suggested endpoints:

- `GET /p2p/status`
- `GET /p2p/peers`
- `GET /p2p/relays`
- `GET /p2p/reachability`

Minimum useful output:

```json
{
  "peer_id": "...",
  "listen_addrs": [],
  "observed_addrs": [],
  "reachability": "private|public|unknown",
  "connected_peers": [],
  "relay_peers": [],
  "using_private_network": true,
  "allowed_peers_count": 3
}
```

## 7) HTTP 502 error taxonomy (target)

When the gateway cannot forward to a discovered service, distinguish at least:

1. `discovery_missing`
2. `peer_not_allowed`
3. `peer_not_connected`
4. `dial_failed`
5. `stream_open_failed`
6. `relay_unavailable`
7. `service_expired`
8. `target_unreachable_from_agent`

Include at least these fields in a 502 log entry:

- `serviceName`
- target `PeerID`
- known addresses
- relay addresses
- connection type (`direct|relayed|none`)
- last announcement timestamp
- last dial error

## 8) NAT/NAT private runbook (LM Studio + Hermes)

Topology:

```text
                    Internet
                       |
              +----------------+
              | public relay   |
              | stable IP      |
              +----------------+
                /            \
               /              \
      +--------------+   +--------------+
      | laptop       |   | remote host  |
      | LM Studio    |   | Hermes/edge  |
      | service      |   | gateway      |
      +--------------+   +--------------+
```

Recommended flow:

1. generate a PSK;
2. start the public relay node;
3. start the edge/gateway on the remote host;
4. start the service on the laptop;
5. ensure service announcements advertise relay-aware addresses;
6. verify discovery on the edge;
7. verify the request path goes through the relay.

## 9) Complete example config (target)

### Relay

```yaml
network:
  private_key_file: /etc/hermes-p2p/swarm.key

p2p:
  listen: /ip4/0.0.0.0/tcp/4001
  relay_service: true
  autonat_service: true
  allowed_peers:
    - <EDGE_PEER_ID>
    - <SERVICE_AGENT_PEER_ID>
```

### Edge

```yaml
network:
  private_key_file: /etc/hermes-p2p/swarm.key
  bootstrap_peers:
    - /ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>
  relay_peers:
    - /ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>
  allowed_peers:
    - <SERVICE_AGENT_PEER_ID>
    - <PUBLIC_NODE_PEER_ID>

edge:
  listen: :8443
  admin_listen: 127.0.0.1:8444
  force_direct_paths: false
  discovery_topic: opaque-cluster-namespace-topic
```

### Service

```yaml
network:
  private_key_file: /etc/hermes-p2p/swarm.key
  bootstrap_peers:
    - /ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>
  relay_peers:
    - /ip4/<PUBLIC_NODE_IP>/tcp/4001/p2p/<PUBLIC_NODE_PEER_ID>
  allowed_peers:
    - <PUBLIC_NODE_PEER_ID>
    - <EDGE_PEER_ID>

service:
  name: lmstudio
  target: http://127.0.0.1:1234
  listen: /ip4/0.0.0.0/tcp/40123
  force_reachability: private
```

## 10) Test checklist (target)

1. service announcement appears in discovery;
2. announcement is signed and verified;
3. invalid signatures are rejected;
4. invalid PeerIDs are rejected by the connection gate;
5. relay path is used when direct dialing is unavailable;
6. direct path is preferred when it is actually reachable;
7. `Host=lmstudio` route is created after valid discovery;
8. `Host=lmstudio` route is removed after expiry;
9. no disallowed peer can publish/forward traffic;
10. no node uses the public DHT or external relays.

## 11) Security note

A private swarm with PSK isolates the libp2p network from external peers that do not possess the key, but it does not replace application-layer authorization.

For this reason the deployment should use multiple layers:

- PSK for transport-level network isolation;
- PeerID allowlist for connection-level control;
- signed discovery announcements for discovery integrity;
- application authorization for who may publish or connect.

Important limit:

- PSK is a shared secret, not an identity system;
- if the PSK leaks, the overlay is exposed.

## 12) LM Studio + Hermes scenario: minimal commands (as-is, today)

This section remains useful until the target features above are all implemented.

### Relay

```bash
NODE_SEED=public-relay-seed \
P2P_LISTEN=/ip4/0.0.0.0/tcp/4001 \
RELAY_HEALTH_LISTEN=127.0.0.1:8092 \
ENABLE_RELAY_SERVICE=true \
ENABLE_AUTONAT_SERVICE=true \
ENABLE_DISCOVERY_PUBSUB=true \
FORCE_REACHABILITY_PUBLIC=true \
PRINT_RUN_COMMANDS=true \
LIBP2P_PRIVATE_NETWORK_KEY=/etc/p2p/swarm.key \
go run ./cmd/tubo relay
```

### Edge

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

### Service

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
