# Tubo CLI UX v2

This document collects the proposed design for a new `tubo` CLI UX, oriented more toward user intent and less toward internal implementation roles.

The goal is to keep `tubo` as a single binary, daemonless by default, while making the day-to-day flow simpler for:

- publishing local services into the swarm;
- consuming remote services as local endpoints;
- starting gateways and relays;
- managing long-running processes without a central daemon;
- querying published swarm resources with a consistent grammar.

This proposal does not immediately remove the current commands. It reclassifies them as an advanced/compatibility layer, on top of which a more direct UX can be introduced.

---

## Current implementation status

Already implemented in the current CLI:

- `attach`, `connect`, `gateway`, `relay`;
- `join`;
- `-d` / `--detach` with XDG-style local state;
- `ps`, `get processes`, `logs`, `stop`, `rm --stale`, `describe process/...`, `inspect process/...`;
- `get services`, `get service/<name>`, `describe service/<name>`, `inspect service/<name> --json`, `watch services`;
- implicit local init for `attach`, `gateway`, and `relay`.

Still out of scope / future in this document:

- `get agents`, `get peers`;
- `watch events`;
- optional systemd/launchd integration.

---

## Goals

The new UX should make it possible to:

- publish a local endpoint into the swarm with an intuitive command;
- open a local tunnel to a remote service;
- start a generic HTTP gateway for services in the swarm;
- start a relay/bootstrap node;
- easily configure a host to use an existing swarm;
- leave long-running processes in the foreground by default;
- detach processes to the background with `-d` / `--detach` in a Podman-like style;
- inspect detached local processes with `ps`, `logs`, `stop`, `inspect`;
- query swarm resources with a kubectl-like grammar: `get`, `describe`, `inspect`, `watch`;
- reduce the need to manually write, render, and distribute topology/config files for the common case.

---

## Core principle

`Daemonless` does not mean that no long-running processes exist.

It means there is no mandatory central daemon, like `dockerd`, that must always be running in order to use the CLI.

Instead:

- `tubo attach` starts a process that must stay alive to publish a service;
- `tubo connect` starts a process that must stay alive to keep a local listener open;
- `tubo gateway` starts an HTTP gateway process;
- `tubo relay` starts a relay process.

By default these processes stay in the foreground. With `-d`, they are started in the background and managed through local state.

This is closer to Podman than Docker.

---

## New mental model

```text
attach    = publish a local endpoint into the swarm
connect   = open a local tunnel toward a swarm service
gateway   = expose an HTTP gateway to swarm services
relay     = start a relay/bootstrap node
join      = configure this host for an existing swarm
init      = create a new local / local-swarm configuration

get       = list or fetch resources
describe  = show human-readable resource details
inspect   = show technical/raw resource details
watch     = observe services

ps        = practical alias for local detached processes
logs      = show logs for detached processes
stop      = stop detached local processes
rm        = remove local state/logs for terminated processes
```

The word `mesh` remains useful as an architectural concept, but it does not necessarily need to be a primary CLI namespace.

---

## kubectl-like grammar

For read/inspect operations, the UX borrows the `kubectl` style:

```bash
tubo get services
tubo get service/lmstudio
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
tubo watch services
```

This separates verb and resource:

```text
get       = brief/tabular view
describe  = detailed human view
inspect   = technical/raw view, suitable for scripting/debugging
watch     = live change/event stream
```

Possible resources:

```text
service / services / svc
agent / agents
peer / peers
process / processes / proc
tunnel / tunnels
gateway / gateways
route / routes
relay / relays
reservation / reservations
circuit / circuits
event / events
capability / capabilities
```

Typed resource IDs:

```text
service/lmstudio
agent/reviewer.gpubox
peer/12D3...
process/attach-lmstudio
tunnel/lmstudio-51234
gateway/default
relay/default
route/lmstudio
```

---

## Mapping from current UX to proposed UX

| Current UX | Proposed UX | Notes |
|---|---|---|
| `tubo service run --name X --target URL` | `tubo attach URL --name X` | Publishes a local endpoint into the swarm. |
| `tubo bridge run ...` | `tubo connect X --local ADDR` | Opens a local tunnel to a remote service. Requires bridge/discovery evolution. |
| `tubo edge run --listen :8443` | `tubo gateway --listen :8443` | Current edge is an HTTP gateway, not a point-to-point connection. |
| `tubo relay run` | `tubo relay` | Shorter, more direct form. |
| manual topology/config to join a swarm | `tubo join --relay ... --swarm-key ...` | Imports configuration for an existing swarm. |
| `tubo mesh services` | `tubo get services` | `mesh` is not needed as a primary namespace. |
| `tubo mesh inspect X` | `tubo describe X` / `tubo inspect X` | `describe` for human output, `inspect` for technical output. |
| `tubo mesh watch` | `tubo watch services` | `watch` becomes a top-level verb. |
| foreground processes | default | No flag required. |
| background processes | `-d` / `--detach` | Implemented with local state, no central daemon. |

