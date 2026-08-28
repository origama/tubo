# CLI contract

Normative human CLI contract for Tubo. The command surface, parsing rules,
help text, output discipline, and JSON shapes described here are the current
post-A0 behavior and the compatibility target for scripts and future
releases. The CLI is a thin L5 parsing/presentation adapter over L4 use
cases; it owns no connectivity, authorization, or runtime semantics.

## Mental model

Tubo's CLI is organized into three command families plus infrastructure:

**Workflows** — ergonomic presets, not resource kinds:

| Command | Purpose |
| --- | --- |
| `tubo attach <target> --name <name>` | create/reuse a Service and its Pipe for a local endpoint, then run it |
| `tubo connect --token <invite> --local <host:port>` | redeem an exact invite and run a local Pipe |
| `tubo share <pipe-ref>` | mint a one-time invite for an owned, running Pipe |

**Generic resource operations** — apply to resource refs consistently:

| Command | Purpose |
| --- | --- |
| `tubo get <collection>` | compact inventory tables (`pipes`, `services`, `scopes`, `peers`, `overlays`) |
| `tubo inspect <resource>` | labeled detail for one resource |
| `tubo start <pipe-ref>` | start a Pipe runtime (foreground, or `-d` detached) |
| `tubo stop <ref>` | stop a running runtime |
| `tubo restart <pipe-ref>` | restart a Pipe runtime detached |
| `tubo rm <ref>` | remove a Service or Pipe (with `--force` for a running Pipe), or `--stale` runs |
| `tubo logs <ref>` | show runtime logs (`--tail N`, `-f`) |
| `tubo ps` | compact lifecycle/runtime overview |
| `tubo top` | live runtime and network metrics (`--once`, `--interval`) |
| `tubo watch pipes` | stream verified Pipe projection events |
| `tubo doctor` | local health checks |

**Resource-specific administration** — subcommand trees:

| Command | Purpose |
| --- | --- |
| `tubo service create <name> --target <url>` | define a local Service |
| `tubo pipe create <name> --ingress <kind> --target <ref>` | define a Pipe |
| `tubo scope create|invite|join|use ...` | Scope administration |
| `tubo peers alias <peer-id> --name <label>` | label a peer |
| `tubo config validate|print` | configuration checks |
| `tubo supervise install|status|uninstall <ref>` | launchd supervision |

**Infrastructure** — deliberately distinct verbs, not resource operations:

| Command | Purpose |
| --- | --- |
| `tubo join overlay --relay <multiaddr> --swarm-key <file>` | configure overlay reachability |
| `tubo relay --listen <addr> --public-addr <addr>` | run a relay in the foreground |
| `tubo authority start <cluster> --scope <scope>` | run a Scope authority in the foreground |
| `tubo revoke <type> <id>` | revoke invites, sessions, access or publish grants |
| `tubo keygen swarm --out <file>` | generate a swarm key |
| `tubo version` | version information |

## Command grammar

* **Resource refs** are `<kind>/<name>`: `pipe/origin`, `service/echo`,
  `scope/<cluster>/<namespace>`, `overlay/default`, `session/<id>`,
  `peer/<peer-id>`, `relay/default`, `authority/<cluster>`.
* **Command paths are contiguous and flags come after the leaf.** The
  command/subcommand path must be a contiguous prefix of the invocation;
  command-local flags may only follow the resolved leaf:

  ```
  tubo get pipes --wide --json      # ok
  tubo get --wide pipes             # fails: flag before subcommand
  tubo scope --json create solar/default   # fails: flag before subcommand
  ```

  Misplaced flags fail clearly (`get: flag --wide before subcommand; see
  'get --help'`) instead of being silently dropped. Global flags
  (`-q`, `-v`, `--log-level`) are exception: they are stripped before
  resolution and may appear anywhere.
