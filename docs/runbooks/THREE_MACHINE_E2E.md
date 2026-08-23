# Three-machine E2E runbook

**Prototype reference runbook:** This harness and its evidence document `prototype-v2-final` behavior. Its isolated-state safety rules remain mandatory. Future A0–A4 architecture journeys are [`../testing/ARCHITECTURE_ACCEPTANCE.md`](../testing/ARCHITECTURE_ACCEPTANCE.md); do not treat prototype topology/CLI/protocol as future compatibility contract.

This is the canonical behavioral gate because Docker cannot prove real NAT, public relay reservation, routing changes, or cross-machine process behavior.

## Topology

```text
                     public Internet

 tuborelay                 oripi                       macb
 stable public host        authority + origin          consumer
 relay transport only      service + published pipe    local pipe
        \                         |                       /
         \________________ private overlay ____________/
```

Default SSH aliases and relay address are configurable in `tests/e2e/three-machine/.env.example`.

## Safety boundary

The rebuilt harness must coexist with the current Tubo installation.

It therefore:

- never writes `/usr/local/bin/tubo`;
- never stops or edits `tubo-relay`, `tubo-grants`, any systemd unit, or user `launchd` job;
- never runs `pkill tubo`;
- never reads or deletes `~/.config/tubo`, `~/.local/share/tubo`, or `/etc/tubo`;
- uploads binaries under `/tmp/tubo-rebuilt-e2e-<run-id>/bin`;
- sets `TUBO_HOME=/tmp/tubo-rebuilt-e2e-<run-id>/home` remotely;
- uses `generated/e2e-three-machine/<run-id>` locally;
- records exact PIDs and removes only the exact run directory;
- uses non-default ports to avoid existing relay/service listeners.

Mutating scenarios require:

```bash
TUBO_E2E_CONFIRM=three-machine
```

Cleanup requires the exact `TUBO_E2E_RUN_ID`; this prevents broad `/tmp` deletion.

## Preflight

```bash
cd ~/local_repos/origama/tuboRebuilt
make e2e-three-machine-preflight
```

This checks local build/SSH tools, both SSH aliases, remote OS/architecture, curl, writable `/tmp`, and that selected relay, publisher HTTP, local client, and periodic-sync contender ports are not already listening. It builds an owner-only per-run SSH `known_hosts` bundle only from existing operator-verified alias entries in `~/.ssh/known_hosts`; it never runs `ssh-keyscan`, accepts changed keys, or uses SSH multiplexing. Each host gets a fresh non-multiplexed probe with relevant effective SSH settings and full failure stderr. Failed scenario preflight writes retained local `artifacts/report.json` with phase `preflight` and stable `preflight_failure`; it never collects artifacts or runs cleanup because no run state exists.

The selected relay port must also be permitted by both the host firewall and the Linode/cloud firewall. The isolated default is TCP `44001`; this is deliberately different from the currently installed relay on `4001`. If `44001` is not open, add a temporary narrowly scoped firewall rule or choose another isolated port. Do not stop the current relay merely to reuse `4001`.

Expected defaults:

```text
relay SSH:     tuborelay
publisher SSH: oripi
client:        local macb
relay IP:      172.232.189.160
remote arch:   linux/amd64
```

Override through exported variables or untracked `tests/e2e/three-machine/.env.local`.

## First release scenario

```bash
TUBO_E2E_CONFIRM=three-machine \
make e2e-three-machine E2E_SCENARIO=private-http
```

The scenario performs these assertions:

1. builds Darwin/local and Linux/remote binaries from this checkout;
2. creates a run-specific private swarm key;
3. starts a transport-only relay on `tuborelay`;
4. joins `oripi` and creates `solar/default`;
5. starts the single scope authority on `oripi`;
6. starts a loopback-only HTTP test service;
7. defines `service/echo`;
8. defines and publishes `pipe/echo-team -> service/echo`;
9. joins clean macb state through a member invite;
10. discovers the exact `pipe_id` without leaking the service target;
11. creates a local consumer pipe through `connect`;
12. sends a POST request and checks origin, method, path, query, and body path;
13. verifies `ps` contains pipe/relay/authority primary refs and no attach/connect/service runtime kinds;
14. forces publisher/client reachability private and proves the initial session transport path is relayed;
15. records each run's PID, process identity, and source; harness-managed processes must remain isolated `foreground`/`detached` runs and must never register a host supervisor;
15. captures status, process, pipe, session, response, and logs as artifacts.

Periodic-sync artifacts intentionally omit raw invites, keys, homes, databases, service targets, fencing tokens, and process identities.

Implementation now covers commands and relay data path required by this scenario. Local loopback equivalent runs in `make verify-local`; real three-machine execution remains separate and requires explicit confirmation.

## Periodic sync election scenario

`periodic-sync` uses split homes on `oripi`: an authority-only home creates `solar/default` and does not join membership; a distinct publisher-member home serves/publishes origin. `macb` uses one member home with two distinct local pipes. Therefore one active `owner_ref` proves one local state/scope scheduler owner, never one global network owner.

```bash
TUBO_E2E_CONFIRM=three-machine \
  make e2e-three-machine E2E_SCENARIO=periodic-sync
```