The existing commands can remain available as advanced/compatibility commands:

```bash
tubo service run
tubo edge run
tubo bridge run
tubo relay run
tubo config print
tubo config validate
tubo topology render
tubo topology commands
```

---

## Why `service` becomes `attach`

The current `service` role takes a `ServiceName` and a `Target`, creates a libp2p host, registers stream handlers, publishes announcements in the swarm, keeps heartbeats alive, and forwards requests to the HTTP target.

Semantically, the user is attaching a local service to the `tubo` network.

Current command:

```bash
tubo service run --name lmstudio --target http://127.0.0.1:1234
```

Proposed UX:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio
```

or:

```bash
tubo attach --target http://127.0.0.1:1234 --name lmstudio
```

---

## Why `edge` should not become `connect`

The current `edge` is not just connecting to a single service.

It acts as an HTTP gateway:

- it starts an HTTP server;
- it starts an admin API;
- it joins the swarm;
- it subscribes to discovery;
- it maintains a route table;
- it receives HTTP requests;
- it resolves `host/path -> service/peer`;
- it opens direct or relayed streams;
- it handles retries, stale routes, and relay recovery;
- it proxies requests/responses.

So `connect` would be misleading as a direct alias for `edge`.

The proposal is:

```bash
tubo gateway --listen :8443
```

for the current edge.

The `connect` command, instead, should be a new UX or an evolution of the bridge:

```bash
tubo connect lmstudio --local 127.0.0.1:51234
```

meaning: make the remote service `lmstudio` available locally.

---

## Foreground by default

Long-running commands should stay in the foreground unless the user explicitly asks for detaching.

Example:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio
```

Possible output:

```text
attaching service "lmstudio"
target: http://127.0.0.1:1234
peer: 12D3...
status: published
```

If the user wants background mode:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio -d
```

This is the simplest behavior for development, debugging, and demos.

---

## Detached process model

Without a central daemon, `tubo` can manage detached processes through local files.

Example local state:

```text
~/.local/share/tubo/processes/attach-lmstudio.json
~/.local/share/tubo/processes/connect-lmstudio-51234.json
~/.local/share/tubo/logs/attach-lmstudio.log
~/.local/share/tubo/run/attach-lmstudio.pid
```

`-d` would:

1. start the process in the background;
2. write a stable process ID;
3. keep metadata and logs on disk;
4. allow `ps`, `logs`, `stop`, `inspect`, `rm --stale`.

`tubo ps` reads these files, checks whether the PID is alive, and shows status.

This is much closer to Podman / `podman generate systemd` than to a central service manager.

---

## Local process management UX

Useful commands:

```bash
tubo ps
tubo get processes
tubo describe process/attach-lmstudio
tubo inspect process/attach-lmstudio --json
tubo logs process/connect-lmstudio-51234
tubo stop process/connect-lmstudio-51234
tubo rm --stale
```

A human-readable `describe` view should show:

- process ID;
- command;
- pid;
- status;
- service ID (if any);
- scope (if any);
- local listener;
- target;
- start time;
- log file path.

The technical/raw `inspect` view should be script-friendly and include the full stored metadata.

This distinction is central.

---

## Process resources vs swarm resources

The CLI should clearly separate:

- local processes running on this machine;
- services/resources published in the swarm.

For example:

- `tubo ps` can show `process/connect-lmstudio-51234`, i.e. the local process keeping a tunnel open;
- `tubo get services` can show `service/lmstudio`, i.e. the remote service published in the swarm.

---

## Discovery UX

`get services` cannot magically see the swarm from outside. It must participate in the network or use a local cache.

The intended behavior is:

1. If a local edge cache exists, use it.
2. If not, try a remote discovery query through a bootstrap/relay peer.
3. If that fails or is insufficient, start an ephemeral observer:
   - connect to the swarm for an explicit timeout;
   - collect at least one discovery heartbeat when possible;
   - then exit.

The output should always say clearly which mode is being used.

Useful flags:

```bash
tubo get services --config <path> --timeout 20s --live
tubo get services --cached-only --json
```

`config print` masks secrets and never prints the contents of `swarm.key`.

---

## LM Studio / Ollama examples

LM Studio published in the swarm:

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio -d
tubo get services
tubo describe service/lmstudio
```