* **Flag syntax is explicit for long and short forms.**

  ```text
  --long          long boolean flag
  --long=value    value flag (or --bool=true/false)
  -x              single-letter short alias, only when declared as short
  ```

  A single-dash token with more than one character (`-json`, `-name foo`,
  `-scope x`) is never a long flag and is rejected. Short aliases exist
  only where declared: `-d` = `--detach`, `-f` = `--follow`, plus `-q`,
  `-v`, `-h`. Accidental long forms of shorts (`--d`, `--f`) are rejected.
* **Unknown flags fail.** A typo is never silently ignored:
  `tubo get pipes --scpoe solar/default` fails with
  `invalid_input: get pipes: unknown flag --scpoe; see 'get pipes --help'`.
* **Required flags fail clearly**: `invalid_input: connect: --local is
  required; see 'connect --help'`. An empty `--name=` fails the same way.
* **A value flag followed by another flag fails as “requires a value”**
  instead of consuming the flag: `--name --json` is rejected, never
  interpreted as the name `--json`.
* **Extra or missing positionals fail clearly** with the offending argument.
* **Duplicate flags are rejected** for both value and boolean flags
  (`--json --json`, `-d -d`, `-d --detach` all fail).
* **Obsolete flags fail with a replacement hint**: `--live` is rejected with
  `invalid_input: --live is obsolete; use --refresh`.
* **Global flags** (`-q`/`--quiet`, `-v/-vv/-vvv`, `--log-level <level>`)
  are accepted anywhere in the argument list.
* Errors identify the command: `tubo: invalid_input: attach: unknown flag
  --detail; see 'attach --help'`.

## Contextual help

Help is available at every level of the command tree and requires no
initialized Store, config, or network access:

```bash
tubo --help
tubo attach --help
tubo get --help
tubo get pipes --help
tubo scope --help
tubo scope invite --help
tubo supervise install --help
tubo help <command> [<subcommand>]   # same output
```

Help is generated from the same declarative command specification that drives
parsing and validation, so described flags, requiredness and the real surface
cannot drift apart. The top-level help lists the full public command surface;
hidden internal commands (e.g. `_run-pipe`, used by detached children) are
not advertised.

## stdout / stderr discipline

```text
stdout = primary command result (machine-friendly)
stderr = progress, warnings, hints, diagnostics
```

* Mutations print their result to stdout: which resource was affected and
  what happened (`created` / `reused` / `removed`), plus readiness when it is
  actually known (detached starts).
* Progress (`starting pipe/origin...`), warnings (refresh failures), and next
  actions (`share it: tubo share pipe/origin`) go to stderr.
* `--json` prints **exactly one parseable JSON value** to stdout; stderr stays
  clean for normal listing commands.
* `watch --json` is the explicit exception: it emits NDJSON (one JSON object
  per event).
* Detached child output is redirected to per-resource log files and never
  leaks into structured stdout.

### Happy path

Publisher:

```console
$ tubo attach http://127.0.0.1:8080 --name origin -d
service/origin created
pipe/origin created
pipe/origin ready (pid 5021)
$ INVITE="$(tubo share pipe/origin)"
```

`share` keeps the raw invite as the **only** stdout line so command
substitution works unchanged; expiry, one-time semantics and the recipient
command are printed to stderr:

```console
$ tubo share pipe/origin
eyJ2ZXJzaW9uIjoxLCJ0eXBlIjoicGlwZS5vcGVuLmludml0ZSI...
```

Recipient:

```console
$ tubo connect --token "$INVITE" --local 127.0.0.1:51234 --name west -d
pipe/west ready (pid 7244)
local listen: 127.0.0.1:51234
```

`connect` accepts an optional friendly `--name`; without it the local Pipe
uses the canonical deterministic name derived from the remote Pipe ID
(`connect-<pipe_id>`, bounded to 63 chars), so lifecycle commands stay stable
across reconnects. The recipient identity key (and therefore the derived
local Pipe ID) is canonical per remote Pipe ID, never per name. Reconnects
to the same remote therefore resolve one durable local Pipe definition, and
an existing definition is reusable only when every relevant durable semantic
matches the request:

* same name, same remote, same local listener — compatible reuse;
* reconnect without `--name` on the same local listener — the existing
  connection's ref is returned (no duplicate identity, no rename);
* reconnect with a **different local listener** — `conflict` naming the
  effective persisted listener and the requested one, e.g.
  `conflict: pipe/west already connects this remote pipe on
  127.0.0.1:9004; reuse that listener or remove the pipe before
  reconnecting on 127.0.0.1:9005`;
* explicit different name for an already-connected remote — `conflict`
  naming the ref that already owns the connection, suggesting reuse or
  remove-before-renaming;
* same name, different remote — `conflict` (different target);
* an existing non-local-ingress or differently-owned Pipe under the
  requested ref — `conflict` (incompatible shape/identity).

The compatibility check is resolved before any durable write; it never
surfaces as a storage uniqueness failure, and `connect` never performs an
implicit mutation, rebind, or rename. Connect output (human `local listen`
and JSON `local`) always reports the effective persisted listener from the
application result, never an echo of the requested input.

## Resource lifecycle vs operation result

Two distinct concepts are never conflated:

**Operation result** — what the command just did:

* `created`, `reused`, `removed`, `joined`, `configured`, `saved`,
  `current`, `stopping`, `ready` (a detached start that reached readiness).

**Runtime lifecycle** — the durable resource's state:

* Service: always `defined` (a Service has no runtime).
* Pipe: `defined` (never started), `starting`, `ready`, `degraded`,
  `stopped`, `stale` (run record exists but the process identity no longer
  matches).

**Remote availability** — verified network records:

* `available` for remote Pipe records projected by discovery.

Mapping: `attach` prints result semantics (`service/origin created`,
`pipe/origin reused`); `get pipes` and `inspect pipe/<name>` print runtime
status (`defined`, `ready`, `available`). `ps` and `top` print run records
only — resources that are defined but not running do not appear there.

## View commands

### `get` — compact inventory

| Command | Columns |
| --- | --- |
| `tubo get pipes` | NAME, ID (truncated), STATUS, SOURCE, PATH, ACCESS, SCOPE |
| `tubo get pipes --wide` | adds INGRESS, TARGET, SESSIONS, FRESHNESS, RECORD AGE, PATH SOURCE, full ID |
| `tubo get services` | NAME, KIND, TARGET |
| `tubo get scopes` | SCOPE, ROLE (authority/member/...), CURRENT |
| `tubo get peers` | PEER ID, ALIAS, SOURCE, CONNECTED, OBSERVED BY, ADDRESSES, LAST SEEN |
| `tubo get overlays` | REF, RELAY, REACHABILITY, CURRENT SCOPE |

`get pipes` is cache-first (see Discovery below) and shows local definitions
plus verified network records. Empty inventories print a short hint teaching
the next action (`No services. Define one with: tubo service create
<name> --target <url>`) instead of a bare header. `--json` prints a
structured projection for every collection (`{"items": [...]}`; `get pipes`
adds a typed `{"scopes": [...]}` refresh-status array).

### `inspect` — labeled detail

`inspect` is the detailed view of exactly one resource. The human form is
labeled plain text, not a collapsed JSON line:

```console
$ tubo inspect pipe/origin
Pipe:
Ref       pipe/origin
ID        pip_3bgpjcjak67vx5xj3zw4wftpwztqdnn7
Status    ready
Ingress   tubo
Target    service/origin
Scope     -
Access    invite-only
Path      relayed (last_session)
Sessions  2
Record    -
```

