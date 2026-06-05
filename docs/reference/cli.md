# tubo CLI

`tubo` is the main executable for starting runtime roles without having to remember many environment variables.

## Configuration precedence

Effective configuration follows:

```text
CLI flag > env var > config file > default > interactive prompt
```

The interactive prompt is only used in TTY cases, when `--non-interactive` is not set, and when `CI` is not `true`. In non-interactive mode, missing required fields produce an explicit operational error.

## CLI output and logging

Current contract:

```text
stdout = primary command result
stderr = human progress/warning/hint output
technical logs = hidden by default, visible with verbosity/log-level
```

Supported global controls:

```bash
tubo --quiet ...
tubo -v ...
tubo -vv ...
tubo -vvv ...
tubo --log-level error|warn|info|debug|trace ...
```

The forms above are accepted both before the command and after the top-level subcommand, for example `tubo -vv share service/myapi` and `tubo share -vv service/myapi`.

Current defaults:

- one-shot commands: clean output, no technical diagnostics;
- runtime foreground commands (`attach`, `connect`, `gateway`, `relay`, `grants serve`): clean output by default; technical logs appear only with `-v`/`-vv`/`--log-level ...`;
- processes registered by Tubo: logs remain available through `tubo logs ...` when Tubo knows a log file.

For `--json` commands, Tubo must keep stdout as parseable JSON even when the command performs internal sub-flows such as implicit join or grant refresh.

## Main commands

The primary `tubo` UX is intent-based:

```text
attach    = publish a local HTTP or raw TCP endpoint into the swarm
connect   = open a local HTTP or raw TCP listener toward a remote service
gateway   = start an HTTP gateway into the swarm
relay     = start a relay/bootstrap node
join      = configure this machine for an existing swarm

get       = list or fetch resources
create    = create local config resources
share     = create local membership invites for a cluster
rotate    = rotate managed namespace discovery secrets
grants    = manage Publish Grant requests
describe  = show human-readable details
inspect   = show technical/raw details
watch     = watch services in the swarm

ps        = show locally registered processes
logs      = follow or tail local logs when Tubo knows the file
stop      = stop a locally registered process
rm --stale = clean up state/logs for terminated processes
```

The most common commands are:

```bash
tubo relay
tubo join overlay/public
tubo join overlay/manual --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... --swarm-key ./swarm.key
tubo gateway
tubo attach http://127.0.0.1:1234 --name lmstudio
tubo connect lmstudio --local 127.0.0.1:51234
tubo get services
tubo get secrets
tubo describe service/lmstudio
tubo describe secret/namespace-discovery/home/default
tubo inspect service/lmstudio --json
tubo watch services
```

`attach` supports both the explicit form and the shorthand name+port form; it also accepts `service/<name>` as the first argument. For raw TCP passthrough, use an explicit `tcp://host:port` target. In that case the service is published as `service_kind=tcp` and invites / `connect` preserve that kind:

```bash
tubo attach --target http://127.0.0.1:1234 --name lmstudio
tubo attach http://127.0.0.1:1234 --name lmstudio
tubo attach tcp://127.0.0.1:8443 --name tlsdemo
```

## Happy path

### Relay host

```bash
tubo relay -d
```

### Service host

```bash
tubo join overlay/manual \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key

tubo attach http://127.0.0.1:1234 --name lmstudio -d
```

### Client host

```bash
tubo join \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key

tubo get services
tubo describe service/lmstudio
tubo connect lmstudio --local 127.0.0.1:51234 -d
```

### Gateway host

```bash
tubo gateway --listen :8443 -d
```

Long-running commands stay in the foreground by default. With `-d` / `--detach`, they can be left in the background:

```bash
tubo relay -d
tubo gateway -d
tubo attach http://127.0.0.1:1234 --name lmstudio -d
tubo connect lmstudio --local 127.0.0.1:51234 -d
```

## Implicit join/init and `--no-init`

If the local default config does not exist yet:

