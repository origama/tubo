# AGENTS.md — Entry Point For Coding Agents

This file is the **canonical entry point** for all coding agents (including Codex).

## 1) Project mission

**P2P API Tunnel Platform**: a self-hosted platform that forwards HTTP traffic to services behind NAT/firewalls using encrypted libp2p streams and distributed discovery via signed pubsub.

Base flow:

```text
Client HTTP -> Edge Gateway -> libp2p stream -> Service Agent -> Origin Service
```

## 2) Current implementation status (as-is)

Working components:

- `cmd/tubo` (`tubo edge run`): HTTP ingress, discovery subscription, local cache, auto-routing, stream proxying, relay fallback, admin API.
- `cmd/tubo` (`tubo service run`): signed service announcement, heartbeat, stream handler, forwarding to local/remote HTTP targets.
- `cmd/tubo` (`tubo bridge run`): client-side HTTP proxy toward a service peer.
- `cmd/tubo` (`tubo relay run`): public bootstrap/relay v2 node with health endpoint, PSK support, and PeerID allowlist support (connection gater).
- `internal/protocol`: binary framing + bidirectional body streaming.
- `internal/discovery`: signed announcements + TTL cache + add/remove events.
- `internal/p2p`: host creation from seed + private swarm PSK support + PeerID allowlist parser.

Known gaps:

- AutoNAT/hole punching is not complete yet.
- Advanced reachability diagnostics are not complete yet.

## 3) Mandatory workflow for agents

### 3.1 Before starting

1. Read this `AGENTS.md`.
2. Read the assigned GitHub issue and linked issues.
3. Verify the real code state on the current branch.
4. Read the relevant documentation in `docs/`.
5. If the work touches releases, compatibility, or protocol behavior, read `docs/reference/VERSIONING.md`.
6. If you find untracked work, open or update a GitHub issue instead of using local trackers.

### 3.2 During the work

1. If behavior/config/interface changes, update the documentation in `docs/` **immediately**.
2. Keep code, env vars, and operational runbooks consistent.
3. Do not leave untracked operational TODOs: they must have a GitHub issue or be resolved in the PR.

### 3.3 Before closing

1. Run the current verification gates.
2. Update the GitHub issue with status, evidence, executed tests, and follow-up.
3. Update the docs touched by the change.

## 4) Current completion gates (mandatory)

A task is `DONE` only if these pass:

1. `go test ./...`
2. `./tests/smoke-compose.sh`
3. `RUN_INTEGRATION=1 go test -v ./tests/integration` (recommended before merge when Docker is stable)

Note: tests in `tests/integration` are skipped (`SKIP`) when the Docker daemon is unavailable (infrastructure error).

## 5) Documentation policy (single source)

Rules:

1. Technical documentation lives in `docs/`.
2. `docs/README.md` is the canonical index for the documentation.
3. Any implementation change must be reflected in the relevant documentation in the same PR/commit.
4. Historical/superseded documents belong in `docs/archive/obsoletes/` and must not guide new implementations.

## 6) Canonical operational runbook

For component startup and secure P2P tunnel creation across 2+ services, use this single reference:

- `docs/runbooks/OPERABILITY.md`

For versioning policy and cross-role/node compatibility:

- `docs/reference/VERSIONING.md`

In particular:

- local quick start with Docker Compose;
- private swarm PSK setup;
- multi-host startup (`edge` + multiple `service` nodes);
- end-to-end discovery/routes/proxy verification;
- current limitations and troubleshooting.

## 7) Project workflow

GitHub Issues are the canonical source for:

- work status;
- implementation scope;
- acceptance criteria;
- priority;
- verification evidence;
- follow-up.

Do not use local files as parallel trackers. The historical material migrated from the old tracker is preserved in `#180` only as a migration snapshot.

## 8) GitHub issue / PR labels

Issues and PRs should use consistent GitHub labels for triage and prioritization.

### 8.1 Type

- `bug`
- `performance`
- `security`
- `docs`
- `test`
- `infra`
- `release`
- `planning`
- `enhancement`

### 8.2 Area

- `area:edge`
- `area:service`
- `area:relay`
- `area:protocol`
- `area:testbench`
- `area:cli`
- `area:docs`
- `area:linode`

### 8.3 Priority

- `prio:high`
- `prio:medium`
- `prio:low`

### 8.4 Status / risk

- `needs-triage`
- `investigation`
- `blocked`
- `breaking-compat`
- `good-first-release-candidate`

### 8.5 Rule of thumb

Each issue/PR should have at least:

1. one **type** label;
2. one **area** label (or more if needed);
3. one **priority** label when the priority is known.

Also use `investigation`, `blocked`, or `breaking-compat` when they help clarify the risk or current work state.