It performs a one-time setup refresh, then uses only `get pipes --cached-only` and local scope-status observations for election/outage/convergence. Election polling requires `last_success` strictly later than pre-contender baseline. After successor lease takeover, scope status must show successor `last_success` strictly later than pre-crash baseline within 90s: controller 60s cadence, up to 6s jitter, 20s attempt deadline, and polling grace. It validates exact PID + `/proc/<pid>/cmdline` run-root/resource checks before `SIGKILL` of elected contender and isolated relay. No `stop`/release runs before crash. Lease samples reject successor ownership before recorded expiry. Relay outage must retain origin cache and record bounded `authority_unreachable` or `discovery_unreachable`; restart requires same PeerID plus exact new PID, live `/proc` command, and `started_at` newer than pre-kill status. Before one later-pipe start, a fresh publisher CLI process must force-refresh and record `last_success` newer than restarted relay; post-recovery new publication must appear in cached-only output. Default limits: 65s takeover, 120s outage, 180s recovery. Override only with `TUBO_E2E_SYNC_{TAKEOVER,OUTAGE,RECOVERY}_TIMEOUT`.

Failure classes include `assertion_failed`, `sync_takeover_timeout`, `sync_recovery_timeout`, `unsafe_pid_rejected`, `relay_identity_changed`, `artifact_finalization_failed`, `cleanup_failure`, and `internal_error`, alongside classes in Failure classification below. Failed runs preserve completed phase and class in `report.json`; original scenario failure class wins over finalization/cleanup failure. Artifact finalization removes complete text-log lines containing known generated secret or local-target values and rejects those values in structured artifacts. Scenario implementation is not successful 3M evidence: retain exact run ID plus `artifacts/report.json` with `result: pass` before updating evidence claims.

## Artifacts

```text
generated/e2e-three-machine/<run-id>/
  artifacts/
  client-home/
  bin/
```

Remote run roots are removed by exit trap after artifact collection. Redacted local artifacts remain for review.

`periodic-sync` `report.json` includes scenario/run ID, result, phase, stable failure class, validated host aliases, relay address, origin/new pipe IDs, election timestamps/refs, configured bounds, and names of required evidence files. Evidence files contain source SHA/dirty metadata, phase JSONL, redacted relay status, redacted scope views, cached listings, takeover samples, and safe process resource/status projections. Every persisted `*-ps-*.json` and `relay-*-status.json` contains only each resource `primary_ref` and `status`; it never contains `pid`, `peer_id`, `address`, `process_identity`, or another process identity field. Relay logs have no safe artifact schema and are removed. Exact PID/resource cleanup manifests remain private under run state and are deleted by cleanup. Private HTTP keeps response/process/inspection evidence separately. No report or artifact may contain an invite value, swarm-key value, database/home copy, fencing token, process identity, or service target.

## Cleanup

For an explicit exact-run cleanup retry after interrupted SSH cleanup:

```
TUBO_E2E_RUN_ID=<exact-run-id> \
TUBO_E2E_CONFIRM=three-machine \
./tests/e2e/three-machine/run.sh clean
```

Inspect paths before cleanup. Never replace run ID with glob. Default exit and this explicit cleanup remove exact remote run root plus local swarm key, client/viewer/authority/publisher homes, PID files, and secret-value manifest. Redacted `artifacts/` remain under exact local run root for review.

Set `KEEP_WORK=1` only for an inspected debugging run. Finalization still redacts retained artifacts, but skips cleanup and preserves exact run-specific local and remote roots, including secret-bearing state, under owner-only `0700` roots. Run exact-ID `clean` after inspection; default remains cleanup.

## Mandatory scenarios

The full release gate eventually includes:

### Private collaborative HTTP

- member discovers and connects by pipe name/ID;
- viewer discovers but is denied connect;
- outsider cannot discover;
- HTTP request fidelity;
- relay path observable;
- no local target leakage.

### Invite-only

- pipe absent from ambient listing;
- clean client connects with self-contained invite;
- exact `pipe_id` binding;
- one successful redemption;
- denied redemption does not consume token;
- second successful redemption is rejected.

### Raw TCP/TLS

- small and 256 KiB echo;
- six or more concurrent sessions;
- 8 MiB transfer;
- control half-close remains correct during concurrent data transfer;
- HTTPS/TLS passthrough tested with a native TLS client.

### Recovery

- origin run stops and restarts with the same pipe-owner identity;
- runtime PeerID/address may change;
- record updates retain exact `pipe_id`;
- new client connection recovers without byte replay;
- relay reservation renewal survives beyond its initial TTL in a long campaign;
- supervisor-specific reboot/login validation is a separate operator campaign using an explicit supervisor install, never this isolated harness.

### Pipe graph and trace

- explicit `pipe.forward` allows one re-export;
- missing forward grant is denied;
- cycles and hop overflow are denied;
- logical, authorization, and transport paths remain separate;
- remote trace is redacted by default.

## Failure classification

A failed scenario report must distinguish:

```text
build_failure
ssh_unreachable
remote_dependency_missing
port_conflict
relay_unreachable
authority_unreachable
discovery_timeout
authorization_denied_unexpected
pipe_readiness_timeout
data_plane_mismatch
cleanup_failure
artifact_finalization_failed
internal_error
invalid_input
preflight_failure
unsafe_pid_rejected
relay_identity_changed
sync_takeover_timeout
sync_recovery_timeout
assertion_failed
```

Infrastructure failure is not evidence of product correctness. Preserve artifacts and report which phases completed.

## Why not reuse the current live setup directly?

The old installation has different resource, protocol, state, and authority semantics. Reusing its config, systemd units, keys, bundles, or grant stores would both risk disruption and accidentally test compatibility that this repository explicitly rejects. Only the physical topology and behavioral expectations are reused.