Resources: `pipe/<name>`, `service/<name>`, `scope/<cluster>/<namespace>`,
`overlay/default`, `session/<id>`, `peer/<peer-id>`, `relay/default`,
`authority/<cluster>`. A remote record is shown as `Status available` with
peer, scope, advertised path, and record age/freshness. `--json` emits the
full structured view (logical path, authorization path, transport summary,
sessions) without changing what human users see.

### `ps` vs `top`

* **`ps`** — compact lifecycle overview: REF, KIND, STATUS, PID, SOURCE,
  SESS, PATH, ADDRESS. It answers *what is running and is it healthy*, with
  no telemetry columns.
* **`top`** — live runtime/network metrics: adds APP RX/TX, NET RX/TX,
  RX/s, TX/s, PEERS, CONNS, refreshed every second (`--interval 1s`,
  `--once` for a single snapshot). `--json` emits `observed_at` plus items.

## Workflow and administration semantics

* `attach` – create-if-missing, reuse-if-compatible, fail-if-conflicting.
  Detached (`-d`) reports real readiness before returning; foreground runs
  until Ctrl-C.
* `share` – succeeds only for a current, ready, live owned Pipe runtime; it
  derives one canonical exact-redemption circuit locator and confirms it
  matches runtime state. Missing/stopped/stale/mismatched state fails as
  `runtime_unavailable` before any invite output. Invites target the exact
  `pipe_id`, are one-time (one successful lease redemption; idempotent only
  for the same client key/JTI retry), and expire after 10 minutes.
* `connect` – redeems the invite over an acquired carrier, verifies the
  resulting short-lived Access Lease binds this recipient to the exact Pipe,
  persists exact connection state for detached start, and runs the local
  ingress Pipe. See “connect identity” below for reuse and rename rules.
* `service create` / `pipe create` – durable definitions; the Pipe needs
  `start` (or `attach`/`connect -d`) to run. Recreating identical definitions
  reports `reused`; conflicts fail.
* `authority start` – foreground only; detached authority launch is provided
  by supervision (`tubo supervise install authority/<cluster> --scope ...`).
  `-d` is rejected with that hint.
* `supervise install` (macOS) writes a user-owned launchd plist under
  `~/Library/LaunchAgents`, bootstraps the exact label, waits for a
  `source=launchd` ready run, and registers it transactionally. `uninstall`
  boots out the exact label, waits for the recorded process identity to exit,
  then removes registry and plist. `stop`, `restart` and `rm` reject a
  supervised resource; uninstall/disable supervision first.

## Status, errors, and hints

Errors carry stable classes, lower layers first:

```text
invalid_input          unknown command/flag/argument or malformed value
not_found              resource does not exist
conflict               already exists / already running / ambiguous match
authorization_denied   invalid, expired, tampered or mis-bound credential
authority_unreachable  scope authority not ready
discovery_unreachable  discovery refresh failed (verified cache still shown)
pipe_unreachable       carrier acquisition failed
runtime_unavailable    required runtime state missing or mismatched
persistence_failed     storage write failed
readiness_failed       detached start did not become ready
```

Where a deterministic next action exists, the top-level error presentation
adds a concise `Try:` hint on stderr:

```console
$ tubo stop pipe/nope
tubo: not_found: pipe/nope is not running

Try:
  start it first: tubo start pipe/nope
```

Hints never echo credentials, tokens, key paths or private addresses.

## Discovery and caches (`get pipes`, `watch pipes`)

`get pipes` is cache-first. A scope cache is `fresh` only after a complete
pinned-authority snapshot success strictly less than five minutes old. The
default listing opens no discovery socket for fresh scopes; stale or missing
selected memberships receive one bounded synchronous refresh before verified
cached/local projection. `--refresh` refreshes every selected membership and
bypasses the freshness window and persisted retry cooldown, without bypassing
request bounds or verification. `--cached-only` never refreshes or opens
discovery sockets. The flags are mutually exclusive.

* Rows: local definitions (`source=local`), verified network records
  (`source=network`, `status=available`), and merged rows (`local+network`).
  Unscoped local pipes are local-only and remain visible under any `--scope`
  filter.
