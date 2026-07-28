# Three-machine E2E runbook

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

This checks local build/SSH tools, both SSH aliases, remote OS/architecture, curl, writable `/tmp`, and that selected relay, publisher HTTP, and local client ports are not already listening. It is read-only.

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
KEEP_WORK=1 \
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

Implementation now covers commands and relay data path required by this scenario. Local loopback equivalent runs in `make verify-local`; real three-machine execution remains separate and requires explicit confirmation.

## Artifacts

```text
generated/e2e-three-machine/<run-id>/
  artifacts/
  client-home/
  bin/
```

Remote while preserved:

```text
/tmp/tubo-rebuilt-e2e-<run-id>/
  bin/
  home/
  logs/
  pids/
```

A successful report must include the run ID, machine aliases, relay address, origin `pipe_id`, response body, `ps` JSON, pipe inspection, and logs.

## Cleanup

When `KEEP_WORK=1` was used:

```bash
TUBO_E2E_RUN_ID=<exact-run-id> \
TUBO_E2E_CONFIRM=three-machine \
./tests/e2e/three-machine/run.sh clean
```

Inspect paths before cleanup. Never replace the run ID with a glob.

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
```

Infrastructure failure is not evidence of product correctness. Preserve artifacts and report which phases completed.

## Why not reuse the current live setup directly?

The old installation has different resource, protocol, state, and authority semantics. Reusing its config, systemd units, keys, bundles, or grant stores would both risk disruption and accidentally test compatibility that this repository explicitly rejects. Only the physical topology and behavioral expectations are reused.
