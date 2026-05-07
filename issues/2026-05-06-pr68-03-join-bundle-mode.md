# [PR #68] Issue 3 — `tubo join` bundle mode

Linked PR: https://github.com/origama/tubo/pull/68

Labels: enhancement, area:cli, area:docs, prio:high

## Goal
Extend `tubo join` with:
- `tubo join` (default public bundle)
- `tubo join tubo-public`
- `tubo join --bundle-url <url>`

## Scope
- fetch + verify bundle
- install config + swarm key in current layout
- keep existing manual mode (`--relay`, `--swarm-key`) fully supported

## Acceptance
- signed bundle join works end-to-end
- manual join still works unchanged
- output includes joined network summary
