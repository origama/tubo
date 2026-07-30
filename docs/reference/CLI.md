# CLI contract

Implementation status: private-HTTP/WebSocket/raw-TCP vertical slice supports `keygen swarm`, `join overlay`, scope create/invite/join/use, remote publisher grants and announcements, foreground relay/authority/published pipes, service/pipe create, `attach`, detached `connect`, one-time share invites, revocation, local inventory/inspection, peer aliases, masked config validation/printing, `doctor`, `logs`, detached pipe restart/removal/stale cleanup, `ps`, `top`, and identity-safe `stop`.

## Output

```text
stdout = primary command result
stderr = progress, warning, hint, diagnostics
```

`--json` prints exactly one parseable JSON value to stdout. Detached children and implicit authorization/discovery work must not contaminate it.

## Ergonomic commands

```bash
tubo attach http://127.0.0.1:1234 --name lmstudio -d
tubo attach tcp://127.0.0.1:8443 --name tlsdemo -d
tubo connect lmstudio --local 127.0.0.1:51234 -d
tubo connect --token <share-invite> --local 127.0.0.1:51234 -d
```

Semantics:

```text
attach  = create/reuse service + create/reuse/start published pipe
connect = resolve exact pipe_id + create/reuse/start local pipe
```

Both use `create-if-missing, reuse-if-compatible, fail-if-conflicting`.

## Explicit resources

```bash
tubo service create lmstudio --target http://127.0.0.1:1234
tubo get services                   # local services only
tubo get scopes|processes|peers|overlays
tubo describe service/lmstudio
tubo rm service/lmstudio

tubo pipe create lmstudio-team \
  --ingress tubo \
  --target service/lmstudio \
  --scope solar/default \
  --visibility discoverable \
  --access members

tubo pipe create lmstudio-local \
  --ingress local://127.0.0.1:51234 \
  --target pipe/pip_...

tubo get pipes                      # local definitions + verified network records
tubo describe pipe/lmstudio-team
tubo inspect pipe/lmstudio-team --json
tubo start|restart|stop|rm pipe/lmstudio-team
```

A service may not be started. Only a pipe has a connectivity runtime.

## Overlay and scope

```bash
tubo join overlay --relay <multiaddr> --swarm-key <file> [--force-reachability private|public]
tubo scope create solar/default
tubo authority start solar --scope solar/default -d
tubo scope invite solar/default --role member --expires 24h --json
tubo scope invite solar/default --role viewer --expires 24h --json
tubo scope join --token <membership-invite>
tubo scope use solar/default
tubo scope rotate-discovery-key solar/default --grace 24h
```

Roles:

- member: discover and connect; publish only if policy grants it;
- viewer: discover/list but cannot connect;
- publisher: may request/receive pipe publication authority;
- forwarder: may re-export a pipe when explicitly granted.

## Sharing and revocation

```bash
tubo share pipe/lmstudio-team --json
tubo revoke invite <jti>
tubo revoke session <session-id>
tubo revoke pipe-access <pipe-id>
tubo revoke pipe-publish <pipe-id>
```

Invites target exact `pipe_id`; display name is only a hint. Invite carries published pipe peer/address/protocol endpoint, never local service endpoint. One-time means one successful lease redemption, idempotent only for same client key/JTI retry. Access leases last 10 minutes; refresh leases last 48 hours and refresh later sessions only.

## Runtime views

```bash
tubo ps
tubo top [--interval 1s]
tubo top --once
tubo top --json
tubo logs pipe/lmstudio-team [--tail 200] [-f]
tubo inspect pipe/lmstudio-team --json
tubo supervise install pipe/lmstudio-team --json
tubo supervise install authority/solar --scope solar/default --json
tubo supervise status pipe/lmstudio-team --json
tubo supervise uninstall pipe/lmstudio-team --json
tubo inspect session/ses_... --json
```

`ps` displays primary resources and capabilities, never `attach` or `connect` as kinds. JSON includes `source`: `foreground`, `detached`, `launchd`, or `systemd`.

Suggested compact columns:

```text
NAME  KIND  INGRESS  TARGET  CAPS  STATUS  AUTH  SESSIONS  PATH
```

A pipe's `PATH` is an aggregate summary. `PATH SOURCE=advertised` describes verified record candidates; `last_session` describes most recent concrete traversal. Per-session direct/relay data belongs in session inspection. Application counters are completed-session totals. `NET RX/TX`, `RX/s`, `TX/s`, `PEERS`, and `CONNS` come from each runtime's libp2p bandwidth/network telemetry, including relay traffic; active/error application-session counters remain pending.

## Local operations

```bash
tubo peers alias <peer-id> --name <label> [--note <text>]
tubo config validate
tubo config print
tubo doctor
tubo rm --stale
tubo --quiet|-v|-vv|-vvv|--log-level <level> <command>
tubo supervise install|status|uninstall <pipe/name|authority/cluster> [--scope cluster/namespace]
```

On macOS, `supervise install` writes one user-owned `launchd` plist under `~/Library/LaunchAgents`, bootstraps its exact label, waits for a `source=launchd` ready run, and registers it transactionally. It supports only pipe and authority primary resources. `uninstall` bootouts the exact registered label, waits for the recorded process identity to exit, then removes registry and plist. `stop`, `restart`, and `rm` reject a supervised resource; uninstall/disable supervision first so `KeepAlive` cannot create duplicate runs. Linux templates are documented under `deploy/systemd/` and are never installed by Tubo.

