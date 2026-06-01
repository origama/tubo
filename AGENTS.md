# AGENTS.md — Coding Agent Entry Point

This is the required entry point for coding agents working on Tubo.

Tubo evolves quickly. Do not treat this file as a complete implementation map.
Use it as an operating contract, then verify the current state in code, docs,
GitHub Issues, and changelog before changing anything.

## Product model

Tubo creates private libp2p tunnels for HTTP APIs, raw TCP/TLS services, and
future AI agent workflows.

The current CLI is intent-based:

```text
tubo relay
tubo gateway
tubo attach ...
tubo connect ...
tubo join ...
tubo get|describe|inspect|watch ...
tubo ps|logs|stop|rm ...
```

Do not use or reintroduce legacy runtime commands such as:

```text
tubo edge run
tubo service run
tubo bridge run
tubo relay run
```

## Before working

Always read or verify:

1. the assigned GitHub Issue and linked issues;
2. the relevant code on the current branch;
3. `docs/README.md`;
4. `docs/reference/cli.md`;
5. the relevant `docs/runbooks/*` file;
6. `CHANGELOG.md`;
7. `docs/reference/VERSIONING.md` for protocol, compatibility, release,
   persisted-state, config, or wire-behavior changes.

GitHub Issues are the canonical tracker for planning, scope, acceptance
criteria, status, and follow-up. Do not create local task trackers.

Historical material under `docs/archive/obsoletes/` must not guide new work
unless the issue explicitly asks for historical analysis.

## Engineering rules

Prefer small, testable, incremental changes.

Preserve:

- simple CLI UX;
- coherent code, docs, config, examples, and runbooks;
- clean stdout/stderr behavior;
- deterministic tests;
- documented compatibility expectations.

Avoid:

- broad refactors mixed with behavior changes;
- hidden TODOs;
- duplicated planning state;
- fragile tests coupled to implementation details;
- changes that complicate the user model without clear benefit.

## Documentation rules

Technical docs live in `docs/`.

Canonical entry points:

```text
docs/README.md
docs/reference/cli.md
docs/reference/PROTOCOL.md
docs/reference/SECURITY.md
docs/reference/VERSIONING.md
docs/runbooks/OPERABILITY.md
docs/runbooks/PROCESS_SUPERVISORS.md
CHANGELOG.md
```

Any behavior, CLI, config, protocol, test, or operational change must update the
relevant docs in the same PR.

## Issue / PR workflow

Non-trivial work should be grounded in a GitHub Issue.

Good issues include:

```text
context
goal
scope
out of scope
acceptance criteria
expected tests
risks
open questions
```

If an issue is too large, split it into smaller subissues before implementing.

Before closing an issue, comment with:

```text
what changed
evidence
tests run
known limitations
follow-up issues
```

## Verification gates

Default full gate:

```bash
make verify
```

For docs/hygiene-only changes:

```bash
make verify-repo-hygiene
```

Useful targeted gates:

```bash
go test ./...
go test -race ./...
go build ./...
./tests/smoke-compose.sh
./tests/smoke-cli-ux.sh
RUN_INTEGRATION=1 go test -v ./tests/integration
tests/e2e/run.sh all
tests/e2e/run.sh 001-default-cluster-default-namespace
```

If only targeted gates are run, explain why broader verification was skipped.

Docker-dependent tests may skip when Docker is unavailable. Treat that as an
infrastructure limitation, not proof of correctness.

## Code map

Start searches from these areas, but verify current structure before editing:

```text
cmd/tubo/              CLI and command wiring
internal/app/          runtime application logic
internal/config/       config loading and materialization
internal/catalog/      service/resource catalog
internal/discovery/    discovery publication/query/cache
internal/grants/       grants, leases, invites, authorization
internal/p2p/          libp2p host/network behavior
internal/protocol/     tunnel protocol and stream framing
internal/connectflow/  connect resolution and tunnel setup
internal/launcher/     detached process management
tests/                 smoke, integration, e2e, hygiene
docs/                  canonical documentation
```

## CLI output contract

```text
stdout = primary command result
stderr = progress, warnings, hints
technical logs = hidden unless verbosity/log-level is enabled
```

For `--json`, stdout must remain parseable JSON, including during implicit join,
grant refresh, discovery query, or lease redemption.

## Security / compatibility

Be conservative with:

```text
invite tokens
grants and leases
service identity
namespace membership
public bundle trust
persisted config/state
protocol negotiation
raw TCP/TLS passthrough
public-default invite-only semantics
```

Never commit secrets, swarm keys, private keys, access tokens, local runtime
state, or user-specific config.

## Labels

Use consistent GitHub labels.

Types:

```text
bug performance security docs test infra release planning enhancement
```

Areas:

```text
area:edge area:service area:relay area:protocol area:testbench
area:cli area:docs area:linode
```

Priority / status:

```text
prio:high prio:medium prio:low
needs-triage investigation blocked breaking-compat
good-first-release-candidate
```

Each issue or PR should normally have at least one type label and one area label.