- `attach`, `connect`, `gateway`, `relay`, and the discovery commands (`get`, `describe`, `inspect`, `watch`) perform an **implicit public join** to the default public network by downloading and verifying the signed bundle; the public bundle also installs the `home/default` cluster metadata (cluster ID, authority public key, and grant-service peers), so `tubo attach`/`tubo connect` can start from a clean config without an explicit `join cluster/home`;
- this means that, from zero, relay/service/client all start on the same swarm key from the public bundle;
- in cluster/namespace mode, `attach` creates or reuses a stable identity for `(cluster, namespace, service)` (`service_id`, `service_owner_key_file`, `service_seed`, `service_claim_file`) before starting the runtime;
- without explicit config, `attach` still generates a unique libp2p seed per process if you do not pass `--seed`, avoiding shared demo PeerIDs between machines;
- `attach` listens by default on `/ip4/0.0.0.0/tcp/0` to allow direct dialing/hole punching when the network permits it.

Files involved:

```text
~/.config/tubo/config.yaml
~/.config/tubo/swarm.key
~/.config/tubo/clusters/.../namespaces/.../services/...owner.key
```

To disable implicit behavior explicitly:

```bash
--no-init
```

In `CI=true`, both the implicit public join and the implicit init are disabled and the command fails with explicit next steps instead of creating local state implicitly.

## Utilities

```bash
tubo keygen swarm --out swarm.key
tubo id from-seed service-lmstudio-seed
tubo config validate --config service.yaml
tubo config print --config service.yaml
tubo doctor --config service.yaml
```

## Init vs Join

`init` creates a new local configuration; `join` imports the configuration of an existing swarm.

`join` configures this machine locally to use an existing swarm. It does not start background processes.

Default mode (signed public-network bundle):

```bash
tubo join
tubo join tubo-public
```

Manual mode (private existing swarm):

```bash
tubo join \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key
```

For scripting:

```bash
tubo join \
  --relay /ip4/1.2.3.4/tcp/4001/p2p/12D3... \
  --swarm-key ./swarm.key \
  --json
```

For a custom bundle:

```bash
tubo join --bundle-url https://example.com/network.bundle
```

For test/dev, before the default bundle is actually published on GitHub Pages, you can also force the URL used by `tubo join` and the implicit public join:

```bash
export TUBO_DEFAULT_PUBLIC_BUNDLE_URL=https://example.com/tubo-public.bundle
```

By default it saves:

```text
~/.config/tubo/config.yaml
~/.config/tubo/swarm.key
```

You can change the directory with `--config-dir`, force overwrite with `--force`, or run a basic TCP reachability check on the relay with `--check`.

## Connect

`connect` opens a local listener toward a service discovered in the swarm: HTTP for `service_kind=http`, raw TCP for `service_kind=tcp`.

If the local default config does not exist yet, `connect` first attempts the implicit public join to the default public network.
`get services`, `describe`, `inspect`, and `watch` use the same bootstrap path.

```bash
tubo connect lmstudio --local 127.0.0.1:51234
# for a tcp service, the local listener is raw TCP, for example tcp://127.0.0.1:51234
```

With `-d` / `--detach`, the client tunnel remains in the background as `process/connect-...` and is visible in `tubo ps` / `tubo get processes`. The foreground form is also registered and therefore visible in `tubo ps`.

If `--local` is not specified, Tubo automatically picks a free port on `127.0.0.1`.

For scripting:

```bash
tubo connect lmstudio --json
```