Ollama published in the swarm:

```bash
tubo attach http://127.0.0.1:11434 --name ollama -d
tubo get services
tubo describe service/ollama
```

---

## Init commands

```bash
tubo init relay --out relay.yaml
tubo init edge --out edge.yaml
tubo init service --out service.yaml
tubo init bridge --out bridge.yaml
```

Existing files are not overwritten without `--force`.

---

## Example `swarm.key`

```bash
tubo keygen swarm --out swarm.key
chmod 600 swarm.key
```

The generated file uses the libp2p pnet format:

```text
/key/swarm/psk/1.0.0/
/base16/
<32 random bytes in hex>
```

---

## Service config

```yaml
role: service
node:
  seed: service-lmstudio-seed
  p2p_listen: /ip4/0.0.0.0/tcp/40123
network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
  relay_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
  autorelay: true
  hole_punching: true
  force_reachability: private
service:
  name: lmstudio
  target: http://192.168.1.28:1234
health_listen: 127.0.0.1:8091
heartbeat_interval: 5s
```

---

## Edge config

```yaml
role: edge
node:
  seed: edge-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers: [/ip4/1.2.3.4/tcp/4001/p2p/12D3...]
  relay_peers: [/ip4/1.2.3.4/tcp/4001/p2p/12D3...]
edge:
  listen: :8443
  admin_listen: 127.0.0.1:8444
  direct_stream_timeout: 750ms
```

---

## Relay config

```yaml
role: relay
node:
  seed: public-relay-seed
  p2p_listen: /ip4/0.0.0.0/tcp/4001
network:
  private_key_file: /etc/p2p/swarm.key
relay:
  public_addr: /ip4/1.2.3.4/tcp/4001
  health_listen: 127.0.0.1:8092
  enable_relay_service: true
  enable_autonat_service: true
  enable_discovery_pubsub: true
  force_reachability_public: true
```

---

## Bridge config

```yaml
role: bridge
node:
  seed: bridge-demo-seed
  p2p_listen: /ip4/127.0.0.1/tcp/0
network:
  private_key_file: /etc/p2p/swarm.key
bridge:
  listen: 127.0.0.1:18081
  service_seed: service-lmstudio-seed
  service_p2p_listen: /ip4/127.0.0.1/tcp/40123
```

---

## Resource config model (Phase 1)

The configuration supports a minimal resource model for overlay, cluster, and namespace, but runtime still reads `network:` as the operational source of truth.

```yaml
current_overlay: public
current_cluster: home
current_namespace: default

overlays:
  public:
    relays: []
    bootstrap_peers: []
    swarm_key_file: ""

clusters:
  home:
    cluster_id: ""
    authority_public_key: ""
    capabilities: []
    namespaces:
      default: {}

network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
  relay_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
```

`current_overlay` materializes overlay fields into `network:` when the file uses the new layout; the writers for `join` and signed bundles write both formats for compatibility. `tubo join overlay/public` is the explicit public join form; `tubo join overlay/manual --relay ... --swarm-key ...` is the explicit manual/legacy join form. When the current config carries a cluster with identity metadata (`cluster_id` + `authority_public_key` + membership grant/capability), runtime discovery uses an opaque V2 topic derived from `current_cluster/current_namespace` and validates topic/scope, membership capability, and replay nonce; configs without those metadata no longer support runtime discovery.

---

## Local resource CLI (Phase 2a)

With the new local model you can inspect, create, invite, and select overlay, cluster, and namespace entries already present in the config:

```bash
tubo get overlays
tubo get clusters
tubo get namespaces

tubo create cluster/home
tubo create namespace/observability
tubo create service/myapi

tubo share cluster/home --role member
tubo share cluster/home --role grant-requester --grant-peer /ip4/1.2.3.4/tcp/4001/p2p/12D3...
tubo share service/myapi --expires 1h
tubo join cluster/home --token <cluster-invite>

tubo describe overlay/public
tubo describe cluster/home
tubo describe namespace/default

tubo use overlay/public
tubo use cluster/home
tubo use namespace/default
```

Notes:

- `get overlays` and `get clusters` read only the local config.
- `get namespaces` uses the current `current_cluster`.
- `create cluster/...` generates a local authority keypair, writes a `cluster_id`, sets `authority_public_key`, creates the `default` namespace, saves a local membership capability without printing secrets, and initializes the namespace policy as `discovery: enabled` + `connect_policy: namespace_members`. In new collaborative configs, the creator capability also includes `connect`, so `connect <service>` by name works immediately in the same namespace.
- `create namespace/...` requires a valid `current_cluster`, adds the namespace to the current cluster, makes the new `current_namespace` explicit, materializes a signed membership capability for that namespace, and initializes the local policy as `discovery: enabled` + `connect_policy: namespace_members`. Here too the local creator capability includes `connect`; older namespaces are not widened automatically and `tubo doctor` now warns when the current discovery-enabled context still lacks `connect`.
- `create service/...` requires `current_cluster` and `current_namespace`, materializes a stable `service_owner_key_file` for the service identity, derives `service_id` from that key, signs a local `ServiceClaim`, and also saves a `service_publish_lease_file` when the node owns the local authority; Discovery V2 uses the lease as the primary authorization.
- `attach` in cluster/namespace mode automatically materializes a stable service identity if missing: `service_id` is derived from the service owner key stored in the local config (`0600`), while `service_seed` is generated once and stored in the local config (`0600`).
- before starting the runtime, `attach` resolves publish authorization: it uses an existing valid `PublishLease`, signs it locally if the node owns `authority_private_key_file`, or submits/polls a Publish Grant request if the service has `grant_service_peer`; an expired lease is treated as absent and therefore goes through the normal renewal/request path, and an expired local `ServiceClaim` is also treated as stale/renewable state rather than a fatal error. The `ServiceClaim` fallback remains local compatibility only. When available, it also prints a copyable `service_share_token` connect-only token for Bob (`tubo connect --token ...`). If publish authorization is valid but the token cannot yet be generated (for example because the remote endpoint is not ready), `attach` suggests rerunning `tubo share service/...`; the old `tubo grants request ... --poll` hint remains reserved for cases where the grant request is still actually pending.
- in the signed bundle public default (`tubo-public` / `home/default`), `attach` runs in **unlisted** mode: the service remains reachable via invite token and libp2p stream, but it does not start ambient publication/discovery and the output makes `visibility: unlisted` + `access: invite token required` explicit. In this scope, the token printed by `attach` must be self-contained: if there is still no relay-aware/remote-dialable endpoint, `attach` fails before printing an unusable invite.
- in collaborative namespaces with discovery enabled, `attach` also registers a libp2p `/tubo/grants/1.0` endpoint on the service’s peer and publishes a service-scoped `grant_service` in Discovery V2 with reachable peers (relay-aware when available, otherwise only actually dialable direct ones). `connect <service>` now uses this endpoint to obtain discovery-driven leases: `namespace_members` accepts both local membership capabilities with `connect` and imported cluster invites with `connect` permission; revoked invites can no longer obtain new leases.
- `share cluster/...` uses the local authority key to emit a signed invite, includes namespace/expiry/grant data, and prints a copyable `tubo join ...` command. `--role member` grants `subscribe,list,publish,connect`, `--role viewer` grants only `subscribe,list` (you can see the service but not open connect leases), while `--role grant-requester --grant-peer ...` emits an invite without direct publish rights but with metadata to request a Publish Grant.
- `share service/...` resolves the current or explicit cluster/namespace (`--cluster`/`--namespace`) and prints a copyable `tubo connect --token ...` command; if the local authority key is available it uses the local authority path, otherwise it can delegate minting to the cluster grant service when the local service owner holds a valid `PublishLease` with `share.mint` and a `grant_service_peer`. If the local `PublishLease` is missing or expired, `share service/...` does not require a new service name: it reuses the same local identity / same `service_id`, tries to renew or re-request publish authorization for that scope first, then continues with delegated minting if the renewal is approved; leases with invalid signature/scope/service/peer are still fatal errors and do not enter the renewal path. If you pass `service/<service_id>`, it uses exact lookup instead of the display name. The token includes cluster/namespace/service/authority metadata, including `service_kind`, but does not authorize generic listing and the new tokens no longer include a reusable embedded bearer `ConnectCapability`. When available it can also include a self-contained `service_endpoint` with relay-aware `/p2p-circuit` addresses, so invite-only flows do not depend on ambient listing; in the public default this endpoint is no longer optional, so `share service/...` fails clearly if it can only produce local/non-dialable addresses. If it also includes `grant_service` metadata, `connect --token` redeems it into a short-lived `ConnectAccessLease` and a `ConnectRefreshLease` bound to the bridge’s local key. The bridge renews the access lease before expiry. Share invites are now one-time at the grant endpoint: `one-time` means one successful lease/session redemption, not one HTTP request on the tunnel, and a denied redemption no longer falls back to legacy bearer grants. The local `share-invite-registry.json` remains only a UX guard rail; the authoritative decision happens on the server that redeems the invite. `share revoke <share-invite>` marks the `jti` as revoked/used in the local config, while `tubo revoke invite|session|service-access|publish ...` updates the issuer-side revocation store used by `grants serve`.
- `join cluster/... --token ...` and `join <cluster-invite>` verify the invite and store cluster metadata + grant in the local config without touching the runtime.
- `describe overlay/...`, `describe cluster/...`, and `describe namespace/...` show only local metadata and do not print secrets. `describe namespace/...` also includes the effective policy (`discovery`, `connect_policy`) of the current namespace; for the signed public bundle, `home/default` implicitly resolves to `discovery: disabled` + `connect_policy: invite_only` even when an older config did not yet have those fields explicitly. `describe service/...` also shows `Connect policy` and, when available, the protocol/peer of the `grant_service` published by the observed service.
- `use` updates only the local config file; it does not start or stop runtime processes.
- `--json` remains available for `get` and for the new local flows when useful.

