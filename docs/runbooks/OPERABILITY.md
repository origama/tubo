# Operability

Status: remote-publisher HTTP/raw-TCP relay workflow is implemented and covered by isolated local integration and three-machine deployment. Direct-path upgrade, access refresh/revocation, forwarding, gateway, and supervisor generation remain target contract.

## Runtime topology

Recommended private three-node topology:

```text
public relay          authority + origin          consumer
transport only        scope + service + pipe      local pipe
     \                         |                      /
      \________________ private overlay ___________/
```

The relay is never authority. The origin service stays loopback-local; its published pipe obtains and periodically renews a relay reservation, and refreshes its signed record before expiry.

Legacy public bundles are not imported. Controlled protocol-2.0 deployments provision overlay relay/PSK explicitly, pin authority through explicit scope join, and keep membership invites separate from public bootstrap metadata.

## Expected workflow

Relay:

```bash
tubo relay --listen /ip4/0.0.0.0/tcp/4001 --public-addr /ip4/203.0.113.1/tcp/4001 -d
```

Authority/origin:

```bash
tubo join overlay --relay <relay-multiaddr> --swarm-key ./swarm.key
tubo scope create solar/default
tubo authority start solar --scope solar/default -d
tubo service create lmstudio --target http://127.0.0.1:1234
tubo pipe create lmstudio-team --ingress tubo --target service/lmstudio \
  --scope solar/default --visibility discoverable --access members
tubo start pipe/lmstudio-team -d
tubo scope invite solar/default --role member --json
```

Consumer:

```bash
tubo join overlay --relay <relay-multiaddr> --swarm-key ./swarm.key
tubo scope join --token <membership-invite>
tubo get pipes
tubo connect lmstudio --local 127.0.0.1:51234 -d
curl http://127.0.0.1:51234/
```

## Health

Each infrastructure or pipe run exposes a local-only control endpoint with:

```text
/healthz    readiness/liveness summary
/statusz    domain/runtime/auth status
/statsz     bounded counters
/p2pz       peer, relay, reachability diagnostics
/sessionsz  active session summaries, redacted by default
```

TCP local ingress must not consume an adjacent implicit port for health. Control endpoints use Unix sockets where supported or an explicitly allocated independent address.

## Path diagnostics

Operational output keeps separate:

- logical pipe chain;
- authorization state and next renewal;
- direct/relayed transport route per active session;
- historical failures versus current health.

A recovered session/pipe clears active degradation while retaining historical diagnostics.

## Supervisors

Foreground remains canonical. `-d` is lightweight local background execution. `restart pipe/...` performs identity-safe stop, waits for termination, and starts the persisted pipe detached. `rm` refuses live resources unless `--force`; `rm --stale` removes registry entries only after PID/process-identity mismatch and never removes an installed supervisor registration.

macOS user supervision is explicit and non-privileged:

```bash
tubo supervise install authority/solar --scope solar/default --json
tubo supervise install pipe/lmstudio --json
tubo supervise status authority/solar --json
tubo supervise uninstall pipe/lmstudio --json
```

Install writes a `0600` plist at `~/Library/LaunchAgents/io.github.origama.tubo.<kind>.<name>.plist`, sets `TUBO_HOME`, `TUBO_RUN_SOURCE=launchd`, log level, and one exact primary-resource command. It bootstraps that label and waits up to 15 seconds for a ready run with matching process identity. `KeepAlive` restarts crashes with a five-second throttle. `status` reports `ready`, `not_loaded`, or `crash_loop_suspected`; inspect resource log with `tubo logs <resource>`. Before `stop`, `restart`, `rm`, binary replacement, or rollback, run `tubo supervise uninstall <resource>`; these lifecycle commands refuse registered supervision to prevent supervisor-created duplicates.

Linux systemd templates live in [`../../deploy/systemd/README.md`](../../deploy/systemd/README.md). They are operator-installed examples only; Tubo never installs privileged units. Existing relay units remain deployment-owned and must preserve the same `TUBO_HOME`, source marker, keys, and public listener configuration across restart/rollback.

## Failure handling

- relay unavailable: runs mark degraded, local listeners remain bound, verified cached pipe inventory remains available with an explicit discovery warning, and new relayed sessions fail closed; configured-relay recovery retries every five seconds with bounded 15-second dials and explicitly clears stale libp2p dial backoff; successful reconnect renews reservations/records and returns runs to ready without process restart;
- authority unavailable: existing access remains TTL-bounded; issuance/refresh fails clearly; remote publishers retry failed grant/announcement renewal every five seconds after the normal renewal deadline until authority recovers;
- discovery unavailable: exact cached records remain locally inspectable; stale `get pipes` attempts one bounded refresh, then returns verified cached/local rows with scoped `refresh_error.class` and stderr diagnostics instead of hiding inventory; `--cached-only` opens no discovery sockets; new authorization still fails when authority is unavailable;
- service target unavailable: pipe remains running but rejects sessions with `target_unreachable` and reports degraded target health;
- state store unavailable: mutation and credential refresh fail closed.

## Logs and output

Runtime telemetry samples libp2p bandwidth totals/rates, connected peers, and open connections once per second into transactional state. Relay traffic therefore appears in `ps`/`top` even though relays do not create application `Session` rows. `get peers` is local observation plus local aliases, not cluster consensus.

Technical logs include IDs and reason classes but never raw tokens, keys, service target credentials, or request bodies. Detached pipe logs live under the active Tubo home and are read with `tubo logs pipe/<name> [--tail N] [-f]`. Externally supervised resources require their supervisor log when no Tubo-owned file exists. `--quiet`, verbosity flags, and `--log-level` affect diagnostics only; ordinary successful CLI output remains concise and JSON remains isolated on stdout.
