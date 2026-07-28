# Contributing

## Source of work

Non-trivial implementation work must be grounded in a GitHub Issue once this repository has a remote. Do not maintain a second task tracker in the repository. Design documents define contracts; issues define implementation status and ownership.

## Before changing code

Read:

1. `AGENTS.md`;
2. the assigned issue and linked issues;
3. `docs/README.md`;
4. `docs/reference/INVARIANTS.md`;
5. the relevant protocol, security, state, CLI, and runbook documents;
6. `CHANGELOG.md` and `docs/reference/VERSIONING.md` for persisted-state or wire changes.

## Engineering rules

- Prefer small, testable slices.
- Keep the service/pipe/session ontology intact.
- Do not introduce service and client runtime implementations: both are pipe plans.
- Do not make relay/cache/query peers authorities.
- Preserve parseable stdout for `--json`; progress and diagnostics go to stderr.
- Never commit keys, invite tokens, state databases, logs, or machine-specific configuration.
- Update code, tests, docs, runbooks, and changelog together when behavior changes.

## Verification

```bash
make verify-local
```

Three-machine behavior is a separate explicit gate:

```bash
make e2e-three-machine-preflight
TUBO_E2E_CONFIRM=three-machine make e2e-three-machine E2E_SCENARIO=private-http
```

A red required check blocks merge. Docker/SSH infrastructure failures must be reported as infrastructure limitations, not as proof of correctness.