---

## Publish grants

Authority nodes can start the grant protocol listener and review local requests. For the public bundle, `grants serve --public-auto-approve` uses the public cluster authority key and automatically approves publish requests for the simplified attach/connect flow:

```bash
tubo grants serve --cluster home --namespace default --public-auto-approve \
  --connect-access-ttl 10m --connect-refresh-ttl 48h
# prints direct addr plus relay addr when relay peers are configured
tubo grants pending
tubo grants describe gr_123
tubo grants approve gr_123 --ttl 168h
tubo grants deny gr_123

tubo grants request service/myapi --peer /ip4/1.2.3.4/tcp/4001/p2p/12D3...
tubo grants request service/myapi --poll
# if joined with a grant-requester invite, --peer can be omitted
tubo grants history
```

The listener uses `/tubo/grants/1.0`, stores pending requests under the local Tubo data dir, derives the requester PeerID from the libp2p stream, and never signs publication material without approval. `grants serve` uses the configured overlay bootstrap/relay peers, enables AutoRelay/hole punching from config, maintains relay reservations, and prints relay-aware `/p2p-circuit` addresses for signed invites; it does not publish itself in Discovery V2. Approval is explicit and signs a service-scoped `PublishLease`/`ServiceClaim` with the local authority key plus an optional connect-only `service_share_token`. The grant server also reads `--revocations` (default local data dir) to reject revoked invite redemption, revoked session refresh, stale service-access epochs, and publish-revoked services. The grant server bounds pending requests globally/per requester/per `service_id`, clamps share TTL, and rejects active `service_id` collisions for a different service peer; duplicate display names are allowed. `grants history` now prints `SCOPE` and `SERVICE_ID`, sorts by `service_id`, and prefixes the output with the local store path so the source is explicit. `attach` also uses the saved `grant_service_peer`/`grant_request_id` metadata to submit or poll before service publication; when a token is available it is printed before the process detaches or enters the foreground wait; denied, expired, revoked, or still-pending grants stop publication.

---

## Multi-node setup

For multi-node setups, use one of these two paths:

- canonical local flow: `tubo join`, `tubo create cluster/...`, `tubo create namespace/...`, `tubo create service/...`, `tubo share ...`, `tubo attach`, `tubo connect --token ...`
- YAML per role: `tubo init relay|edge|service|bridge` and then edit the generated files with relay peers, swarm key, and the required cluster/namespace metadata

---

## Key decisions

1. Keep the core model daemonless.
2. Add `tubo generate systemd` first.
3. Keep `-d` as lightweight detached mode.
4. Do not implement `--install` first.
5. Keep `launchd` for a later phase.
6. `process/...` remains the canonical ID for installed services too.
7. Local `ps/logs/stop/inspect` read locally registered Tubo runtimes; for supervised services without Tubo-owned log files, use the OS-native tools.
8. Optional supervisor integration is a future enhancement, not part of the core.

---

## Open questions

- How far should automatic local generation go?
- Should `connect` eventually become a true persistent tunnel UX, or remain a bridge evolution?
- How much should the old role commands stay visible in the primary documentation?