`connect` uses the same discovery resolution as `get service/<name>`: local cache when available, then remote discovery query toward a bootstrap/relay peer, and only then an ephemeral live observer. The name can be passed either as `lmstudio` or `service/lmstudio`, or provided via `--token <share-invite>` without listing services. The `service/<service_id>` form forces an exact lookup by `service_id`, which is useful when display names are duplicated; in that case, an ambiguous name fails with a hint toward `tubo connect service/<service_id>`. With an invite, `connect` always resolves the exact `service_id` instead of trusting the display name and rejects tokens signed by an issuer different from the one already pinned for the local scope. If the token contains a self-contained `service_endpoint`, `connect --token` uses that endpoint directly without going through discovery; legacy tokens without an endpoint can still fall back to discovery only in scopes where discovery is enabled. New tokens also propagate `service_kind`: for a `tcp` service, `connect` opens a local raw TCP listener and the printed/JSON endpoint uses `tcp://host:port` instead of `http://...`. Data access always goes through the grant endpoint: when the token contains `grant_service` metadata, `connect --token` must first redeem the invite into a `ConnectAccessLease`/`ConnectRefreshLease`; if redemption is denied (`already redeemed`, revocation, scope mismatch, etc.) Tubo no longer falls back to legacy embedded bearer grants. If the discovery-enabled service publishes `grant_service` metadata, `connect <name>` uses that grant endpoint to request a service-scoped `ConnectAccessLease`/`ConnectRefreshLease` before opening the tunnel: today the attached publisher signs delegated leases with its service owner key, while the service validates the chain authority -> publish lease (delegation) -> connect lease -> connect proof. The `--cluster` and `--namespace` options are resolved from the current config when present or from the service token; `get services` also supports `-n/--namespace` and `-A/--all-namespaces` for future scoped lookups. In the signed bundle public default (`home/default` on `tubo-public`), ambient discovery is now treated as disabled: `get services`, `watch services`, `describe service/...`, `inspect service/...`, and `connect <name>` fail with a hint toward `tubo connect --token <share-invite>` or toward a private/custom collaborative scope; an old invite without `service_endpoint` fails with a compatibility error instead of trying an ambient listing.

Normal HTTP and WebSocket (`Upgrade: websocket`) are forwarded over the same tunnel. If a service advertises only direct loopback/unspecified addresses (`127.0.0.1`, `0.0.0.0`, `::1`), `connect` ignores them for remote dialing and uses the relayed path. The `connect` client enables AutoRelay/hole punching when the config contains relay peers; successful direct upgrade still depends on NAT/firewall behavior and on the advertised service addresses. Even when the initial path is `relayed`, libp2p can later open a direct connection through hole punching. For raw TCP services, `connect` now proactively renews its short-lived access lease before expiry when a refresh lease is available. When stream setup still fails before any application bytes have flowed, `connect` may do one short inline self-heal attempt before failing the local connection; once bytes are already flowing, Tubo does not transparently replay that connection. `tubo ps`, `describe process/...`, and `inspect process/... --json` also expose degraded runtime state and remaining lease lifetime for detached connects.

## Local process state

With `-d`, Tubo stores local state in a daemonless style:

```text
~/.local/share/tubo/processes/
~/.local/share/tubo/logs/
~/.local/share/tubo/run/
```

with XDG support via `XDG_DATA_HOME` when set.

For persistence after reboot or restart-on-failure via the OS supervisor, also see `docs/runbooks/PROCESS_SUPERVISORS.md`.

## Local processes vs swarm resources

This distinction is important:

```bash
tubo ps
tubo get processes
```

show local processes registered on this machine.

```bash
tubo get services
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
```

show discovery resources observed in the swarm instead.

Example: `process/connect-lmstudio-51234` is the local process that keeps the tunnel open; `service/lmstudio` is the resource advertised by the remote publisher.

## Local process management

Registered local processes can be inspected and managed with:

```bash
tubo ps
tubo get processes
tubo describe process/attach-lmstudio
tubo inspect process/attach-lmstudio --json
tubo logs process/connect-lmstudio-51234
tubo stop process/connect-lmstudio-51234
tubo rm --stale
```

`ps` / `get processes` refer to local processes registered on this machine. The table also shows `SERVICE ID` and `SCOPE` when a process publishes a service.
`get services`, instead, refers to discovery resources advertised in the swarm. The table and JSON report `SERVICE ID`, `SCOPE`, and now also `ACCESS`/`connect_policy` when the service publishes connection metadata; the `grant_service` field is propagated in JSON when present, so duplicate display names stay separated and future collaborative flows can also see the associated grant endpoint. `get service/<service_id>`, `describe service/<service_id>`, and `inspect service/<service_id>` perform exact lookup. When the local config contains `current_cluster` / `current_namespace`, these values are reported in the command’s resolved scope; you can override them with `--cluster`, `-n/--namespace`, and, for lists only, `-A/--all-namespaces`. In cluster mode, the query and list are allowed only if the namespace membership capability allows it; `-A` requires a capability for each namespace or a broad capability with namespace `*`.