* `--scope cluster/namespace` filters both network rows and scoped local
  definitions to that scope.
* JSON stdout is one object: `items` plus typed `scopes` entries with
  `scope`, `cache_state` (`fresh`/`stale`/`missing`), optional
  `source`/`last_success`, `refresh_attempted`, and optional
  `refresh_error.class`. Refresh warnings go only to stderr; verified
  unexpired cache stays visible.

`watch pipes` polls only the locally persisted, verified pipe projection; it
never opens a discovery socket and never treats a remote event as trusted
input. Events: `added`, `updated`, `expired`, `removed`, `degraded`,
`recovered`. Human stdout has one stable key/value line per event; `--json`
writes NDJSON with `event_type`, `scope`, `pipe_id`, `name`, `observed_at`,
and `item`/`previous`/`changed` per event type. Default interval 2s, minimum
500ms.

## Peers and configuration

`get peers` returns the union of local aliases and peers currently observed
by live local libp2p runtimes; `SOURCE` is `alias`, `observed`, or
`alias+observed`; `OBSERVED BY` names the local primary runs. This is a local
network view, not a synchronized roster. Peer aliases are operator-only
metadata, never authorization input.

`config validate` and `config print` present the static overlay
configuration. `config print` masks secret paths (`<configured>`); JSON and
YAML forms never expose key paths, tokens, or private addresses.
`get scopes --json` and `inspect scope/<scope> --json` expose a `sync`
projection (owner_ref, lease_expires_at, last_success, next_retry,
error_class) but never fencing tokens, process identity, membership material,
or service targets.

## JSON reference

Mutation outputs:

```json
{"ref":"pipe/lmstudio-team","id":"pip_...","status":"created"}
{"service_ref":"service/origin","service_status":"created","pipe_ref":"pipe/origin","pipe_id":"pip_...","pipe_status":"created"}
{"ref":"pipe/origin","id":"pip_...","local":"127.0.0.1:51234","target_id":"pip_...","status":"ready","pid":7244}
```

`share --json`:

```json
{"pipe_id":"pip_...","invite":"eyJ2ZXJzaW9uIjox...","expires_at":"2026-08-28T16:41:46.379485Z"}
```

`attach -d --json` adds `"status":"ready"` and `"pid"` only when readiness is
known; the non-detached JSON form carries no fabricated state.

## Canonical commands and retained aliases

The following historical spellings were resolved deliberately:

| Resolved | Final form | Reason |
| --- | --- | --- |
| `describe` | `inspect` | one command for detailed views |
| `get processes` / `get runs` | `ps` | one runtime overview |
| `connect <pipe-name>` (legacy scope direct connect) | `connect --token <invite> --local <host:port>` | the A0 CLI connects through exact redemption only; the legacy scope-based direct connect was never rewired through L4 and is not part of the surface |
| `join overlay` (reachability) vs `scope join` (membership) | both retained | distinct concepts, distinct verbs |
| `authority start` vs generic `start` | both retained | infrastructure verb; generic `start` stays Pipe-scoped |
| `inspect peer/<id>` vs `peers alias` vs `get peers` | all three | inspect = one alias; peers alias = mutation; get peers = inventory |
| `--live` | `--refresh` | obsolete; rejected with hint |

Retained aliases with real UX value: `-f` for `--follow` on `logs`
(tail-style muscle memory), `-d` for detach across `attach`, `connect`,
`start`, `-q`/`-v` for global verbosity. Everything else is canonical only.

## Global flags

| Flag | Effect |
| --- | --- |
| `-q`, `--quiet` | suppress stderr progress/hints |
| `-v`, `-vv`, `-vvv` | increase verbosity (debug lines on stderr) |
| `--log-level error\|warn\|info\|debug\|trace` | set embedded runtime log level |
| `-h`, `--help` | contextual help at any command level |