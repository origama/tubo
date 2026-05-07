# [PR #68] Issue 2 — Signed bundle parser + verifier

Linked PR: https://github.com/origama/tubo/pull/68

Labels: enhancement, security, area:protocol, prio:high

## Goal
Add `internal/networkbundle` to parse and verify signed network bundles (Ed25519).

## Scope
- envelope parsing
- base64url payload decode
- SSH ed25519 public key parse
- signature verify with trusted key_id
- validity window check (`not_before`, `not_after`)

## Acceptance
- invalid signature rejected
- unknown key_id rejected
- expired/not-yet-valid bundle rejected
- valid bundle accepted
- unit tests for all cases