## Resource discovery

With a local config created via `join`, you can inspect the services advertised in the swarm:

```bash
tubo get services
tubo get service/lmstudio
tubo describe service/lmstudio
tubo inspect service/lmstudio --json
tubo watch services --timeout 20s
```

Behavior:

- if a local edge is already listening on the admin API, it uses its local discovery cache;
- otherwise it first tries a remote discovery query toward the first available bootstrap/relay peer;
- if the remote query also fails or is insufficient, it starts an ephemeral observer, connects to the swarm for an explicit timeout, and then exits; the default is intended to cover at least one initial discovery heartbeat;
- output messages explicitly indicate whether it is using local cache, remote query, live observer, or a fallback between them.

Useful flags in this MVP:

```bash
--config <path>
--timeout 20s
--live
--cached-only
--json
```

`config print` masks secrets (`private_key_b64`) and does not print the contents of `swarm.key`.

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

## Init

```bash
tubo init relay --out relay.yaml
tubo init edge --out edge.yaml
tubo init service --out service.yaml
tubo init bridge --out bridge.yaml
```

Existing files are not overwritten without `--force`.

## Example swarm.key

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
  # 0 means no relay byte cap; positive values cap cumulative bytes per relayed circuit connection.
  limit_data_bytes: 0
```

`relay.limit_data_bytes` is a circuit relay v2 connection cap, not an application-request cap. Small values can reset long-running raw TCP tunnels because multiple TCP tunnel streams may share the same relayed libp2p connection.

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
      default:
        discovery: enabled
        discovery_secret_current:
          type: namespace-discovery
          key_id: nsdk_20260602T100000Z_abcd1234
          file: ~/.config/tubo/clusters/home/namespaces/default/discovery-current.secret
          created_at: "2026-06-02T10:00:00Z"
        discovery_secret_previous: null

network:
  private_key_file: /etc/p2p/swarm.key
  bootstrap_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
  relay_peers:
    - /ip4/1.2.3.4/tcp/4001/p2p/12D3...
```

`current_overlay` materializes overlay fields into `network:` when the file uses the new layout; the writers for `join` and signed bundles write both formats for compatibility. `tubo join overlay/public` is the explicit public join form; `tubo join overlay/manual --relay ... --swarm-key ...` is the explicit manual/legacy join form. When the current config carries a collaborative cluster with identity metadata plus a usable namespace discovery secret, runtime discovery uses an opaque Discovery V3 topic derived from the namespace discovery entry and validates scope, membership capability, service identity, publish authorization, and replay state. Configs without these metadata do not support collaborative ambient discovery.

## Local resource CLI (Phase 2a)

With the new local model you can inspect, create, invite, and select overlay, cluster, and namespace entries already present in the config:

```bash
tubo get overlays
tubo get clusters
tubo get namespaces
tubo get secrets

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
tubo describe secret/namespace-discovery/home/default

tubo rotate secret/namespace-discovery/home/default --grace 24h

tubo use overlay/public
tubo use cluster/home
tubo use namespace/default
```

Notes:

- `get overlays` and `get clusters` read only the local config.
- `get namespaces` uses the current `current_cluster`.
- `create cluster/...` generates a local authority keypair, writes a `cluster_id`, sets `authority_public_key`, creates the `default` namespace, generates a random 32-byte `namespace-discovery` secret file with mode `0600`, saves a local membership capability without printing secret bytes, and initializes the namespace policy as `discovery: enabled` + `connect_policy: namespace_members`. In new collaborative configs, the creator capability also includes `connect`, so `connect <service>` by name works immediately in the same namespace.
- `create namespace/...` requires a valid `current_cluster`, adds the namespace to the current cluster, makes the new `current_namespace` explicit, materializes a signed membership capability for that namespace, generates a random 32-byte `namespace-discovery` secret file with mode `0600`, and initializes the local policy as `discovery: enabled` + `connect_policy: namespace_members`. Here too the local creator capability includes `connect`; older namespaces are not widened automatically and `tubo doctor` now warns when the current discovery-enabled context still lacks `connect`.
- `create service/...` requires `current_cluster` and `current_namespace`, materializes a stable `service_owner_key_file` for the service identity, derives `service_id` from that key, signs a local `ServiceClaim`, and also saves a `service_publish_lease_file` when the node owns the local authority; Discovery V3 uses the lease as the primary publication authorization.
- `attach` in cluster/namespace mode automatically materializes a stable service identity if missing: `service_id` is derived from the service owner key stored in the local config (`0600`), while `service_seed` is generated once and stored in the local config (`0600`).
- before starting the runtime, `attach` resolves publish authorization: it uses an existing valid `PublishLease`, signs it locally if the node owns `authority_private_key_file`, or submits/polls a Publish Grant request if the service has `grant_service_peer`; an expired lease is treated as absent and therefore goes through the normal renewal/request path, and an expired local `ServiceClaim` is also treated as stale/renewable state rather than a fatal error. The `ServiceClaim` fallback remains local compatibility only. When available, it also prints a copyable `service_share_token` connect-only token for Bob (`tubo connect --token ...`). If publish authorization is valid but the token cannot yet be generated (for example because the remote endpoint is not ready), `attach` suggests rerunning `tubo share service/...`; the old `tubo grants request ... --poll` hint remains reserved for cases where the grant request is still actually pending.
- in the signed bundle public default (`tubo-public` / `home/default`), `attach` runs in **unlisted** mode: the service remains reachable via invite token and libp2p stream, but it does not start ambient publication/discovery and the output makes `visibility: unlisted` + `access: invite token required` explicit. In this scope, the token printed by `attach` must be self-contained: if there is still no relay-aware/remote-dialable endpoint, `attach` fails before printing an unusable invite.
- in collaborative namespaces with discovery enabled, `attach` also registers a libp2p `/tubo/grants/1.0` endpoint on the service’s peer and publishes a service-scoped `grant_service` in Discovery V3 with reachable peers (relay-aware when available, otherwise only actually dialable direct ones). `connect <service>` now uses this endpoint to obtain discovery-driven leases: `namespace_members` accepts both local membership capabilities with `connect` and imported cluster invites with `connect` permission; revoked invites can no longer obtain new leases.
- `share cluster/...` uses the local authority key to emit a signed invite, includes namespace/expiry/grant data plus the current `namespace-discovery` entry for the selected namespace, and now also embeds a separate signed `cluster-membership-grant` token for reusable connect/list proof. The raw discovery secret bytes remain only inside the full install/join invite, not in standalone CLI fields or in the reusable membership token. `--role member` grants `subscribe,list,publish,connect`, `--role viewer` grants only `subscribe,list` (you can see the service but not open connect leases), while `--role grant-requester --grant-peer ...` emits an invite without direct publish rights but with metadata to request a Publish Grant. If the namespace is missing a usable current discovery entry, `share cluster/...` now fails clearly instead of minting an unusable invite.
- `share service/...` resolves the current or explicit cluster/namespace (`--cluster`/`--namespace`) and prints a copyable `tubo connect --token ...` command; if the local authority key is available it uses the local authority path, otherwise it can delegate minting to the cluster grant service when the local service owner holds a valid `PublishLease` with `share.mint` and a `grant_service_peer`. If the local `PublishLease` is missing or expired, `share service/...` does not require a new service name: it reuses the same local identity / same `service_id`, tries to renew or re-request publish authorization for that scope first, then continues with delegated minting if the renewal is approved; leases with invalid signature/scope/service/peer are still fatal errors and do not enter the renewal path. If you pass `service/<service_id>`, it uses exact lookup instead of the display name. The token includes cluster/namespace/service/authority metadata, including `service_kind`, but does not authorize generic listing and the new tokens no longer include a reusable embedded bearer `ConnectCapability`. When available it can also include a self-contained `service_endpoint` with relay-aware `/p2p-circuit` addresses, so invite-only flows do not depend on ambient listing; in the public default this endpoint is no longer optional, so `share service/...` fails clearly if it can only produce local/non-dialable addresses. If it also includes `grant_service` metadata, `connect --token` redeems it into a short-lived `ConnectAccessLease` and a `ConnectRefreshLease` bound to the bridge’s local key. The bridge renews the access lease before expiry. Share invites are now one-time at the grant endpoint: `one-time` means one successful lease/session redemption, not one HTTP request on the tunnel, and a denied redemption no longer falls back to legacy bearer grants. The local `share-invite-registry.json` remains only a UX guard rail; the authoritative decision happens on the server that redeems the invite. `share revoke <share-invite>` marks the `jti` as revoked/used in the local config, while `tubo revoke invite|session|service-access|publish ...` updates the issuer-side revocation store used by `grants serve`.
- `join cluster/... --token ...` and `join <cluster-invite>` verify the invite, install the namespace discovery secret file locally with mode `0600`, install the reusable membership-grant token into a local `0600` file, and store only safe config references/metadata (`membership_grant.invite_token_file`, grant metadata, discovery secret refs) in the local config without touching the runtime or persisting the full cluster invite token.
- `get secrets` lists local namespace-discovery current/previous entries as metadata only. When a `previous` entry is already expired, the local secret-management view cleans up that expired metadata and removes the old local `discovery-previous.secret` file when safe.
- `describe secret/namespace-discovery/<cluster>/<namespace>` shows metadata only for the current/previous entries in that scope, including missing-file diagnostics when relevant. It also reflects the same expired-previous cleanup/repair behavior.
- `rotate secret/namespace-discovery/<cluster>/<namespace> --grace <duration>` moves the current entry to `previous`, sets an explicit grace expiry, and creates a fresh current entry. Publishers use only the new current entry; observers accept current plus non-expired previous entries.
- `describe overlay/...`, `describe cluster/...`, and `describe namespace/...` show only local metadata and do not print secrets. `describe namespace/...` also includes the effective policy (`discovery`, `connect_policy`) of the current namespace; for the signed public bundle, `home/default` implicitly resolves to `discovery: disabled` + `connect_policy: invite_only` even when an older config did not yet have those fields explicitly. `describe service/...` also shows `Connect policy` and, when available, the protocol/peer of the `grant_service` published by the observed service.
- `use` updates only the local config file; it does not start or stop runtime processes.
- `--json` remains available for `get` and for the new local flows when useful.