`get peers` returns the union of local aliases and peers currently observed by live local libp2p runtimes. `SOURCE=alias`, `observed`, or `alias+observed` distinguishes them; `OBSERVED BY` names local primary runs. This is a local network view, not a synchronized membership roster, so different machines normally report different peers. Peer aliases remain operator-only metadata, are not synchronized, and never become authorization input. `config print` masks secret paths. Verbose diagnostics use stderr and never contaminate JSON stdout.

`get scopes --json` and `inspect scope/<scope> --json` expose a `sync` projection with optional `owner_ref`, `lease_expires_at`, `last_success`, `next_retry`, and stable `error_class`. It never exposes fencing tokens, process identity, membership material, or service targets. Only membership-backed published/local pipe runtimes contend for periodic sync; a local authority never self-syncs.

## Discovery

```bash
tubo get pipes [--scope cluster/namespace] [--refresh|--cached-only]
tubo get pipe/<pipe-id>
tubo watch pipes [--scope cluster/namespace] [--interval 2s] [--json]
```

`get services` is local-only. Remote connectivity is represented by verified pipes.

`watch pipes` polls only the locally persisted, verified pipe projection; it never opens a discovery socket and never treats a remote event as trusted input. It emits the initial projection as `added` events. Later events have an explicit `event_type`: `added` for a verified pipe first observed locally, `updated` for source/freshness or other non-health projection changes, `expired` when a previously verified record expires (including a `local+network` row that becomes local-only), `removed` when a non-expiry projection disappears, `degraded` when a local verified pipe status becomes degraded, and `recovered` when that status leaves degraded. The default interval is two seconds and the minimum is 500ms. Without `--scope`, every scope for which the local state has a membership is aggregated; scope remains part of each event identity (`scope` + `pipe_id`), so same-name and same-ID records in distinct authorized scopes remain distinct. A scoped watch includes only that authorized membership's network projection; records from a scope with no local membership are never exposed.

Human stdout has exactly one stable key/value line per event. `--json` writes NDJSON (one JSON object per event, no enclosing array), with `event_type`, `scope`, `pipe_id`, `name`, `observed_at`, and either `item` (`added`), `previous` (`expired`/`removed`), or both plus ordered `changed` fields (`updated`/`degraded`/`recovered`). A slow stdout consumer applies backpressure to polling; Tubo retains no event queue or writer goroutine. Human `get pipes` output includes stable ID, source, scope, ingress, target ref, access, status, session count, path, and path source. A local definition and its verified network record are merged as `local+network`; re-exports into distinct scopes remain separate rows. JSON exposes the same fields plus peer, capabilities, and record expiry.

Current implementation advertises only relay circuit addresses, so network rows report `relayed`. `direct` and `mixed` are reserved for verified direct candidates once direct dialing/hole punching is implemented. Network rows include `record_age`, a non-negative Go-duration string measured from signed `issued_at`; the human table exposes it as `RECORD AGE`. `inspect pipe/... --json` includes `record_age` when a verified record exists and `record_fresh`, which is true only while its signed record is strictly younger than five minutes.

`get pipes` is cache-first. A scope cache is `fresh` only after a complete pinned-authority 2.1 snapshot success strictly less than five minutes old. Default listing opens no discovery network socket for fresh scopes; stale or missing selected memberships receive one bounded synchronous refresh before verified cached/local projection. `--refresh` refreshes every selected membership and bypasses this freshness window plus persisted discovery retry cooldown, without bypassing request bounds or verification. `--cached-only` never refreshes or opens discovery network sockets. Flags are mutually exclusive. `--live` is obsolete and rejected with `invalid_input`; use `--refresh`. No selected membership is local-only and opens no discovery network socket.

A running local pipe re-resolves its exact remote `pipe_id` from the pinned authority snapshot every five minutes. One classified transient dial/stream/handshake failure before `OpenOK` can trigger one cooldown-bounded resolution and retry using a newer verified endpoint; authorization/protocol failures and any post-`OpenOK` tunnel never trigger a retry or application-byte replay.

`--scope` filters both network rows and scoped local pipe definitions to that exact scope. Unscoped local pipes have no discovery scope and remain visible under every `--scope` filter; they are local-only rows and never cause a network operation.

JSON stdout remains one object: `items` plus typed `scopes` entries with `scope`, `cache_state` (`fresh`, `stale`, or `missing`), optional pinned snapshot `source`/`last_success`, `refresh_attempted`, and optional `refresh_error.class`. Refresh errors are scope-local structured classes and do not include tokens, keys, service targets, addresses, or private routes. Human table keeps pipe-row `SOURCE` semantics and adds `FRESHNESS`; refresh warnings go only to stderr. On refresh failure, verified unexpired cache and local rows remain visible.

## Infrastructure

```bash
tubo relay --listen /ip4/0.0.0.0/tcp/4001 --public-addr <addr> -d
tubo gateway --listen :8443 -d
```

Relay and authority remain primary infrastructure resources in `ps`; they are not pipes.

## Common JSON requirements

Mutation output includes:

```json
{"ref":"pipe/lmstudio-team","id":"pip_...","status":"running"}
```

Pipe inspection distinguishes:

```json
{
  "logical_path": [],
  "authorization_path": [],
  "transport_summary": {},
  "sessions": []
}
```

Errors have stable classes, for example:

```text
invalid_input
not_found
conflict
authorization_denied
authority_unreachable
discovery_unreachable
discovery_retry_after
pipe_unreachable
relay_unavailable
persistence_failed
readiness_failed
```
