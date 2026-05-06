# [PR #68] Issue 4 — Implicit public join for attach/connect/gateway

Linked PR: https://github.com/origama/tubo/pull/68

Labels: enhancement, area:cli, prio:medium

## Goal
Replace implicit local swarm init with implicit join to default public network.

## Scope
- new helper `ensureJoinedPublicNetwork`
- used by `attach`, `connect`, `gateway`
- blocked in `CI=true` and with `--no-init`

## Acceptance
- first run installs signed default bundle
- existing config -> no network fetch
- CI and --no-init paths fail with clear guidance