## Publish Grants

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

The listener uses `/tubo/grants/1.0`, stores pending requests under the local Tubo data dir, derives the requester PeerID from the libp2p stream, and never signs publication material without approval. `grants serve` uses the configured overlay bootstrap/relay peers, enables AutoRelay/hole punching from config, maintains relay reservations, and prints relay-aware `/p2p-circuit` addresses for signed invites; it is not itself a namespace discovery publisher. Approval is explicit and signs a service-scoped `PublishLease`/`ServiceClaim` with the local authority key plus an optional connect-only `service_share_token`. The grant server also reads `--revocations` (default local data dir) to reject revoked invite redemption, revoked session refresh, stale service-access epochs, and publish-revoked services. The grant server bounds pending requests globally/per requester/per `service_id`, clamps share TTL, and rejects active `service_id` collisions for a different service peer; duplicate display names are allowed. `grants history` now prints `SCOPE` and `SERVICE_ID`, sorts by `service_id`, and prefixes the output with the local store path so the source is explicit. `attach` also uses the saved `grant_service_peer`/`grant_request_id` metadata to submit or poll before service publication; when a token is available it is printed before the process detaches or enters the foreground wait; denied, expired, revoked, or still-pending grants stop publication.

## Multi-node setup

For multi-node setups, use one of these two paths:

- canonical local flow: `tubo join`, `tubo create cluster/...`, `tubo create namespace/...`, `tubo create service/...`, `tubo share ...`, `tubo attach`, `tubo connect --token ...`
- role YAML: `tubo init relay|edge|service|bridge` and then edit the generated files with relay peer, swarm key, and required cluster/namespace metadata
