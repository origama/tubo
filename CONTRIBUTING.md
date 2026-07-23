# Contributing to Tubo

## Before working

Always read or verify:

1. the relevant GitHub Issue and linked issues;
2. the relevant code on the current branch;
3. `docs/README.md`, `docs/reference/cli.md`, and the relevant `docs/runbooks/*` file;
4. `CHANGELOG.md`;
5. `docs/reference/VERSIONING.md` for protocol, compatibility, release, persisted-state, config, or wire-behavior changes.

GitHub Issues are the canonical tracker for planning, scope, acceptance criteria, status, and follow-up.

## Engineering rules

Prefer small, testable, incremental changes. Preserve simple CLI UX, coherent code/docs/config/examples, clean stdout/stderr behavior, deterministic tests, and documented compatibility expectations.

Avoid broad refactors mixed with behavior changes, hidden TODOs, duplicated planning state, fragile tests coupled to implementation details, and changes that complicate the user model without clear benefit.

Never commit secrets, swarm keys, private keys, access tokens, local runtime state, or user-specific config.

## Verification gates

Default full gate:

```bash
make verify
```

Useful targeted gates:

```bash
go test ./...
go test -race ./...
go build ./...
./tests/smoke-cli-ux.sh
./tests/smoke-compose.sh
./tests/verify-repo-hygiene.sh
```

If only targeted gates are run, explain why broader verification was skipped.

## CI and the no-merge-on-red policy

The `main` branch is protected. The following status checks are **required** and must pass before a pull request can be merged:

- `test`
- `lint`
- `smoke-cli-ux`
- `smoke-compose`

Branch protection also enforces:

- **require branches to be up to date** with `main` before merge;
- **`enforce_admins`** — administrators cannot bypass a red required check;
- no force pushes to `main`.

### No-merge-on-red

A red required check blocks merge. Do not merge a pull request while `test`, `lint`, `smoke-cli-ux`, or `smoke-compose` is failing, even if the failure looks unrelated or flaky. Repeated merge-on-red makes the gate advisory instead of binding and hides real regressions.

If a required check is genuinely flaky, do one of the following — never silently merge on red:

1. **fix** the flakiness in the same PR or a follow-up PR;
2. **quarantine** the flaky case with a tracked issue, an explicit `t.Skip` with a reason, and a comment linking the issue;
3. **demote** the check to non-required with a tracked issue and an explicit decision record (rare; requires maintainer sign-off).

If a change is docs/hygiene-only and does not exercise a check (for example, `smoke-cli-ux` for a pure-docs change), note that in the PR description and explain why the broader gate was skipped. The PR template reminds authors of this.

## Documentation rules

Technical docs live in `docs/`. Any behavior, CLI, config, protocol, test, or operational change must update the relevant docs in the same PR. See `docs/README.md` for canonical entry points.

## Issue / PR workflow

Non-trivial work should be grounded in a GitHub Issue. Good issues include context, goal, scope, out of scope, acceptance criteria, expected tests, risks, and open questions. If an issue is too large, split it into smaller subissues before implementing.

Before closing an issue, comment with what changed, evidence, tests run, known limitations, and follow-up issues.
